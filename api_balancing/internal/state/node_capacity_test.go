package state

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestNodeLifecycleCannotAuthorBandwidthBudget(t *testing.T) {
	manager := NewStreamStateManager()
	defer manager.Shutdown()
	for _, reported := range []uint64{0, 134217728, 9000000000} {
		if err := manager.ApplyNodeLifecycle(context.Background(), &ipcpb.NodeLifecycleUpdate{NodeId: "edge", BwLimit: reported, IsHealthy: true}); err != nil {
			t.Fatal(err)
		}
		if got := manager.GetNodeState("edge").BWLimit; got != float64(capacity.DefaultBudget) {
			t.Errorf("reported budget %d changed authoritative default to %v", reported, got)
		}
	}
}

func TestNodeCapacityPreservesValidObservationAndFencesRevision(t *testing.T) {
	manager := NewStreamStateManager()
	defer manager.Shutdown()
	budget := DefaultNodeCapacity()
	budget.EgressBudgetBytesPerSec, budget.Revision = 300000000, 4
	manager.SetNodeCapacity("edge", budget)
	for _, update := range []*ipcpb.NodeLifecycleUpdate{
		{NodeId: "edge", IsHealthy: true, UpSpeed: 100, DownSpeed: 200, ObservedValid: proto.Bool(true)},
		{NodeId: "edge", IsHealthy: true, UpSpeed: 0, DownSpeed: 0, ObservedValid: proto.Bool(false)},
	} {
		if err := manager.ApplyNodeLifecycle(context.Background(), update); err != nil {
			t.Fatal(err)
		}
	}
	manager.SetNodeCapacity("edge", DefaultNodeCapacity())
	node := manager.GetNodeState("edge")
	if node.UpSpeed != 100 || node.DownSpeed != 200 || node.BWLimit != 300000000 || node.Capacity.GetRevision() != 4 {
		t.Fatalf("invalid report or old seed replaced trusted observation: %+v", node)
	}
	now := time.Now()
	if !manager.ShouldResendNodeCapacity("edge", proto.Uint64(3), now) || manager.ShouldResendNodeCapacity("edge", proto.Uint64(3), now.Add(time.Second)) || !manager.ShouldResendNodeCapacity("edge", proto.Uint64(3), now.Add(31*time.Second)) {
		t.Fatal("capacity mismatch re-send must be limited to once per 30 seconds")
	}
	if manager.ShouldResendNodeCapacity("edge", proto.Uint64(4), now.Add(time.Minute)) || manager.ShouldResendNodeCapacity("edge", nil, now.Add(time.Minute)) {
		t.Fatal("applied capacity or an older sidecar must not trigger a mismatch resend")
	}
	local := manager.GetNodeState("edge")
	incoming := &NodeState{NodeID: "edge", BWLimit: 9000000000}
	mergeIncomingNode(incoming, local)
	if incoming.BWLimit != 300000000 {
		t.Fatal("an older peer's report overwrote composed capacity")
	}
}

func TestPeerBudgetCannotComeFromALegacyNodeReport(t *testing.T) {
	incoming := &NodeState{NodeID: "node", BWLimit: 9000000000, MetricsObservedAt: time.Now()}
	mergeIncomingNode(incoming, nil)
	if incoming.BWLimit != float64(capacity.DefaultBudget) || incoming.Capacity == nil {
		t.Fatalf("legacy peer authored budget: %+v", incoming)
	}
	if incoming.ObservedSource != ipcpb.ObservedSource_OBSERVED_SOURCE_MIST || !incoming.ObservedValid {
		t.Fatal("legacy metric provenance was not marked MIST")
	}
}

func TestPeerCapacityAndMeasurementHaveIndependentClocks(t *testing.T) {
	now := time.Now()
	oldBudget, newBudget := DefaultNodeCapacity(), DefaultNodeCapacity()
	oldBudget.EgressBudgetBytesPerSec, oldBudget.Revision = 100000000, 1
	newBudget.EgressBudgetBytesPerSec, newBudget.Revision = 200000000, 2
	local := &NodeState{NodeID: "node", Capacity: oldBudget, BWLimit: 100000000, MetricsObservedAt: now, UpSpeed: 50000000, ObservedSource: ipcpb.ObservedSource_OBSERVED_SOURCE_MIST, ObservedValid: true, SampleWindowStartMS: 10000, SampleWindowEndMS: 20000, CapacityRevisionApplied: proto.Uint64(1)}
	incoming := &NodeState{NodeID: "node", Capacity: newBudget, BWLimit: 200000000, MetricsObservedAt: now.Add(-time.Second), UpSpeed: 75000000, ObservedSource: ipcpb.ObservedSource_OBSERVED_SOURCE_NIC, SampleWindowStartMS: 0, SampleWindowEndMS: 1000, CapacityRevisionApplied: proto.Uint64(0)}
	mergeIncomingNode(incoming, local)
	if incoming.Capacity.GetRevision() != 2 || incoming.BWLimit != 200000000 || incoming.UpSpeed != 50000000 {
		t.Errorf("budget or measurement clock regressed: %+v", incoming)
	}
	if incoming.ObservedSource != local.ObservedSource || !incoming.ObservedValid || incoming.SampleWindowEndMS != 20000 || incoming.CapacityRevisionApplied == nil || *incoming.CapacityRevisionApplied != 1 {
		t.Errorf("measurement provenance regressed: %+v", incoming)
	}
}
