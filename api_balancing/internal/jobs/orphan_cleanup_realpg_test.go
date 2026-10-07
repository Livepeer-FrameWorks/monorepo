//go:build schema_verify

package jobs

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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

// A node that registers again is sent, at once, every delete it still owes: a
// clip, DVR or VOD soft-deleted moments ago whose bytes it still holds, which a
// delete sent to its previous connection or deferred in its sidecar's memory
// did not remove. Another node's deletes, a live artifact, a copy already gone
// from the node and a federated pointer's origin copy are left alone.
func TestRegisteredNodeIsSentTheDeletesItOwes_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const (
		tenant  = "00000000-0000-4000-8000-0000000000d6"
		node    = "edge-reregistered"
		other   = "edge-other"
		clip    = "owedclip000000000000000000000001"
		dvr     = "oweddvr0000000000000000000000001"
		vod     = "owedvod0000000000000000000000001"
		live    = "owedlive000000000000000000000001"
		gone    = "owedgone000000000000000000000001"
		foreign = "owedforeign000000000000000000001"
		pointer = "owedpointer000000000000000000001"
	)
	seed := func(hash, kind, status string, pointerRow bool, holder, role string, orphaned bool) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, federated_pointer, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW() - INTERVAL '1 minute')`, hash, kind, tenant, status, pointerRow); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, last_seen_at, is_orphaned, role)
			VALUES ($1, $2, $3, NOW(), $4, $5)`, hash, holder, "/var/lib/frameworks/"+hash, orphaned, role); err != nil {
			t.Fatal(err)
		}
	}
	seed(clip, "clip", "deleted", false, node, "origin", false)
	seed(dvr, "dvr", "deleted", false, node, "origin", false)
	seed(vod, "vod", "deleted", false, node, "origin", false)
	seed(live, "vod", "ready", false, node, "origin", false)
	seed(gone, "dvr", "deleted", false, node, "origin", true)
	seed(foreign, "clip", "deleted", false, other, "origin", false)
	seed(pointer, "vod", "deleted", true, node, "origin", false)

	job := NewOrphanCleanupJob(OrphanCleanupConfig{DB: conn, Logger: logging.NewLogger()})
	sent := map[string]string{}
	job.sendClipDelete = func(nodeID string, req *ipcpb.ClipDeleteRequest) error {
		sent[req.GetClipHash()] = "clip@" + nodeID
		return nil
	}
	job.sendDVRDelete = func(nodeID string, req *ipcpb.DVRDeleteRequest) error {
		sent[req.GetDvrHash()] = "dvr@" + nodeID
		return nil
	}
	job.sendVodDelete = func(nodeID string, req *ipcpb.VodDeleteRequest) error {
		sent[req.GetVodHash()] = "vod@" + nodeID
		return nil
	}

	// Every delete above is younger than the periodic pass's reconciliation
	// age, so only the registration re-drive can send it.
	job.Start()
	t.Cleanup(job.Stop)
	control.RedriveNodeDeletions(node)

	want := map[string]string{clip: "clip@" + node, dvr: "dvr@" + node, vod: "vod@" + node}
	for hash, target := range want {
		if sent[hash] != target {
			t.Errorf("owed delete %s sent as %q, want %q", hash, sent[hash], target)
		}
	}
	if len(sent) != len(want) {
		t.Errorf("sent %v, want exactly %v", sent, want)
	}
}
