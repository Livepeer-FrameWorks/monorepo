package tools

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
)

func TestSetNodeCapacityToolPreservesResetAndRevision(t *testing.T) {
	fake := &clientstest.FakeCommodore{SetNodeCapacityFn: func(_ context.Context, req *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
		if req.GetNodeId() != "edge" || req.GetExpectedRevision() != 4 || req.EgressBudgetBytesPerSec != nil || req.GetIngressBudgetBytesPerSec() != 250000000 {
			t.Errorf("tool lost node/CAS/reset: %+v", req)
		}
		return &quartermasterpb.NodeCapacity{EffectiveEgressBytesPerSec: 125000000, EffectiveIngressBytesPerSec: 250000000, EgressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_DEFAULT, IngressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR, Revision: 5}, nil
	}}
	sc := clientstest.Clients(clientstest.WithCommodore(fake))
	result, out, err := handleSetNodeCapacity(clientstest.AuthedCtx("tenant"), SetNodeCapacityInput{NodeID: "edge", ExpectedRevision: 4, IngressBudgetBytesPerSec: proto.Uint64(250000000)}, sc, clientstest.DiscardLogger())
	if err != nil || result.IsError {
		t.Fatalf("set capacity failed: %v %s", err, extractToolText(result))
	}
	capacity, ok := out.(*NodeCapacityResult)
	if !ok || capacity.EgressSource != "DEFAULT" || capacity.IngressSource != "OPERATOR" || capacity.Revision != 5 {
		t.Fatalf("capacity provenance missing: %#v", out)
	}
}

func TestSetNodeCapacityToolRejectsUnauthenticatedAndInvalidBudgets(t *testing.T) {
	fake := &clientstest.FakeCommodore{}
	sc := clientstest.Clients(clientstest.WithCommodore(fake))
	for _, tc := range []struct {
		ctx  context.Context
		args SetNodeCapacityInput
	}{
		{context.Background(), SetNodeCapacityInput{NodeID: "edge"}},
		{clientstest.AuthedCtx("tenant"), SetNodeCapacityInput{NodeID: "edge", EgressBudgetBytesPerSec: proto.Uint64(0)}},
		{context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyDemoMode, true), SetNodeCapacityInput{NodeID: "edge"}},
	} {
		result, _, err := handleSetNodeCapacity(tc.ctx, tc.args, sc, clientstest.DiscardLogger())
		if err != nil || !result.IsError {
			t.Fatalf("invalid tool call accepted: %v %+v", err, result)
		}
	}
	if fake.Calls != 0 {
		t.Fatal("invalid tool call reached Commodore")
	}
}
