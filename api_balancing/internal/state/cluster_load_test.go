package state

import (
	"testing"
	"time"
)

func TestClusterLoadCPUOnly(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	defer sm.Shutdown()

	// Two healthy nodes in target cluster, one in another cluster, one
	// unhealthy. No node declares BWLimit so the signal is CPU-only.
	sm.SetNodeInfo("n1", "", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("n2", "", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("n3-other", "", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("n4-unhealthy", "", false, nil, nil, "", "", nil)

	setMetrics(sm, "n1", 30, 0, 0)
	setMetrics(sm, "n2", 50, 0, 0)
	setMetrics(sm, "n3-other", 90, 0, 0)
	setMetrics(sm, "n4-unhealthy", 90, 0, 0)

	setClusterID(sm, "n1", "media-cluster-a")
	setClusterID(sm, "n2", "media-cluster-a")
	setClusterID(sm, "n3-other", "media-cluster-b")
	setClusterID(sm, "n4-unhealthy", "media-cluster-a")

	load, sample := sm.ClusterLoad("media-cluster-a")
	if sample != 2 {
		t.Fatalf("expected 2 samples (healthy nodes in cluster), got %d", sample)
	}
	if load != 40 {
		t.Errorf("expected load=40 (avg CPU, no bandwidth signal), got %v", load)
	}
}

func TestClusterLoadBandwidthDominatesCPU(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	defer sm.Shutdown()

	// Two nodes, low CPU but bandwidth saturated → load reflects bandwidth.
	sm.SetNodeInfo("n1", "", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("n2", "", true, nil, nil, "", "", nil)
	setMetrics(sm, "n1", 10, 900, 1000) // 90% uplink
	setMetrics(sm, "n2", 10, 900, 1000) // 90% uplink
	setClusterID(sm, "n1", "media-cluster-a")
	setClusterID(sm, "n2", "media-cluster-a")

	load, _ := sm.ClusterLoad("media-cluster-a")
	if load != 90 {
		t.Errorf("expected load=90 (uplink dominates), got %v", load)
	}
}

func TestClusterLoadCPUDominatesBandwidth(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	defer sm.Shutdown()

	// High CPU, idle bandwidth → load reflects CPU.
	sm.SetNodeInfo("n1", "", true, nil, nil, "", "", nil)
	setMetrics(sm, "n1", 85, 100, 1000) // 10% uplink, 85% CPU
	setClusterID(sm, "n1", "media-cluster-a")

	load, _ := sm.ClusterLoad("media-cluster-a")
	if load != 85 {
		t.Errorf("expected load=85 (CPU dominates), got %v", load)
	}
}

func TestClusterLoadIgnoresNodesWithoutBWLimit(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	defer sm.Shutdown()

	// Mixed cluster: one node reports BW, one doesn't. Bandwidth aggregation
	// only counts the node with BWLimit.
	sm.SetNodeInfo("with-bw", "", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("no-bw", "", true, nil, nil, "", "", nil)
	setMetrics(sm, "with-bw", 20, 950, 1000) // 95% uplink
	setMetrics(sm, "no-bw", 60, 0, 0)        // no BWLimit; ignored by BW term
	setClusterID(sm, "with-bw", "media-cluster-a")
	setClusterID(sm, "no-bw", "media-cluster-a")

	load, sample := sm.ClusterLoad("media-cluster-a")
	if sample != 2 {
		t.Fatalf("expected 2 samples, got %d", sample)
	}
	// Avg CPU = (20+60)/2 = 40. BW = 950/1000*100 = 95. Max = 95.
	if load != 95 {
		t.Errorf("expected load=95, got %v", load)
	}
}

func TestClusterLoadReturnsZeroWhenClusterEmpty(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	defer sm.Shutdown()

	load, sample := sm.ClusterLoad("ghost-cluster")
	if sample != 0 {
		t.Errorf("expected zero samples for empty cluster, got %d", sample)
	}
	if load != 0 {
		t.Errorf("expected load=0, got %v", load)
	}
}

func setMetrics(sm *StreamStateManager, nodeID string, cpu, upSpeed, bwLimit float64) {
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
	}{CPU: cpu, UpSpeed: upSpeed, BWLimit: bwLimit})
}

func setClusterID(sm *StreamStateManager, nodeID, clusterID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if n := sm.nodes[nodeID]; n != nil {
		n.ClusterID = clusterID
		n.LastHeartbeat = time.Now()
		n.IsStale = false
	}
}
