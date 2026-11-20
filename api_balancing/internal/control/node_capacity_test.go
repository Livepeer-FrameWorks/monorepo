package control

import (
	"context"
	"net"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestConfigSeedCapacityDefaultAndOutageFallback(t *testing.T) {
	previousQM, previousOwner := quartermasterClient, getNodeOwnerFn
	quartermasterClient, getNodeOwnerFn = nil, nil
	t.Cleanup(func() { quartermasterClient, getNodeOwnerFn = previousQM, previousOwner })
	seed, fallback := composeConfigSeedCandidate("capacity-edge", nil, "", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL, "")
	if seed.GetCapacity().GetEgressBudgetBytesPerSec() != capacity.DefaultBudget || seed.GetCapacity().GetEgressSource() != ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT {
		t.Errorf("seed must carry the visible decimal default: %v", seed.GetCapacity())
	}
	lastGood := &ipcpb.ConfigSeed{NodeId: seed.NodeId, Capacity: &ipcpb.NodeBandwidthCapacity{
		EgressBudgetBytesPerSec: 300000000, IngressBudgetBytesPerSec: 200000000, Revision: 7,
		EgressSource: ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_OPERATOR,
	}}
	merged := mergeConfigSeedFallback(seed, lastGood, fallback)
	if merged.GetCapacity().GetRevision() != 7 || merged.GetCapacity().GetEgressBudgetBytesPerSec() != 300000000 {
		t.Fatalf("Quartermaster outage discarded persisted capacity: %v", merged.GetCapacity())
	}
}

type capacityQuartermasterFake struct {
	quartermasterpb.UnimplementedNodeServiceServer
	capacity *quartermasterpb.NodeCapacity
}

func (f *capacityQuartermasterFake) GetNodeByLogicalName(context.Context, *quartermasterpb.GetNodeByLogicalNameRequest) (*quartermasterpb.NodeResponse, error) {
	return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{NodeId: "capacity-edge", Capacity: f.capacity}}, nil
}

func TestPushNodeCapacityWithoutReconnect(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &capacityQuartermasterFake{capacity: &quartermasterpb.NodeCapacity{
		EgressBudgetBytesPerSec: proto.Uint64(300000000), IngressBudgetBytesPerSec: proto.Uint64(200000000), Revision: 5,
	}}
	rpcServer := grpc.NewServer()
	quartermasterpb.RegisterNodeServiceServer(rpcServer, fake)
	go func() { _ = rpcServer.Serve(listener) }()
	t.Cleanup(func() { rpcServer.Stop(); _ = listener.Close() })
	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	previousQM, previousOwner, previousDB := quartermasterClient, getNodeOwnerFn, db
	quartermasterClient, getNodeOwnerFn, db = client, nil, nil
	t.Cleanup(func() { quartermasterClient, getNodeOwnerFn, db = previousQM, previousOwner, previousDB })
	manager := state.ResetDefaultManagerForTests()
	t.Cleanup(manager.Shutdown)
	stream := &mockStream{}
	t.Cleanup(SetupTestRegistry("capacity-edge", stream))
	revision, delivered, err := PushNodeCapacity("capacity-edge")
	if err != nil || !delivered || revision != 5 || len(stream.sent) != 1 {
		t.Fatalf("push revision=%d delivered=%v messages=%d err=%v", revision, delivered, len(stream.sent), err)
	}
	seed := stream.sent[0].GetConfigSeed()
	if seed.GetCapacity().GetEgressBudgetBytesPerSec() != 300000000 || seed.GetCapacity().GetIngressBudgetBytesPerSec() != 200000000 || seed.GetCapacity().GetEgressSource() != ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_OPERATOR {
		t.Fatalf("delivered capacity=%v", seed.GetCapacity())
	}
	if got := manager.GetNodeState("capacity-edge").BWLimit; got != 300000000 {
		t.Fatalf("composed routing budget = %v", got)
	}
}
