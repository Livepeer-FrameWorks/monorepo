package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func nodeArtifactCount(sm *state.StreamStateManager, nodeID string) int {
	n := sm.GetNodeState(nodeID)
	if n == nil {
		return 0
	}
	return len(n.Artifacts)
}

// A snapshot the sidecar flags incomplete (no complete filesystem scan yet) must NOT be applied as an
// authoritative whole-node inventory — otherwise a partial/empty list orphans live copies.
func TestHandleNodeLifecycleUpdate_IncompleteSnapshotSkipped(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	p := &Processor{logger: logging.NewLogger()}
	sm.SetNodeInfo("node-1", "https://1.2.3.4:8080", true, nil, nil, "", "", nil)
	sm.TouchNode("node-1", true)

	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
		NodeId:                      "node-1",
		IsHealthy:                   true,
		EventType:                   "node_lifecycle_update",
		Timestamp:                   time.Now().Unix(),
		ArtifactsConnectionFence:    1,
		ArtifactsReportSeq:          1,
		ArtifactsSnapshotIncomplete: true,
		Artifacts: []*ipcpb.StoredArtifact{
			{ClipHash: "shouldnotapply01", FilePath: "/d/x.mp4"},
		},
	})); err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	if got := nodeArtifactCount(sm, "node-1"); got != 0 {
		t.Fatalf("incomplete snapshot must not populate inventory, got %d artifacts", got)
	}
}

// A legacy sidecar predating the versioned-complete contract reports report_seq=0 and never sets the
// incomplete flag. Its (possibly partial/empty) scan must NOT be trusted as authoritative — otherwise
// a mixed-version rollout lets an old sidecar's transient empty scan clear routing-visible copies. We
// fail closed: keep the last trusted inventory until a versioned report arrives.
func TestHandleNodeLifecycleUpdate_UnversionedReportSkipped(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	p := &Processor{logger: logging.NewLogger()}
	sm.SetNodeInfo("node-1", "https://1.2.3.4:8080", true, nil, nil, "", "", nil)
	sm.TouchNode("node-1", true)

	// Seed a trusted versioned inventory first.
	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
		NodeId: "node-1", IsHealthy: true, EventType: "node_lifecycle_update", Timestamp: time.Now().Unix(),
		ArtifactsConnectionFence: 1, ArtifactsReportSeq: 1,
		Artifacts: []*ipcpb.StoredArtifact{{ClipHash: "trusted000000001", FilePath: "/d/x.mp4"}},
	})); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	// A legacy sidecar (report_seq=0, no incomplete flag) reports an EMPTY inventory. It must be ignored.
	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
		NodeId: "node-1", IsHealthy: true, EventType: "node_lifecycle_update", Timestamp: time.Now().Unix(),
		ArtifactsConnectionFence: 2, ArtifactsReportSeq: 0, // unversioned live report
		Artifacts: nil,
	})); err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	if got := nodeArtifactCount(sm, "node-1"); got != 1 {
		t.Fatalf("unversioned report must not clear the trusted inventory, got %d artifacts", got)
	}
}

// A complete (default) snapshot is applied normally.
func TestHandleNodeLifecycleUpdate_CompleteSnapshotApplied(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	p := &Processor{logger: logging.NewLogger()}
	sm.SetNodeInfo("node-1", "https://1.2.3.4:8080", true, nil, nil, "", "", nil)
	sm.TouchNode("node-1", true)

	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
		NodeId:                   "node-1",
		IsHealthy:                true,
		EventType:                "node_lifecycle_update",
		Timestamp:                time.Now().Unix(),
		ArtifactsConnectionFence: 1,
		ArtifactsReportSeq:       1,
		Artifacts: []*ipcpb.StoredArtifact{
			{ClipHash: "shouldapply00001", FilePath: "/d/x.mp4"},
		},
	})); err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	if got := nodeArtifactCount(sm, "node-1"); got != 1 {
		t.Fatalf("complete snapshot must populate inventory, got %d artifacts", got)
	}
}
