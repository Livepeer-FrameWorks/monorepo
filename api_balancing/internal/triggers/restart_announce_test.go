package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func nodeLifecycleTrigger(nu *ipcpb.NodeLifecycleUpdate) *ipcpb.MistTrigger {
	return &ipcpb.MistTrigger{
		TriggerType:    "NODE_LIFECYCLE_UPDATE",
		NodeId:         nu.GetNodeId(),
		TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{NodeLifecycleUpdate: nu},
	}
}

// A "node_restarting" announce must early-return before the snapshot writes:
// its empty payload would otherwise wipe BaseURL and flip metrics via
// SetNodeInfo/UpdateNodeMetrics. Arming the window is NOT this handler's job
// — it happens synchronously in the control receive loop, because this
// handler runs in a goroutine that can lose the race against disconnect
// cleanup when helmsman exits right after announcing.
func TestHandleNodeLifecycleUpdate_RestartAnnouncePreservesStateWithoutArming(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	p := &Processor{logger: logging.NewLogger()}

	sm.SetNodeInfo("node-1", "https://1.2.3.4:8080", true, nil, nil, "", "", nil)
	sm.TouchNode("node-1", true)

	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
		NodeId:    "node-1",
		IsHealthy: true,
		EventType: state.EventNodeRestarting,
		Timestamp: time.Now().Unix(),
	})); err != nil {
		t.Fatalf("restart announce failed: %v", err)
	}

	if _, ok := sm.NodePendingReconnect("node-1"); ok {
		t.Fatal("the trigger processor must not arm the window; the control receive loop owns that")
	}
	node := sm.GetNodeState("node-1")
	if node == nil || node.BaseURL != "https://1.2.3.4:8080" {
		t.Fatalf("restart announce must not clobber node state, got %+v", node)
	}
	if !node.IsHealthy {
		t.Fatal("restart announce must not mark the node unhealthy")
	}
}

// A "node_shutdown" event with is_healthy=false takes the immediate-unhealthy
// path and must not arm a window.
func TestHandleNodeLifecycleUpdate_LegacyShutdownStaysImmediate(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	p := &Processor{logger: logging.NewLogger()}

	sm.TouchNode("node-1", true)

	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
		NodeId:    "node-1",
		IsHealthy: false,
		EventType: "node_shutdown",
		Timestamp: time.Now().Unix(),
	})); err != nil {
		t.Fatalf("legacy shutdown failed: %v", err)
	}

	if node := sm.GetNodeState("node-1"); node == nil || node.IsHealthy {
		t.Fatalf("legacy shutdown must mark the node unhealthy, got %+v", node)
	}
	if _, ok := sm.NodePendingReconnect("node-1"); ok {
		t.Fatal("legacy shutdown must not arm a reconnect window")
	}
}
