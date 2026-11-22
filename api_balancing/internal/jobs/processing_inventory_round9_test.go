package jobs

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// D3d: never-fit is judged over registered PROCESSING nodes. A relay/edge
// with processing disabled cannot run the job, so its budget must not make
// an impossible ladder look possible.
func TestR9NonProcessingEdgeBudgetDoesNotDefeatNeverFit(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	useRuntimeProcessingInventory(t)
	previous := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = previous })

	touchInCluster(sm, "proc", routeTestCluster)
	setNodeMemory(sm, "proc", 2*1024*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	touchInCluster(sm, "relay", routeTestCluster)
	sm.UpdateNodeMetrics("relay", state.NodeMetrics{RAMMax: 64 * 1024 * 1024 * 1024, CapProcessing: false, CapEdge: true})
	sm.SetNodeProcessingMemoryBudget("relay", mist.ProcessingMemoryBudget(64*1024*1024*1024))

	job := originJob()
	job.RequiredMemoryBytes = 6 * 1024 * 1024 * 1024
	id, reason := routeProcessingJob(job)
	if id != "" || reason != mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("non-processing edge budget kept an impossible job waiting: node=%q reason=%q", id, reason)
	}
}

// D3d: the last reported budget of a disconnected, drained registered node
// still counts, and an unreported registered node yields a
// fitting_node_unavailable wait rather than a failure.
func TestR9NeverFitCountsUnhealthyDrainedAndUnknownNodes(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	useRuntimeProcessingInventory(t)
	previous := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = previous })
	classes := map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}}
	touchInCluster(sm, "small", routeTestCluster)
	setNodeMemory(sm, "small", 2*1024*1024*1024, classes)
	touchInCluster(sm, "big", routeTestCluster)
	setNodeMemory(sm, "big", 64*1024*1024*1024, classes)
	_ = sm.SetNodeOperationalMode(context.Background(), "big", state.NodeModeDraining, "test")
	sm.MarkNodeDisconnected("big")
	job := originJob()
	job.RequiredMemoryBytes = 6 * 1024 * 1024 * 1024
	id, reason, wait := routeProcessingJobWithInventory(context.Background(), job, newProcessingTickInventory(true))
	if id != "" || reason == mist.ProcessingMemoryNoNodeReason || wait != mist.ProcessingWaitFittingNodeUnavailable {
		t.Fatalf("drained disconnected big node: id=%q reason=%q wait=%q", id, reason, wait)
	}
	touchInCluster(sm, "unreported", routeTestCluster)
	job.RequiredMemoryBytes = 128 * 1024 * 1024 * 1024
	id, reason, wait = routeProcessingJobWithInventory(context.Background(), job, newProcessingTickInventory(true))
	if id != "" || reason == mist.ProcessingMemoryNoNodeReason || wait != mist.ProcessingWaitFittingNodeUnavailable {
		t.Fatalf("unreported node: id=%q reason=%q wait=%q", id, reason, wait)
	}
}
