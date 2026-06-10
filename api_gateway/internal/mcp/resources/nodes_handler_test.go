package resources

import (
	"context"
	"encoding/json"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func resTenantCtx(tenant string) context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenant)
}

func TestHandleNodesList(t *testing.T) {
	// No tenant → AuthRequired error, backend never consulted.
	qm := &clientstest.FakeQuartermaster{}
	sc := clientstest.Clients(clientstest.WithQuartermaster(qm))
	if _, err := handleNodesList(context.Background(), sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("missing tenant must error")
	}
	if qm.Calls != 0 {
		t.Fatalf("auth gate must short-circuit before Quartermaster, got %d calls", qm.Calls)
	}

	// Happy path: default First:50 pagination, region pointer mapped, has_more from pagination.
	region := "eu-central"
	var gotPage *commonpb.CursorPaginationRequest
	qmOK := &clientstest.FakeQuartermaster{
		ListNodesFn: func(_ context.Context, _, _, _ string, p *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
			gotPage = p
			return &quartermasterpb.ListNodesResponse{
				Nodes:      []*quartermasterpb.InfrastructureNode{{Id: "n1", NodeId: "node-1", NodeName: "edge-a", NodeType: "edge", ClusterId: "c1", Region: &region}},
				Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true},
			}, nil
		},
	}
	res, err := handleNodesList(resTenantCtx("t1"), clientstest.Clients(clientstest.WithQuartermaster(qmOK)), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if gotPage == nil || gotPage.First != 50 {
		t.Fatalf("expected default First:50 pagination, got %+v", gotPage)
	}
	var body NodesListResponse
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Nodes) != 1 || body.Nodes[0].Region != "eu-central" || body.Nodes[0].Name != "edge-a" {
		t.Fatalf("node not mapped (incl. region ptr): %+v", body.Nodes)
	}
	if !body.HasMore {
		t.Error("has_more should reflect pagination.HasNextPage")
	}

	// Backend error propagates.
	qmErr := &clientstest.FakeQuartermaster{
		ListNodesFn: func(context.Context, string, string, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
			return nil, context.DeadlineExceeded
		},
	}
	if _, err := handleNodesList(resTenantCtx("t1"), clientstest.Clients(clientstest.WithQuartermaster(qmErr)), clientstest.DiscardLogger()); err == nil {
		t.Fatal("backend error should propagate")
	}
}

func TestHandleNodeByID(t *testing.T) {
	// No tenant → AuthRequired.
	if _, err := HandleNodeByID(context.Background(), "nodes://n1", clientstest.Clients(clientstest.WithQuartermaster(&clientstest.FakeQuartermaster{})), clientstest.DiscardLogger()); err == nil {
		t.Fatal("missing tenant must error")
	}

	// Invalid URIs ("" or "list") are rejected before any backend call.
	for _, uri := range []string{"nodes://", "nodes://list"} {
		qm := &clientstest.FakeQuartermaster{}
		if _, err := HandleNodeByID(resTenantCtx("t1"), uri, clientstest.Clients(clientstest.WithQuartermaster(qm)), clientstest.DiscardLogger()); err == nil {
			t.Fatalf("uri %q should be rejected", uri)
		}
		if qm.Calls != 0 {
			t.Fatalf("invalid uri %q must not reach backend", uri)
		}
	}

	// Happy path: id extracted from URI, node mapped.
	var gotID string
	qmOK := &clientstest.FakeQuartermaster{
		GetNodeFn: func(_ context.Context, id string) (*quartermasterpb.NodeResponse, error) {
			gotID = id
			return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{Id: id, NodeName: "edge-b", NodeType: "edge"}}, nil
		},
	}
	res, err := HandleNodeByID(resTenantCtx("t1"), "nodes://node-9", clientstest.Clients(clientstest.WithQuartermaster(qmOK)), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if gotID != "node-9" {
		t.Errorf("node id not extracted from URI: %q", gotID)
	}
	var info NodeInfo
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "edge-b" {
		t.Errorf("node not mapped: %+v", info)
	}

	// A NodeResponse with a nil Node is a not-found error, not a nil deref.
	qmNil := &clientstest.FakeQuartermaster{
		GetNodeFn: func(context.Context, string) (*quartermasterpb.NodeResponse, error) {
			return &quartermasterpb.NodeResponse{Node: nil}, nil
		},
	}
	if _, err := HandleNodeByID(resTenantCtx("t1"), "nodes://ghost", clientstest.Clients(clientstest.WithQuartermaster(qmNil)), clientstest.DiscardLogger()); err == nil {
		t.Fatal("nil node should be a not-found error")
	}
}
