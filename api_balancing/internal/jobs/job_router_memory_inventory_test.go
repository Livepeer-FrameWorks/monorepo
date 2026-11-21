package jobs

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestRegisteredNodeAwaitingFirstReportDoesNotProveMemoryImpossible(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	previousAccess, previousInventory := clusterAccessibleForTenant, registeredProcessingNodes
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	registeredProcessingNodes = func(context.Context, string) ([]string, error) {
		return []string{"small", "awaiting-report"}, nil
	}
	t.Cleanup(func() {
		clusterAccessibleForTenant, registeredProcessingNodes = previousAccess, previousInventory
	})
	touchInCluster(sm, "small", routeTestCluster)
	setNodeMemory(sm, "small", 4*1024*1024*1024, map[string]state.ClassCapacity{
		mist.ProcessingClassVideoTranscode: {Total: 8},
	})
	// Registration can precede the first capacity report on a new connection.
	touchInCluster(sm, "awaiting-report", routeTestCluster)
	job := originJob()
	job.RequiredMemoryBytes = 6 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "" || reason == mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("a registration without its first report made the job permanently impossible: %s %s", id, reason)
	}
	setNodeMemory(sm, "awaiting-report", 32*1024*1024*1024, map[string]state.ClassCapacity{
		mist.ProcessingClassVideoTranscode: {Total: 8},
	})
	if id, reason := routeProcessingJob(job); id != "awaiting-report" {
		t.Fatalf("first report did not restore placement: %s %s", id, reason)
	}
}
