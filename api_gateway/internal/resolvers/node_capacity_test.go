package resolvers

import (
	"context"
	"math"
	"sync/atomic"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/loaders"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestNodeCapacityInputAndCAS(t *testing.T) {
	for _, value := range []float64{0, -1, 0.5, math.NaN(), math.Inf(1), 9007199254740992} {
		if _, err := capacityInputBytes(&value, "egress"); err == nil {
			t.Errorf("invalid budget accepted: %v", value)
		}
	}
	fake := &clientstest.FakeCommodore{SetNodeCapacityFn: func(_ context.Context, req *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
		if req.GetNodeId() != "edge" || req.GetExpectedRevision() != 4 || req.GetEgressBudgetBytesPerSec() != 250000000 || req.IngressBudgetBytesPerSec != nil {
			t.Errorf("node/CAS/reset values lost: %+v", req)
		}
		return nil, status.Error(codes.Aborted, "revision changed")
	}}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
	egress := float64(250000000)
	result, err := r.DoSetNodeCapacity(clientstest.AuthedCtx("tenant"), model.SetNodeCapacityInput{NodeID: globalid.Encode(globalid.TypeInfrastructureNode, "edge"), ExpectedRevision: 4, EgressBudgetBytesPerSec: &egress})
	if err != nil {
		t.Fatal(err)
	}
	validation, ok := result.(*model.ValidationError)
	if !ok || validation.Field == nil || *validation.Field != "expectedRevision" {
		t.Fatalf("stale revision not actionable: %#v", result)
	}
	ctx := context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:write"})
	if _, err = r.DoSetNodeCapacity(ctx, model.SetNodeCapacityInput{NodeID: "edge"}); err == nil {
		t.Fatal("unscoped token accepted")
	}
	if fake.Calls != 1 {
		t.Fatalf("unauthorized request fanned out: %d", fake.Calls)
	}
}

func TestNodeCapacityKeepsBudgetWhenEdgeIsOffline(t *testing.T) {
	record := &quartermasterpb.NodeCapacity{EffectiveEgressBytesPerSec: 125000000, EffectiveIngressBytesPerSec: 250000000, IngressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR, Revision: 7}
	for _, online := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "online"}[online], func(t *testing.T) {
			fake := &clientstest.FakeCommodore{GetNodeHealthFn: func(context.Context, *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error) {
				if !online {
					return nil, status.Error(codes.Unavailable, "offline")
				}
				return &foghorncontrolpb.GetNodeHealthResponse{CapacityRevisionApplied: proto.Uint64(6)}, nil
			}}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
			result, err := r.DoNodeCapacity(clientstest.AuthedCtx("tenant"), &quartermasterpb.InfrastructureNode{NodeId: "edge", NodeType: "edge", Capacity: record})
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || result.EgressBudgetBytesPerSec != 125000000 || result.IngressBudgetBytesPerSec != 250000000 || result.EgressSource != model.CapacityBudgetSourceDefault || result.IngressSource != model.CapacityBudgetSourceOperator || result.Revision != 7 {
				t.Fatalf("budget lost: %#v", result)
			}
			if online && (result.AppliedRevision == nil || *result.AppliedRevision != 6) {
				t.Fatalf("pending revision not visible: %#v", result)
			}
			if !online && result.AppliedRevision != nil {
				t.Fatal("offline edge invented an applied revision")
			}
		})
	}
}

func TestClusterCapacitySumsNodesAndCachesTheLegacyAlias(t *testing.T) {
	var calls atomic.Int32
	fake := &clientstest.FakeQuartermaster{ListNodeCapacitiesFn: func(_ context.Context, req *quartermasterpb.ListNodeCapacitiesRequest) (*quartermasterpb.ListNodeCapacitiesResponse, error) {
		calls.Add(1)
		if len(req.GetClusterIds()) != 1 || req.GetClusterIds()[0] != "cell" {
			t.Errorf("wrong cluster: %v", req)
		}
		return &quartermasterpb.ListNodeCapacitiesResponse{Capacities: map[string]*quartermasterpb.NodeCapacity{
			"a": {EffectiveEgressBytesPerSec: 125000000, EffectiveIngressBytesPerSec: 125000000},
			"b": {EffectiveEgressBytesPerSec: 250000000, EffectiveIngressBytesPerSec: 500000000, EgressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR, IngressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR},
		}}, nil
	}}
	sc := clientstest.Clients(clientstest.WithQuartermaster(fake))
	r := &Resolver{Clients: sc}
	ctx := loaders.ContextWithLoaders(clientstest.AuthedCtx("tenant"), loaders.New(sc))
	cluster := &quartermasterpb.InfrastructureCluster{ClusterId: "cell"}
	got, err := r.DoClusterCapacity(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if got.EgressBudgetBytesPerSec != 375000000 || got.IngressBudgetBytesPerSec != 625000000 || got.EgressUsableBytesPerSec != 318750000 || got.IngressUsableBytesPerSec != 531250000 || got.DefaultBudgetNodes != 1 {
		t.Fatalf("wrong node sums: %+v", got)
	}
	legacy, err := r.DoClusterMaxBandwidthMbps(ctx, cluster)
	if err != nil || legacy != 3000 || calls.Load() != 1 {
		t.Fatalf("legacy alias used stored limit or extra query: value=%d calls=%d err=%v", legacy, calls.Load(), err)
	}
}
