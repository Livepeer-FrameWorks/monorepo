//go:build schema_verify

package foghorndb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestFoghornGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	prepareFoghornCatalog(t, startFoghornCatalogPostgres(t))
}

func TestConfigSeedApplyAckOutboxSameVersionReplacement_RealPG(t *testing.T) {
	verifyConfigSeedApplyAckOutboxSameVersionReplacement(t, startFoghornCatalogPostgres(t))
}

func verifyConfigSeedApplyAckOutboxSameVersionReplacement(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q := New(db)
	connA, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()
	qA := New(connA)
	qB := New(connB)
	params := EnqueueConfigSeedApplyAckParams{
		NodeID: "node-1", ClusterID: "cluster-1", SeedVersion: 7, RequestPayload: []byte("failed"),
		ResultSignature: bytes.Repeat([]byte{1}, sha256.Size),
	}
	if rows, err := qA.EnqueueConfigSeedApplyAck(ctx, params); err != nil || rows != 1 {
		t.Fatalf("enqueue failure ACK rows=%d err=%v", rows, err)
	}
	var originalPendingSince time.Time
	if err := db.QueryRowContext(ctx, `SELECT pending_since FROM foghorn.config_seed_apply_ack_outbox WHERE node_id=$1`, params.NodeID).Scan(&originalPendingSince); err != nil {
		t.Fatal(err)
	}
	replayed := params
	replayed.RequestPayload = []byte("same projection, different observation time")
	if rows, err := qB.EnqueueConfigSeedApplyAck(ctx, replayed); err != nil || rows != 0 {
		t.Fatalf("deduplicate equivalent failure ACK rows=%d err=%v", rows, err)
	}
	lease := sql.NullString{String: "worker-a", Valid: true}
	claimed, err := q.ClaimDueConfigSeedApplyAcks(ctx, ClaimDueConfigSeedApplyAcksParams{LeaseOwner: lease, BatchSize: 1})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim failure ACK rows=%d err=%v", len(claimed), err)
	}
	params.RequestPayload = []byte("recovered")
	params.ResultSignature = bytes.Repeat([]byte{2}, sha256.Size)
	if rows, err := qB.EnqueueConfigSeedApplyAck(ctx, params); err != nil || rows != 1 {
		t.Fatalf("replace same-version ACK rows=%d err=%v", rows, err)
	}
	var replacementPendingSince time.Time
	if err := db.QueryRowContext(ctx, `SELECT pending_since FROM foghorn.config_seed_apply_ack_outbox WHERE node_id=$1`, params.NodeID).Scan(&replacementPendingSince); err != nil {
		t.Fatal(err)
	}
	if !replacementPendingSince.Equal(originalPendingSince) {
		t.Fatalf("pending replacement reset backlog age: before=%s after=%s", originalPendingSince, replacementPendingSince)
	}
	if rows, err := q.SettleDeliveredConfigSeedApplyAck(ctx, SettleDeliveredConfigSeedApplyAckParams{
		ID: claimed[0].ID, Revision: claimed[0].Revision, LeaseOwner: lease,
	}); err != nil || rows != 0 {
		t.Fatalf("stale delivery settled replacement rows=%d err=%v", rows, err)
	}
	if rows, err := q.ReleaseConfigSeedApplyAckLease(ctx, ReleaseConfigSeedApplyAckLeaseParams{
		ID: claimed[0].ID, LeaseOwner: lease,
	}); err != nil || rows != 1 {
		t.Fatalf("release superseded delivery lease rows=%d err=%v", rows, err)
	}
	claimed, err = q.ClaimDueConfigSeedApplyAcks(ctx, ClaimDueConfigSeedApplyAcksParams{LeaseOwner: lease, BatchSize: 1})
	if err != nil || len(claimed) != 1 || string(claimed[0].RequestPayload) != "recovered" || claimed[0].Revision != 2 {
		t.Fatalf("replacement claim=%#v err=%v", claimed, err)
	}
	if rows, err := q.SettleDeliveredConfigSeedApplyAck(ctx, SettleDeliveredConfigSeedApplyAckParams{
		ID: claimed[0].ID, Revision: claimed[0].Revision, LeaseOwner: lease,
	}); err != nil || rows != 1 {
		t.Fatalf("settle replacement rows=%d err=%v", rows, err)
	}
	if rows, err := q.EnqueueConfigSeedApplyAck(ctx, params); err != nil || rows != 0 {
		t.Fatalf("deduplicate settled ACK rows=%d err=%v", rows, err)
	}
	poisoned := params
	poisoned.RequestPayload = []byte{0xff}
	poisoned.ResultSignature = bytes.Repeat([]byte{3}, sha256.Size)
	if rows, err := qA.EnqueueConfigSeedApplyAck(ctx, poisoned); err != nil || rows != 1 {
		t.Fatalf("enqueue poison test row rows=%d err=%v", rows, err)
	}
	claimed, err = q.ClaimDueConfigSeedApplyAcks(ctx, ClaimDueConfigSeedApplyAcksParams{LeaseOwner: lease, BatchSize: 1})
	if err != nil || len(claimed) != 1 || claimed[0].Revision != 3 {
		t.Fatalf("claim poison test row=%#v err=%v", claimed, err)
	}
	if rows, err := q.QuarantineInvalidConfigSeedApplyAck(ctx, QuarantineInvalidConfigSeedApplyAckParams{
		ID: claimed[0].ID, Revision: claimed[0].Revision, LeaseOwner: lease,
		LastError: sql.NullString{String: "invalid payload", Valid: true},
	}); err != nil || rows != 1 {
		t.Fatalf("quarantine poison test row rows=%d err=%v", rows, err)
	}
	if stats, err := q.GetConfigSeedApplyAckOutboxStats(ctx); err != nil || stats.Quarantined != 1 {
		t.Fatalf("quarantined stats=%#v err=%v, want 1", stats, err)
	}
	olderRepair := poisoned
	olderRepair.SeedVersion = 6
	olderRepair.RequestPayload = []byte("older")
	if rows, err := qB.EnqueueConfigSeedApplyAck(ctx, olderRepair); err != nil || rows != 0 {
		t.Fatalf("older ACK repaired quarantined row rows=%d err=%v", rows, err)
	}
	repair := poisoned
	repair.RequestPayload = []byte("repaired")
	if rows, err := qB.EnqueueConfigSeedApplyAck(ctx, repair); err != nil || rows != 1 {
		t.Fatalf("repair quarantined row rows=%d err=%v", rows, err)
	}
	claimed, err = q.ClaimDueConfigSeedApplyAcks(ctx, ClaimDueConfigSeedApplyAcksParams{LeaseOwner: lease, BatchSize: 1})
	if err != nil || len(claimed) != 1 || claimed[0].Revision != 4 || string(claimed[0].RequestPayload) != "repaired" {
		t.Fatalf("claim repaired row=%#v err=%v", claimed, err)
	}
	if rows, err := q.SettleDeliveredConfigSeedApplyAck(ctx, SettleDeliveredConfigSeedApplyAckParams{
		ID: claimed[0].ID, Revision: claimed[0].Revision, LeaseOwner: lease,
	}); err != nil || rows != 1 {
		t.Fatalf("settle repaired row rows=%d err=%v", rows, err)
	}
	stats, err := q.GetConfigSeedApplyAckOutboxStats(ctx)
	if err != nil || stats.Pending != 0 {
		t.Fatalf("outbox stats=%#v err=%v, want no pending ACKs", stats, err)
	}
}

func TestSourceProjectionRevisionMigrationSeedsDurableHighWater_RealPG(t *testing.T) {
	verifySourceProjectionRevisionMigrationSeedsDurableHighWater(t, startFoghornCatalogPostgres(t))
}

func verifySourceProjectionRevisionMigrationSeedsDurableHighWater(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const durableRevision = int64(900)
	if _, err := db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS foghorn.source_projection_revision`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER SEQUENCE foghorn.source_projection_revision RESTART WITH 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.ingest_offline_effects (
    tenant_id, stream_internal_name, source_node_id, source_revision
) VALUES (
    '10000000-0000-0000-0000-000000000001'::uuid,
    'migration-high-water', 'node-a', $1
)`, durableRevision); err != nil {
		t.Fatal(err)
	}
	migration, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.0/expand/004_source_projection_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var nextLegacyRevision int64
	if err := db.QueryRowContext(ctx, `SELECT nextval('foghorn.source_projection_revision')`).Scan(&nextLegacyRevision); err != nil {
		t.Fatal(err)
	}
	if nextLegacyRevision <= durableRevision {
		t.Fatalf("migration high water: next_legacy=%d, want > %d", nextLegacyRevision, durableRevision)
	}
}

func TestSourceProjectionAllocatorKeyScoped_RealPG(t *testing.T) {
	verifySourceProjectionAllocatorKeyScoped(t, startFoghornCatalogPostgres(t))
}

func TestKeyScopedOrderingAllocators_RealPG(t *testing.T) {
	verifyKeyScopedOrderingAllocators(t, startFoghornCatalogPostgres(t))
}

func TestLegacyOrderingSequencesRemainBelowCounters_RealPG(t *testing.T) {
	verifyLegacyOrderingSequencesRemainBelowCounters(t, startFoghornCatalogPostgres(t))
}

func verifySourceProjectionAllocatorKeyScoped(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connA, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()
	params := NextSourceProjectionRevisionParams{
		TenantID: "10000000-0000-0000-0000-000000000001", StreamInternalName: "two-session-stream",
	}
	first, err := New(connA).NextSourceProjectionRevision(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(connB).NextSourceProjectionRevision(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	third, err := New(connA).NextSourceProjectionRevision(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if !(first < second && second < third) {
		t.Fatalf("key-scoped revisions across sessions = %d, %d, %d", first, second, third)
	}

	start := make(chan struct{})
	results := make(chan struct {
		revision int64
		err      error
	}, 2)
	allocate := func() {
		<-start
		revision, allocErr := AllocateSourceProjectionRevisionAfter(
			ctx, db, params.TenantID, "concurrent-repair-stream", 1000,
		)
		results <- struct {
			revision int64
			err      error
		}{revision: revision, err: allocErr}
	}
	go allocate()
	go allocate()
	close(start)
	left, right := <-results, <-results
	if left.err != nil || right.err != nil {
		t.Fatalf("concurrent repair allocations: left=%d/%v right=%d/%v", left.revision, left.err, right.revision, right.err)
	}
	if left.revision <= 1000 || right.revision <= 1000 || left.revision == right.revision {
		t.Fatalf("concurrent repair revisions = %d, %d; want distinct values above watermark", left.revision, right.revision)
	}
}

func verifyKeyScopedOrderingAllocators(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connA, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()
	qA, qB := New(connA), New(connB)

	assertABA := func(name string, allocateA, allocateB func() (int64, error)) {
		t.Helper()
		first, allocErr := allocateA()
		if allocErr != nil {
			t.Fatalf("%s first allocation: %v", name, allocErr)
		}
		second, allocErr := allocateB()
		if allocErr != nil {
			t.Fatalf("%s second allocation: %v", name, allocErr)
		}
		third, allocErr := allocateA()
		if allocErr != nil {
			t.Fatalf("%s third allocation: %v", name, allocErr)
		}
		if !(first < second && second < third) {
			t.Fatalf("%s A/B/A allocations = %d, %d, %d", name, first, second, third)
		}

		start := make(chan struct{})
		type allocationResult struct {
			value int64
			err   error
		}
		results := make(chan allocationResult, 2)
		allocateConcurrently := func(allocate func() (int64, error)) {
			<-start
			var value int64
			err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
				var allocateErr error
				value, allocateErr = allocate()
				return allocateErr
			})
			results <- allocationResult{value: value, err: err}
		}
		go allocateConcurrently(allocateA)
		go allocateConcurrently(allocateB)
		close(start)
		left, right := <-results, <-results
		if left.err != nil || right.err != nil {
			t.Fatalf("%s concurrent allocations: left=%d/%v right=%d/%v", name, left.value, left.err, right.value, right.err)
		}
		if left.value == right.value || left.value <= third || right.value <= third {
			t.Fatalf("%s concurrent allocations = %d, %d after %d; want distinct advancing values", name, left.value, right.value, third)
		}
	}

	assertABA("node control fence",
		func() (int64, error) { return qA.AllocateNodeControlFence(ctx, "ordering-node") },
		func() (int64, error) { return qB.AllocateNodeControlFence(ctx, "ordering-node") })
	copyParams := AllocateArtifactNodeCopyVersionParams{ArtifactHash: "orderingcopyartifact000000000001", NodeID: "ordering-node"}
	assertABA("artifact node-copy version",
		func() (int64, error) { return qA.AllocateArtifactNodeCopyVersion(ctx, copyParams) },
		func() (int64, error) { return qB.AllocateArtifactNodeCopyVersion(ctx, copyParams) })
	sourceParams := NextSourceProjectionRevisionParams{
		TenantID: "10000000-0000-0000-0000-000000000001", StreamInternalName: "ordering-stream",
	}
	assertABA("source projection revision",
		func() (int64, error) { return qA.NextSourceProjectionRevision(ctx, sourceParams) },
		func() (int64, error) { return qB.NextSourceProjectionRevision(ctx, sourceParams) })

	thumbnailClaim := func(q *Queries, attempt string) (int64, error) {
		if insertErr := q.InsertThumbnailAssignment(ctx, InsertThumbnailAssignmentParams{
			AttemptID: attempt, TenantID: sourceParams.TenantID, AssetKey: "ordering-thumbnail",
			NodeID: "ordering-node", DestinationCluster: "ordering-cluster", Expiry: time.Now().Add(time.Hour),
		}); insertErr != nil {
			return 0, insertErr
		}
		var claim int64
		scanErr := db.QueryRowContext(ctx, `SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id=$1`, attempt).Scan(&claim)
		return claim, scanErr
	}
	assertABA("thumbnail claim",
		func() (int64, error) {
			return thumbnailClaim(qA, "ordering-thumbnail-a-"+time.Now().Format("150405.000000000"))
		},
		func() (int64, error) {
			return thumbnailClaim(qB, "ordering-thumbnail-b-"+time.Now().Format("150405.000000000"))
		})

	const equalAsset = "ordering-thumbnail-equal-claim"
	for _, attempt := range []string{"ordering-equal-attempt-a", "ordering-equal-attempt-b"} {
		if err := qA.InsertThumbnailAssignment(ctx, InsertThumbnailAssignmentParams{
			AttemptID: attempt, TenantID: sourceParams.TenantID, AssetKey: equalAsset,
			NodeID: "ordering-node", DestinationCluster: "ordering-cluster", Expiry: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
UPDATE foghorn.thumbnail_task_assignment
SET claim_seq = (SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id='ordering-equal-attempt-a')
WHERE attempt_id='ordering-equal-attempt-b'`); err != nil {
		t.Fatal(err)
	}
	activate := func(attempt string) int64 {
		t.Helper()
		rows, activateErr := qA.ActivateThumbnailPointer(ctx, ActivateThumbnailPointerParams{
			ActiveVersion: attempt, AssetKey: equalAsset, TenantID: sourceParams.TenantID,
			ActiveToken: sql.NullString{String: "token-" + attempt, Valid: true},
		})
		if activateErr != nil {
			t.Fatal(activateErr)
		}
		return rows
	}
	if rows := activate("ordering-equal-attempt-a"); rows != 1 {
		t.Fatalf("initial equal-claim pointer rows=%d, want 1", rows)
	}
	if rows := activate("ordering-equal-attempt-b"); rows != 0 {
		t.Fatalf("distinct equal claim replaced pointer rows=%d, want 0", rows)
	}
	if rows := activate("ordering-equal-attempt-a"); rows != 1 {
		t.Fatalf("same-attempt equal claim lost idempotency rows=%d, want 1", rows)
	}

	const catalogHash = "orderingcatalogartifact00000001"
	if _, err := connA.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id) VALUES ($1, 'vod', $2::uuid)`, catalogHash, sourceParams.TenantID); err != nil {
		t.Fatal(err)
	}
	var catalog1, catalog2, catalog3 int64
	if err := connA.QueryRowContext(ctx, `SELECT catalog_revision FROM foghorn.artifacts WHERE artifact_hash=$1`, catalogHash).Scan(&catalog1); err != nil {
		t.Fatal(err)
	}
	if err := connB.QueryRowContext(ctx, `UPDATE foghorn.artifacts SET size_bytes=1 WHERE artifact_hash=$1 RETURNING catalog_revision`, catalogHash).Scan(&catalog2); err != nil {
		t.Fatal(err)
	}
	if err := connA.QueryRowContext(ctx, `UPDATE foghorn.artifacts SET size_bytes=2 WHERE artifact_hash=$1 RETURNING catalog_revision`, catalogHash).Scan(&catalog3); err != nil {
		t.Fatal(err)
	}
	if !(catalog1 < catalog2 && catalog2 < catalog3) {
		t.Fatalf("artifact catalog A/B/A revisions = %d, %d, %d", catalog1, catalog2, catalog3)
	}
}

func verifyLegacyOrderingSequencesRemainBelowCounters(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const (
		counterBase  = int64(4503599627370496)
		tenantID     = "10000000-0000-0000-0000-000000000001"
		nodeID       = "legacy-window-node"
		streamName   = "legacy-window-stream"
		thumbnailKey = "legacy-window-thumbnail"
		artifactHash = "legacyseedartifact00000000000000"
	)
	for _, sequence := range []string{
		"node_control_fence_seq",
		"source_projection_revision",
		"thumbnail_attempt_seq",
		"artifact_node_copy_version_seq",
		"artifact_catalog_revision_seq",
	} {
		if _, err := db.ExecContext(ctx, "CREATE SEQUENCE foghorn."+sequence+" START WITH 101"); err != nil {
			t.Fatalf("create legacy sequence %s: %v", sequence, err)
		}
	}
	const restartStream = "legacy-restart-stream"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.ingest_offline_effects (
    tenant_id, stream_internal_name, source_node_id, source_revision
) VALUES ($1::uuid, $2, $3, 150)`, tenantID, restartStream, nodeID); err != nil {
		t.Fatal(err)
	}
	legacyBridge, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.0/expand/004_source_projection_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(legacyBridge)); err != nil {
		t.Fatalf("apply legacy source-revision bridge: %v", err)
	}
	var restartedLegacyRevision int64
	if err := db.QueryRowContext(ctx, `SELECT nextval('foghorn.source_projection_revision')`).Scan(&restartedLegacyRevision); err != nil {
		t.Fatal(err)
	}
	if restartedLegacyRevision <= 150 || restartedLegacyRevision >= counterBase {
		t.Fatalf("legacy source sequence restart=%d, want durable high-water < revision < counter namespace", restartedLegacyRevision)
	}
	var legacyCatalogRevision int64
	if err := db.QueryRowContext(ctx, `SELECT nextval('foghorn.artifact_catalog_revision_seq')`).Scan(&legacyCatalogRevision); err != nil {
		t.Fatal(err)
	}
	if legacyCatalogRevision >= counterBase {
		t.Fatalf("legacy artifact catalog sequence revision=%d entered counter namespace", legacyCatalogRevision)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.node_artifact_report_watermark (node_id, connection_fence, seq)
VALUES ($1, $2, 1)`, nodeID, counterBase+50); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.ingest_offline_effects (
    tenant_id, stream_internal_name, source_node_id, source_revision
) VALUES ($1::uuid, $2, $3, $4)`, tenantID, streamName, nodeID, counterBase+60); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.thumbnail_task_assignment (
    attempt_id, tenant_id, asset_key, node_id, destination_cluster,
    status, version, expiry, durable_backend_local, claim_seq
) VALUES ('legacy-window-thumbnail-seed', $1, $2, $3, 'legacy-window-cluster',
          'assigned', 'legacy-window-thumbnail-seed', NOW()+INTERVAL '1 hour', true, $4)`, tenantID, thumbnailKey, nodeID, counterBase+70); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id)
VALUES ($1, 'vod', $2::uuid)`, artifactHash, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, last_emitted_version)
VALUES ($1, $2, $3)`, artifactHash, nodeID, counterBase+80); err != nil {
		t.Fatal(err)
	}
	migration, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.0/expand/014_key_scoped_ordering_counters.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply key-scoped counter migration: %v", err)
	}
	q := New(db)
	assertSeparated := func(name, sequence string, durableMinimum int64, allocate func() (int64, error)) {
		t.Helper()
		var legacy int64
		if err := db.QueryRowContext(ctx, "SELECT nextval('foghorn."+sequence+"')").Scan(&legacy); err != nil {
			t.Fatalf("%s legacy allocation: %v", name, err)
		}
		counter, allocErr := allocate()
		if allocErr != nil {
			t.Fatalf("%s counter allocation: %v", name, allocErr)
		}
		if legacy >= counterBase || counter <= legacy || counter <= durableMinimum {
			t.Fatalf("%s ordering seed invalid: legacy=%d durable=%d counter=%d", name, legacy, durableMinimum, counter)
		}
	}
	assertSeparated("node fence", "node_control_fence_seq", counterBase+50, func() (int64, error) {
		return q.AllocateNodeControlFence(ctx, nodeID)
	})
	assertSeparated("source revision", "source_projection_revision", counterBase+60, func() (int64, error) {
		return q.NextSourceProjectionRevision(ctx, NextSourceProjectionRevisionParams{
			TenantID: tenantID, StreamInternalName: streamName,
		})
	})
	assertSeparated("artifact copy", "artifact_node_copy_version_seq", counterBase+80, func() (int64, error) {
		return q.AllocateArtifactNodeCopyVersion(ctx, AllocateArtifactNodeCopyVersionParams{
			ArtifactHash: artifactHash, NodeID: nodeID,
		})
	})
	assertSeparated("thumbnail claim", "thumbnail_attempt_seq", counterBase+70, func() (int64, error) {
		const attempt = "legacy-window-thumbnail-attempt"
		if err := q.InsertThumbnailAssignment(ctx, InsertThumbnailAssignmentParams{
			AttemptID: attempt, TenantID: tenantID,
			AssetKey: thumbnailKey, NodeID: nodeID,
			DestinationCluster: "legacy-window-cluster", Expiry: time.Now().Add(time.Hour),
		}); err != nil {
			return 0, err
		}
		var claim int64
		err := db.QueryRowContext(ctx, `SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id=$1`, attempt).Scan(&claim)
		return claim, err
	})
	const catalogHash = "legacycatalogartifact00000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id)
VALUES ($1, 'vod', $2::uuid)`, catalogHash, tenantID); err != nil {
		t.Fatal(err)
	}
	var catalogRevision int64
	if err := db.QueryRowContext(ctx, `SELECT catalog_revision FROM foghorn.artifacts WHERE artifact_hash=$1`, catalogHash).Scan(&catalogRevision); err != nil {
		t.Fatal(err)
	}
	if catalogRevision < counterBase || catalogRevision <= legacyCatalogRevision {
		t.Fatalf("catalog trigger transition: legacy=%d trigger=%d, want trigger in high namespace", legacyCatalogRevision, catalogRevision)
	}
}

func TestFederatedArtifactLifecycleDataMigration_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const artifactHash = "federatedpointermigrationproof01"
	const ownedArtifactHash = "originownedmigrationproof000001"
	const nullAgeArtifactHash = "federatednullagemigrationproof"
	const tenantID = "10000000-0000-0000-0000-000000000001"
	wantEligible := time.Now().UTC().Add(-45 * 24 * time.Hour).Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, origin_cluster_id,
	    catalog_revision, catalog_synced_rev, updated_at
) VALUES
	    ($1, 'vod', $3::uuid, 'active', 'origin-cell', 9, 0, $4),
	    ($2, 'vod', $3::uuid, 'active', 'origin-cell', 9, 0, $4),
	    ($5, 'vod', $3::uuid, 'active', 'origin-cell', 9, 0, NULL)`, artifactHash, ownedArtifactHash, tenantID, wantEligible, nullAgeArtifactHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, role, is_complete)
VALUES ($1, 'origin-node', 'origin', true)`, ownedArtifactHash); err != nil {
		t.Fatal(err)
	}
	result, err := New(db).BackfillFederatedArtifactLifecycleBatch(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var pointer bool
	var revision, synced int64
	var gotEligible time.Time
	if err := db.QueryRowContext(ctx, `
SELECT status, federated_pointer, catalog_revision, catalog_synced_rev, federated_purge_eligible_at
FROM foghorn.artifacts WHERE artifact_hash=$1`, artifactHash).Scan(&status, &pointer, &revision, &synced, &gotEligible); err != nil {
		t.Fatal(err)
	}
	if result.ScannedCount != 2 || result.ChangedCount != 2 || status != "ready" || !pointer || synced != revision || revision <= 0 {
		t.Fatalf("federated pointer migration = scanned:%d changed:%d status:%q pointer:%v revisions:%d/%d", result.ScannedCount, result.ChangedCount, status, pointer, synced, revision)
	}
	if !gotEligible.Equal(wantEligible) {
		t.Fatalf("lifecycle migration reset purge age: got %s, want %s", gotEligible, wantEligible)
	}
	var nullAgeEligible time.Time
	if err := db.QueryRowContext(ctx, `
SELECT federated_purge_eligible_at
FROM foghorn.artifacts WHERE artifact_hash=$1`, nullAgeArtifactHash).Scan(&nullAgeEligible); err != nil {
		t.Fatal(err)
	}
	if nullAgeEligible.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("nullable legacy age did not start at conversion: %s", nullAgeEligible)
	}
	if eligibility, err := New(db).BackfillFederatedPointerPurgeEligibilityBatch(ctx, 100); err != nil {
		t.Fatal(err)
	} else if eligibility.ChangedCount != 0 {
		t.Fatalf("follow-up eligibility migration changed %d freshly converted rows", eligibility.ChangedCount)
	}
	remaining, err := New(db).CountLegacyFederatedArtifactPointers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ownedStatus string
	var ownedPointer bool
	if err := db.QueryRowContext(ctx, `
SELECT status, federated_pointer
FROM foghorn.artifacts WHERE artifact_hash=$1`, ownedArtifactHash).Scan(&ownedStatus, &ownedPointer); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 || ownedStatus != "active" || ownedPointer {
		t.Fatalf("origin-owned row entered pointer migration: remaining=%d status=%q pointer=%v", remaining, ownedStatus, ownedPointer)
	}
}

func TestFederatedPointerPurgeEligibilityDataMigrationPreservesAge_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const artifactHash = "federatedpointerageproof000001"
	const tenantID = "10000000-0000-0000-0000-000000000001"
	wantEligible := time.Now().UTC().Add(-45 * 24 * time.Hour).Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev, updated_at, federated_purge_eligible_at
) VALUES ($1, 'vod', $2::uuid, 'ready', true, 1, 1, $3, NOW())`, artifactHash, tenantID, wantEligible); err != nil {
		t.Fatal(err)
	}
	result, err := New(db).BackfillFederatedPointerPurgeEligibilityBatch(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var gotEligible time.Time
	if err := db.QueryRowContext(ctx, `SELECT federated_purge_eligible_at FROM foghorn.artifacts WHERE artifact_hash=$1`, artifactHash).Scan(&gotEligible); err != nil {
		t.Fatal(err)
	}
	remaining, err := New(db).CountFederatedPointersWithUnnormalizedPurgeEligibility(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.ScannedCount != 1 || result.ChangedCount != 1 || remaining != 0 || !gotEligible.Equal(wantEligible) {
		t.Fatalf("eligibility migration = scanned:%d changed:%d remaining:%d eligible:%s, want %s", result.ScannedCount, result.ChangedCount, remaining, gotEligible, wantEligible)
	}
}

func TestFederatedPointerPurgeEligibilityUsesSessionTimezone_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	const tenantID = "10000000-0000-0000-0000-000000000001"
	for index, zone := range []string{"America/New_York", "Europe/Amsterdam"} {
		t.Run(zone, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback() //nolint:errcheck
			if _, err := tx.ExecContext(ctx, `SELECT set_config('TimeZone', $1, true)`, zone); err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("federatedtimezoneageproof%02d", index)
			freshHash := fmt.Sprintf("federatedtimezonefresh%02d", index)
			if _, err := tx.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev, updated_at, federated_purge_eligible_at
) VALUES
    ($1, 'vod', $2::uuid, 'ready', true, 1, 1, NOW() - INTERVAL '45 days', NOW()),
    ($3, 'vod', $2::uuid, 'ready', true, 1, 1, NOW(), NOW())`, hash, tenantID, freshHash); err != nil {
				t.Fatal(err)
			}
			result, err := New(tx).BackfillFederatedPointerPurgeEligibilityBatch(ctx, 100)
			if err != nil {
				t.Fatal(err)
			}
			var oldSameInstant, freshSameInstant bool
			if err := tx.QueryRowContext(ctx, `
SELECT bool_and(federated_purge_eligible_at = updated_at) FILTER (WHERE artifact_hash=$1),
       bool_and(federated_purge_eligible_at = updated_at) FILTER (WHERE artifact_hash=$2)
FROM foghorn.artifacts WHERE artifact_hash IN ($1, $2)`, hash, freshHash).Scan(&oldSameInstant, &freshSameInstant); err != nil {
				t.Fatal(err)
			}
			remaining, err := New(tx).CountFederatedPointersWithUnnormalizedPurgeEligibility(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if result.ScannedCount != 1 || result.ChangedCount != 1 || !oldSameInstant || !freshSameInstant || remaining != 0 {
				t.Fatalf("zone %s: scanned=%d changed=%d old_same=%v fresh_same=%v remaining=%d",
					zone, result.ScannedCount, result.ChangedCount, oldSameInstant, freshSameInstant, remaining)
			}
		})
	}
}

func TestPurgeableArtifactsIncludeOwnedChaptersAndExcludeFederatedPointers_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev, updated_at
) VALUES
    ('ownedchapter0000000000000000001', 'chapter', $1::uuid, 'deleted', false, 2, 2, NOW() - INTERVAL '2 hours'),
    ('federatedchapter00000000000001', 'chapter', $1::uuid, 'deleted', true, 2, 2, NOW() - INTERVAL '2 hours')`, tenantID); err != nil {
		t.Fatal(err)
	}
	// The catalog trigger raises inserted revisions into the v0.3 namespace. Model
	// confirmed projection using the actual assigned revision, not the pre-v0.3 fixture value.
	if _, err := db.ExecContext(ctx, `UPDATE foghorn.artifacts SET catalog_synced_rev=catalog_revision WHERE tenant_id=$1::uuid`, tenantID); err != nil {
		t.Fatal(err)
	}
	rows, err := New(db).ListPurgeableArtifacts(ctx, "1h")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ArtifactHash != "ownedchapter0000000000000000001" || rows[0].ArtifactType != "chapter" {
		t.Fatalf("purgeable artifacts = %+v, want only the owned chapter", rows)
	}
}

func TestFederatedPointerPurgeRetainsSignedTombstoneFence_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const hash = "federatedtombstone0000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.media_object_authority_projection (
    authority_id, authority_version, object_kind, tenant_id, internal_name, playback_id,
    lifecycle, playback_policy_kind, playback_policy, artifact_id, artifact_hash,
    artifact_kind, valid_until
) VALUES ('artifact-tombstone', 7, 'artifact', $1::uuid, $2, 'deleted-playback',
	      'tombstone', 'deny', '\x'::bytea, $2, $2, 'chapter', NOW() + INTERVAL '1 day')`, tenantID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev, updated_at, federated_purge_eligible_at
) VALUES ($2, 'vod', $1::uuid, 'deleted', true, 4, 3,
          NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours')`, tenantID, hash); err != nil {
		t.Fatal(err)
	}
	queries := New(db)
	listed, err := queries.ListTombstonedFederatedArtifactPointersForPurge(ctx, "1h")
	if err != nil || len(listed) != 1 || listed[0].ArtifactHash != hash {
		t.Fatalf("list tombstoned pointer = rows:%+v err:%v", listed, err)
	}
	n, err := queries.FenceTombstonedFederatedArtifactPointerForPurge(ctx, FenceTombstonedFederatedArtifactPointerForPurgeParams{
		PurgeToken: "11111111-1111-1111-1111-111111111111", LeaseInterval: "3m",
		ArtifactHash: hash, TenantID: tenantID, RetentionInterval: "1h",
	})
	if err != nil || n != 1 {
		t.Fatalf("fence pointer = rows:%d err:%v", n, err)
	}
	n, err = queries.DeleteFencedFederatedArtifactPointer(ctx, DeleteFencedFederatedArtifactPointerParams{
		ArtifactHash: hash, TenantID: tenantID, PurgeToken: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil || n != 1 {
		t.Fatalf("delete fenced pointer = rows:%d err:%v", n, err)
	}
	var artifactRows, tombstoneRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&artifactRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.media_object_authority_projection WHERE artifact_hash=$1 AND lifecycle='tombstone'`, hash).Scan(&tombstoneRows); err != nil {
		t.Fatal(err)
	}
	if artifactRows != 0 || tombstoneRows != 1 {
		t.Fatalf("after purge artifact_rows=%d tombstone_rows=%d, want 0/1", artifactRows, tombstoneRows)
	}
	if n, err := New(db).AdoptRemoteArtifact(ctx, AdoptRemoteArtifactParams{
		ArtifactHash: hash, ArtifactType: "vod", TenantID: tenantID, InternalName: hash,
		OriginClusterID: "origin-cell", SyncStatus: "synced",
	}); err != nil || n != 0 {
		t.Fatalf("stale adoption across tombstone = rows:%d err:%v", n, err)
	}
}

func TestTombstoneDuringFederatedPointerPurgePreservesRecoveryClock_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const hash = "tombstone-held-token-clock"
	const token = "41111111-1111-1111-1111-111111111111"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.media_object_authority_projection (
    authority_id, authority_version, object_kind, tenant_id, internal_name, playback_id,
    lifecycle, playback_policy_kind, playback_policy, artifact_id, artifact_hash,
    artifact_kind, valid_until
) VALUES ('held-token-tombstone', 9, 'artifact', $1::uuid, $2, 'deleted-playback',
          'tombstone', 'deny', '\x'::bytea, $2, $2, 'vod', NOW() + INTERVAL '1 day')`, tenantID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    federated_purge_token, federated_purge_lease_until,
    catalog_revision, catalog_synced_rev, updated_at, federated_purge_eligible_at
) VALUES ($2, 'vod', $1::uuid, 'deleted', true,
          $3::uuid, NOW() + INTERVAL '3 minutes', 1, 1,
          NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours')`, tenantID, hash, token); err != nil {
		t.Fatal(err)
	}
	var beforeUpdated, beforeEligible time.Time
	if err := db.QueryRowContext(ctx, `SELECT updated_at, federated_purge_eligible_at FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&beforeUpdated, &beforeEligible); err != nil {
		t.Fatal(err)
	}
	queries := New(db)
	rows, err := queries.TombstoneFederatedArtifact(ctx, TombstoneFederatedArtifactParams{ArtifactHash: hash, TenantID: tenantID})
	if err != nil || rows != 0 {
		t.Fatalf("tombstone around held purge token: rows=%d err=%v, want guarded no-op", rows, err)
	}
	var afterUpdated, afterEligible time.Time
	if err := db.QueryRowContext(ctx, `SELECT updated_at, federated_purge_eligible_at FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&afterUpdated, &afterEligible); err != nil {
		t.Fatal(err)
	}
	if !afterUpdated.Equal(beforeUpdated) || !afterEligible.Equal(beforeEligible) {
		t.Fatalf("tombstone reset purge clocks: updated=%s/%s eligible=%s/%s", beforeUpdated, afterUpdated, beforeEligible, afterEligible)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE foghorn.artifacts
SET federated_purge_lease_until = NOW() - INTERVAL '1 second'
WHERE artifact_hash=$1`, hash); err != nil {
		t.Fatal(err)
	}
	recoverable, err := queries.ListRecoverableFederatedArtifactPointerPurges(ctx, "1h")
	if err != nil {
		t.Fatal(err)
	}
	if len(recoverable) != 1 || recoverable[0].ArtifactHash != hash || recoverable[0].PurgeKind != "tombstone" {
		t.Fatalf("recoverable pointer = %+v, want held tombstone on the next recovery pass", recoverable)
	}
}

func TestFederatedPointerEligibilityIgnoresOrdinaryMetadataWriters_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const hash = "stable-pointer-purge-clock"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    sync_status, has_thumbnails, catalog_revision, catalog_synced_rev,
    updated_at, federated_purge_eligible_at
) VALUES ($1, 'vod', $2::uuid, 'ready', true, 'synced', false, 1, 1,
          NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours')`, hash, tenantID); err != nil {
		t.Fatal(err)
	}
	q := New(db)
	if err := q.UpdateArtifactReportMetadata(ctx, UpdateArtifactReportMetadataParams{
		ArtifactHash: hash, StreamInternalName: "reported-stream", AccessCount: 9,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkArtifactS3OnlyWhenUnhosted(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkArtifactThumbnailPresent(ctx, hash); err != nil {
		t.Fatal(err)
	}
	var updatedAt, eligibleAt time.Time
	if err := db.QueryRowContext(ctx, `
SELECT updated_at, federated_purge_eligible_at
FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&updatedAt, &eligibleAt); err != nil {
		t.Fatal(err)
	}
	if !updatedAt.After(eligibleAt.Add(time.Hour)) {
		t.Fatalf("metadata clock did not advance independently: updated=%s eligible=%s", updatedAt, eligibleAt)
	}
	listed, err := q.ListStaleFederatedArtifactPointersForPurge(ctx, "1h")
	if err != nil || len(listed) != 1 || listed[0].ArtifactHash != hash {
		t.Fatalf("metadata writers changed purge eligibility: rows=%+v err=%v", listed, err)
	}
}

func TestFederatedPointerFenceSerializesWithAuthorityProjection_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const hash = "authority-before-stale-fence"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev, updated_at, federated_purge_eligible_at
) VALUES ($2, 'vod', $1::uuid, 'ready', true, 1, 1,
          NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours')`, tenantID, hash); err != nil {
		t.Fatal(err)
	}

	authorityTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer authorityTx.Rollback() //nolint:errcheck
	authorityQueries := New(authorityTx)
	if err := authorityQueries.LockThumbnailAsset(ctx, LockThumbnailAssetParams{
		LockNamespace: artifacts.ThumbnailAssetLockNamespace, AssetKey: hash,
	}); err != nil {
		t.Fatal(err)
	}
	if err := authorityQueries.UpsertMediaObjectAuthorityProjection(ctx, UpsertMediaObjectAuthorityProjectionParams{
		AuthorityID: "authority-wins-snapshot-race", AuthorityVersion: 1, ObjectKind: "artifact",
		TenantID: tenantID, InternalName: hash, PlaybackID: "authority-before-fence-playback",
		Lifecycle: "active", PlaybackPolicyKind: "public", PlaybackPolicy: []byte{},
		ArtifactID: hash, ArtifactHash: hash, ArtifactKind: "vod", ValidUntil: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	type fenceResult struct {
		rows int64
		err  error
	}
	fenced := make(chan fenceResult, 1)
	go func() {
		tx, beginErr := db.BeginTx(ctx, nil)
		if beginErr != nil {
			fenced <- fenceResult{err: beginErr}
			return
		}
		defer tx.Rollback() //nolint:errcheck
		q := New(tx)
		if lockErr := q.LockThumbnailAsset(ctx, LockThumbnailAssetParams{
			LockNamespace: artifacts.ThumbnailAssetLockNamespace, AssetKey: hash,
		}); lockErr != nil {
			fenced <- fenceResult{err: lockErr}
			return
		}
		rows, fenceErr := q.FenceStaleFederatedArtifactPointerForPurge(ctx, FenceStaleFederatedArtifactPointerForPurgeParams{
			PurgeToken: "42222222-2222-2222-2222-222222222222", LeaseInterval: "3m",
			ArtifactHash: hash, TenantID: tenantID, RetentionInterval: "1h",
		})
		if fenceErr == nil {
			fenceErr = tx.Commit()
		}
		fenced <- fenceResult{rows: rows, err: fenceErr}
	}()
	select {
	case result := <-fenced:
		t.Fatalf("fence did not wait for authority transaction: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}
	if err := authorityTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-fenced:
		if result.err != nil || result.rows != 0 {
			t.Fatalf("fence after active authority commit: rows=%d err=%v, want guarded no-op", result.rows, result.err)
		}
	case <-ctx.Done():
		t.Fatal("fence did not resume after authority commit")
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ready" {
		t.Fatalf("stale fence terminalized pointer after active authority commit: status=%q", status)
	}
}

func TestFailedFederatedPointerCleanupRemainsFencedAndReclaimable_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const hash = "stale-restore-retry-backoff"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev, updated_at, federated_purge_eligible_at
) VALUES ($1, 'vod', $2::uuid, 'ready', true, 1, 1,
          NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours')`, hash, tenantID); err != nil {
		t.Fatal(err)
	}
	queries := New(db)
	const firstToken = "21111111-1111-1111-1111-111111111111"
	if rows, err := queries.FenceStaleFederatedArtifactPointerForPurge(ctx, FenceStaleFederatedArtifactPointerForPurgeParams{
		PurgeToken: firstToken, LeaseInterval: "3m",
		ArtifactHash: hash, TenantID: tenantID, RetentionInterval: "1h",
	}); err != nil || rows != 1 {
		t.Fatalf("fence stale pointer: rows=%d err=%v", rows, err)
	}
	if rows, err := queries.ReleaseFederatedArtifactPointerPurgeClaim(ctx, ReleaseFederatedArtifactPointerPurgeClaimParams{
		ArtifactHash: hash, TenantID: tenantID, PurgeToken: firstToken,
	}); err != nil || rows != 1 {
		t.Fatalf("release failed cleanup claim: rows=%d err=%v", rows, err)
	}
	var status string
	var reclaimable bool
	if err := db.QueryRowContext(ctx, `
SELECT status, federated_purge_lease_until <= NOW()
FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&status, &reclaimable); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || !reclaimable {
		t.Fatalf("failed cleanup pointer status=%q reclaimable=%v, want deleted/true", status, reclaimable)
	}
	if rows, err := queries.FenceStaleFederatedArtifactPointerForPurge(ctx, FenceStaleFederatedArtifactPointerForPurgeParams{
		PurgeToken: "22222222-2222-2222-2222-222222222222", LeaseInterval: "3m",
		ArtifactHash: hash, TenantID: tenantID, RetentionInterval: "1h",
	}); err != nil || rows != 1 {
		t.Fatalf("immediate stale-pointer reclaim: rows=%d err=%v, want 1", rows, err)
	}
}

func TestActiveAuthorityRestoresOnlyInterruptedStalePointerFence_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const staleHash = "stale-fence-authority-restore"
	const tombstonedHash = "signed-tombstone-stays-deleted"
	const claimedHash = "active-authority-awaits-purge"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev
	) VALUES
	    ($2, 'vod', $1::uuid, 'deleted', true, 1, 1),
	    ($3, 'vod', $1::uuid, 'deleted', true, 1, 1),
	    ($4, 'vod', $1::uuid, 'deleted', true, 1, 1)`, tenantID, staleHash, tombstonedHash, claimedHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE foghorn.artifacts
SET federated_purge_token='31111111-1111-1111-1111-111111111111'::uuid,
    federated_purge_lease_until=NOW() + INTERVAL '3 minutes'
WHERE artifact_hash=$1`, claimedHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.media_object_authority_projection (
    authority_id, authority_version, object_kind, tenant_id, internal_name, playback_id,
    lifecycle, playback_policy_kind, playback_policy, artifact_id, artifact_hash,
    artifact_kind, valid_until
) VALUES (
    'signed-delete-other', 2, 'artifact', $1::uuid, $2, 'signed-delete-playback',
    'tombstone', 'deny', '\x'::bytea, $2, $2, 'vod', NOW() + INTERVAL '1 day'
)`, tenantID, tombstonedHash); err != nil {
		t.Fatal(err)
	}
	queries := New(db)
	applyActive := func(authorityID, hash, playbackID string) {
		t.Helper()
		if err := queries.UpsertMediaObjectAuthorityProjection(ctx, UpsertMediaObjectAuthorityProjectionParams{
			AuthorityID: authorityID, AuthorityVersion: 3, ObjectKind: "artifact", TenantID: tenantID,
			InternalName: hash, PlaybackID: playbackID, Lifecycle: "active",
			PlaybackPolicyKind: "public", PlaybackPolicy: []byte{}, ArtifactID: hash,
			ArtifactHash: hash, ArtifactKind: "vod", ValidUntil: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("apply active authority for %s: %v", hash, err)
		}
	}
	applyActive("active-stale-restore", staleHash, "stale-restore-playback")
	applyActive("active-beside-tombstone", tombstonedHash, "active-beside-tombstone-playback")
	applyActive("active-during-purge", claimedHash, "active-during-purge-playback")
	if rows, err := queries.InsertMigratedArtifactMetadata(ctx, InsertMigratedArtifactMetadataParams{
		ArtifactHash: claimedHash, ArtifactType: "vod", TenantID: tenantID,
		InternalName: sql.NullString{String: claimedHash, Valid: true},
	}); err != nil || rows != 0 {
		t.Fatalf("metadata insert around purge claim: rows=%d err=%v, want fenced", rows, err)
	}
	if err := queries.FillMigratedArtifactMetadata(ctx, FillMigratedArtifactMetadataParams{
		ArtifactHash: claimedHash, ArtifactType: "vod", TenantID: tenantID,
		InternalName: claimedHash,
	}); err != nil {
		t.Fatalf("metadata fill around purge claim: %v", err)
	}

	var staleStatus, tombstonedStatus, claimedStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, staleHash).Scan(&staleStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, tombstonedHash).Scan(&tombstonedStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, claimedHash).Scan(&claimedStatus); err != nil {
		t.Fatal(err)
	}
	if staleStatus != "ready" || tombstonedStatus != "deleted" || claimedStatus != "deleted" {
		t.Fatalf("authority fence recovery = stale:%q tombstoned:%q claimed:%q, want ready/deleted/deleted", staleStatus, tombstonedStatus, claimedStatus)
	}
}

func TestFederatedPointersAreCacheOnlyForCapacityAndStalePurge_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    size_bytes, catalog_revision, catalog_synced_rev, updated_at, federated_purge_eligible_at
) VALUES
	    ('owned-capacity-bytes', 'vod', $1::uuid, 'ready', false, 100, 1, 1, NOW(), NOW()),
	    ('stale-pointer-bytes', 'vod', $1::uuid, 'ready', true, 900, 1, 1, NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours'),
	    ('stuck-upload-pointer', 'vod', $1::uuid, 'uploading', true, 700, 1, 1, NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours'),
	    ('authorized-pointer', 'vod', $1::uuid, 'ready', true, 800, 1, 1, NOW() - INTERVAL '2 hours', NOW() - INTERVAL '2 hours')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.media_object_authority_projection (
    authority_id, authority_version, object_kind, tenant_id, internal_name, playback_id,
    lifecycle, playback_policy_kind, playback_policy, artifact_id, artifact_hash,
    artifact_kind, valid_until
) VALUES ('authorized-pointer', 1, 'artifact', $1::uuid, 'authorized-pointer',
          'authorized-pointer-playback', 'active', 'public', '\x'::bytea,
          'authorized-pointer', 'authorized-pointer', 'vod', NOW() + INTERVAL '1 hour')`, tenantID); err != nil {
		t.Fatal(err)
	}
	// Re-adoption repairs routing metadata but is not an authority lease and must not refresh the
	// stale-pointer clock. Playback activity cannot extend a hard-expired signed decision.
	if rows, err := New(db).AdoptRemoteArtifact(ctx, AdoptRemoteArtifactParams{
		ArtifactHash: "stale-pointer-bytes", ArtifactType: "vod", TenantID: tenantID,
		InternalName: "stale-pointer-bytes", OriginClusterID: "origin-cell", SyncStatus: "synced",
	}); err != nil || rows != 1 {
		t.Fatalf("re-adopt stale pointer: rows=%d err=%v", rows, err)
	}
	bytes, err := New(db).SumTenantActiveArtifactBytes(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 100 {
		t.Fatalf("active storage bytes = %d, want only 100 locally owned bytes", bytes)
	}
	queries := New(db)
	listed, err := queries.ListStaleFederatedArtifactPointersForPurge(ctx, "1h")
	if err != nil || len(listed) != 1 || listed[0].ArtifactHash != "stale-pointer-bytes" {
		t.Fatalf("stale pointer candidates = %+v err:%v, want one unauthorized cache row", listed, err)
	}
	if rows, err := queries.FenceStaleFederatedArtifactPointerForPurge(ctx, FenceStaleFederatedArtifactPointerForPurgeParams{
		PurgeToken: "31111111-1111-1111-1111-111111111111", LeaseInterval: "3m",
		ArtifactHash: "stuck-upload-pointer", TenantID: tenantID, RetentionInterval: "1h",
	}); err != nil || rows != 0 {
		t.Fatalf("stuck upload stale fence = rows:%d err:%v, want untouched", rows, err)
	}
	const purgeToken = "32222222-2222-2222-2222-222222222222"
	purged, err := queries.FenceStaleFederatedArtifactPointerForPurge(ctx, FenceStaleFederatedArtifactPointerForPurgeParams{
		PurgeToken: purgeToken, LeaseInterval: "3m",
		ArtifactHash: "stale-pointer-bytes", TenantID: tenantID, RetentionInterval: "1h",
	})
	if err != nil || purged != 1 {
		t.Fatalf("stale pointer fence = rows:%d err:%v", purged, err)
	}
	if rows, err := queries.AdoptRemoteArtifact(ctx, AdoptRemoteArtifactParams{
		ArtifactHash: "stale-pointer-bytes", ArtifactType: "vod", TenantID: tenantID,
		InternalName: "stale-pointer-bytes", OriginClusterID: "origin-cell", SyncStatus: "synced",
	}); err != nil || rows != 0 {
		t.Fatalf("re-adopt terminal pointer during purge = rows:%d err:%v, want fenced", rows, err)
	}
	purged, err = queries.DeleteFencedFederatedArtifactPointer(ctx, DeleteFencedFederatedArtifactPointerParams{
		ArtifactHash: "stale-pointer-bytes", TenantID: tenantID, PurgeToken: purgeToken,
	})
	if err != nil || purged != 1 {
		t.Fatalf("stale pointer delete = rows:%d err:%v", purged, err)
	}
	var staleRows, authorizedRows int
	var uploadStatus string
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.artifacts WHERE artifact_hash='stale-pointer-bytes'`).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.artifacts WHERE artifact_hash='authorized-pointer'`).Scan(&authorizedRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash='stuck-upload-pointer'`).Scan(&uploadStatus); err != nil {
		t.Fatal(err)
	}
	if staleRows != 0 || authorizedRows != 1 || uploadStatus != "uploading" {
		t.Fatalf("post-purge stale=%d authorized=%d upload=%q, want 0/1/uploading", staleRows, authorizedRows, uploadStatus)
	}
}

func TestFederatedPointersCannotEnterOwnerDeletionPaths_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, status, federated_pointer,
    catalog_revision, catalog_synced_rev
	) VALUES
	    ('pointer-delete-clip', 'clip', $1::uuid, 'ready', true, 1, 1),
	    ('pointer-delete-dvr', 'dvr', $1::uuid, 'ready', true, 1, 1),
	    ('pointer-delete-vod', 'vod', $1::uuid, 'ready', true, 1, 1),
	    ('pointer-delete-chapter', 'vod', $1::uuid, 'processing', true, 1, 1),
	    ('owned-list-vod', 'vod', $1::uuid, 'ready', false, 1, 1)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE foghorn.artifacts SET origin_type='dvr_chapter'
WHERE artifact_hash='pointer-delete-chapter';
UPDATE foghorn.artifacts
SET status='recording', stream_internal_name='pointer-stream', dvr_start_dispatch='{}'::jsonb
WHERE artifact_hash='pointer-delete-dvr';
INSERT INTO foghorn.dvr_chapters (
    chapter_id, artifact_hash, mode, start_ms, end_ms, is_current, state,
    playback_artifact_hash, frozen_at
) VALUES
    ('pointer-closed-chapter', 'pointer-delete-dvr', 'fixed_interval', 0, 1000,
     true, 'closed', 'pointer-delete-chapter', NULL),
    ('pointer-frozen-chapter', 'pointer-delete-dvr', 'fixed_interval', 1000, 2000,
     false, 'frozen', NULL, NOW() - INTERVAL '1 hour')`); err != nil {
		t.Fatal(err)
	}

	queries := New(db)
	if _, err := queries.GetClipForDeletion(ctx, GetClipForDeletionParams{
		ArtifactHash: "pointer-delete-clip", TenantID: tenantID,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("federated clip deletion lookup err=%v, want sql.ErrNoRows", err)
	}
	if _, err := queries.GetDVRForDeletion(ctx, GetDVRForDeletionParams{
		ArtifactHash: "pointer-delete-dvr", TenantID: tenantID,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("federated DVR deletion lookup err=%v, want sql.ErrNoRows", err)
	}
	if _, err := queries.GetDVRStartDispatch(ctx, GetDVRStartDispatchParams{
		ArtifactHash: "pointer-delete-dvr", TenantID: tenantID,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("federated DVR dispatch lookup err=%v, want sql.ErrNoRows", err)
	}
	if _, err := queries.FindActiveDVRForStream(ctx, FindActiveDVRForStreamParams{
		StreamInternalName: sql.NullString{String: "pointer-stream", Valid: true}, TenantID: tenantID,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("federated active-DVR lookup err=%v, want sql.ErrNoRows", err)
	}
	if _, err := queries.GetVodForDeletion(ctx, GetVodForDeletionParams{
		ArtifactHash: "pointer-delete-vod", TenantID: tenantID,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("federated VOD deletion lookup err=%v, want sql.ErrNoRows", err)
	}

	if rows, err := queries.DeleteClipCatalog(ctx, DeleteClipCatalogParams{
		ArtifactHash: "pointer-delete-clip", TenantID: tenantID,
	}); err != nil || rows != 0 {
		t.Fatalf("federated clip deletion rows=%d err=%v, want 0", rows, err)
	}
	if rows, err := queries.SoftDeleteDVRParent(ctx, SoftDeleteDVRParentParams{
		ArtifactHash: "pointer-delete-dvr", TenantID: tenantID,
	}); err != nil || rows != 0 {
		t.Fatalf("federated DVR deletion rows=%d err=%v, want 0", rows, err)
	}
	if rows, err := queries.SoftDeleteVodArtifact(ctx, SoftDeleteVodArtifactParams{
		ArtifactHash: "pointer-delete-vod", TenantID: tenantID,
	}); err != nil || rows != 0 {
		t.Fatalf("federated VOD deletion rows=%d err=%v, want 0", rows, err)
	}
	if hashes, err := queries.SoftDeleteDVRChapterArtifacts(ctx, SoftDeleteDVRChapterArtifactsParams{
		ArtifactHash: "pointer-delete-dvr", TenantID: tenantID,
	}); err != nil || len(hashes) != 0 {
		t.Fatalf("federated DVR child deletion hashes=%v err=%v, want none", hashes, err)
	}
	if err := queries.DeleteDVRChapterRowsForTenant(ctx, DeleteDVRChapterRowsForTenantParams{
		ArtifactHash: "pointer-delete-dvr", TenantID: tenantID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.GetDVRParentArtifactStatus(ctx, "pointer-delete-dvr"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("federated DVR parent-status lookup err=%v, want sql.ErrNoRows", err)
	}
	finalizeRows, err := queries.ListDVRChaptersNeedingFinalization(ctx, ListDVRChaptersNeedingFinalizationParams{
		Secs: 1, Secs_2: 60, Limit: 100,
	})
	if err != nil || len(finalizeRows) != 0 {
		t.Fatalf("federated finalization candidates=%v err=%v, want none", finalizeRows, err)
	}
	reclaimRows, err := queries.ListDVRChaptersNeedingReclaim(ctx, ListDVRChaptersNeedingReclaimParams{Secs: 1, Limit: 100})
	if err != nil || len(reclaimRows) != 0 {
		t.Fatalf("federated reclaim candidates=%v err=%v, want none", reclaimRows, err)
	}
	if rows, err := queries.ClearCurrentChaptersForInactiveDVRs(ctx); err != nil || rows != 0 {
		t.Fatalf("federated inactive-chapter cleanup rows=%d err=%v, want 0", rows, err)
	}
	if _, err := queries.FailDVRChapterArtifact(ctx, FailDVRChapterArtifactParams{
		ArtifactHash: "pointer-delete-chapter", ErrorMessage: sql.NullString{String: "test", Valid: true},
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("federated chapter failure mutation err=%v, want sql.ErrNoRows", err)
	}

	listed, err := queries.ListFederatedTenantArtifacts(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ArtifactHash != "owned-list-vod" {
		t.Fatalf("tenant artifact export = %+v, want only locally owned artifact", listed)
	}
	var pointerRows, deletedPointers int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(*) FILTER (WHERE status='deleted')
FROM foghorn.artifacts WHERE federated_pointer=true`).Scan(&pointerRows, &deletedPointers); err != nil {
		t.Fatal(err)
	}
	if pointerRows != 4 || deletedPointers != 0 {
		t.Fatalf("federated pointers after owner deletion attempts: rows=%d deleted=%d", pointerRows, deletedPointers)
	}
	var chapterRows, currentRows int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(*) FILTER (WHERE is_current)
FROM foghorn.dvr_chapters WHERE artifact_hash='pointer-delete-dvr'`).Scan(&chapterRows, &currentRows); err != nil {
		t.Fatal(err)
	}
	if chapterRows != 2 || currentRows != 1 {
		t.Fatalf("federated chapter ledger mutated: rows=%d current=%d, want 2/1", chapterRows, currentRows)
	}
	if _, err := db.ExecContext(ctx, `UPDATE foghorn.artifacts SET status='deleted' WHERE artifact_hash='pointer-delete-dvr'`); err != nil {
		t.Fatal(err)
	}
	deletedParents, err := queries.ListDeletedDVRParentsWithChapters(ctx, 100)
	if err != nil || len(deletedParents) != 0 {
		t.Fatalf("federated deleted-parent repair candidates=%v err=%v, want none", deletedParents, err)
	}
}

func TestMintArtifactShellRemainsRemoteParentPointer_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const hash = "mint-shell-remote-parent"
	if err := New(db).InsertMintArtifactShell(ctx, InsertMintArtifactShellParams{
		ArtifactHash: hash, ArtifactType: "vod", TenantID: tenantID,
		InternalName:    sql.NullString{String: "remote-parent", Valid: true},
		OriginClusterID: sql.NullString{String: "origin-cell", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	var pointer bool
	var storageLocation string
	if err := db.QueryRowContext(ctx, `SELECT federated_pointer, storage_location FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&pointer, &storageLocation); err != nil {
		t.Fatal(err)
	}
	if !pointer || storageLocation != "pending" {
		t.Fatalf("mint shell pointer=%v storage_location=%q, want remote-parent pointer/pending", pointer, storageLocation)
	}
}

func TestFoghornGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	prepareFoghornCatalog(t, startFoghornCatalogYugabyte(t))
}

func TestConfigSeedApplyAckOutboxSameVersionReplacement_RealYugabyte(t *testing.T) {
	verifyConfigSeedApplyAckOutboxSameVersionReplacement(t, startFoghornCatalogYugabyte(t))
}

func TestSourceProjectionRevisionMigrationSeedsDurableHighWater_RealYugabyte(t *testing.T) {
	verifySourceProjectionRevisionMigrationSeedsDurableHighWater(t, startFoghornCatalogYugabyte(t))
}

func TestSourceProjectionRepairAllocator_RealYugabyte(t *testing.T) {
	db := startFoghornCatalogYugabyte(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const internalName = "repair-yb"
	repairRevision, err := AllocateSourceProjectionRevisionAfter(ctx, db, tenantID, internalName, 1000)
	if err != nil {
		t.Fatal(err)
	}
	nextRevision, err := New(db).NextSourceProjectionRevision(ctx, NextSourceProjectionRevisionParams{TenantID: tenantID, StreamInternalName: internalName})
	if err != nil {
		t.Fatal(err)
	}
	if repairRevision <= 1000 || nextRevision <= repairRevision {
		t.Fatalf("repair revision=%d successor=%d, want ordered values above 1000", repairRevision, nextRevision)
	}
}

func TestSourceProjectionAllocatorKeyScoped_RealYugabyte(t *testing.T) {
	verifySourceProjectionAllocatorKeyScoped(t, startFoghornCatalogYugabyte(t))
}

func TestKeyScopedOrderingAllocators_RealYugabyte(t *testing.T) {
	verifyKeyScopedOrderingAllocators(t, startFoghornCatalogYugabyte(t))
}

func TestLegacyOrderingSequencesRemainBelowCounters_RealYugabyte(t *testing.T) {
	verifyLegacyOrderingSequencesRemainBelowCounters(t, startFoghornCatalogYugabyte(t))
}

func TestArtifactNodePlacementSerializesAbsentRows_RealPG(t *testing.T) {
	verifyArtifactNodePlacementSerialization(t, startFoghornCatalogPostgres(t))
}

func TestArtifactDeletionRejectsReplayOlderThanPlacement_RealPG(t *testing.T) {
	verifyArtifactDeletionRejectsReplayOlderThanPlacement(t, startFoghornCatalogPostgres(t))
}

func TestMediaAuthorityLookupIndexes_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.media_authorities (
    authority_kind, authority_id, authority_version, signer_key_id, audience_cell_id,
    issued_at, refresh_after, valid_until, payload_sha256, signed_envelope, payload
)
SELECT 'media_object', 'object-' || n, 1, 'test-key', 'test-cell',
       NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 hour', NOW() + INTERVAL '2 hours',
       decode(repeat('00', 32), 'hex'), '\x00'::bytea, '\x00'::bytea
FROM generate_series(1, 20000) AS n;

INSERT INTO foghorn.media_object_authority_projection (
    authority_id, authority_version, object_kind, tenant_id, internal_name, playback_id,
    lifecycle, playback_policy_kind, playback_policy, stream_id, ingest_mode, valid_until
)
SELECT 'object-' || n, 1, 'live_stream', md5('tenant-' || n)::uuid,
       'live+stream-' || n, 'playback-' || n, 'active', 'public', '\x'::bytea,
       md5('stream-' || n)::uuid, 'rtmp', NOW() + INTERVAL '2 hours'
FROM generate_series(1, 20000) AS n;

ANALYZE foghorn.media_object_authority_projection;
ANALYZE foghorn.media_authorities;
`); err != nil {
		t.Fatal(err)
	}

	playbackPlan := explainPlan(t, ctx, db, `
SELECT authority.payload
FROM foghorn.media_object_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'media_object'
 AND authority.authority_id = projection.authority_id
 AND authority.authority_version = projection.authority_version
WHERE lower(projection.playback_id) = lower('playback-19999')
ORDER BY (projection.lifecycle = 'active') DESC, projection.authority_version DESC
LIMIT 1`)
	if !strings.Contains(playbackPlan, "idx_media_object_authority_playback_id") {
		t.Fatalf("playback authority lookup did not use its non-partial index:\n%s", playbackPlan)
	}

	internalNamePlan := explainPlan(t, ctx, db, `
SELECT authority.payload
FROM foghorn.media_object_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'media_object'
 AND authority.authority_id = projection.authority_id
 AND authority.authority_version = projection.authority_version
WHERE projection.internal_name = 'live+stream-19999'
ORDER BY (projection.lifecycle = 'active') DESC, projection.authority_version DESC
LIMIT 1`)
	if !strings.Contains(internalNamePlan, "idx_media_object_authority_internal_name") {
		t.Fatalf("internal-name authority lookup did not use its non-partial index:\n%s", internalNamePlan)
	}
}

func TestPushTargetStatusRejectsOlderEvent_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(db)
	targetID := "10000000-0000-0000-0000-000000000001"
	tenantID := "20000000-0000-0000-0000-000000000002"
	if err := queries.EnqueuePushTargetStatus(ctx, EnqueuePushTargetStatusParams{
		TargetID: targetID, TenantID: tenantID, Status: "pushing", EventUnixMillis: 100,
	}); err != nil {
		t.Fatal(err)
	}
	lastError := sql.NullString{String: "connection refused", Valid: true}
	if err := queries.EnqueuePushTargetStatus(ctx, EnqueuePushTargetStatusParams{
		TargetID: targetID, TenantID: tenantID, Status: "failed", LastError: lastError, EventUnixMillis: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.EnqueuePushTargetStatus(ctx, EnqueuePushTargetStatusParams{
		TargetID: targetID, TenantID: tenantID, Status: "pushing", EventUnixMillis: 150,
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var storedError sql.NullString
	var eventUnixMillis int64
	if err := db.QueryRowContext(ctx, `
SELECT status, last_error, event_unix_millis
FROM foghorn.push_target_status_outbox
WHERE target_id = $1::uuid`, targetID).Scan(&status, &storedError, &eventUnixMillis); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || storedError != lastError || eventUnixMillis != 200 {
		t.Fatalf("status fence = (%q, %+v, %d), want failed event 200", status, storedError, eventUnixMillis)
	}
}

func TestPushTargetStatusUnknownEventTimeUsesArrivalOrder_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := New(db)
	targetID := "10000000-0000-0000-0000-000000000003"
	tenantID := "20000000-0000-0000-0000-000000000004"
	lastError := sql.NullString{String: "connection refused", Valid: true}
	if err := queries.EnqueuePushTargetStatus(ctx, EnqueuePushTargetStatusParams{
		TargetID: targetID, TenantID: tenantID, Status: "failed", LastError: lastError,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.EnqueuePushTargetStatus(ctx, EnqueuePushTargetStatusParams{
		TargetID: targetID, TenantID: tenantID, Status: "pushing",
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var storedError sql.NullString
	var eventUnixMillis int64
	if err := db.QueryRowContext(ctx, `
SELECT status, last_error, event_unix_millis
FROM foghorn.push_target_status_outbox
WHERE target_id = $1::uuid`, targetID).Scan(&status, &storedError, &eventUnixMillis); err != nil {
		t.Fatal(err)
	}
	if status != "pushing" || storedError.Valid || eventUnixMillis != 0 {
		t.Fatalf("unknown-time recovery = (%q, %+v, %d), want pushing arrival", status, storedError, eventUnixMillis)
	}
}

func TestArtifactNodePlacementSerializesAbsentRows_RealYugabyte(t *testing.T) {
	verifyArtifactNodePlacementSerialization(t, startFoghornCatalogYugabyte(t))
}

func TestArtifactDeletionRejectsReplayOlderThanPlacement_RealYugabyte(t *testing.T) {
	verifyArtifactDeletionRejectsReplayOlderThanPlacement(t, startFoghornCatalogYugabyte(t))
}

func verifyArtifactDeletionRejectsReplayOlderThanPlacement(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const hash = "deletion-replay-fence-proof"
	const tenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	q := New(db)
	if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'clip', $2::uuid, 'ready')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	reportAt := time.Now().UnixMilli()
	if _, err := q.UpsertReportedArtifactNode(ctx, UpsertReportedArtifactNodeParams{
		ArtifactHash: hash, NodeID: "node-1", FilePath: "/tmp/original", SizeBytes: 1,
		ReportedAtMs: reportAt, Role: "cache", IsComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.DeleteArtifactNodeIfNotNewer(ctx, DeleteArtifactNodeIfNotNewerParams{
		ArtifactHash: hash, NodeID: "node-1", DeletedAtMs: reportAt + 1,
	}); err != nil {
		t.Fatalf("deletion after node snapshot was fenced by later database commit time: %v", err)
	}
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = 'node-1')`, hash).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("genuine deletion after the node snapshot did not remove the placement")
	}
	if _, err := q.UpsertReportedArtifactNode(ctx, UpsertReportedArtifactNodeParams{
		ArtifactHash: hash, NodeID: "node-1", FilePath: "/tmp/delayed-old-report", SizeBytes: 1,
		ReportedAtMs: reportAt, Role: "cache", IsComplete: true,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("inventory report captured before deletion resurrected placement: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = 'node-1')`, hash).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("delayed pre-deletion report resurrected the deleted placement")
	}
	if _, err := q.UpsertReportedArtifactNode(ctx, UpsertReportedArtifactNodeParams{
		ArtifactHash: hash, NodeID: "node-1", FilePath: "/tmp/reacquired", SizeBytes: 1,
		ReportedAtMs: reportAt + 5, Role: "cache", IsComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.DeleteArtifactNodeIfNotNewer(ctx, DeleteArtifactNodeIfNotNewerParams{
		ArtifactHash: hash, NodeID: "node-1", DeletedAtMs: reportAt + 1,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("replayed deletion older than reacquisition = %v, want sql.ErrNoRows", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = 'node-1')`, hash).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("replayed deletion removed the reacquired placement")
	}
}

func verifyArtifactNodePlacementSerialization(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const hash = "placement-serialization-proof"
	const tenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'clip', $2::uuid, 'ready')`, hash, tenant); err != nil {
		t.Fatal(err)
	}

	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback() //nolint:errcheck
	q1 := New(tx1)
	if _, err := q1.LockArtifactPlacementParent(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := q1.LockArtifactNodeState(ctx, LockArtifactNodeStateParams{ArtifactHash: hash, NodeID: "node-1"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("first prior state = %v, want sql.ErrNoRows", err)
	}
	if _, err := q1.UpsertCachedArtifactNode(ctx, UpsertCachedArtifactNodeParams{ArtifactHash: hash, NodeID: "node-1", FilePath: "/tmp/a", SizeBytes: 1}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		priorExisted bool
		err          error
	}
	started := make(chan struct{})
	done := make(chan result, 1)
	go func() {
		var priorExisted bool
		var startedOnce sync.Once
		err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx2 *sql.Tx) error {
			startedOnce.Do(func() { close(started) })
			q2 := New(tx2)
			if _, err := q2.LockArtifactPlacementParent(ctx, hash); err != nil {
				return err
			}
			_, err := q2.LockArtifactNodeState(ctx, LockArtifactNodeStateParams{ArtifactHash: hash, NodeID: "node-1"})
			priorExisted = err == nil
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			_, err = q2.UpsertCachedArtifactNode(ctx, UpsertCachedArtifactNodeParams{ArtifactHash: hash, NodeID: "node-1", FilePath: "/tmp/b", SizeBytes: 2})
			return err
		})
		done <- result{priorExisted: priorExisted, err: err}
	}()
	<-started
	select {
	case second := <-done:
		t.Fatalf("second writer escaped the parent lock before commit: %+v", second)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}
	second := <-done
	if second.err != nil {
		t.Fatal(second.err)
	}
	if !second.priorExisted {
		t.Fatal("second writer did not observe the row committed by the serialized first writer")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.artifact_nodes WHERE artifact_hash=$1 AND node_id='node-1'`, hash).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("placement row count = %d, want 1", count)
	}
}

func explainPlan(t *testing.T, ctx context.Context, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN "+query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func prepareFoghornCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range foghornGeneratedQueries(t) {
		name := fmt.Sprintf("foghorn_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

type foghornGeneratedQuery struct {
	file string
	name string
	sql  string
}

func foghornGeneratedQueries(t *testing.T) []foghornGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []foghornGeneratedQuery
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					queryName := "unknown"
					if index < len(value.Names) {
						queryName = value.Names[index].Name
					}
					queries = append(queries, foghornGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	if len(queries) == 0 {
		t.Fatal("no generated Foghorn queries found")
	}
	return queries
}

func startFoghornCatalogPostgres(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("fw-foghorn-catalog-pg-%d", time.Now().UnixNano())
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	return startFoghornCatalogEngine(t, name, image, "5432/tcp", "postgres", "postgres", "harness", []string{"-e", "POSTGRES_PASSWORD=harness"})
}

func startFoghornCatalogYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("fw-foghorn-catalog-yb-%d", time.Now().UnixNano())
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	return startFoghornCatalogEngine(t, name, image, "5433/tcp", "yugabyte", "yugabyte", "", []string{
		"--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)" --tserver_flags=yb_enable_read_committed_isolation=false`,
	})
}

func startFoghornCatalogEngine(t *testing.T, name, image, containerPort, user, database, password string, args []string) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	runArgs := []string{"run", "-d", "--name", name, "-P"}
	if containerPort == "5432/tcp" {
		runArgs = append(runArgs, args...)
		runArgs = append(runArgs, image)
	} else {
		runArgs = append(runArgs, args...)
	}
	if output, err := dockerpg.Run(runArgs...); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, containerPort)
	if err != nil {
		t.Fatal(err)
	}
	dsn := fmt.Sprintf("postgres://%s@127.0.0.1:%s/%s?sslmode=disable", user, port, database)
	if password != "" {
		dsn = fmt.Sprintf("postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable", user, password, port, database)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
