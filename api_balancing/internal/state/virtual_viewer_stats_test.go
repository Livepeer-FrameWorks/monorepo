package state

import "testing"

// TestGetVirtualViewerStats_EstPendingBandwidth pins the formerly-dead metric:
// estimated pending bandwidth sums PendingRedirects × EstBandwidthPerUser across
// nodes, and a node with zero pending redirects contributes nothing regardless
// of its per-user estimate. Before this was a missing map key that the consumer
// silently rendered as 0.
func TestGetVirtualViewerStats_EstPendingBandwidth(t *testing.T) {
	sm := NewStreamStateManager()
	sm.TouchNode("n1", true)
	sm.TouchNode("n2", true)

	sm.mu.Lock()
	sm.nodes["n1"].PendingRedirects = 3
	sm.nodes["n1"].EstBandwidthPerUser = 1000
	sm.nodes["n2"].PendingRedirects = 0
	sm.nodes["n2"].EstBandwidthPerUser = 5000 // ignored: no pending redirects
	sm.mu.Unlock()

	stats := sm.GetVirtualViewerStats()
	if stats.EstPendingBandwidth != 3000 {
		t.Fatalf("EstPendingBandwidth = %d, want 3000", stats.EstPendingBandwidth)
	}
	if stats.PendingByNode["n1"] != 3 {
		t.Fatalf("PendingByNode[n1] = %d, want 3", stats.PendingByNode["n1"])
	}
}
