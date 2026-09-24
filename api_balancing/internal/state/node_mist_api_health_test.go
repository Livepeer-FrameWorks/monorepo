package state

import (
	"context"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func balancerHasNode(sm *StreamStateManager, nodeID string) bool {
	for _, n := range sm.GetBalancerSnapshotAtomic().Nodes {
		if n.NodeID == nodeID {
			return true
		}
	}
	return false
}

// A node whose Mist controller API stopped answering Helmsman must leave the
// balancer snapshot (placement and processing) even when is_healthy is true,
// and return once Helmsman reports the API reachable again.
func TestApplyNodeLifecycleMistAPIUnreachableExcludesNode(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	ctx := context.Background()
	reachable := func(v bool) *bool { return &v }

	apply := func(api *bool) {
		t.Helper()
		if err := sm.ApplyNodeLifecycle(ctx, &ipcpb.NodeLifecycleUpdate{
			NodeId:           "edge-1",
			BaseUrl:          "https://edge-1.example",
			IsHealthy:        true,
			MistApiReachable: api,
		}); err != nil {
			t.Fatalf("ApplyNodeLifecycle: %v", err)
		}
	}

	apply(reachable(false))
	node := sm.GetNodeState("edge-1")
	if node == nil || node.IsHealthy {
		t.Fatalf("mist_api_reachable=false left node healthy: %#v", node)
	}
	if node.IsStale {
		t.Fatal("unreachable Mist API must not make the heartbeat stale")
	}
	if balancerHasNode(sm, "edge-1") {
		t.Fatal("node with unreachable Mist API is still in the balancer snapshot")
	}

	apply(reachable(true))
	if node := sm.GetNodeState("edge-1"); node == nil || !node.IsHealthy {
		t.Fatal("node did not recover after mist_api_reachable=true")
	}
	if !balancerHasNode(sm, "edge-1") {
		t.Fatal("recovered node missing from the balancer snapshot")
	}
}

func TestNodeLifecycleHealthyLegacySidecarDefersToIsHealthy(t *testing.T) {
	if !NodeLifecycleHealthy(&ipcpb.NodeLifecycleUpdate{IsHealthy: true}) {
		t.Fatal("unset mist_api_reachable must defer to is_healthy=true")
	}
	if NodeLifecycleHealthy(&ipcpb.NodeLifecycleUpdate{IsHealthy: false}) {
		t.Fatal("is_healthy=false must stay unhealthy")
	}
	up := true
	if NodeLifecycleHealthy(&ipcpb.NodeLifecycleUpdate{IsHealthy: false, MistApiReachable: &up}) {
		t.Fatal("a reachable API must not override is_healthy=false")
	}
}
