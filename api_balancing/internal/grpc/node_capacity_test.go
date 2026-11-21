package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestGetNodeHealthUsesCapacityUnits(t *testing.T) {
	server, mock := newLifecycleServer(t)
	manager := seedHealthyNode(t, "capacity-edge", "owner", "cluster")
	manager.SetNodeCapacity("capacity-edge", &ipcpb.NodeBandwidthCapacity{
		EgressBudgetBytesPerSec: 125000000, IngressBudgetBytesPerSec: 125000000, Revision: 4,
		EgressSource: ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT,
	})
	if err := manager.ApplyNodeLifecycle(context.Background(), &ipcpb.NodeLifecycleUpdate{
		NodeId: "capacity-edge", IsHealthy: true, MemoryMetricsAreBytes: true, RamMax: 4 << 30, RamCurrent: 2 << 30,
		UpSpeed: 12500000, DownSpeed: 25000000, CapacityRevisionApplied: proto.Uint64(3),
		ObservedValid: proto.Bool(true), ObservedSource: ipcpb.ObservedSource_OBSERVED_SOURCE_NIC,
	}); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT component, COALESCE\(current_version`).WithArgs("capacity-edge").
		WillReturnRows(sqlmock.NewRows([]string{"component", "version"}))
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	response, err := server.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: "capacity-edge"})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetBwLimitMbps() != 1000 || response.GetBandwidthUpMbps() != 100 || response.GetBandwidthDownMbps() != 200 || response.GetRamUsedMb() != 2048 || response.GetRamMaxMb() != 4096 {
		t.Errorf("node health units: %v", response)
	}
	if response.GetCapacityRevisionComposed() != 4 || response.GetCapacityRevisionApplied() != 3 || response.GetEgressUsableMbps() != 850 || response.GetObservedSource() != ipcpb.ObservedSource_OBSERVED_SOURCE_NIC {
		t.Errorf("node health capacity provenance: %v", response)
	}
}
