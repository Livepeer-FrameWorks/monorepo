package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Arm/inspect/clear lifecycle of the announced-restart window.
func TestPendingReconnectArmAndClear(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	if _, ok := sm.NodePendingReconnect("n1"); ok {
		t.Fatal("unknown node must not report a pending reconnect")
	}

	deadline := time.Now().Add(20 * time.Second)
	sm.SetNodePendingReconnect("n1", deadline)
	got, ok := sm.NodePendingReconnect("n1")
	if !ok || !got.Equal(deadline) {
		t.Fatalf("pending reconnect = %v ok=%v, want %v", got, ok, deadline)
	}

	sm.ClearNodePendingReconnect("n1")
	if _, ok := sm.NodePendingReconnect("n1"); ok {
		t.Fatal("cleared window must not report pending")
	}
}

// Heartbeats must NOT disarm the window: the pre-restart helmsman's
// heartbeat ticker and lifecycle poller keep sending during the 500ms
// post-announce drain, so a healthy touch can land between the announce and
// the disconnect. Only the Register path / finalization disarm.
func TestTouchNodeDoesNotDisarmPendingReconnect(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodePendingReconnect("n1", time.Now().Add(20*time.Second))
	sm.TouchNode("n1", false)
	if _, ok := sm.NodePendingReconnect("n1"); !ok {
		t.Fatal("unhealthy heartbeat must not disarm the reconnect window")
	}

	sm.TouchNode("n1", true)
	if _, ok := sm.NodePendingReconnect("n1"); !ok {
		t.Fatal("healthy heartbeat must not disarm the reconnect window (drain-window race)")
	}
}

// The window is conn-owner-local: it must never ride the write-through node
// snapshot to HA peers (multi-writer rule — a replica that adopted it could
// hold a node healthy without owning the announce).
func TestPendingReconnectNeverRidesSnapshots(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.TouchNode("n1", true)
	sm.SetNodePendingReconnect("n1", time.Now().Add(20*time.Second))

	payload, err := json.Marshal(sm.GetNodeState("n1"))
	if err != nil {
		t.Fatalf("marshal node state: %v", err)
	}
	if strings.Contains(string(payload), "PendingReconnect") || strings.Contains(string(payload), "pending_reconnect") {
		t.Fatalf("pending-reconnect fields leaked into the node snapshot: %s", payload)
	}
}
