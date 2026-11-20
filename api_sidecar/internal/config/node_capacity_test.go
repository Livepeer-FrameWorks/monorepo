package config

import (
	"testing"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestCapacityOnlySeedAppliesLiveWithoutFullReconcile(t *testing.T) {
	appconfigtest.Setenv(t, "HELMSMAN_STATE_DIR", t.TempDir())
	seed := reconcileSeed()
	seed.NodeId, seed.SeedVersion = "capacity-edge", 1
	seed.Capacity = &ipcpb.NodeBandwidthCapacity{EgressBudgetBytesPerSec: 125000000, IngressBudgetBytesPerSec: 125000000, EgressSource: ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT}
	fake := &recordingMistAPI{}
	m := &Manager{mistClient: fake, logger: logging.NewLogger(), lastSeed: seed}
	m.driftRepairOnce.Do(func() {})
	m.reconcile()
	if len(fake.bandwidthSets) != 1 || fake.bandwidthSets[0] != 125000000 {
		t.Errorf("default seed not applied to Mist: %v", fake.bandwidthSets)
	}
	updated := proto.CloneOf(seed)
	updated.SeedVersion = 2
	updated.Capacity.EgressBudgetBytesPerSec = 250000000
	updated.Capacity.Revision = 1
	if hashSeed(seed) == hashSeed(updated) {
		t.Error("capacity is missing from the desired-state hash")
	}
	fake.calls, fake.updatedConfigs = nil, nil
	fake.saveCalls = 0
	m.applySeed(updated, nil)
	if len(fake.bandwidthSets) != 2 || fake.bandwidthSets[1] != 250000000 {
		t.Errorf("live budget not applied: %v", fake.bandwidthSets)
	}
	if len(fake.updatedConfigs) != 0 || fake.saveCalls != 0 {
		t.Errorf("capacity-only change ran full reconcile: calls=%v", fake.calls)
	}
}

func capacityTestSeed() *ipcpb.ConfigSeed {
	seed := reconcileSeed()
	seed.Capacity = &ipcpb.NodeBandwidthCapacity{EgressBudgetBytesPerSec: 125000000, IngressBudgetBytesPerSec: 125000000, EgressSource: ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT, IngressSource: ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT}
	return seed
}
