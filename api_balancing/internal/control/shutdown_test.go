package control

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A deploy restarts every instance in turn. The nodes behind the one going down
// are healthy and reconnect to another instance within a second, so nothing
// about the hand-off may read as the nodes failing: their health is held through
// the disconnect, they are told to go only once the listener is closed (or the
// redial lands straight back here), and a stream that is still open after the
// drain window is ended rather than left to the hard stop.
func TestBeginShutdownHandsNodesOffWithoutMarkingThemUnhealthy(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { shuttingDown.Store(false) })

	stream := &captureStream{}
	t.Cleanup(SetupTestRegistry("node-1", stream))
	release := make(chan struct{})
	registry.mu.Lock()
	registry.conns["node-1"].canonicalID = "canon-1"
	registry.conns["node-1"].release = release
	registry.mu.Unlock()
	sm.TouchNode("node-1", true)

	listenersClosed := false
	noticeSentBeforeClose := false
	started := time.Now()
	// The node closes its stream as soon as it is told to go, which is what a
	// current Helmsman does; the drain must not wait out its whole window then.
	go func() {
		for stream.lastSent() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		cleanupControlDisconnect("node-1", "canon-1", stream, logging.NewLogger())
	}()
	BeginShutdown(context.Background(), func() {
		listenersClosed = true
		noticeSentBeforeClose = stream.lastSent() != nil
	}, logging.NewLogger())

	if !listenersClosed || noticeSentBeforeClose {
		t.Fatalf("listeners closed=%v, node told to go before that=%v; a redial must not be able to land here", listenersClosed, noticeSentBeforeClose)
	}
	if notice := stream.lastSent(); notice == nil || notice.GetGoingAway() == nil {
		t.Fatalf("node was not told this instance is going away: %+v", notice)
	}
	if time.Since(started) >= shutdownDrainWindow {
		t.Fatalf("hand-off took %s although the node closed its stream at once", time.Since(started))
	}
	if node := sm.GetNodeState("node-1"); node == nil || !node.IsHealthy {
		t.Fatalf("a node handed to the rest of the cell was marked unhealthy: %+v", node)
	}
	for _, id := range []string{"node-1", "canon-1"} {
		if _, armed := sm.NodePendingReconnect(id); !armed {
			t.Fatalf("no reconnect window armed for %s", id)
		}
	}
	select {
	case <-release:
	default:
		t.Fatal("a stream still open after the drain would be left to the hard stop")
	}
	if err := (&Server{}).Connect(stream); status.Code(err) != codes.Unavailable {
		t.Fatalf("a draining instance accepted a new control stream: %v", err)
	}
}
