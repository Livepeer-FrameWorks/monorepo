package state

import (
	"encoding/json"
	"testing"
	"time"
)

func setupStateManager(t *testing.T) *StreamStateManager {
	t.Helper()
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	return sm
}

func configureTestNode(sm *StreamStateManager, nodeID string) {
	sm.SetNodeInfo(nodeID, nodeID, true, nil, nil, "", "", nil)
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]ClassCapacity
	}{
		CPU:        0,
		RAMMax:     1024,
		RAMCurrent: 0,
		UpSpeed:    0,
		BWLimit:    1024 * 1024,
	})
	sm.TouchNode(nodeID, true)
}

// The destination cell reserves a prepared viewer under the preparation's
// attempt ID: the node's projected load rises at once, a retried preparation
// reserves once, and the viewer's connection presenting that ID activates it.
func TestReservePendingViewerByAttemptID(t *testing.T) {
	sm := setupStateManager(t)
	nodeID := "node-1"
	configureTestNode(sm, nodeID)
	sm.mu.Lock()
	sm.nodes[nodeID].EstBandwidthPerUser = 256 * 1024
	sm.mu.Unlock()

	if !sm.ReservePendingViewer("attempt-1", nodeID, "stream") {
		t.Fatal("first preparation did not reserve")
	}
	if sm.ReservePendingViewer("attempt-1", nodeID, "stream") {
		t.Fatal("a retried preparation reserved twice")
	}
	sm.mu.Lock()
	pending, reserved := sm.nodes[nodeID].PendingRedirects, sm.nodes[nodeID].AddBandwidth
	sm.mu.Unlock()
	if pending != 1 || reserved == 0 {
		t.Fatalf("reservation not applied: pending=%d reserved=%d", pending, reserved)
	}

	viewerID, started := sm.StartVirtualViewerByID("attempt-1", nodeID, "stream", "203.0.113.10")
	if viewerID != "attempt-1" || !started {
		t.Fatalf("connection did not activate the reservation: id=%q started=%v", viewerID, started)
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.nodes[nodeID].PendingRedirects != 0 {
		t.Fatalf("activated reservation still pending: %d", sm.nodes[nodeID].PendingRedirects)
	}
}

func TestCreateVirtualViewer_SaturatesAddBandwidth(t *testing.T) {
	sm := setupStateManager(t)
	nodeID := "node-1"
	configureTestNode(sm, nodeID)

	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.AddBandwidth = ^uint64(0) - 128
	node.EstBandwidthPerUser = 256
	sm.mu.Unlock()

	sm.CreateVirtualViewer(nodeID, "stream", "203.0.113.10")

	sm.mu.Lock()
	defer sm.mu.Unlock()
	if node.AddBandwidth != ^uint64(0) {
		t.Fatalf("expected AddBandwidth to saturate at max uint64, got %d", node.AddBandwidth)
	}
}

func TestReconcileVirtualViewers_TimesOutStalePending(t *testing.T) {
	sm := setupStateManager(t)
	nodeID := "node-2"
	configureTestNode(sm, nodeID)

	viewerID := sm.CreateVirtualViewer(nodeID, "stream", "203.0.113.20")

	sm.mu.Lock()
	viewer := sm.virtualViewers[viewerID]
	viewer.RedirectTime = time.Now().Add(-time.Minute)
	sm.mu.Unlock()

	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	sm.mu.Lock()
	defer sm.mu.Unlock()
	if viewer.State != VirtualViewerAbandoned {
		t.Fatalf("expected viewer to be abandoned, got %s", viewer.State)
	}
	if node := sm.nodes[nodeID]; node != nil {
		if node.PendingRedirects != 0 {
			t.Fatalf("expected PendingRedirects to be 0, got %d", node.PendingRedirects)
		}
		if node.AddBandwidth != 0 {
			t.Fatalf("expected AddBandwidth to be 0 after timeout, got %d", node.AddBandwidth)
		}
	}
}

func TestCalculateEstBandwidthPerUser_UsesNodeMetricsAndClusterAverage(t *testing.T) {
	sm := setupStateManager(t)
	nodeID := "node-3"
	configureTestNode(sm, nodeID)

	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.UpSpeed = 512 * 1024
	sm.streamInstances["stream-1"] = map[string]*StreamInstanceState{
		nodeID: {
			NodeID:           nodeID,
			TotalConnections: 4,
		},
	}
	est := sm.calculateEstBandwidthPerUserLocked(node)
	if est != 128*1024 {
		sm.mu.Unlock()
		t.Fatalf("expected 128KB/s estimate, got %d", est)
	}

	otherID := "node-4"
	sm.nodes[otherID] = &NodeState{
		NodeID:    otherID,
		IsHealthy: true,
		UpSpeed:   256 * 1024,
	}
	sm.streamInstances["stream-2"] = map[string]*StreamInstanceState{
		otherID: {
			NodeID:           otherID,
			TotalConnections: 2,
		},
	}
	sm.streamInstances["stream-1"][nodeID].TotalConnections = 0
	node.UpSpeed = 0
	node.EstBandwidthPerUser = 0
	est = sm.calculateEstBandwidthPerUserLocked(node)
	sm.mu.Unlock()
	if est != 128*1024 {
		t.Fatalf("expected cluster average 128KB/s, got %d", est)
	}
}

func TestClampBandwidthEnforcesMinAndMax(t *testing.T) {
	sm := setupStateManager(t)

	min := sm.clampBandwidth(1)
	if min != 64*1024 {
		t.Fatalf("expected min clamp 64KB/s, got %d", min)
	}

	max := sm.clampBandwidth(10 * 1024 * 1024)
	if max != 1024*1024 {
		t.Fatalf("expected max clamp 1MB/s, got %d", max)
	}
}

func TestUpdateUserConnection_ClampsBandwidthPenalty(t *testing.T) {
	sm := setupStateManager(t)
	nodeID := "node-5"
	configureTestNode(sm, nodeID)

	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]ClassCapacity
	}{
		UpSpeed: 10 * 1024 * 1024,
	})

	sm.UpdateUserConnection("stream-3", nodeID, "tenant-3", 1)

	sm.mu.Lock()
	defer sm.mu.Unlock()
	node := sm.nodes[nodeID]
	if node.AddBandwidth != 1024*1024 {
		t.Fatalf("expected AddBandwidth to be clamped to 1MB/s, got %d", node.AddBandwidth)
	}
}

// Staging rc7: four of six Foghorn replicas refused every live /play for 12
// minutes, scoring idle edges as capacity=exhausted. A replica that does not
// hold an edge's control stream gets the edge's lifecycle update only as
// replicated node state, and it never timed out its own pending redirects
// there, so their reserved bandwidth grew without bound.
func TestReplicatedNodeUpdateTimesOutLocalPendingRedirects(t *testing.T) {
	sm := setupStateManager(t)
	nodeID := "edge-not-held"
	configureTestNode(sm, nodeID)

	stale := sm.CreateVirtualViewer(nodeID, "stream", "203.0.113.30")
	fresh := sm.CreateVirtualViewer(nodeID, "stream", "203.0.113.31")
	sm.mu.Lock()
	sm.virtualViewers[stale].RedirectTime = time.Now().Add(-time.Minute)
	freshBandwidth := sm.virtualViewers[fresh].EstBandwidth
	payload, err := json.Marshal(sm.nodes[nodeID])
	sm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// The holder's lifecycle update arrives as replicated node state.
	sm.applyRedisChange(StateChange{Entity: StateEntityNode, NodeID: nodeID, Payload: payload})

	sm.mu.Lock()
	defer sm.mu.Unlock()
	if state := sm.virtualViewers[stale].State; state != VirtualViewerAbandoned {
		t.Fatalf("stale redirect state = %s, want abandoned", state)
	}
	node := sm.nodes[nodeID]
	if node.PendingRedirects != 1 || node.AddBandwidth != freshBandwidth {
		t.Fatalf("pending=%d addBandwidth=%d, want only the fresh redirect (1, %d)", node.PendingRedirects, node.AddBandwidth, freshBandwidth)
	}
}
