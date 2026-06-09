package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func strptr(s string) *string { return &s }

// ----- get_node_info -----

func TestHandleGetNodeInfo(t *testing.T) {
	qm := &clientstest.FakeQuartermaster{
		GetNodeFn: func(_ context.Context, id string) (*quartermasterpb.NodeResponse, error) {
			return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{
				NodeId: id, NodeName: "edge-1", NodeType: "edge", ClusterId: "c1",
				Region: strptr("eu"), ExternalIp: strptr("1.2.3.4"),
			}}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithQuartermaster(qm))

	res, out, err := handleGetNodeInfo(clientstest.AuthedCtx("t1"), GetNodeInfoInput{NodeID: "n1"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("get_node_info should succeed: err=%v text=%s", err, extractToolText(res))
	}
	ni := out.(NodeInfoResult)
	if ni.NodeID != "n1" || ni.Region != "eu" || ni.ExternalIP != "1.2.3.4" {
		t.Fatalf("unexpected node info: %+v", ni)
	}
}

func TestHandleGetNodeInfo_RequiresAuth(t *testing.T) {
	qm := &clientstest.FakeQuartermaster{} // GetNode unstubbed → panics if reached
	sc := clientstest.Clients(clientstest.WithQuartermaster(qm))
	res, _, err := handleGetNodeInfo(context.Background(), GetNodeInfoInput{NodeID: "n1"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing tenant should be a tool error")
	}
	if qm.Calls != 0 {
		t.Fatalf("backend consulted on unauthenticated request: %d calls", qm.Calls)
	}
}

func TestHandleGetNodeInfo_NotFound(t *testing.T) {
	qm := &clientstest.FakeQuartermaster{
		GetNodeFn: func(context.Context, string) (*quartermasterpb.NodeResponse, error) {
			return nil, errors.New("no such node")
		},
	}
	sc := clientstest.Clients(clientstest.WithQuartermaster(qm))
	res, _, err := handleGetNodeInfo(clientstest.AuthedCtx("t1"), GetNodeInfoInput{NodeID: "ghost"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("backend error should surface as a tool error")
	}
}

// ----- manage_node -----

func TestHandleManageNode_Actions(t *testing.T) {
	qm := &clientstest.FakeQuartermaster{
		GetNodeFn: func(_ context.Context, id string) (*quartermasterpb.NodeResponse, error) {
			return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{
				NodeId: id, NodeName: "edge-1", ClusterId: "c1", ExternalIp: strptr("1.2.3.4"),
			}}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithQuartermaster(qm))
	ctx := clientstest.AuthedCtx("t1")

	for _, action := range []string{"drain", "maintenance", "restore", "status", "diagnose", "logs"} {
		res, out, err := handleManageNode(ctx, ManageNodeInput{NodeID: "n1", Action: action}, sc, clientstest.DiscardLogger())
		if err != nil || res.IsError {
			t.Fatalf("action %q should succeed: err=%v text=%s", action, err, extractToolText(res))
		}
		mr := out.(ManageNodeResult)
		if mr.Action != action || len(mr.Commands) == 0 || mr.Message == "" {
			t.Fatalf("action %q produced empty guidance: %+v", action, mr)
		}
		// Remote node (has external IP) must surface an --ssh hint.
		if !strings.Contains(mr.Message, "--ssh") {
			t.Errorf("action %q message missing remote ssh hint: %q", action, mr.Message)
		}
	}

	// Unknown action is rejected.
	res, _, err := handleManageNode(ctx, ManageNodeInput{NodeID: "n1", Action: "explode"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("unknown action should be a tool error")
	}
}

// ----- set_node_mode -----

func TestHandleSetNodeMode(t *testing.T) {
	var gotReq *foghorncontrolpb.SetNodeModeRequest
	commo := &clientstest.FakeCommodore{
		SetNodeModeFn: func(_ context.Context, req *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
			gotReq = req
			return &foghorncontrolpb.SetNodeModeResponse{NodeId: req.NodeId, Mode: req.Mode, Message: "ok"}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	ctx := clientstest.AuthedCtx("t1")

	// Invalid mode rejected before any backend call.
	res, _, err := handleSetNodeMode(ctx, SetNodeModeInput{NodeID: "n1", Mode: "turbo"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("invalid mode should be a tool error")
	}
	if commo.Calls != 0 {
		t.Fatalf("backend called on invalid mode: %d", commo.Calls)
	}

	// Valid mode succeeds; empty reason defaults to an audit marker, not blank.
	res, out, err := handleSetNodeMode(ctx, SetNodeModeInput{NodeID: "n1", Mode: "Draining"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("valid mode should succeed: err=%v text=%s", err, extractToolText(res))
	}
	if gotReq.Mode != "draining" {
		t.Errorf("mode not normalized to lowercase: %q", gotReq.Mode)
	}
	if gotReq.SetBy == "" {
		t.Error("empty reason should default to a non-empty audit marker")
	}
	if sr := out.(SetNodeModeResult); sr.Mode != "draining" {
		t.Errorf("unexpected result mode: %q", sr.Mode)
	}
}

func TestHandleGetNodeHealth(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		GetNodeHealthFn: func(_ context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error) {
			return &foghorncontrolpb.GetNodeHealthResponse{
				NodeId: req.NodeId, IsHealthy: true, ActiveViewers: 12, ClusterId: "c1",
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleGetNodeHealth(clientstest.AuthedCtx("t1"), GetNodeHealthInput{NodeID: "n1"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("get_node_health should succeed: err=%v text=%s", err, extractToolText(res))
	}
	hr := out.(NodeHealthResult)
	if hr.NodeID != "n1" || !hr.IsHealthy || hr.ActiveViewers != 12 {
		t.Fatalf("unexpected health result: %+v", hr)
	}
}

// ----- create_enrollment_token -----

func TestHandleCreateEnrollmentToken(t *testing.T) {
	var gotReq *quartermasterpb.CreateEnrollmentTokenRequest
	qm := &clientstest.FakeQuartermaster{
		CreateEnrollmentTokenFn: func(_ context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
			gotReq = req
			return &quartermasterpb.CreateBootstrapTokenResponse{
				Token: &quartermasterpb.BootstrapToken{Token: "enroll-xyz"},
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithQuartermaster(qm))

	res, out, err := handleCreateEnrollmentToken(clientstest.AuthedCtx("t1"),
		CreateEnrollmentTokenInput{ClusterID: "c1"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("create_enrollment_token should succeed: err=%v text=%s", err, extractToolText(res))
	}
	// The tenant from context must be bound into the request — the token is
	// scoped to (cluster, tenant) and a missing tenant would leak enrollment.
	if gotReq.TenantId == nil || *gotReq.TenantId != "t1" || gotReq.ClusterId != "c1" {
		t.Fatalf("request not tenant/cluster-scoped: %+v", gotReq)
	}
	er := out.(EnrollmentTokenResult)
	if er.Token != "enroll-xyz" || !strings.Contains(er.Message, "enroll-xyz") {
		t.Fatalf("unexpected enrollment result: %+v", er)
	}
}

func TestHandleCreateEnrollmentToken_RequiresAuth(t *testing.T) {
	qm := &clientstest.FakeQuartermaster{}
	sc := clientstest.Clients(clientstest.WithQuartermaster(qm))
	res, _, err := handleCreateEnrollmentToken(context.Background(), CreateEnrollmentTokenInput{ClusterID: "c1"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing tenant should be a tool error")
	}
	if qm.Calls != 0 {
		t.Fatalf("backend called on unauthenticated request: %d", qm.Calls)
	}
}
