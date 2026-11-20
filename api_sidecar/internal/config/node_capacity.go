package config

import (
	"fmt"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// CurrentCapacityState returns one coherent seed/application snapshot.
func CurrentCapacityState() (*ipcpb.NodeBandwidthCapacity, *uint64) {
	if manager == nil {
		return nil, nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	budget := proto.CloneOf(manager.lastSeed.GetCapacity())
	if manager.capacityRevisionApplied == nil {
		return budget, nil
	}
	revision := *manager.capacityRevisionApplied
	return budget, &revision
}

func CurrentNodeCapacity() *ipcpb.NodeBandwidthCapacity {
	budget, _ := CurrentCapacityState()
	return budget
}
func CurrentCapacityRevisionApplied() *uint64 { _, applied := CurrentCapacityState(); return applied }

func capacityOnlySeedChange(previous, current *ipcpb.ConfigSeed) bool {
	if current.GetCapacity() == nil || proto.Equal(previous.GetCapacity(), current.GetCapacity()) {
		return false
	}
	a, b := proto.CloneOf(previous), proto.CloneOf(current)
	a.Capacity, b.Capacity = nil, nil
	a.SeedVersion, b.SeedVersion = 0, 0
	return proto.Equal(a, b)
}

func (m *Manager) applySeedCapacity(seed *ipcpb.ConfigSeed) error {
	budget := seed.GetCapacity()
	if budget == nil {
		return nil
	}
	if budget.GetEgressBudgetBytesPerSec() == 0 || budget.GetIngressBudgetBytesPerSec() == 0 {
		return fmt.Errorf("seed bandwidth budgets must be positive")
	}
	if err := m.mistClient.SetBandwidthLimit(budget.GetEgressBudgetBytesPerSec()); err != nil {
		return fmt.Errorf("bandwidth limit: %w", err)
	}
	revision := budget.GetRevision()
	m.mu.Lock()
	m.capacityRevisionApplied = &revision
	m.mu.Unlock()
	return nil
}
