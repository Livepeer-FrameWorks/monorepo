package control

import (
	"context"
	"strconv"
	"sync"

	"frameworks/api_balancing/internal/database/foghorndb"
)

var processingCapacityEvents struct {
	sync.RWMutex
	handler func(string)
}

func SetProcessingCapacityHandler(handler func(string)) {
	processingCapacityEvents.Lock()
	processingCapacityEvents.handler = handler
	processingCapacityEvents.Unlock()
}

func NotifyProcessingCapacity(nodeID string) {
	processingCapacityEvents.RLock()
	handler := processingCapacityEvents.handler
	processingCapacityEvents.RUnlock()
	if handler != nil {
		handler(nodeID)
	}
}

// LastProcessingMemoryBudgets retains capacity evidence after runtime eviction.
// Node IDs come from the authorized cluster's infrastructure inventory.
func LastProcessingMemoryBudgets(ctx context.Context, ids []string) (map[string]uint64, error) {
	budgets := make(map[string]uint64)
	if db == nil || len(ids) == 0 {
		return budgets, nil
	}
	rows, err := foghorndb.New(db).ListProcessingNodeBudgets(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if value, parseErr := strconv.ParseUint(row.BudgetBytes, 10, 64); parseErr == nil {
			budgets[row.NodeID] = value
		}
	}
	return budgets, nil
}
