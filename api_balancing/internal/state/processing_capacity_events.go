package state

func processingClassesFreed(before, after map[string]ClassCapacity) bool {
	for name, next := range after {
		previous, exists := before[name]
		if !exists || next.Used < previous.Used || (previous.Total != 0 && (next.Total == 0 || next.Total > previous.Total)) {
			return true
		}
	}
	return false
}

func processingNodeCapacityImproved(before, after *NodeState) bool {
	if after == nil {
		return false
	}
	if before == nil {
		return after.IsHealthy || after.CapProcessing || after.ProcessingMemoryBudgetBytes != nil
	}
	return (!before.IsHealthy && after.IsHealthy) || (before.IsStale && !after.IsStale) ||
		(before.OperationalMode != after.OperationalMode && after.OperationalMode == NodeModeNormal) ||
		(after.CapProcessing && !before.CapProcessing) || processingClassesFreed(before.ProcessingClasses, after.ProcessingClasses) ||
		(after.ProcessingMemoryBudgetBytes != nil && (before.ProcessingMemoryBudgetBytes == nil || *after.ProcessingMemoryBudgetBytes > *before.ProcessingMemoryBudgetBytes))
}
