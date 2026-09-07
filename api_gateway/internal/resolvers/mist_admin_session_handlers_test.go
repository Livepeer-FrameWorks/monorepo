package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func mistAdminCtx(tenantID, role string) context.Context {
	ctx := clientstest.AuthedCtx(tenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "u1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	return ctx
}

func mistAdminAPITokenCtx(tenantID, role string, permissions ...string) context.Context {
	ctx := mistAdminCtx(tenantID, role)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
}

func mistResolver(commo *clientstest.FakeCommodore) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithCommodore(commo)),
		Logger:  clientstest.DiscardLogger(),
	}
}

func TestDoOpenMistAdminSessionHappyPath(t *testing.T) {
	var mintReq *commodorepb.MintMistAdminSessionRequest
	commo := &clientstest.FakeCommodore{
		MintMistAdminSessionFn: func(_ context.Context, req *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
			mintReq = req
			return &commodorepb.MintMistAdminSessionResponse{
				Token:      "sess-jwt",
				ExpiresAt:  1234,
				EdgeDomain: "edge-1.example.net",
			}, nil
		},
	}
	res, err := mistResolver(commo).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("DoOpenMistAdminSession err: %v", err)
	}
	sess, ok := res.(*model.MistAdminSession)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	// Edge domain is composed into the per-edge POST URL; token/exp surfaced.
	if sess.PostURL != "https://edge-1.example.net/_mist-session" || sess.SessionToken != "sess-jwt" || sess.ExpiresAt != 1234 {
		t.Fatalf("session = %+v", sess)
	}
	if mintReq.NodeId != "node-7" {
		t.Fatalf("mint req node = %q", mintReq.NodeId)
	}
}

func TestDoOpenMistAdminSessionRequiresWriteScopeBeforeLookup(t *testing.T) {
	commo := &clientstest.FakeCommodore{}
	_, err := mistResolver(commo).DoOpenMistAdminSession(
		mistAdminAPITokenCtx("t1", "owner", "infrastructure:read"),
		model.OpenMistAdminSessionInput{NodeID: "node-7"},
	)
	if err == nil {
		t.Fatal("read-scoped API token minted a Mist admin session")
	}
	if commo.Calls != 0 {
		t.Fatalf("scope denial reached Commodore (%d calls)", commo.Calls)
	}
}

func TestDoOpenMistAdminSessionWriteScopedTokenUsesAuthoritativeMintBoundary(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		MintMistAdminSessionFn: func(_ context.Context, req *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
			if req.GetNodeId() != "node-7" {
				t.Fatalf("node ID = %q", req.GetNodeId())
			}
			return &commodorepb.MintMistAdminSessionResponse{Token: "session", EdgeDomain: "edge.example"}, nil
		},
	}
	result, err := mistResolver(commo).DoOpenMistAdminSession(
		mistAdminAPITokenCtx("t1", "owner", "infrastructure:write"),
		model.OpenMistAdminSessionInput{NodeID: globalid.Encode(globalid.TypeInfrastructureNode, "node-7")},
	)
	if err != nil {
		t.Fatalf("write-scoped token failed before Commodore ownership check: %v", err)
	}
	if _, ok := result.(*model.MistAdminSession); !ok {
		t.Fatalf("result = %T", result)
	}
}

func TestDoOpenMistAdminSessionEmptyNodeID(t *testing.T) {
	// Validation precedes any backend call.
	commo := &clientstest.FakeCommodore{}
	res, err := mistResolver(commo).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "  "})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("empty nodeId result = %#v", res)
	}
	if commo.Calls != 0 {
		t.Fatalf("validation should not touch Commodore (%d calls)", commo.Calls)
	}
}

func TestDoOpenMistAdminSessionUnauthenticated(t *testing.T) {
	// No user ID in context → AuthError before any backend lookup.
	commo := &clientstest.FakeCommodore{}
	res, err := mistResolver(commo).DoOpenMistAdminSession(clientstest.AuthedCtx("t1"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("unauth result = %#v", res)
	}
	if commo.Calls != 0 {
		t.Fatalf("auth guard should not touch Commodore (%d calls)", commo.Calls)
	}
}

func TestDoOpenMistAdminSessionMintPermissionDenied(t *testing.T) {
	// Commodore disagreeing after resolver allowed → mapped to AuthError.
	commo := &clientstest.FakeCommodore{
		MintMistAdminSessionFn: func(context.Context, *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "nope")
		},
	}
	res, err := mistResolver(commo).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("mint-denied result = %#v", res)
	}
}

func TestDoOpenMistAdminSessionMintNotFound(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		MintMistAdminSessionFn: func(context.Context, *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
			return nil, status.Error(codes.NotFound, "node not found")
		},
	}
	res, err := mistResolver(commo).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.NotFoundError); !ok {
		t.Fatalf("mint-not-found result = %#v", res)
	}
}

func TestDoOpenMistAdminSessionEmptyEdgeDomain(t *testing.T) {
	// Mint succeeds but returns no edge_domain → hard error (can't build URL).
	commo := &clientstest.FakeCommodore{
		MintMistAdminSessionFn: func(context.Context, *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
			return &commodorepb.MintMistAdminSessionResponse{Token: "t", EdgeDomain: ""}, nil
		},
	}
	if _, err := mistResolver(commo).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"}); err == nil {
		t.Fatal("empty edge_domain should hard-error")
	}
}
