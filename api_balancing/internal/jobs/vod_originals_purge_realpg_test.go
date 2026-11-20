//go:build schema_verify

package jobs

import (
	"slices"
	"testing"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestPurgeIncludesRetainedVodOriginal_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	ctx := t.Context()
	const tenant = "7a1e0000-0000-4000-8000-000000000056"
	const hash = "original-purge-proof"
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, format, sync_status,
		 active_object_key, active_dtsh_key, durable_backend_local, backend_id)
		VALUES ($1, 'vod', $2::uuid, 'ready', 'mkv', 'synced', 'processed.mkv', 'processed.dtsh', true, 'backend-eu')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.vod_metadata
		(artifact_hash, s3_key, original_s3_key, original_size_bytes, keep_original)
		VALUES ($1, 'processed.mkv', 'original.mov', 2000, true)`, hash); err != nil {
		t.Fatal(err)
	}
	store := &fakeS3{}
	job := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB: conn, Logger: logging.NewLogger(), RetentionAge: 30 * 24 * time.Hour,
		Cleaner: &artifacts.Cleaner{S3: store, LocalBackendID: "backend-eu"},
	})
	job.purgeArtifactBytesAndRows(ctx)
	if len(store.deleteCalls) != 0 {
		t.Fatalf("purge touched a live video: %v", store.deleteCalls)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts
		SET status = 'deleted', updated_at = NOW() - INTERVAL '40 days'
		WHERE tenant_id = $1::uuid AND artifact_hash = $2`, tenant, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.artifacts SET catalog_synced_rev = catalog_revision
		WHERE tenant_id = $1::uuid AND artifact_hash = $2`, tenant, hash); err != nil {
		t.Fatal(err)
	}
	job.purgeArtifactBytesAndRows(ctx)
	for _, key := range []string{"original.mov", "processed.mkv", "processed.dtsh"} {
		if !slices.Contains(store.deleteCalls, key) {
			t.Fatalf("purge omitted %q: deleted %v", key, store.deleteCalls)
		}
	}
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM foghorn.artifacts
		WHERE tenant_id = $1::uuid AND artifact_hash = $2)`, tenant, hash).Scan(&exists); err != nil || exists {
		t.Fatalf("purged artifact remains: exists=%v error=%v", exists, err)
	}
}
