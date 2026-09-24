//go:build schema_verify

package foghorndb

import (
	"context"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// The v0.3.11 backfill moves existing recordings whose segments reached the cell's S3 off 'pending': running ones to
// 's3'/'in_progress', finished ones to 'synced' with the recording prefix under the cell's recorded store. Recordings
// with no uploaded segment, and failed ones, are left alone.
func TestDVRParentStorageBackfillMigration_RealPG(t *testing.T) {
	db := startFoghornCatalogPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenant = "10000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.cell_storage_identity (backend_id, bucket, endpoint, region, prefix)
VALUES ('backend-eu', 'media-eu', 'https://s3.eu.example', 'eu-central-1', 'prod/')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, stream_internal_name, origin_cluster_id)
VALUES ('dvrcompleted0000000000000000001', 'dvr', $1::uuid, 'completed', 'live-a', 'cluster-eu'),
       ('dvrrecording0000000000000000001', 'dvr', $1::uuid, 'recording', 'live-b', 'cluster-eu'),
       ('dvrnouploads0000000000000000001', 'dvr', $1::uuid, 'completed', 'live-c', 'cluster-eu'),
       ('dvrfailed0000000000000000000001', 'dvr', $1::uuid, 'failed', 'live-d', 'cluster-eu')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO foghorn.dvr_segments (artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms, s3_key, status)
VALUES ('dvrcompleted0000000000000000001', 's1.ts', 1, 0, 6000, 6000, 'k1', 'deleted_local'),
       ('dvrrecording0000000000000000001', 's1.ts', 1, 0, 6000, 6000, 'k2', 'uploaded'),
       ('dvrnouploads0000000000000000001', 's1.ts', 1, 0, 6000, 6000, 'k3', 'lost_local'),
       ('dvrfailed0000000000000000000001', 's1.ts', 1, 0, 6000, 6000, 'k4', 'uploaded')`); err != nil {
		t.Fatal(err)
	}
	migration, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.11/postdeploy/004_backfill_dvr_parent_storage.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]struct{ location, sync, backend, url string }{
		"dvrcompleted0000000000000000001": {"s3", "synced", "backend-eu", "s3://media-eu/prod/dvr/" + tenant + "/live-a/dvrcompleted0000000000000000001"},
		"dvrrecording0000000000000000001": {"s3", "in_progress", "backend-eu", ""},
		"dvrnouploads0000000000000000001": {"pending", "pending", "", ""},
		"dvrfailed0000000000000000000001": {"pending", "pending", "", ""},
	} {
		var location, sync, backend, url string
		if err := db.QueryRowContext(ctx, `
SELECT COALESCE(storage_location, ''), COALESCE(sync_status, ''), COALESCE(backend_id, ''), COALESCE(s3_url, '')
FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&location, &sync, &backend, &url); err != nil {
			t.Fatal(err)
		}
		if location != want.location || sync != want.sync || backend != want.backend || url != want.url {
			t.Fatalf("%s after backfill = (%q, %q, %q, %q), want %+v", hash, location, sync, backend, url, want)
		}
	}
}
