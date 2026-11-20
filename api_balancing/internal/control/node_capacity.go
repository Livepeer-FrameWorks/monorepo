package control

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func capacityFromQuartermaster(record *quartermasterpb.NodeCapacity) *ipcpb.NodeBandwidthCapacity {
	budget := state.DefaultNodeCapacity()
	if record == nil {
		return budget
	}
	budget.Revision = record.GetRevision()
	if record.EgressBudgetBytesPerSec != nil && record.GetEgressBudgetBytesPerSec() > 0 {
		budget.EgressBudgetBytesPerSec = record.GetEgressBudgetBytesPerSec()
		budget.EgressSource = ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_OPERATOR
	}
	if record.IngressBudgetBytesPerSec != nil && record.GetIngressBudgetBytesPerSec() > 0 {
		budget.IngressBudgetBytesPerSec = record.GetIngressBudgetBytesPerSec()
		budget.IngressSource = ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_OPERATOR
	}
	return budget
}

// PushNodeCapacity persists a full seed before sending or relaying it to the current connection owner.
func PushNodeCapacity(nodeID string) (uint64, bool, error) {
	mode := ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	switch state.DefaultManager().GetNodeOperationalMode(nodeID) {
	case state.NodeModeDraining:
		mode = ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING
	case state.NodeModeMaintenance:
		mode = ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE
	}
	peerAddr := ""
	if registry != nil {
		registry.mu.RLock()
		if c := registry.conns[nodeID]; c != nil {
			peerAddr = c.peerAddr
		}
		registry.mu.RUnlock()
	}
	seed, fallback := composeConfigSeedCandidate(nodeID, nil, peerAddr, mode, "")
	err := SendConfigSeedWithFallback(nodeID, seed, fallback)
	return seed.GetCapacity().GetRevision(), err == nil, err
}

func ReconcileNodeCapacityReport(nodeID string, applied *uint64, log logging.Logger) {
	if !state.DefaultManager().ShouldResendNodeCapacity(nodeID, applied, time.Now()) {
		return
	}
	go func() {
		if _, _, err := PushNodeCapacity(nodeID); err != nil {
			log.WithError(err).WithField("node_id", nodeID).Warn("Could not re-send unapplied node capacity")
		}
	}()
}

func StartCapacitySyncLoop(ctx context.Context, interval time.Duration, log logging.Logger) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncNodeCapacities(ctx, log)
		}
	}
}

func syncNodeCapacities(ctx context.Context, log logging.Logger) {
	client := quartermasterClient
	if client == nil || registry == nil {
		return
	}
	registry.mu.RLock()
	seen := make(map[string]struct{}, len(registry.conns))
	for _, c := range registry.conns {
		seen[c.session().NodeID()] = struct{}{}
	}
	registry.mu.RUnlock()
	nodeIDs := make([]string, 0, len(seen))
	for nodeID := range seen {
		nodeIDs = append(nodeIDs, nodeID)
	}
	for start := 0; start < len(nodeIDs); start += 1000 {
		end := min(start+1000, len(nodeIDs))
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		response, err := client.ListNodeCapacities(readCtx, &quartermasterpb.ListNodeCapacitiesRequest{NodeIds: nodeIDs[start:end]})
		cancel()
		if err != nil {
			log.WithError(err).Warn("Could not refresh connected node capacities")
			return
		}
		for nodeID, record := range response.GetCapacities() {
			if ctx.Err() != nil {
				return
			}
			current := state.DefaultManager().GetNodeState(nodeID)
			if record == nil || (current != nil && current.Capacity != nil && current.Capacity.GetRevision() >= record.GetRevision()) {
				continue
			}
			if _, _, err := PushNodeCapacity(nodeID); err != nil {
				log.WithError(err).WithField("node_id", nodeID).Warn("Could not push refreshed node capacity")
			}
		}
	}
}
