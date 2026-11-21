package state

// SetNodeProcessingMemoryBudget stores Helmsman's effective budget without
// extending liveness. A job refusal can refine capacity after a heartbeat.
func (sm *StreamStateManager) SetNodeProcessingMemoryBudget(nodeID string, budget uint64) {
	sm.mu.Lock()
	n := sm.nodes[nodeID]
	if n == nil {
		sm.mu.Unlock()
		return
	}
	if n.ProcessingMemoryBudgetBytes != nil && *n.ProcessingMemoryBudgetBytes == budget {
		sm.mu.Unlock()
		return
	}
	increased := n.ProcessingMemoryBudgetBytes == nil || budget > *n.ProcessingMemoryBudgetBytes
	n.ProcessingMemoryBudgetBytes = &budget
	payload := marshalNodeState(n)
	sm.mu.Unlock()
	sm.persistNodeWriteThrough(nodeID, payload)
	if increased {
		sm.processingCapacityChanged(nodeID)
	}
}

// SetProcessingCapacityHandler installs the queue wake-up for newly available work.
func (sm *StreamStateManager) SetProcessingCapacityHandler(handler func(string)) {
	sm.mu.Lock()
	sm.processingCapacityHandler = handler
	sm.mu.Unlock()
}

func (sm *StreamStateManager) processingCapacityChanged(nodeID string) {
	sm.mu.RLock()
	handler := sm.processingCapacityHandler
	sm.mu.RUnlock()
	if handler != nil {
		handler(nodeID)
	}
}
