package control

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// armRestartWindow arms the window for both the registered and the
// canonical identifier — disconnect cleanup checks the window per
// identifier, and fingerprint resolution can rewrite the id.
func TestArmRestartWindowCoversCanonicalID(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	stream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", stream)
	t.Cleanup(cleanup)
	registry.mu.Lock()
	registry.conns["node-1"].canonicalID = "canon-1"
	registry.mu.Unlock()

	armRestartWindow("node-1", stream, logging.NewLogger())

	if _, ok := sm.NodePendingReconnect("node-1"); !ok {
		t.Fatal("registered id must be armed")
	}
	if _, ok := sm.NodePendingReconnect("canon-1"); !ok {
		t.Fatal("canonical id must be armed")
	}
}

// An announced restart defers the unhealthy flip: the disconnect removes the
// conn but holds node health until the window finalizes.
func TestCleanupControlDisconnectDefersForAnnouncedRestart(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	stream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", stream)
	t.Cleanup(cleanup)

	sm.TouchNode("node-1", true)
	// Far-future deadline: the AfterFunc timer must not fire during the test
	// run; finalization is exercised directly below.
	sm.SetNodePendingReconnect("node-1", time.Now().Add(time.Hour))

	cleanupControlDisconnect("node-1", "", stream, logging.NewLogger())

	if node := sm.GetNodeState("node-1"); node == nil || !node.IsHealthy {
		t.Fatalf("announced restart must hold node health through the disconnect, got %+v", node)
	}

	finalizePendingDisconnect("node-1", logging.NewLogger())
	if node := sm.GetNodeState("node-1"); node == nil || node.IsHealthy {
		t.Fatalf("expired window must mark the node disconnected, got %+v", node)
	}
}

// Without an announce (crash, SIGKILL) the disconnect must mark the node
// unhealthy immediately.
func TestCleanupControlDisconnectImmediateWithoutAnnounce(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	stream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", stream)
	t.Cleanup(cleanup)

	sm.TouchNode("node-1", true)

	cleanupControlDisconnect("node-1", "", stream, logging.NewLogger())

	if node := sm.GetNodeState("node-1"); node == nil || node.IsHealthy {
		t.Fatalf("unannounced disconnect must mark the node unhealthy immediately, got %+v", node)
	}
}

// A re-register before the deadline disarms the window (the Register path
// is the only disarm besides finalization — heartbeats can't, because the
// pre-restart process keeps heartbeating through its post-announce drain);
// the stale timer must then no-op.
func TestFinalizePendingDisconnectNoopAfterReconnect(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	stream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", stream)
	t.Cleanup(cleanup)

	sm.TouchNode("node-1", true)
	sm.SetNodePendingReconnect("node-1", time.Now().Add(time.Hour))
	cleanupControlDisconnect("node-1", "", stream, logging.NewLogger())

	// Reconnect: the Register path clears the window, then the first
	// heartbeat restores health.
	sm.ClearNodePendingReconnect("node-1")
	sm.TouchNode("node-1", true)

	finalizePendingDisconnect("node-1", logging.NewLogger())
	if node := sm.GetNodeState("node-1"); node == nil || !node.IsHealthy {
		t.Fatalf("finalize after reconnect must be a no-op, got %+v", node)
	}
}
