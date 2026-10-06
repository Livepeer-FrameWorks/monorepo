//go:build schema_verify

package jobs

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A DVR delete an edge deferred (its recording push not yet proven gone) is
// re-driven while the edge keeps reporting the files it retained: the edge's
// inventory reports must not keep the deleted DVR looking freshly changed. A
// DVR whose edge no longer holds it, and a delete younger than the
// reconciliation age, are left alone.
func TestDeletedDVRDeferredOnEdgeIsRedriven_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	prev := control.GetDB()
	control.SetDB(conn)
	t.Cleanup(func() { control.SetDB(prev) })

	const (
		tenant    = "00000000-0000-4000-8000-0000000000d5"
		edge      = "edge-deferred-delete"
		reported  = "deferreddvrreported0000000000001"
		confirmed = "deferreddvrconfirmed000000000001"
		gone      = "deferreddvrgone00000000000000001"
		recent    = "deferreddvrrecent000000000000001"
	)
	seed := func(hash string, deletedAgo time.Duration) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, updated_at)
			VALUES ($1, 'dvr', $2, 'deleted', NOW() - $3::interval)`, hash, tenant, deletedAgo.String()); err != nil {
			t.Fatal(err)
		}
	}
	seed(reported, 40*time.Minute)
	seed(confirmed, 40*time.Minute)
	seed(gone, 40*time.Minute)
	seed(recent, 5*time.Minute)
	for _, hash := range []string{reported, gone, recent} {
		if _, err := conn.Exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, last_seen_at, is_orphaned)
			VALUES ($1, $2, $3, NOW() - INTERVAL '1 minute', false)`, hash, edge, "/var/lib/frameworks/dvr/s/"+hash); err != nil {
			t.Fatal(err)
		}
	}
	// The edge deleted this one and stopped reporting it; the stale sweep
	// orphaned the copy.
	if _, err := conn.Exec(`UPDATE foghorn.artifact_nodes SET is_orphaned = true, last_seen_at = NOW() - INTERVAL '20 minutes'
		WHERE artifact_hash = $1`, gone); err != nil {
		t.Fatal(err)
	}

	// The edge keeps reporting the files it retained for the deferred delete.
	if err := control.NewArtifactRepository().UpsertArtifacts(t.Context(), edge, []state.ArtifactRecord{{
		ArtifactHash: reported,
		ArtifactType: "dvr",
		StreamName:   "s",
		FilePath:     "/var/lib/frameworks/dvr/s/" + reported,
		SizeBytes:    16 * 1024,
		ReportedAtMs: time.Now().UnixMilli(),
	}}); err != nil {
		t.Fatal(err)
	}

	job := NewOrphanCleanupJob(OrphanCleanupConfig{DB: conn, Logger: logging.NewLogger()})
	orphans, err := job.findOrphanedDVRs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	redriven := map[string]string{}
	for _, o := range orphans {
		redriven[o.DVRHash] = o.NodeID
	}
	if redriven[reported] != edge {
		t.Errorf("deferred delete of %s is not re-driven on %s (re-drive set %v)", reported, edge, redriven)
	}
	for _, hash := range []string{confirmed, gone, recent} {
		if node, ok := redriven[hash]; ok {
			t.Errorf("%s re-driven on %s, want left alone", hash, node)
		}
	}
}
