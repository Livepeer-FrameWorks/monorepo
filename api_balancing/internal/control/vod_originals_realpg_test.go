//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestVodOriginalRetentionAccountingAndDelete_RealPG(t *testing.T) {
	verifyVodOriginalAccounting(t, false)
}

func TestVodOriginalStorageOutageDoesNotRetryProcessing_RealPG(t *testing.T) {
	verifyVodOriginalAccounting(t, true)
}

func TestVodOriginalStampRejectsChangedSource_RealPG(t *testing.T) {
	conn := startRealPG(t)
	const tenant = "7a1e0000-0000-4000-8000-000000000053"
	const hash = "original-source-changed"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash,artifact_type,tenant_id,status,size_bytes,active_object_key) VALUES ($1,'vod',$2::uuid,'processing',2000,'different.mkv')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.vod_metadata (artifact_hash,s3_key,keep_original) VALUES ($1,'original.mkv',true)`, hash); err != nil {
		t.Fatal(err)
	}
	if err := stampVodOriginal(context.Background(), foghorndb.New(conn), tenant, hash); err == nil {
		t.Fatal("eligible original with a changed active source silently skipped retention")
	}
}

func verifyVodOriginalAccounting(t *testing.T, storageUnavailable bool) {
	t.Helper()
	conn := startRealPG(t)
	ctx := context.Background()
	previous := s3Client
	store := &mockS3Client{headObjectInfoFn: func(context.Context, string) (bool, int64, string, error) {
		if storageUnavailable {
			return false, 0, "", errors.New("storage temporarily unavailable")
		}
		return true, 2000, "etag", nil
	}}
	SetS3Client(store)
	t.Cleanup(func() { SetS3Client(previous) })
	const tenant = "7a1e0000-0000-4000-8000-000000000051"
	const hash = "original-retention-mkv"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, size_bytes, active_object_key, sync_status, durable_backend_local, backend_id) VALUES ($1, 'vod', $2::uuid, 'processing', 2000, 'original.mkv', 'synced', true, 'backend')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.vod_metadata (artifact_hash,s3_key,filename,keep_original) VALUES ($1,'original.mkv','phone.mkv',true)`, hash); err != nil {
		t.Fatal(err)
	}
	q := foghorndb.New(conn)
	usage := func(want int64) {
		t.Helper()
		rows, err := q.ListColdStorageUsage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.TenantID == tenant {
				if r.TotalBytes != want {
					t.Fatalf("billed bytes %d, want %d", r.TotalBytes, want)
				}
				return
			}
		}
		if want != 0 {
			t.Fatalf("no billed usage, want %d", want)
		}
	}
	usage(2000)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tq := foghorndb.New(tx)
	if err := stampVodOriginal(ctx, tq, tenant, hash); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	affected, err := tq.MarkProcessingArtifactReady(ctx, foghorndb.MarkProcessingArtifactReadyParams{ArtifactHash: hash, SizeBytes: sql.NullInt64{Int64: 1000, Valid: true}, Format: sql.NullString{String: "mkv", Valid: true}})
	if err != nil || affected != 1 {
		_ = tx.Rollback()
		t.Fatalf("readiness %d: %v", affected, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetVodOriginal(ctx, foghorndb.GetVodOriginalParams{TenantID: tenant, ArtifactHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if row.OriginalS3Key.String != "original.mkv" || row.OriginalSizeBytes.Int64 != 2000 {
		t.Fatalf("lost original: %+v", row)
	}
	usage(2000) // Processed output is local until sync; the original is already durable.
	capBytes, err := q.SumTenantActiveArtifactBytes(ctx, tenant)
	if err != nil || capBytes != 3000 {
		t.Fatalf("cap bytes %d, want 3000: %v", capBytes, err)
	}
	if _, err := conn.Exec(`UPDATE foghorn.artifacts SET active_object_key='original.mkv.att-processed',sync_status='synced' WHERE artifact_hash=$1 AND tenant_id=$2::uuid`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	usage(3000)
	// Clearing and queueing either roll back together or commit together.
	for _, commit := range []bool{false, true} {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		tq := foghorndb.New(tx)
		n, err := tq.ClearVodOriginal(ctx, foghorndb.ClearVodOriginalParams{TenantID: tenant, ArtifactHash: hash, ObjectKey: "original.mkv"})
		if err != nil || n != 1 {
			_ = tx.Rollback()
			t.Fatalf("clear original %d: %v", n, err)
		}
		if err := EnqueueStagingCleanupTx(ctx, tx, "original.mkv"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tq.TouchVodOriginalCatalog(ctx, foghorndb.TouchVodOriginalCatalogParams{TenantID: tenant, ArtifactHash: hash}); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		if !commit {
			usage(3000)
		}
	}
	usage(1000)
	capBytes, err = q.SumTenantActiveArtifactBytes(ctx, tenant)
	if err != nil || capBytes != 1000 {
		t.Fatalf("cap after original deletion %d: %v", capBytes, err)
	}
	var queued bool
	if err := conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM foghorn.staging_cleanup_queue WHERE object_key='original.mkv')`).Scan(&queued); err != nil || !queued {
		t.Fatalf("original deletion not queued: %v", err)
	}
}

func TestVodOriginalStampExcludesImportsFailuresAndOptOut_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	q := foghorndb.New(conn)
	const tenant = "7a1e0000-0000-4000-8000-000000000052"
	for _, test := range []struct {
		hash, status string
		keep         bool
		url          any
	}{{"failed", "failed", true, nil}, {"import", "processing", true, "https://example/video.mkv"}, {"opt-out", "processing", false, nil}} {
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash,artifact_type,tenant_id,status,active_object_key) VALUES ($1,'vod',$2::uuid,$3,'original.mkv')`, test.hash, tenant, test.status); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.vod_metadata (artifact_hash,s3_key,keep_original,source_url) VALUES ($1,'original.mkv',$2,$3)`, test.hash, test.keep, test.url); err != nil {
			t.Fatal(err)
		}
		if err := stampVodOriginal(ctx, q, tenant, test.hash); err != nil {
			t.Fatal(err)
		}
		key, err := q.GetKeptVodOriginalKey(ctx, foghorndb.GetKeptVodOriginalKeyParams{TenantID: tenant, ArtifactHash: test.hash})
		if err != nil {
			t.Fatal(err)
		}
		if key != "" {
			t.Fatalf("%s retained an original", test.hash)
		}
	}
}

func TestVodOriginalStampWaitsForReleaseActivation_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	previous := s3Client
	heads := 0
	SetS3Client(&mockS3Client{headObjectInfoFn: func(context.Context, string) (bool, int64, string, error) { heads++; return true, 2000, "etag", nil }})
	t.Cleanup(func() { SetS3Client(previous) })
	const tenant = "7a1e0000-0000-4000-8000-000000000052"
	const hash = "original-release-activation"
	q := foghorndb.New(conn)
	if activated, err := q.IsVodOriginalRetentionActivated(ctx); err != nil || !activated {
		t.Fatalf("fresh baseline activation: %v / %v", activated, err)
	}
	if _, err := conn.Exec(`DROP TABLE foghorn.vod_original_retention_activation`); err != nil {
		t.Fatal(err)
	}
	expand, err := os.ReadFile("../../../pkg/database/sql/migrations/foghorn/v0.3.16/expand/007_vod_original_activation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(expand)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.control_replicas(replica_id,release_version,placement_schema_version,placement_enforced,authority_feature_level,started_at,last_seen_at)
 VALUES('old-paused','v0.3.15',4,true,1,NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '2 minutes'),('upgraded','v0.3.16',4,true,2,NOW(),NOW())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status,size_bytes,active_object_key,sync_status,durable_backend_local,backend_id) VALUES($1,'vod',$2::uuid,'processing',2000,'activation.mov','synced',true,'backend')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.vod_metadata(artifact_hash,s3_key,filename,keep_original) VALUES($1,'activation.mov','phone.mov',true)`, hash); err != nil {
		t.Fatal(err)
	}
	capability, err := q.ReadControlCellPlacementCapability(ctx, 60)
	if err != nil || capability.MinAuthorityFeatureLevel != 2 {
		t.Fatalf("stale old replica setup: %+v / %v", capability, err)
	}
	affected, err := q.StampVodOriginal(ctx, foghorndb.StampVodOriginalParams{TenantID: tenant, ArtifactHash: hash, ObjectKey: "activation.mov", SizeBytes: 2000})
	if err != nil || affected != 0 {
		t.Fatalf("inactive SQL stamp affected %d rows: %v", affected, err)
	}
	if err := stampVodOriginal(ctx, q, tenant, hash); err == nil {
		t.Fatal("stamped an original before release activation after the old replica aged out")
	}
	if heads != 0 {
		t.Fatalf("inactive retention performed %d storage reads", heads)
	}
	key, err := q.GetKeptVodOriginalKey(ctx, foghorndb.GetKeptVodOriginalKeyParams{TenantID: tenant, ArtifactHash: hash})
	if err != nil || key != "" {
		t.Fatalf("inactive retention changed original: %q / %v", key, err)
	}
	migration, err := os.ReadFile("../../../pkg/database/sql/migrations/foghorn/v0.3.16/postdeploy/005_vod_original_activation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var activatedAt, repeatedAt string
	if err := conn.QueryRow(`SELECT activated_at::text FROM foghorn.vod_original_retention_activation`).Scan(&activatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT activated_at::text FROM foghorn.vod_original_retention_activation`).Scan(&repeatedAt); err != nil || activatedAt != repeatedAt {
		t.Fatalf("activation replay changed timestamp: %q -> %q / %v", activatedAt, repeatedAt, err)
	}
	if err := stampVodOriginal(ctx, q, tenant, hash); err != nil {
		t.Fatal(err)
	}
	key, err = q.GetKeptVodOriginalKey(ctx, foghorndb.GetKeptVodOriginalKeyParams{TenantID: tenant, ArtifactHash: hash})
	if err != nil || key != "activation.mov" || heads != 0 {
		t.Fatalf("activated retention: key=%q heads=%d / %v", key, heads, err)
	}
}
