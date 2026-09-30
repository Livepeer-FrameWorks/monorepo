//go:build schema_verify

package jobs

import (
	"context"
	"sync"
	"testing"
	"time"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// A clip's Foghorn row commits before Commodore registers commodore.clips (CreateClip registers
// only after Foghorn's RPC returns), so a projection pass that runs in that window gets
// Found=false. The row must come back for projection promptly: the sync + thumbnail facts that
// land seconds later have to reach the catalog within one short retry, not after a backoff
// sized from the 5-minute fallback interval. A row that stays unregistered still backs off to
// the one-hour ceiling so it cannot hammer Commodore.
func TestCatalogProjectionRetriesRegistrationLagPromptly_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	const tenant = "7a1e0000-0000-4000-8000-0000000000c1"
	const clipHash = "20260930163655b0dce135e4f3783b"
	const origin = "staging-media-eu"

	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, origin_cluster_id, storage_cluster_id)
		VALUES ($1, 'clip', $2::uuid, 'processing', $3, $3)`, clipHash, tenant, origin); err != nil {
		t.Fatalf("seed clip: %v", err)
	}

	var mu sync.Mutex
	registered := false
	commodore := &mockCommodoreClient{
		snapshotRespFn: func(req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			if !registered {
				return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: false}, nil
			}
			return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: req.GetSourceRevision()}, nil
		},
	}
	r := newTestReconciler(t, conn, nil, commodore, nil)
	r.interval = 5 * time.Minute // production fallback cadence
	r.clusterID = origin

	if advanced, _ := r.projectCommodoreArtifactState(ctx); advanced != 0 {
		t.Fatalf("unregistered clip must not advance, advanced=%d", advanced)
	}
	if n := len(commodore.snapshotCalls); n != 1 {
		t.Fatalf("expected one projection attempt before registration, got %d", n)
	}

	// Commodore registers the clip; processing finishes, the sync completes and thumbnails are
	// activated — each a catalog-projected mutation on the Foghorn row.
	mu.Lock()
	registered = true
	mu.Unlock()
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts
		SET status = 'ready', size_bytes = 716921, sync_status = 'synced', storage_location = 's3', has_thumbnails = TRUE
		WHERE artifact_hash = $1`, clipHash); err != nil {
		t.Fatalf("apply sync + thumbnail mutation: %v", err)
	}

	// Fifteen seconds pass (shift the backoff deadline instead of sleeping).
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts
		SET catalog_next_attempt_at = catalog_next_attempt_at - INTERVAL '15 seconds'
		WHERE artifact_hash = $1`, clipHash); err != nil {
		t.Fatalf("age backoff: %v", err)
	}
	if advanced, _ := r.projectCommodoreArtifactState(ctx); advanced != 1 {
		t.Fatalf("registered clip must project on the first retry after the registration-lag miss, advanced=%d calls=%d",
			advanced, len(commodore.snapshotCalls))
	}
	last := commodore.snapshotCalls[len(commodore.snapshotCalls)-1]
	if last.GetSizeBytes() != 716921 || !last.GetHasThumbnails() || last.GetSyncStatus() != "synced" {
		t.Fatalf("projected snapshot lost the sync/thumbnail facts: size=%d thumbs=%v sync=%q",
			last.GetSizeBytes(), last.GetHasThumbnails(), last.GetSyncStatus())
	}
	var syncedRev, revision int64
	if err := conn.QueryRowContext(ctx, `SELECT catalog_synced_rev, catalog_revision FROM foghorn.artifacts WHERE artifact_hash = $1`,
		clipHash).Scan(&syncedRev, &revision); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if syncedRev != revision {
		t.Fatalf("watermark must cover the projected revision: synced=%d revision=%d", syncedRev, revision)
	}

	// A row that never registers keeps doubling to the one-hour ceiling.
	const orphan = "orphanclip00000000000000000001"
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, origin_cluster_id)
		VALUES ($1, 'clip', $2::uuid, 'processing', $3)`, orphan, tenant, origin); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	for range 12 {
		r.backoffCatalogRow(ctx, orphan)
	}
	var delaySecs float64
	if err := conn.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM catalog_next_attempt_at - NOW()) FROM foghorn.artifacts WHERE artifact_hash = $1`,
		orphan).Scan(&delaySecs); err != nil {
		t.Fatalf("read orphan backoff: %v", err)
	}
	if delaySecs < 3500 || delaySecs > 3600 {
		t.Fatalf("persistently unregistered row must back off to the 1h ceiling, got %.0fs", delaySecs)
	}
}
