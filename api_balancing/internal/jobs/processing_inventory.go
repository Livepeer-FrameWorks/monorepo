package jobs

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
)

const processingInventoryGrace = time.Minute

var processingNodeProtocolOK = control.NodeProcessingProtocolOK

type processingClusterInventory struct {
	ids     []string
	budgets map[string]uint64
	err     error
}

func (i processingClusterInventory) budget(id string, sm *state.StreamStateManager) (uint64, bool) {
	if n := sm.GetNodeState(id); n != nil && n.ProcessingMemoryBudgetBytes != nil {
		if !n.CapProcessing {
			return 0, true
		}
		return *n.ProcessingMemoryBudgetBytes, true
	}
	b, ok := i.budgets[id]
	return b, ok
}

type processingTickInventory struct {
	warm      bool
	clusters  map[string]processingClusterInventory
	protocols map[string]bool
}

func newProcessingTickInventory(warm bool) *processingTickInventory {
	return &processingTickInventory{warm: warm, clusters: make(map[string]processingClusterInventory), protocols: make(map[string]bool)}
}

func (t *processingTickInventory) protocolCompatible(ctx context.Context, nodeID string) bool {
	if supported, present := t.protocols[nodeID]; present {
		return supported
	}
	supported, known := processingNodeProtocolOK(ctx, nodeID)
	compatible := !known || supported
	t.protocols[nodeID] = compatible
	return compatible
}

func (t *processingTickInventory) cluster(ctx context.Context, id string) processingClusterInventory {
	if result, ok := t.clusters[id]; ok {
		return result
	}
	ids, err := registeredProcessingNodes(ctx, id)
	result := processingClusterInventory{ids: ids, err: err}
	if err == nil {
		result.budgets, result.err = control.LastProcessingMemoryBudgets(ctx, ids)
	}
	t.clusters[id] = result
	return result
}
