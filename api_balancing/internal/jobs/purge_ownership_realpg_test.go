//go:build schema_verify

package jobs

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestPurgeOwnershipFilter_RealPG exercises the BACKEND-AFFINE ownership filter against a REAL Postgres with
// representative rows (sqlmock can only regex-match the SQL, not its NULL semantics). Production runs each cell on its
// OWN physical Foghorn database (foghorn_eu / foghorn_us) and immutable store, so a worker claims ONLY rows recorded on
// its own store. Ownership is read from recorded evidence, never reconstructed (invariant I2): a NULL backend is NOT
// claimed by cluster. A remaining NULL is unattributed and RETAINED (a safe row leak), never deleted from a guessed
// store.
// The contract:
//   - backend_id = this cell's store             -> claimed (reaped)
//   - backend_id = another cell's store           -> retained (never a no-op wrong-store delete then hard-delete)
//   - backend_id NULL (any cluster / durable flag) -> retained (unattributed; fail closed, never claimed by cluster)
func TestPurgeOwnershipFilter_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := context.Background()
	const tid = "11111111-1111-1111-1111-111111111111"

	// seed a purge-eligible deleted clip: past retention, catalog deletion acked, no non-orphaned node copies.
	seed := func(hash, storageCluster, backendID string, durableLocal bool) {
		nullable := func(s string) interface{} {
			if s == "" {
				return nil
			}
			return s
		}
		if _, err := conn.Exec(`
			INSERT INTO foghorn.artifacts
			  (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location,
			   storage_cluster_id, origin_cluster_id, durable_backend_local, backend_id,
			   catalog_revision, catalog_synced_rev, updated_at)
			VALUES ($1, 'clip', $2::uuid, 'deleted', 'synced', 'local',
			   $3, NULL, $4, $5,
			   1, 1, NOW() - INTERVAL '40 days')`, hash, tid, nullable(storageCluster), durableLocal, nullable(backendID)); err != nil {
			t.Fatalf("seed %s: %v", hash, err)
		}
		// The catalog-revision trigger bumps catalog_revision on insert (monotonic). Ack the deletion so the
		// coverage gate (catalog_synced_rev >= catalog_revision) is satisfied and the row is purge-eligible.
		if _, err := conn.Exec(`UPDATE foghorn.artifacts SET catalog_synced_rev = catalog_revision WHERE artifact_hash = $1`, hash); err != nil {
			t.Fatalf("ack catalog %s: %v", hash, err)
		}
	}
	seed("hash-backend-local", "media-eu-1", "backend-eu", false)   // our store -> reaped
	seed("hash-backend-foreign", "media-us-1", "backend-us", false) // another cell's store -> retained
	seed("hash-null-local", "platform-eu", "", false)               // NULL, local cluster -> retained (fail closed, no cluster claim)
	seed("hash-null-remote", "remote-x", "", false)                 // NULL, remote cluster -> retained
	seed("hash-null-unattributed", "", "", false)                   // NULL, no cluster -> retained
	seed("hash-null-durable-remote", "remote-x", "", true)          // NULL, durable flag must NOT attribute -> retained

	fake := &fakeS3{}
	job := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:           conn,
		Logger:       logging.NewLogger(),
		RetentionAge: 30 * 24 * time.Hour,
		Cleaner:      &artifacts.Cleaner{LocalCluster: "platform-eu", LocalBackendID: "backend-eu", S3: fake},
		// AllowCrossClusterDelete omitted → false, so the backend-affinity filter applies.
	})
	job.purgeArtifactBytesAndRows(ctx)

	exists := func(hash string) bool {
		var n int
		if err := conn.QueryRow(`SELECT count(*) FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", hash, err)
		}
		return n > 0
	}
	reaped := map[string]bool{"hash-backend-local": true}
	retained := map[string]bool{
		"hash-backend-foreign":     true,
		"hash-null-local":          true,
		"hash-null-remote":         true,
		"hash-null-unattributed":   true,
		"hash-null-durable-remote": true,
	}
	for h := range reaped {
		if exists(h) {
			t.Errorf("%s must be reaped (owned by this cell's store/cluster) but survived", h)
		}
	}
	for h := range retained {
		if !exists(h) {
			t.Errorf("%s must be retained (not this cell's store) but was hard-deleted", h)
		}
	}
}
