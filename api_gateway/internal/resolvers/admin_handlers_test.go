package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// svcTokenCtx returns a non-demo context carrying a service token, the gate
// every bootstrap-token admin handler requires (middleware.HasServiceToken).
// AuthedCtx alone has no service token, so it drives the rejection paths.
func svcTokenCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyServiceToken, "svc")
}

// ---- DoCreateBootstrapToken: service-token gate, days->TTL, unwrap token ----

func TestDoCreateBootstrapToken_HappyAndDefaultsAndGate(t *testing.T) {
	var gotReq *quartermasterpb.CreateBootstrapTokenRequest
	qm := &clientstest.FakeQuartermaster{
		CreateBootstrapTokenFn: func(_ context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
			gotReq = req
			return &quartermasterpb.CreateBootstrapTokenResponse{
				Token: &quartermasterpb.BootstrapToken{Id: "bt1", Token: "secret", Kind: "edge_node"},
			}, nil
		},
	}
	cluster := "c1"
	expires := 3
	usage := 10
	got, err := qmR(qm).DoCreateBootstrapToken(svcTokenCtx(), model.CreateBootstrapTokenInput{
		Name:       "ci",
		Type:       model.BootstrapTokenTypeEdgeNode,
		ClusterID:  &cluster,
		ExpiresIn:  &expires,
		UsageLimit: &usage,
	})
	if err != nil {
		t.Fatalf("DoCreateBootstrapToken: %v", err)
	}
	// Name/kind come from input; ExpiresIn (days) becomes a "<days*24>h" TTL.
	if gotReq.GetName() != "ci" || gotReq.GetKind() != "EDGE_NODE" {
		t.Fatalf("req name/kind = (%q,%q), want (ci, EDGE_NODE)", gotReq.GetName(), gotReq.GetKind())
	}
	if gotReq.GetTtl() != "72h" {
		t.Fatalf("req ttl = %q, want 72h (3 days)", gotReq.GetTtl())
	}
	if gotReq.GetClusterId() != "c1" || gotReq.GetUsageLimit() != 10 {
		t.Fatalf("req cluster/usage = (%q,%d), want (c1,10)", gotReq.GetClusterId(), gotReq.GetUsageLimit())
	}
	if got.GetToken() != "secret" {
		t.Fatalf("token = %q, want secret", got.GetToken())
	}

	// No ExpiresIn: TTL defaults to 24h.
	var defReq *quartermasterpb.CreateBootstrapTokenRequest
	defQM := &clientstest.FakeQuartermaster{
		CreateBootstrapTokenFn: func(_ context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
			defReq = req
			return &quartermasterpb.CreateBootstrapTokenResponse{Token: &quartermasterpb.BootstrapToken{Id: "bt2"}}, nil
		},
	}
	if _, derr := qmR(defQM).DoCreateBootstrapToken(svcTokenCtx(), model.CreateBootstrapTokenInput{Name: "x", Type: model.BootstrapTokenTypeService}); derr != nil {
		t.Fatalf("DoCreateBootstrapToken (default ttl): %v", derr)
	}
	if defReq.GetTtl() != "24h" {
		t.Fatalf("default ttl = %q, want 24h", defReq.GetTtl())
	}

	// No service token: hard error, no backend call.
	guard := &clientstest.FakeQuartermaster{}
	if _, gerr := qmR(guard).DoCreateBootstrapToken(clientstest.AuthedCtx("t1"), model.CreateBootstrapTokenInput{Name: "x", Type: model.BootstrapTokenTypeService}); gerr == nil {
		t.Fatal("expected service-token-required error")
	}
	if guard.Calls != 0 {
		t.Fatalf("gate leaked a backend call: Calls=%d", guard.Calls)
	}
}

func TestDoCreateBootstrapToken_EmptyTokenAndError(t *testing.T) {
	// Response with a nil token is treated as an error.
	emptyResp := &clientstest.FakeQuartermaster{
		CreateBootstrapTokenFn: func(context.Context, *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
			return &quartermasterpb.CreateBootstrapTokenResponse{}, nil
		},
	}
	if _, err := qmR(emptyResp).DoCreateBootstrapToken(svcTokenCtx(), model.CreateBootstrapTokenInput{Name: "x", Type: model.BootstrapTokenTypeService}); err == nil {
		t.Fatal("expected empty-token error")
	}

	failing := &clientstest.FakeQuartermaster{
		CreateBootstrapTokenFn: func(context.Context, *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoCreateBootstrapToken(svcTokenCtx(), model.CreateBootstrapTokenInput{Name: "x", Type: model.BootstrapTokenTypeService}); err == nil {
		t.Fatal("expected backend error to propagate")
	}
}

// ---- DoRevokeBootstrapToken: gate, DeleteSuccess, NotFound classification ----

func TestDoRevokeBootstrapToken_HappyNotFoundAndGate(t *testing.T) {
	var gotID string
	qm := &clientstest.FakeQuartermaster{
		RevokeBootstrapTokenFn: func(_ context.Context, tokenID string) error {
			gotID = tokenID
			return nil
		},
	}
	res, err := qmR(qm).DoRevokeBootstrapToken(svcTokenCtx(), "bt1")
	if err != nil {
		t.Fatalf("DoRevokeBootstrapToken: %v", err)
	}
	if gotID != "bt1" {
		t.Fatalf("revoked id = %q, want bt1", gotID)
	}
	if del, ok := res.(*model.DeleteSuccess); !ok || !del.Success || del.DeletedID != "bt1" {
		t.Fatalf("result = %T %+v, want DeleteSuccess{bt1}", res, res)
	}

	// "not found" substring maps to a typed NotFoundError result (no Go error).
	notFound := &clientstest.FakeQuartermaster{
		RevokeBootstrapTokenFn: func(context.Context, string) error {
			return errors.New("token not found")
		},
	}
	nres, nerr := qmR(notFound).DoRevokeBootstrapToken(svcTokenCtx(), "bt1")
	if nerr != nil {
		t.Fatalf("not-found should be classified: %v", nerr)
	}
	if _, ok := nres.(*model.NotFoundError); !ok {
		t.Fatalf("result = %T, want *model.NotFoundError", nres)
	}

	// Other backend errors propagate as Go errors.
	failing := &clientstest.FakeQuartermaster{
		RevokeBootstrapTokenFn: func(context.Context, string) error {
			return errors.New("boom")
		},
	}
	if _, ferr := qmR(failing).DoRevokeBootstrapToken(svcTokenCtx(), "bt1"); ferr == nil {
		t.Fatal("expected non-not-found error to propagate")
	}

	// No service token: typed AuthError, no backend call.
	guard := &clientstest.FakeQuartermaster{}
	gres, _ := qmR(guard).DoRevokeBootstrapToken(clientstest.AuthedCtx("t1"), "bt1")
	if _, ok := gres.(*model.AuthError); !ok {
		t.Fatalf("result = %T, want *model.AuthError", gres)
	}
	if guard.Calls != 0 {
		t.Fatalf("gate leaked a backend call: Calls=%d", guard.Calls)
	}
}

// ---- DoGetBootstrapTokens: gate, all-kinds listing ----

func TestDoGetBootstrapTokens_HappyAndGate(t *testing.T) {
	var gotKind, gotTenant string
	var gotPag *commonpb.CursorPaginationRequest
	qm := &clientstest.FakeQuartermaster{
		ListBootstrapTokensFn: func(_ context.Context, kind, tenantID string, pag *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
			gotKind, gotTenant, gotPag = kind, tenantID, pag
			return &quartermasterpb.ListBootstrapTokensResponse{
				Tokens: []*quartermasterpb.BootstrapToken{{Id: "bt1"}},
			}, nil
		},
	}
	got, err := qmR(qm).DoGetBootstrapTokens(svcTokenCtx())
	if err != nil {
		t.Fatalf("DoGetBootstrapTokens: %v", err)
	}
	// Unfiltered listing: empty kind/tenant, fixed page size 100.
	if gotKind != "" || gotTenant != "" || gotPag.GetFirst() != 100 {
		t.Fatalf("list args = (kind %q, tenant %q, first %d), want (\"\",\"\",100)", gotKind, gotTenant, gotPag.GetFirst())
	}
	if len(got) != 1 || got[0].GetId() != "bt1" {
		t.Fatalf("unexpected tokens: %+v", got)
	}

	guard := &clientstest.FakeQuartermaster{}
	if _, gerr := qmR(guard).DoGetBootstrapTokens(clientstest.AuthedCtx("t1")); gerr == nil {
		t.Fatal("expected service-token-required error")
	}
	if guard.Calls != 0 {
		t.Fatalf("gate leaked a backend call: Calls=%d", guard.Calls)
	}

	failing := &clientstest.FakeQuartermaster{
		ListBootstrapTokensFn: func(context.Context, string, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, ferr := qmR(failing).DoGetBootstrapTokens(svcTokenCtx()); ferr == nil {
		t.Fatal("expected backend error to propagate")
	}
}

// ---- DoGetBootstrapTokensConnection: gate, kind filter, pagination passthrough ----

func TestDoGetBootstrapTokensConnection_HappyAndGate(t *testing.T) {
	var gotKind string
	var gotPag *commonpb.CursorPaginationRequest
	qm := &clientstest.FakeQuartermaster{
		ListBootstrapTokensFn: func(_ context.Context, kind, _ string, pag *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
			gotKind, gotPag = kind, pag
			return &quartermasterpb.ListBootstrapTokensResponse{
				Tokens:     []*quartermasterpb.BootstrapToken{{Id: "bt1"}},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1, HasNextPage: true},
			}, nil
		},
	}
	kind := "edge_node"
	first := 5
	after := "cursor"
	conn, err := qmR(qm).DoGetBootstrapTokensConnection(svcTokenCtx(), &kind, &first, &after, nil, nil)
	if err != nil {
		t.Fatalf("DoGetBootstrapTokensConnection: %v", err)
	}
	// kind filter + first/after flow through to the backend pagination request.
	if gotKind != "edge_node" || gotPag.GetFirst() != 5 || gotPag.GetAfter() != "cursor" {
		t.Fatalf("list args = (kind %q, first %d, after %q), want (edge_node,5,cursor)", gotKind, gotPag.GetFirst(), gotPag.GetAfter())
	}
	if conn.TotalCount != 1 || len(conn.Nodes) != 1 || conn.Nodes[0].GetId() != "bt1" {
		t.Fatalf("unexpected connection: total=%d nodes=%+v", conn.TotalCount, conn.Nodes)
	}
	if !conn.PageInfo.HasNextPage {
		t.Fatal("expected HasNextPage from backend pagination")
	}

	guard := &clientstest.FakeQuartermaster{}
	if _, gerr := qmR(guard).DoGetBootstrapTokensConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil); gerr == nil {
		t.Fatal("expected service-token-required error")
	}
	if guard.Calls != 0 {
		t.Fatalf("gate leaked a backend call: Calls=%d", guard.Calls)
	}

	failing := &clientstest.FakeQuartermaster{
		ListBootstrapTokensFn: func(context.Context, string, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, ferr := qmR(failing).DoGetBootstrapTokensConnection(svcTokenCtx(), nil, nil, nil, nil, nil); ferr == nil {
		t.Fatal("expected backend error to propagate")
	}
}
