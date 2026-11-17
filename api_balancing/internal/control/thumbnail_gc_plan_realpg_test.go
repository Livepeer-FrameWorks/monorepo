//go:build schema_verify

package control

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestThumbnailGCSelectionBoundsHistoryBeforePointerLookups_RealPG(t *testing.T) {
	testThumbnailGCSelectionPlan(t, startRealPG(t))
}

func TestThumbnailGCSelectionBoundsHistoryBeforePointerLookups_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "thumbnail_gc_plan", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testThumbnailGCSelectionPlan(t, db)
}

func testThumbnailGCSelectionPlan(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO foghorn.thumbnail_task_assignment
   (attempt_id,tenant_id,asset_key,node_id,destination_cluster,status,version,claim_seq,expiry,publish_lease_token,superseded_at)
   SELECT prefix||n,'gc-tenant','gc-asset-'||n,'gc-node','gc-cluster','published',prefix||n,
          4503599627370496+n,NOW()+INTERVAL '1 hour','lease-'||prefix||n,
          CASE WHEN prefix='old-' THEN NOW()-INTERVAL '1 hour'+n*INTERVAL '1 millisecond' END
   FROM generate_series(1,4096) n CROSS JOIN (VALUES ('old-'),('head-')) AS versions(prefix)`,
		`INSERT INTO foghorn.thumbnail_active_pointer(asset_key,tenant_id,active_version,active_token)
   SELECT 'gc-asset-'||n,'gc-tenant','head-'||n,'lease-head-'||n FROM generate_series(1,4096) n`,
		`ANALYZE foghorn.thumbnail_task_assignment`,
		`ANALYZE foghorn.thumbnail_active_pointer`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().Add(-5 * time.Minute)
	rows, err := foghorndb.New(db).ListSupersededThumbnailAttemptIDs(t.Context(), foghorndb.ListSupersededThumbnailAttemptIDsParams{SupersededAt: sql.NullTime{Time: cutoff, Valid: true}, Limit: 18})
	if err != nil || len(rows) != 18 || rows[0] != "old-1" || rows[17] != "old-18" {
		t.Fatalf("GC must select the oldest eligible versions: %v err=%v", rows, err)
	}
	var encoded []byte
	err = db.QueryRowContext(t.Context(), `EXPLAIN (ANALYZE, FORMAT JSON)
 SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment a JOIN foghorn.thumbnail_active_pointer p ON p.asset_key=a.asset_key
 WHERE a.status='published' AND a.version<>p.active_version AND a.superseded_at IS NOT NULL AND a.superseded_at<$1
 ORDER BY a.superseded_at LIMIT 18`, cutoff).Scan(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	type node struct {
		Relation string            `json:"Relation Name"`
		Index    string            `json:"Index Name"`
		Rows     float64           `json:"Actual Rows"`
		Loops    float64           `json:"Actual Loops"`
		Children []json.RawMessage `json:"Plans"`
	}
	var roots []struct {
		Plan json.RawMessage `json:"Plan"`
	}
	if err := json.Unmarshal(encoded, &roots); err != nil {
		t.Fatal(err)
	}
	candidateScan, pointerLookup := false, false
	var inspect func(json.RawMessage)
	inspect = func(raw json.RawMessage) {
		var current node
		if err := json.Unmarshal(raw, &current); err != nil {
			t.Fatal(err)
		}
		if current.Relation == "thumbnail_task_assignment" {
			candidateScan = true
			if current.Rows*current.Loops > 36 || current.Index != "idx_foghorn_thumb_superseded_range" {
				t.Errorf("GC scans published history before limiting the batch: index=%q rows=%.0f loops=%.0f", current.Index, current.Rows, current.Loops)
			}
		}
		if current.Relation == "thumbnail_active_pointer" {
			pointerLookup = true
			if current.Rows*current.Loops > 36 {
				t.Errorf("GC probes every historical pointer before limiting the batch: rows=%.0f loops=%.0f", current.Rows, current.Loops)
			}
		}
		for _, child := range current.Children {
			inspect(child)
		}
	}
	for _, root := range roots {
		inspect(root.Plan)
	}
	if !candidateScan || !pointerLookup {
		t.Fatalf("selection plan omitted an expected relation: %s", encoded)
	}
}
