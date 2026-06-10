package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mistAdminCtx returns a context owning a node in tenant t1 as role admin —
// enough to clear the resolver-side ownership wall (auth.CanAdminMistNode).
func mistAdminCtx(tenantID, role string) context.Context {
	ctx := clientstest.AuthedCtx(tenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "u1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	return ctx
}

// mistResolver wires both Commodore and Quartermaster (the resolver needs both
// the ownership lookups and the mint RPC).
func mistResolver(commo *clientstest.FakeCommodore, qm *clientstest.FakeQuartermaster) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithCommodore(commo), clientstest.WithQuartermaster(qm)),
		Logger:  clientstest.DiscardLogger(),
	}
}

func TestDoOpenMistAdminSessionHappyPath(t *testing.T) {
	owner := "t1"
	qm := &clientstest.FakeQuartermaster{
		GetNodeOwnerFn: func(_ context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error) {
			return &quartermasterpb.NodeOwnerResponse{ClusterId: "c1", OwnerTenantId: &owner}, nil
		},
		GetClusterFn: func(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{Id: "c1"}}, nil
		},
	}
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
	res, err := mistResolver(commo, qm).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
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

func TestDoOpenMistAdminSessionEmptyNodeID(t *testing.T) {
	// Validation precedes any backend call.
	commo := &clientstest.FakeCommodore{}
	qm := &clientstest.FakeQuartermaster{}
	res, err := mistResolver(commo, qm).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "  "})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("empty nodeId result = %#v", res)
	}
	if commo.Calls != 0 || qm.Calls != 0 {
		t.Fatalf("validation should not touch backends (commo=%d qm=%d)", commo.Calls, qm.Calls)
	}
}

func TestDoOpenMistAdminSessionUnauthenticated(t *testing.T) {
	// No user ID in context → AuthError before any backend lookup.
	commo := &clientstest.FakeCommodore{}
	qm := &clientstest.FakeQuartermaster{}
	res, err := mistResolver(commo, qm).DoOpenMistAdminSession(clientstest.AuthedCtx("t1"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("unauth result = %#v", res)
	}
	if commo.Calls != 0 || qm.Calls != 0 {
		t.Fatalf("auth guard should not touch backends (commo=%d qm=%d)", commo.Calls, qm.Calls)
	}
}

func TestDoOpenMistAdminSessionOwnershipDenied(t *testing.T) {
	// Caller tenant differs from owner tenant → denied; mint never reached.
	otherOwner := "t-other"
	qm := &clientstest.FakeQuartermaster{
		GetNodeOwnerFn: func(context.Context, string) (*quartermasterpb.NodeOwnerResponse, error) {
			return &quartermasterpb.NodeOwnerResponse{ClusterId: "c1", OwnerTenantId: &otherOwner}, nil
		},
		GetClusterFn: func(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{Id: "c1"}}, nil
		},
	}
	commo := &clientstest.FakeCommodore{}
	res, err := mistResolver(commo, qm).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("denied result = %#v", res)
	}
	if commo.Calls != 0 {
		t.Fatalf("denied caller reached mint (commo=%d)", commo.Calls)
	}
}

func TestDoOpenMistAdminSessionNodeNotFound(t *testing.T) {
	// GetNodeOwner NotFound is masked as AuthError (do not leak node existence).
	qm := &clientstest.FakeQuartermaster{
		GetNodeOwnerFn: func(context.Context, string) (*quartermasterpb.NodeOwnerResponse, error) {
			return nil, status.Error(codes.NotFound, "no node")
		},
	}
	commo := &clientstest.FakeCommodore{}
	res, err := mistResolver(commo, qm).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("not-found result = %#v", res)
	}
}

func TestDoOpenMistAdminSessionMintPermissionDenied(t *testing.T) {
	// Commodore disagreeing after resolver allowed → mapped to AuthError.
	owner := "t1"
	qm := &clientstest.FakeQuartermaster{
		GetNodeOwnerFn: func(context.Context, string) (*quartermasterpb.NodeOwnerResponse, error) {
			return &quartermasterpb.NodeOwnerResponse{ClusterId: "c1", OwnerTenantId: &owner}, nil
		},
		GetClusterFn: func(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{Id: "c1"}}, nil
		},
	}
	commo := &clientstest.FakeCommodore{
		MintMistAdminSessionFn: func(context.Context, *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "nope")
		},
	}
	res, err := mistResolver(commo, qm).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("mint-denied result = %#v", res)
	}
}

func TestDoOpenMistAdminSessionEmptyEdgeDomain(t *testing.T) {
	// Mint succeeds but returns no edge_domain → hard error (can't build URL).
	owner := "t1"
	qm := &clientstest.FakeQuartermaster{
		GetNodeOwnerFn: func(context.Context, string) (*quartermasterpb.NodeOwnerResponse, error) {
			return &quartermasterpb.NodeOwnerResponse{ClusterId: "c1", OwnerTenantId: &owner}, nil
		},
		GetClusterFn: func(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{Id: "c1"}}, nil
		},
	}
	commo := &clientstest.FakeCommodore{
		MintMistAdminSessionFn: func(context.Context, *commodorepb.MintMistAdminSessionRequest) (*commodorepb.MintMistAdminSessionResponse, error) {
			return &commodorepb.MintMistAdminSessionResponse{Token: "t", EdgeDomain: ""}, nil
		},
	}
	if _, err := mistResolver(commo, qm).DoOpenMistAdminSession(mistAdminCtx("t1", "admin"), model.OpenMistAdminSessionInput{NodeID: "node-7"}); err == nil {
		t.Fatal("empty edge_domain should hard-error")
	}
}
