package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/metadata"
)

func TestMatchClusterNode(t *testing.T) {
	nodes := []*quartermasterpb.InfrastructureNode{
		nil, // nil entries are skipped
		{NodeId: "n-1", NodeName: "edge-eu", Id: "row-1"},
		{NodeId: "n-2", NodeName: "edge-us", Id: "row-2"},
		{NodeId: "n-2", NodeName: "dup", Id: "row-3"}, // shares NodeId with n-2
	}

	// Match by node id (unique).
	if got, err := matchClusterNode(nodes, "n-1", "c-1"); err != nil || got.GetNodeName() != "edge-eu" {
		t.Errorf("by node id: got %v err %v", got, err)
	}
	// Match by node name.
	if got, err := matchClusterNode(nodes, "edge-us", "c-1"); err != nil || got.GetId() != "row-2" {
		t.Errorf("by node name: got %v err %v", got, err)
	}
	// Match by registry row id.
	if got, err := matchClusterNode(nodes, "row-1", "c-1"); err != nil || got.GetNodeId() != "n-1" {
		t.Errorf("by row id: got %v err %v", got, err)
	}
	// Ambiguous selector → error listing candidates.
	if _, err := matchClusterNode(nodes, "n-2", "c-1"); err == nil || !strings.Contains(err.Error(), "matched multiple") {
		t.Errorf("ambiguous: want multiple-match error, got %v", err)
	}
	// Not found.
	if _, err := matchClusterNode(nodes, "ghost", "c-9"); err == nil || !strings.Contains(err.Error(), "not found in cluster c-9") {
		t.Errorf("not-found: got %v", err)
	}
}

func TestClusterNodesUseServiceAuth(t *testing.T) {
	if !clusterNodesUseServiceAuth(fwcfg.Context{Persona: fwcfg.PersonaPlatform}) {
		t.Error("platform persona should use service auth")
	}
	for _, p := range []string{string(fwcfg.PersonaUser), string(fwcfg.PersonaSelfHosted), string(fwcfg.PersonaEdge), ""} {
		if clusterNodesUseServiceAuth(fwcfg.Context{Persona: fwcfg.Persona(p)}) {
			t.Errorf("persona %q should NOT use service auth", p)
		}
	}
}

func TestClusterNodesRPCContext_JWTThreading(t *testing.T) {
	// Non-platform persona with a JWT → token is threaded onto the context.
	user := fwcfg.Context{Persona: fwcfg.PersonaUser, Auth: fwcfg.Auth{JWT: "jwt-abc"}}
	cctx, cancel := clusterNodesRPCContext(context.Background(), user, 5_000_000)
	defer cancel()
	if got := cctx.Value(ctxkeys.KeyJWTToken); got != "jwt-abc" {
		t.Errorf("user persona: JWT = %v, want jwt-abc", got)
	}

	// Platform persona uses service auth → JWT is NOT threaded (avoids
	// shadowing the service token).
	plat := fwcfg.Context{Persona: fwcfg.PersonaPlatform, Auth: fwcfg.Auth{JWT: "jwt-abc"}}
	pctx, pcancel := clusterNodesRPCContext(context.Background(), plat, 5_000_000)
	defer pcancel()
	if got := pctx.Value(ctxkeys.KeyJWTToken); got != nil {
		t.Errorf("platform persona: JWT should not be threaded, got %v", got)
	}
}

type fakeNodeModeClient struct {
	req  *foghorncontrolpb.SetNodeModeRequest
	resp *foghorncontrolpb.SetNodeModeResponse
	err  error
}

func (f *fakeNodeModeClient) SetNodeMode(_ context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, metadata.MD, error) {
	f.req = req
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.resp, nil, nil
}

func TestRunSetNodeMode_OKStatuses(t *testing.T) {
	// SUCCESS and ALREADY_IN_MODE both render as OK; other statuses render as failure.
	okStatuses := []foghorncontrolpb.SetNodeModeStatus{
		foghorncontrolpb.SetNodeModeStatus_SET_NODE_MODE_STATUS_SUCCESS,
		foghorncontrolpb.SetNodeModeStatus_SET_NODE_MODE_STATUS_ALREADY_IN_MODE,
	}
	for _, st := range okStatuses {
		fake := &fakeNodeModeClient{resp: &foghorncontrolpb.SetNodeModeResponse{Status: st, Mode: "active", Message: "done"}}
		var buf bytes.Buffer
		if err := runSetNodeMode(context.Background(), &buf, fake, fwcfg.Context{}, "n-1", "active"); err != nil {
			t.Fatalf("status %v: %v", st, err)
		}
		if fake.req.GetNodeId() != "n-1" || fake.req.GetMode() != "active" || fake.req.GetSetBy() != "frameworks-cli" {
			t.Errorf("request fields wrong: %+v", fake.req)
		}
		out := buf.String()
		if !strings.Contains(out, "[OK]") || !strings.Contains(out, "active: done") {
			t.Errorf("status %v: expected OK render, got %q", st, out)
		}
	}

	// A non-success status renders as failure.
	failFake := &fakeNodeModeClient{resp: &foghorncontrolpb.SetNodeModeResponse{Status: foghorncontrolpb.SetNodeModeStatus_SET_NODE_MODE_STATUS_NOT_FOUND, Mode: "active"}}
	var fbuf bytes.Buffer
	if err := runSetNodeMode(context.Background(), &fbuf, failFake, fwcfg.Context{}, "n-1", "active"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(fbuf.String(), "[FAIL]") {
		t.Errorf("NOT_FOUND should render as failure: %q", fbuf.String())
	}
}

func TestRunSetNodeMode_Error(t *testing.T) {
	fake := &fakeNodeModeClient{err: errors.New("rpc down")}
	if err := runSetNodeMode(context.Background(), &bytes.Buffer{}, fake, fwcfg.Context{}, "n-1", "active"); err == nil {
		t.Fatal("expected SetNodeMode error to propagate")
	}
}

type fakeNodeStatusClient struct {
	req  *quartermasterpb.UpdateNodeStatusRequest
	resp *quartermasterpb.NodeResponse
	err  error
}

func (f *fakeNodeStatusClient) UpdateNodeStatus(_ context.Context, req *quartermasterpb.UpdateNodeStatusRequest) (*quartermasterpb.NodeResponse, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestRunUpdateNodeStatus(t *testing.T) {
	// Registry reflects the requested status → OK.
	fake := &fakeNodeStatusClient{resp: &quartermasterpb.NodeResponse{
		Node: &quartermasterpb.InfrastructureNode{NodeId: "n-1", Status: "retired"},
	}}
	var buf bytes.Buffer
	if err := runUpdateNodeStatus(context.Background(), &buf, fake, fwcfg.Context{}, "n-1", "c-1", "retired"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.req.GetNodeId() != "n-1" || fake.req.GetStatus() != "retired" || fake.req.GetExpectedClusterId() != "c-1" {
		t.Errorf("request fields wrong: %+v", fake.req)
	}
	if out := buf.String(); !strings.Contains(out, "[OK]") || !strings.Contains(out, "n-1 status=retired") {
		t.Errorf("expected OK render, got %q", out)
	}

	// Registry status differs from requested → rendered as not-OK.
	mismatch := &fakeNodeStatusClient{resp: &quartermasterpb.NodeResponse{
		Node: &quartermasterpb.InfrastructureNode{NodeId: "n-1", Status: "retired"},
	}}
	var mbuf bytes.Buffer
	if err := runUpdateNodeStatus(context.Background(), &mbuf, mismatch, fwcfg.Context{}, "n-1", "c-1", "evicted"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mbuf.String(), "[FAIL]") {
		t.Errorf("status mismatch should render as failure: %q", mbuf.String())
	}

	errFake := &fakeNodeStatusClient{err: errors.New("rpc down")}
	if err := runUpdateNodeStatus(context.Background(), &bytes.Buffer{}, errFake, fwcfg.Context{}, "n-1", "c-1", "retired"); err == nil {
		t.Fatal("expected UpdateNodeStatus error to propagate")
	}
}
