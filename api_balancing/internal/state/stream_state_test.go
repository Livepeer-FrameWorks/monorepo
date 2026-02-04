package state

import (
	"testing"
	"time"
)

func TestReconcileVirtualViewers_CleansUpAbandonedViewers(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create a virtual viewer (PENDING)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")
	if viewerID == "" {
		t.Fatal("expected viewer ID to be created")
	}

	// Manually set the viewer to ABANDONED with old timestamp (simulating timeout)
	sm.mu.Lock()
	viewer := sm.virtualViewers[viewerID]
	if viewer == nil {
		sm.mu.Unlock()
		t.Fatal("expected viewer to exist")
	}
	viewer.State = VirtualViewerAbandoned
	viewer.DisconnectTime = time.Now().Add(-10 * time.Minute) // 10 min ago, older than 5 min retention
	sm.mu.Unlock()

	// Verify viewer exists before reconciliation
	stats := sm.GetVirtualViewerStats()
	if stats["abandoned"].(int) != 1 {
		t.Fatalf("expected 1 abandoned viewer, got %v", stats["abandoned"])
	}

	// Call ReconcileVirtualViewers - this should clean up the old abandoned viewer
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was cleaned up
	stats = sm.GetVirtualViewerStats()
	if stats["abandoned"].(int) != 0 {
		t.Fatalf("expected 0 abandoned viewers after cleanup, got %v", stats["abandoned"])
	}
	if stats["total_viewers"].(int) != 0 {
		t.Fatalf("expected 0 total viewers after cleanup, got %v", stats["total_viewers"])
	}
}

func TestReconcileVirtualViewers_KeepsRecentAbandonedViewers(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create a virtual viewer (PENDING)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")

	// Manually set the viewer to ABANDONED with recent timestamp
	sm.mu.Lock()
	viewer := sm.virtualViewers[viewerID]
	viewer.State = VirtualViewerAbandoned
	viewer.DisconnectTime = time.Now().Add(-1 * time.Minute) // 1 min ago, within 5 min retention
	sm.mu.Unlock()

	// Call ReconcileVirtualViewers
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was NOT cleaned up (too recent)
	stats := sm.GetVirtualViewerStats()
	if stats["abandoned"].(int) != 1 {
		t.Fatalf("expected 1 abandoned viewer (recent), got %v", stats["abandoned"])
	}
}

func TestReconcileVirtualViewers_TimeoutsPendingViewers(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create a virtual viewer (PENDING)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")

	// Manually set old redirect time (simulating >30s ago)
	sm.mu.Lock()
	viewer := sm.virtualViewers[viewerID]
	viewer.RedirectTime = time.Now().Add(-1 * time.Minute) // 1 min ago, older than 30s timeout
	sm.mu.Unlock()

	// Verify viewer is PENDING before reconciliation
	stats := sm.GetVirtualViewerStats()
	if stats["pending"].(int) != 1 {
		t.Fatalf("expected 1 pending viewer, got %v", stats["pending"])
	}

	// Call ReconcileVirtualViewers - this should timeout the pending viewer
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was marked as ABANDONED
	stats = sm.GetVirtualViewerStats()
	if stats["pending"].(int) != 0 {
		t.Fatalf("expected 0 pending viewers after timeout, got %v", stats["pending"])
	}
	if stats["abandoned"].(int) != 1 {
		t.Fatalf("expected 1 abandoned viewer after timeout, got %v", stats["abandoned"])
	}
}

func TestGetViewerDrift(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create and confirm a virtual viewer (ACTIVE)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")
	sm.ConfirmVirtualViewerByID(viewerID, nodeID, streamName, "192.168.1.1", "mist-session-1")

	// No real connections reported by Helmsman yet
	drift := sm.GetViewerDrift()
	if drift[nodeID] != 1 {
		t.Fatalf("expected drift of 1 (1 virtual, 0 real), got %v", drift[nodeID])
	}

	// Simulate Helmsman reporting 1 real connection
	sm.UpdateNodeStats(streamName, nodeID, 1, 0, 0, 0, false)

	drift = sm.GetViewerDrift()
	if drift[nodeID] != 0 {
		t.Fatalf("expected drift of 0 (1 virtual, 1 real), got %v", drift[nodeID])
	}

	// Simulate Helmsman reporting 2 real connections (more real than virtual)
	sm.UpdateNodeStats(streamName, nodeID, 2, 0, 0, 0, false)

	drift = sm.GetViewerDrift()
	if drift[nodeID] != -1 {
		t.Fatalf("expected drift of -1 (1 virtual, 2 real), got %v", drift[nodeID])
	}
}

func TestTouchNodeUpdatesLastHeartbeat(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "heartbeat-node"
	sm.TouchNode(nodeID, true)

	node := sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if node.LastHeartbeat.IsZero() {
		t.Fatal("expected LastHeartbeat to be set")
	}
}

func TestCheckStaleNodesUsesLastHeartbeat(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "stale-node"
	sm.TouchNode(nodeID, true)

	now := time.Now()
	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.LastHeartbeat = now.Add(-2 * time.Minute)
	node.LastUpdate = now
	node.IsHealthy = true
	node.IsStale = false
	sm.mu.Unlock()

	sm.checkStaleNodes()

	node = sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected node to be stale based on heartbeat")
	}
	if node.IsHealthy {
		t.Fatal("expected node to be unhealthy when stale")
	}
}

func TestMetricsUpdateDoesNotPreventStaleness(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "metrics-node"
	sm.TouchNode(nodeID, true)

	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.LastHeartbeat = time.Now().Add(-2 * time.Minute)
	node.IsHealthy = true
	node.IsStale = false
	sm.mu.Unlock()

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
		CPU:                  10,
		RAMMax:               1024,
		RAMCurrent:           512,
		UpSpeed:              100,
		DownSpeed:            100,
		BWLimit:              1000,
		CapIngest:            true,
		CapEdge:              true,
		CapStorage:           false,
		CapProcessing:        false,
		Roles:                []string{"edge"},
		StorageCapacityBytes: 1024,
		StorageUsedBytes:     256,
		MaxTranscodes:        1,
		CurrentTranscodes:    0,
	})

	sm.checkStaleNodes()

	node = sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected node to remain stale after metrics update")
	}
}

func TestMarkNodeDisconnected(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "disconnect-node"
	sm.TouchNode(nodeID, true)

	sm.MarkNodeDisconnected(nodeID)

	node := sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if node.IsHealthy {
		t.Fatal("expected node to be unhealthy after disconnect")
	}
	if !node.IsStale {
		t.Fatal("expected node to be stale after disconnect")
	}
}

func TestSetNodeInfoDoesNotReviveStaleNode(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "rehydrate-node"
	sm.TouchNode(nodeID, true)

	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.IsStale = true
	node.IsHealthy = false
	node.LastHeartbeat = time.Now().Add(-2 * time.Minute)
	sm.mu.Unlock()

	sm.SetNodeInfo(nodeID, "http://example.com", true, nil, nil, "us-east", "", nil)

	node = sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected node to remain stale after SetNodeInfo")
	}
}

func TestNewNodeStartsStaleUntilHeartbeat(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "new-node"
	sm.SetNodeInfo(nodeID, "http://example.com", true, nil, nil, "us-west", "", nil)

	node := sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected new node to start stale before heartbeat")
	}
	if node.LastHeartbeat.IsZero() == false {
		t.Fatal("expected LastHeartbeat to be zero before heartbeat")
	}
}
