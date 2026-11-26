//go:build schema_verify

package jobs

import (
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestFreezeRetryAgesAttemptInsteadOfInventory_RealPG(t *testing.T) {
	db := startRealPGForCleanup(t)
	const tenant = "11111111-1111-4111-8111-111111111111"
	cases := []struct {
		hash, attempt, created string
		failures               int
		want                   bool
	}{
		{"old-attempt", "2 days", "2 days", 0, true},
		{"legacy-no-attempt", "", "2 days", 0, true},
		{"recent-attempt", "1 minute", "2 days", 0, false},
		{"backing-off", "10 minutes", "2 days", 4, false},
		{"new-legacy", "", "1 minute", 0, false},
	}
	q := foghorndb.New(db)
	for _, c := range cases {
		if _, err := db.Exec(`INSERT INTO foghorn.artifacts
   (artifact_hash,artifact_type,tenant_id,status,sync_status,storage_location,created_at,updated_at,last_sync_attempt,failure_count)
   VALUES ($1,'vod',$2::uuid,'ready','failed','local',NOW()-$3::interval,NOW()-INTERVAL '2 days',
   CASE WHEN $4='' THEN NULL ELSE NOW()-NULLIF($4,'')::interval END,$5)`, c.hash, tenant, c.created, c.attempt, c.failures); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash,node_id,role,is_complete,is_orphaned)
   VALUES ($1,'origin','origin',true,false)`, c.hash); err != nil {
			t.Fatal(err)
		}
		if err := q.UpdateArtifactReportMetadata(t.Context(), foghorndb.UpdateArtifactReportMetadataParams{ArtifactHash: c.hash}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := q.ListFailedArtifactsForFreezeRetry(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[row.ArtifactHash] = true
	}
	for _, c := range cases {
		if got[c.hash] != c.want {
			t.Errorf("%s freeze retry eligible=%v, want %v after fresh inventory; retry must age its attempt, not inventory", c.hash, got[c.hash], c.want)
		}
	}
}
