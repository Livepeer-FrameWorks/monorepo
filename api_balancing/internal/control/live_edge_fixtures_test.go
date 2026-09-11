package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
)

// seedLiveEdgeNode registers a healthy, probe-verified edge node that the load
// balancer will score above zero: CapEdge=true, RAMMax/BWLimit set with idle
// up-speed so BWAvailable>0. Returns after recompute+touch+probe so the node
// appears IsActive in the balancer snapshot. Unique IDs per test avoid
// cross-test contamination through the package-global DefaultManager.
func seedLiveEdgeNode(t *testing.T, sm *state.StreamStateManager, nodeID, baseURL string, lat, lon float64, outputs map[string]any) {
	t.Helper()
	plat, plon := lat, lon
	sm.SetNodeInfo(nodeID, baseURL, true, &plat, &plon, "loc-"+nodeID, "", outputs)
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
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		CPU:     10,
		RAMMax:  16_000_000_000,
		BWLimit: 1_000_000_000,
		UpSpeed: 1_000, // negligible vs BWLimit -> BWAvailable > 0
		CapEdge: true,
	})
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, true)
	AddPlatformSharedCluster("test-platform-live")
	sm.SetNodeConnectionInfo(context.Background(), nodeID, baseURL, "", "test-platform-live", nil)
}
