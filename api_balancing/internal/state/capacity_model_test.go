package state

import "testing"

func TestClusterLoadIncludesReservations(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("edge", "", true, nil, nil, "", "", nil)
	setMetrics(sm, "edge", 0, 0, 1000)
	setClusterID(sm, "edge", "cluster")
	sm.mu.Lock()
	sm.nodes["edge"].AddBandwidth = 900
	sm.recomputeNodeScoresLocked(sm.nodes["edge"])
	sm.mu.Unlock()
	if got, count := sm.ClusterLoad("cluster"); got != 90 || count != 1 {
		t.Fatalf("reserved load = %v/%d, want 90/1", got, count)
	}
}
