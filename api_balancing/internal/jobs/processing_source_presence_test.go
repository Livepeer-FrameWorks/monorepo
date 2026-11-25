package jobs

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
)

func TestProcessingSourceNodeReconnectWindow(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		node    *state.NodeState
		present bool
	}{
		{name: "unknown node"},
		{name: "connected unhealthy", node: &state.NodeState{LastHeartbeat: now}, present: true},
		{name: "ten second disconnect", node: &state.NodeState{DisconnectedAt: now.Add(-10 * time.Second)}, present: true},
		{name: "disconnect deadline", node: &state.NodeState{DisconnectedAt: now.Add(-time.Minute)}},
		{name: "stale connection", node: &state.NodeState{LastHeartbeat: now.Add(-time.Minute)}},
		{name: "metadata cannot extend disconnect", node: &state.NodeState{DisconnectedAt: now.Add(-2 * time.Minute), LastUpdate: now}},
		{name: "announced reconnect", node: &state.NodeState{DisconnectedAt: now.Add(-2 * time.Minute), PendingReconnectUntil: now.Add(time.Minute)}, present: true},
		{name: "expired announced reconnect", node: &state.NodeState{DisconnectedAt: now.Add(-2 * time.Minute), PendingReconnectUntil: now}},
		{name: "persisted old node state", node: &state.NodeState{LastUpdate: now.Add(-10 * time.Second)}, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := processingSourceNodePresent(tc.node, now); got != tc.present {
				t.Fatalf("source present = %v, want %v", got, tc.present)
			}
		})
	}
}

func TestProcessingSourceDisconnectTimestampSurvivesMetadataUpdates(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.TouchNode("source", true)
	sm.MarkNodeDisconnected("source")
	disconnected := sm.GetNodeState("source").DisconnectedAt
	if disconnected.IsZero() {
		t.Fatal("control-stream drop did not record disconnect time")
	}
	sm.MarkNodeDisconnected("source")
	sm.SetNodeGPUInfo("source", "nvidia", 1, 8192, "")
	if got := sm.GetNodeState("source").DisconnectedAt; !got.Equal(disconnected) {
		t.Fatalf("node metadata extended reconnect grace: %s -> %s", disconnected, got)
	}
	sm.TouchNode("source", true)
	if node := sm.GetNodeState("source"); !node.DisconnectedAt.IsZero() || !processingSourceNodePresent(node, time.Now()) {
		t.Fatal("heartbeat did not restore source presence")
	}
}
