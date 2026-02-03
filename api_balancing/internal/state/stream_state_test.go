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
