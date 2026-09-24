//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// The v0.3.11 backfill gives artifacts without an origin the cluster their creation intent recorded, never a
// tenant-level cluster. An artifact without an intent is left for the origin Foghorn's catalog projection, which
// only fills a NULL origin, so an empty string is normalized to NULL.
func TestArtifactOriginBackfillMigration_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000071"
	const userID = "20000000-0000-0000-0000-000000000071"
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.vod_assets (tenant_id, user_id, vod_hash, internal_name, playback_id, filename, origin_cluster_id)
VALUES ($1, $2, 'vodwithintent00000000000000001', 'vod-intent', 'vod-intent-playback', 'a.mp4', NULL),
       ($1, $2, 'vodwithoutintent000000000000001', 'vod-no-intent', 'vod-no-intent-playback', 'b.mp4', ''),
       ($1, $2, 'vodkeepsorigin0000000000000001', 'vod-keeps', 'vod-keeps-playback', 'c.mp4', 'cluster-us')`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.dvr_recordings (tenant_id, user_id, dvr_hash, internal_name, playback_id, stream_internal_name, origin_cluster_id)
VALUES ($1, $2, 'dvrwithintent00000000000000001', 'dvr-intent', 'dvr-intent-playback', 'live-a', '')`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO commodore.artifact_creation_intents (tenant_id, kind, artifact_hash, request_id, origin_cluster_id, status)
VALUES ($1, 'vod', 'vodwithintent00000000000000001', gen_random_uuid(), 'cluster-eu', 'committed'),
       ($1, 'vod', 'vodkeepsorigin0000000000000001', gen_random_uuid(), 'cluster-eu', 'committed'),
       ($1, 'dvr', 'dvrwithintent00000000000000001', gen_random_uuid(), 'cluster-eu', 'committed')`, tenantID); err != nil {
		t.Fatal(err)
	}
	migration, err := dbsql.Content.ReadFile("migrations/commodore/v0.3.11/postdeploy/007_backfill_artifact_origin_cluster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	readOrigin := func(query, hash string) sql.NullString {
		t.Helper()
		var origin sql.NullString
		if err := db.QueryRowContext(ctx, query, hash).Scan(&origin); err != nil {
			t.Fatal(err)
		}
		return origin
	}
	const vodQuery = `SELECT origin_cluster_id FROM commodore.vod_assets WHERE vod_hash = $1`
	if got := readOrigin(vodQuery, "vodwithintent00000000000000001"); got.String != "cluster-eu" {
		t.Fatalf("vod with intent origin = %+v, want cluster-eu", got)
	}
	if got := readOrigin(vodQuery, "vodwithoutintent000000000000001"); got.Valid {
		t.Fatalf("vod without intent origin = %+v, want NULL", got)
	}
	if got := readOrigin(vodQuery, "vodkeepsorigin0000000000000001"); got.String != "cluster-us" {
		t.Fatalf("recorded origin was overwritten: %+v", got)
	}
	if got := readOrigin(`SELECT origin_cluster_id FROM commodore.dvr_recordings WHERE dvr_hash = $1`, "dvrwithintent00000000000000001"); got.String != "cluster-eu" {
		t.Fatalf("dvr with intent origin = %+v, want cluster-eu", got)
	}
}
