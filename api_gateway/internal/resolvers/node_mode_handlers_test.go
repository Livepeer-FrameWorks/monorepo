package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// DoSetNodeMode normalizes the node ID, translates the GraphQL enum to its wire
// string, defaults the reason to the caller identity, and returns the node on
// success. RPC-level status codes and response-level status enums both surface
// as typed union members.
func TestDoSetNodeMode(t *testing.T) {
	var got *foghorncontrolpb.SetNodeModeRequest
	c := &clientstest.FakeCommodore{
		SetNodeModeFn: func(_ context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
			got = req
			return &foghorncontrolpb.SetNodeModeResponse{
				NodeId: req.NodeId,
				Status: foghorncontrolpb.SetNodeModeStatus_SET_NODE_MODE_STATUS_SUCCESS,
			}, nil
		},
	}
	res, err := commoW2(c).DoSetNodeMode(clientstest.AuthedCtx("t1"), model.SetNodeModeInput{
		NodeID: "node-1",
		Mode:   model.NodeOperationalModeDraining,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.NodeId != "node-1" || got.Mode != "draining" {
		t.Fatalf("request built wrong: %+v", got)
	}
	// Reason defaults to a non-empty caller identity (no anonymous audit rows).
	if got.SetBy == "" {
		t.Fatal("SetBy should default to caller identity")
	}
	if node, ok := res.(*quartermasterpb.InfrastructureNode); !ok || node.NodeId != "node-1" {
		t.Fatalf("expected InfrastructureNode, got %T %+v", res, res)
	}

	denied := &clientstest.FakeCommodore{}
	if _, derr := commoW2(denied).DoSetNodeMode(context.Background(), model.SetNodeModeInput{NodeID: "n", Mode: model.NodeOperationalModeNormal}); derr == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}

	// Invalid (empty) node ID → ValidationError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoSetNodeMode(clientstest.AuthedCtx("t1"), model.SetNodeModeInput{NodeID: "  ", Mode: model.NodeOperationalModeNormal})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError for empty nodeId, got %T", res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	// Response-level NOT_FOUND status maps to NotFoundError union member.
	nf := commoW2(&clientstest.FakeCommodore{
		SetNodeModeFn: func(_ context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
			return &foghorncontrolpb.SetNodeModeResponse{
				Status:  foghorncontrolpb.SetNodeModeStatus_SET_NODE_MODE_STATUS_NOT_FOUND,
				Message: "no such node",
			}, nil
		},
	})
	res, err = nf.DoSetNodeMode(clientstest.AuthedCtx("t1"), model.SetNodeModeInput{NodeID: "node-1", Mode: model.NodeOperationalModeNormal})
	if err != nil {
		t.Fatalf("status NOT_FOUND should be a union member: %v", err)
	}
	if _, ok := res.(*model.NotFoundError); !ok {
		t.Fatalf("expected NotFoundError, got %T", res)
	}

	// RPC error propagates.
	fail := commoW2(&clientstest.FakeCommodore{
		SetNodeModeFn: func(context.Context, *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoSetNodeMode(clientstest.AuthedCtx("t1"), model.SetNodeModeInput{NodeID: "node-1", Mode: model.NodeOperationalModeNormal}); err == nil {
		t.Fatal("RPC error should propagate")
	}
}
