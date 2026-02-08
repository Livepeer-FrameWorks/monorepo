package state

import (
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
		MaxTranscodes        int
		CurrentTranscodes    int
	}{
		CPU:        0,
		RAMMax:     1024,
		RAMCurrent: 0,
		UpSpeed:    0,
		BWLimit:    1024 * 1024,
	})
	sm.TouchNode(nodeID, true)
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
