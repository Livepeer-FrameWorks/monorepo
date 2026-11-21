package jobs

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestProcessingInventoryIsSharedAndBoundedPerTick(t *testing.T) {
	previous := registeredProcessingNodes
	calls := 0
	registeredProcessingNodes = func(ctx context.Context, _ string) ([]string, error) {
		calls++
		select {
		case <-time.After(2 * time.Second):
			return []string{"slow"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	t.Cleanup(func() { registeredProcessingNodes = previous })
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	tick := newProcessingTickInventory(true)
	start := time.Now()
	for range 20 {
		tick.cluster(ctx, "origin")
	}
	if calls != 1 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("twenty claimed jobs exceeded tick budget: calls=%d duration=%s", calls, time.Since(start))
	}
}

func TestProcessingInventoryWarmupCannotFailUpload(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	useRuntimeProcessingInventory(t)
	previous := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = previous })
	touchInCluster(sm, "small", routeTestCluster)
	setNodeMemory(sm, "small", 1536*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	job := originJob()
	job.RequiredMemoryBytes = 2 * 1024 * 1024 * 1024
	if _, reason, _ := routeProcessingJobWithInventory(t.Context(), job, newProcessingTickInventory(false)); reason == mist.ProcessingMemoryNoNodeReason {
		t.Fatal("warming inventory permanently failed upload")
	}
	if _, reason, _ := routeProcessingJobWithInventory(t.Context(), job, newProcessingTickInventory(true)); reason != mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("fully reported warm inventory did not prove no-fit: %s", reason)
	}
}
