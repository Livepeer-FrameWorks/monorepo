package state

import (
	"encoding/json"
	"math"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// metricBytes rejects invalid telemetry conservatively before converting floats.
func metricBytes(value float64) uint64 {
	if math.IsNaN(value) || value < 0 || value >= math.Exp2(64) {
		return math.MaxUint64
	}
	return uint64(value)
}

func nodeLink(budget, observed float64, reserved uint64) capacity.Link {
	if math.IsNaN(budget) || math.IsInf(budget, 0) || budget < 1 || budget >= math.Exp2(64) {
		return capacity.Link{}
	}
	return capacity.Link{Budget: uint64(budget), Observed: metricBytes(observed), Reserved: reserved}
}

func (n *NodeState) EgressLink() capacity.Link {
	link := nodeLink(n.BWLimit, n.UpSpeed, n.AddBandwidth)
	if n.Capacity != nil {
		link.Budget = n.Capacity.GetEgressBudgetBytesPerSec()
	}
	return link
}

func (n *NodeState) IngressLink() capacity.Link {
	link := nodeLink(n.BWLimit, n.DownSpeed, n.IngressReservedBytesPerSec)
	if n.Capacity != nil {
		link.Budget = n.Capacity.GetIngressBudgetBytesPerSec()
	}
	return link
}

func (n EnhancedBalancerNodeSnapshot) EgressLink() capacity.Link {
	link := nodeLink(n.BWLimit, n.UpSpeed, n.AddBandwidth)
	if n.Capacity != nil {
		link.Budget = n.Capacity.GetEgressBudgetBytesPerSec()
	}
	return link
}

func (n EnhancedBalancerNodeSnapshot) IngressLink() capacity.Link {
	link := nodeLink(n.BWLimit, n.DownSpeed, n.IngressReservedBytesPerSec)
	if n.Capacity != nil {
		link.Budget = n.Capacity.GetIngressBudgetBytesPerSec()
	}
	return link
}

func cloneUint64Ptr(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func DefaultNodeCapacity() *ipcpb.NodeBandwidthCapacity {
	return &ipcpb.NodeBandwidthCapacity{
		EgressBudgetBytesPerSec: capacity.DefaultBudget, IngressBudgetBytesPerSec: capacity.DefaultBudget,
		EgressSource:  ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT,
		IngressSource: ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT,
	}
}

func (sm *StreamStateManager) ShouldResendNodeCapacity(nodeID string, applied *uint64, now time.Time) bool {
	if applied == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	n := sm.nodes[nodeID]
	if n == nil || n.Capacity == nil || n.Capacity.GetRevision() == *applied || now.Sub(n.CapacityResendAt) < 30*time.Second {
		return false
	}
	n.CapacityResendAt = now
	return true
}

func (sm *StreamStateManager) SetNodeCapacity(nodeID string, budget *ipcpb.NodeBandwidthCapacity) {
	if budget == nil || budget.GetEgressBudgetBytesPerSec() == 0 || budget.GetIngressBudgetBytesPerSec() == 0 {
		return
	}
	sm.mu.Lock()
	if _, evicted := sm.evictedNodes[nodeID]; evicted {
		sm.mu.Unlock()
		return
	}
	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
	}
	if n.Capacity != nil && (n.Capacity.GetRevision() > budget.GetRevision() || proto.Equal(n.Capacity, budget)) {
		sm.mu.Unlock()
		return
	}
	n.Capacity = proto.CloneOf(budget)
	n.BWLimit = float64(budget.GetEgressBudgetBytesPerSec())
	sm.recomputeNodeScoresLocked(n)
	payload, err := json.Marshal(n)
	sm.mu.Unlock()
	if err != nil {
		logging.NewLogger().WithError(err).WithField("node_id", nodeID).Warn("Could not serialize composed node capacity")
		return
	}
	sm.persistNodeWriteThrough(nodeID, payload)
}

func (sm *StreamStateManager) nodeCapacityObservation(update *ipcpb.NodeLifecycleUpdate) (budget, up, down float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, evicted := sm.evictedNodes[update.GetNodeId()]; evicted {
		return 0, 0, 0
	}
	n := sm.nodes[update.GetNodeId()]
	if n == nil {
		n = newNodeState(update.GetNodeId())
		sm.nodes[update.GetNodeId()] = n
	}
	if n.Capacity == nil {
		n.Capacity = DefaultNodeCapacity()
	}
	n.CapacityRevisionApplied = nil
	if update.CapacityRevisionApplied != nil {
		applied := update.GetCapacityRevisionApplied()
		n.CapacityRevisionApplied = &applied
	}
	n.ObservedSource = update.GetObservedSource()
	if n.ObservedSource == ipcpb.ObservedSource_OBSERVED_SOURCE_UNSPECIFIED {
		n.ObservedSource = ipcpb.ObservedSource_OBSERVED_SOURCE_MIST
	}
	n.ObservedValid = update.ObservedValid == nil || update.GetObservedValid()
	n.SampleWindowStartMS, n.SampleWindowEndMS = update.GetSampleWindowStartMs(), update.GetSampleWindowEndMs()
	if !n.ObservedValid {
		return float64(n.Capacity.GetEgressBudgetBytesPerSec()), n.UpSpeed, n.DownSpeed
	}
	return float64(n.Capacity.GetEgressBudgetBytesPerSec()), float64(update.GetUpSpeed()), float64(update.GetDownSpeed())
}
