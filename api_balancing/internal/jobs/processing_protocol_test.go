package jobs

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
)

func TestProcessingPlacementSkipsLegacyResultProtocol(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	previousAccess, previousProtocol := clusterAccessibleForTenant, processingNodeProtocolOK
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	calls := map[string]int{}
	processingNodeProtocolOK = func(_ context.Context, nodeID string) (bool, bool) {
		calls[nodeID]++
		return nodeID == "current", true
	}
	t.Cleanup(func() {
		clusterAccessibleForTenant, processingNodeProtocolOK = previousAccess, previousProtocol
	})
	for _, id := range []string{"legacy", "current"} {
		touchInCluster(sm, id, routeTestCluster)
		setNodeProcessing(sm, id, true, 8, 0)
	}
	setNodeProcessing(sm, "current", true, 8, 4)
	tick := newProcessingTickInventory(true)
	for i := 0; i < 2; i++ {
		id, reason, _ := routeProcessingJobWithInventory(t.Context(), originJob(), tick)
		if id != "current" {
			t.Fatalf("legacy idle node displaced capable busy node: id=%q reason=%q", id, reason)
		}
	}
	if calls["legacy"] != 1 || calls["current"] != 1 {
		t.Fatalf("capability was not cached within dispatch tick: %v", calls)
	}
}
