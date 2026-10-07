//go:build schema_verify

package control

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// An edge reports a successful immediate DVR delete as DVRStopped{deleted}. That report retires the
// reporting node's placement, so the orphan cleanup stops re-driving the delete to it, and leaves every
// other node's placement of the same recording alone.
func TestDVRStoppedDeletedRetiresReportingNodePlacement_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prevDB, prevRepo := db, artifactRepo
	db, artifactRepo = conn, NewArtifactRepository()
	t.Cleanup(func() { db, artifactRepo = prevDB, prevRepo })

	const (
		tenant   = "00000000-0000-4000-8000-0000000000d6"
		dvrHash  = "dvrstoppeddeleted000000000000001"
		reporter = "edge-deleted-dvr"
		peer     = "edge-still-holding"
	)
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, updated_at)
		VALUES ($1, 'dvr', $2, 'deleted', NOW() - INTERVAL '40 minutes')`, dvrHash, tenant); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{reporter, peer} {
		if _, err := conn.Exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, last_seen_at, is_orphaned, inventory_reported_at_ms)
			VALUES ($1, $2, $3, NOW() - INTERVAL '1 minute', false, $4)`,
			dvrHash, node, "/var/lib/frameworks/dvr/s/"+dvrHash, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}

	redriven := func() map[string]bool {
		t.Helper()
		rows, err := foghorndb.New(conn).ListDeletedDVRNodes(t.Context(), (30 * time.Minute).String())
		if err != nil {
			t.Fatal(err)
		}
		nodes := map[string]bool{}
		for _, row := range rows {
			if row.ArtifactHash == dvrHash {
				nodes[row.NodeID] = true
			}
		}
		return nodes
	}
	if before := redriven(); !before[reporter] || !before[peer] {
		t.Fatalf("seeded deleted DVR not selected for re-drive on both nodes: %v", before)
	}

	processDVRStopped(
		&ipcpb.DVRStopped{DvrHash: dvrHash, Status: "deleted", RequestId: "req-1"},
		NodeSession{RawNodeID: reporter, CanonicalNodeID: reporter},
		timestamppb.Now(),
		logging.NewLogger(),
	)

	after := redriven()
	if after[reporter] {
		t.Errorf("orphan cleanup still re-drives the delete on %s after it reported the DVR deleted (re-drive set %v)", reporter, after)
	}
	if !after[peer] {
		t.Errorf("deleted report from %s retired %s's placement (re-drive set %v)", reporter, peer, after)
	}
	var reporterRows int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2`,
		dvrHash, reporter).Scan(&reporterRows); err != nil {
		t.Fatal(err)
	}
	if reporterRows != 0 {
		t.Errorf("%s still has %d artifact_nodes rows for the deleted DVR, want 0", reporter, reporterRows)
	}
}
