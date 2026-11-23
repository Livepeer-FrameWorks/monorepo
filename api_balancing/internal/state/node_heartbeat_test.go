package state

import (
	"slices"
	"testing"
	"time"
)

func TestConnectedHeartbeatDoesNotRequireMistHealth(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	if slices.Contains(sm.ConnectedNodeIDs(time.Minute), "missing") {
		t.Fatal("unknown node has a heartbeat")
	}
	sm.TouchNode("node", false)
	if !slices.Contains(sm.ConnectedNodeIDs(time.Minute), "node") {
		t.Fatal("a failed Mist health poll erased the sidecar heartbeat")
	}
	if len(sm.AliveNodeIDs(time.Minute)) != 0 {
		t.Fatal("unhealthy node became eligible for new work")
	}
	sm.MarkNodeDisconnected("node")
	if slices.Contains(sm.ConnectedNodeIDs(time.Minute), "node") {
		t.Fatal("disconnected node remained connected")
	}
	sm.TouchNode("node", true)
	sm.mu.Lock()
	sm.nodes["node"].LastHeartbeat = time.Now().Add(-2 * time.Minute)
	sm.mu.Unlock()
	if slices.Contains(sm.ConnectedNodeIDs(time.Minute), "node") {
		t.Fatal("expired heartbeat remained live")
	}
}
