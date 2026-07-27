//go:build schema_verify

package jobs

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestPurgeOwnershipFilter_RealPG exercises the bytes+rows ownership filter against a REAL Postgres with
// representative rows (sqlmock can only regex-match the SQL, not its NULL semantics). While cross-cluster
// deletion is disabled the sweep must reap LOCAL rows and skip REMOTE ones, and "local" must be computed
// correctly for the tricky cases:
//   - both cluster columns NULL  -> local (a plain empty-string-or-cluster IN test would DROP this row,
//     because a NULL COALESCE result never matches an IN list)
//   - remote cluster label       -> remote (skipped)
//   - remote label BUT durable_backend_local=true -> local (the authoritative flag wins)
func TestPurgeOwnershipFilter_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	const tid = "11111111-1111-1111-1111-111111111111"

	// seed a purge-eligible deleted clip: past retention, catalog deletion acked, no non-orphaned node copies.
	seed := func(hash, storageCluster string, durableLocal bool) {
		var sc interface{}
		if storageCluster == "" {
			sc = nil
		} else {
			sc = storageCluster
		}
		if _, err := conn.Exec(`
			INSERT INTO foghorn.artifacts
			  (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location,
			   storage_cluster_id, origin_cluster_id, durable_backend_local,
			   catalog_revision, catalog_synced_rev, updated_at)
			VALUES ($1, 'clip', $2::uuid, 'deleted', 'synced', 'local',
			   $3, NULL, $4,
			   1, 1, NOW() - INTERVAL '40 days')`, hash, tid, sc, durableLocal); err != nil {
			t.Fatalf("seed %s: %v", hash, err)
		}
		// The catalog-revision trigger bumps catalog_revision on insert (monotonic). Ack the deletion so the
		// coverage gate (catalog_synced_rev >= catalog_revision) is satisfied and the row is purge-eligible.
		if _, err := conn.Exec(`UPDATE foghorn.artifacts SET catalog_synced_rev = catalog_revision WHERE artifact_hash = $1`, hash); err != nil {
			t.Fatalf("ack catalog %s: %v", hash, err)
		}
	}
	seed("hash-local-null", "", false)            // both cluster cols NULL -> local
	seed("hash-remote", "remote-x", false)        // remote-owned -> skipped
	seed("hash-durable-remote", "remote-x", true) // remote label but durable_backend_local=true -> local

	fake := &fakeS3{}
	job := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:           conn,
		Logger:       logging.NewLogger(),
		RetentionAge: 30 * 24 * time.Hour,
		Cleaner:      &artifacts.Cleaner{LocalCluster: "platform-eu", S3: fake},
		// AllowCrossClusterDelete omitted → false, so remote rows are excluded.
	})
	job.purgeArtifactBytesAndRows(ctx)

	exists := func(hash string) bool {
		var n int
		if err := conn.QueryRow(`SELECT count(*) FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", hash, err)
		}
		return n > 0
	}
	if exists("hash-local-null") {
		t.Error("both-NULL local row must be reaped, but it survived (NULL-IN leak)")
	}
	if exists("hash-durable-remote") {
		t.Error("durable_backend_local=true row must be reaped regardless of cluster label")
	}
	if !exists("hash-remote") {
		t.Error("remote-owned row must be retained while cross-cluster delete is disabled")
	}
}
