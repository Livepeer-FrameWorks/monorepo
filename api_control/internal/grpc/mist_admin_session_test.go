package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
)

// stubOwnership swaps the package-level Quartermaster lookup functions so
// MintMistAdminSession sees canned ownership state without standing up a
// real Quartermaster gRPC server.
func stubOwnership(
	t *testing.T,
	nodeOwner *pb.NodeOwnerResponse,
	nodeOwnerErr error,
	cluster *pb.ClusterResponse,
	clusterErr error,
) {
	t.Helper()
	prevOwner, prevCluster := mistAdminGetNodeOwner, mistAdminGetCluster
	t.Cleanup(func() {
		mistAdminGetNodeOwner = prevOwner
		mistAdminGetCluster = prevCluster
	})
	mistAdminGetNodeOwner = func(s *CommodoreServer, ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error) {
		return nodeOwner, nodeOwnerErr
	}
	mistAdminGetCluster = func(s *CommodoreServer, ctx context.Context, clusterID string) (*pb.ClusterResponse, error) {
		return cluster, clusterErr
	}
}

// ctxAs builds a context carrying a trusted gateway-set identity. Tests
// MUST use this rather than context.Background() so the server's
// extractUserContext + ctxkeys.GetRole find what they expect.
func ctxAs(userID, tenantID, role string) context.Context {
	ctx := context.Background()
	if userID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	}
	if tenantID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	}
	if role != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	}
	return ctx
}

func newMistAdminTestServer(t *testing.T) *CommodoreServer {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-secret-please-do-not-use-in-prod")
	// quartermasterClient stays nil because these tests replace the
	// ownership lookup functions.
	return &CommodoreServer{logger: logrus.New()}
}

func platformOfficialOwner(node, cluster string) *pb.NodeOwnerResponse {
	return &pb.NodeOwnerResponse{NodeId: node, ClusterId: cluster, ClusterName: cluster}
}

func tenantOwnedOwner(node, cluster, ownerTenant string) *pb.NodeOwnerResponse {
	t := ownerTenant
	return &pb.NodeOwnerResponse{NodeId: node, ClusterId: cluster, ClusterName: cluster, OwnerTenantId: &t}
}

func clusterResp(id string, isPlatformOfficial bool) *pb.ClusterResponse {
	return &pb.ClusterResponse{
		Cluster: &pb.InfrastructureCluster{
			ClusterId:          id,
			IsPlatformOfficial: isPlatformOfficial,
		},
	}
}

// --- happy paths ---

func TestMintMistAdminSession_SystemTenantBreakGlassAllowsOwnerAndAdmin(t *testing.T) {
	srv := newMistAdminTestServer(t)
	systemTenant := tenants.SystemTenantID.String()
	for _, role := range []string{"owner", "admin"} {
		t.Run(role, func(t *testing.T) {
			stubOwnership(t,
				platformOfficialOwner("edge-us-1", "media-us-1"), nil,
				clusterResp("media-us-1", true), nil,
			)
			ctx := ctxAs("platform-user", systemTenant, role)
			resp, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-us-1"})
			if err != nil {
				t.Fatalf("mint: %v", err)
			}
			if resp.GetToken() == "" {
				t.Fatal("empty token")
			}

			// Token claims must come from the trusted context, not from
			// any request body field — round-trip through Validate.
			vresp, err := srv.ValidateMistAdminSession(ctx, &pb.ValidateMistAdminSessionRequest{
				Token:          resp.GetToken(),
				ExpectedNodeId: "edge-us-1",
			})
			if err != nil || !vresp.GetValid() {
				t.Fatalf("validate: valid=%v err=%v", vresp.GetValid(), err)
			}
			if vresp.GetUserId() != "platform-user" || vresp.GetTenantId() != systemTenant || vresp.GetRole() != role {
				t.Errorf("token claims from request body, not trusted ctx: %+v", vresp)
			}
			if vresp.GetClusterId() != "media-us-1" {
				t.Errorf("cluster_id should be server-resolved; got %q", vresp.GetClusterId())
			}
		})
	}
}

func TestMintMistAdminSession_TenantPrivateAllowsOwnerTenant(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		tenantOwnedOwner("edge-acme-1", "acme-private", "tenant-acme"), nil,
		clusterResp("acme-private", false), nil,
	)
	ctx := ctxAs("acme-user", "tenant-acme", "owner")
	resp, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-acme-1"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if resp.GetToken() == "" {
		t.Fatal("empty token")
	}
}

func TestMintMistAdminSession_TenantPrivateAllowsOwnerTenantAdmin(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		tenantOwnedOwner("edge-acme-1", "acme-private", "tenant-acme"), nil,
		clusterResp("acme-private", false), nil,
	)
	ctx := ctxAs("acme-admin", "tenant-acme", "admin")
	resp, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-acme-1"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if resp.GetToken() == "" {
		t.Fatal("empty token")
	}
}

// --- deny paths (the security-critical ones) ---

func TestMintMistAdminSession_DeniesMemberOnPlatformOfficial(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		platformOfficialOwner("edge-us-1", "media-us-1"), nil,
		clusterResp("media-us-1", true), nil,
	)
	ctx := ctxAs("member-user", "any-tenant", "member")
	_, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-us-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("member on platform-official must be denied; got %v", err)
	}
}

func TestMintMistAdminSession_DeniesTenantOwnerOnPlatformOfficial(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		platformOfficialOwner("edge-us-1", "media-us-1"), nil,
		clusterResp("media-us-1", true), nil,
	)
	ctx := ctxAs("u", "tenant-customer", "owner")
	_, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-us-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("tenant owner on platform-owned node must be denied; got %v", err)
	}
}

func TestMintMistAdminSession_DeniesOwnerTenantMemberOnPrivateCluster(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		tenantOwnedOwner("edge-acme-1", "acme-private", "tenant-acme"), nil,
		clusterResp("acme-private", false), nil,
	)
	ctx := ctxAs("member-user", "tenant-acme", "member")
	_, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-acme-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("owner tenant member must be denied; got %v", err)
	}
}

func TestMintMistAdminSession_SystemTenantCanAdminPrivateCluster(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		tenantOwnedOwner("edge-acme-1", "acme-private", "tenant-acme"), nil,
		clusterResp("acme-private", false), nil,
	)
	ctx := ctxAs("platform-admin", tenants.SystemTenantID.String(), "admin")
	resp, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-acme-1"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if resp.GetToken() == "" {
		t.Fatal("empty token")
	}
}

func TestMintMistAdminSession_DeniesOtherTenantOnPrivateCluster(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		tenantOwnedOwner("edge-acme-1", "acme-private", "tenant-acme"), nil,
		clusterResp("acme-private", false), nil,
	)
	// Trusted identity says "tenant-evil" but the cluster is owned by
	// "tenant-acme" — must be PermissionDenied.
	ctx := ctxAs("evil-user", "tenant-evil", "owner")
	_, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-acme-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("cross-tenant access must be denied; got %v", err)
	}
}

func TestMintMistAdminSession_DeniesPrivateClusterWithoutOwner(t *testing.T) {
	// Defensive: a tenant-private cluster row that somehow lacks
	// owner_tenant_id (data anomaly) must fail closed, not match-any.
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		&pb.NodeOwnerResponse{NodeId: "edge-x", ClusterId: "weird"}, nil,
		clusterResp("weird", false), nil,
	)
	ctx := ctxAs("u", "any", "owner")
	_, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-x"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("missing owner_tenant_id must fail closed; got %v", err)
	}
}

func TestMintMistAdminSession_RejectsMissingTrustedIdentity(t *testing.T) {
	srv := newMistAdminTestServer(t)
	// No user/tenant in context — extractUserContext should fail.
	_, err := srv.MintMistAdminSession(context.Background(), &pb.MintMistAdminSessionRequest{NodeId: "edge-x"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing trusted identity must be Unauthenticated; got %v", err)
	}
}

func TestMintMistAdminSession_RejectsEmptyNode(t *testing.T) {
	srv := newMistAdminTestServer(t)
	ctx := ctxAs("u", tenants.SystemTenantID.String(), "owner")
	_, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty node_id must be InvalidArgument; got %v", err)
	}
}

func TestMintMistAdminSession_PropagatesNodeOwnerErrors(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t, nil, errors.New("qm boom"), nil, nil)
	ctx := ctxAs("u", tenants.SystemTenantID.String(), "owner")
	_, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-x"})
	if status.Code(err) != codes.Internal {
		t.Errorf("quartermaster failure must be Internal; got %v", err)
	}
}

// --- validate-side coverage ---

func TestValidateMistAdminSession_RejectsWrongNode(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		platformOfficialOwner("edge-us-1", "media-us-1"), nil,
		clusterResp("media-us-1", true), nil,
	)
	ctx := ctxAs("u", tenants.SystemTenantID.String(), "owner")
	mint, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-us-1"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	resp, _ := srv.ValidateMistAdminSession(context.Background(), &pb.ValidateMistAdminSessionRequest{
		Token:          mint.GetToken(),
		ExpectedNodeId: "edge-eu-1",
	})
	if resp.GetValid() {
		t.Error("token minted for edge-us-1 must not validate against edge-eu-1")
	}
}

func TestValidateMistAdminSession_RejectsMissingExpectedNode(t *testing.T) {
	srv := newMistAdminTestServer(t)
	stubOwnership(t,
		platformOfficialOwner("edge-us-1", "media-us-1"), nil,
		clusterResp("media-us-1", true), nil,
	)
	ctx := ctxAs("u", tenants.SystemTenantID.String(), "owner")
	mint, err := srv.MintMistAdminSession(ctx, &pb.MintMistAdminSessionRequest{NodeId: "edge-us-1"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	resp, _ := srv.ValidateMistAdminSession(context.Background(), &pb.ValidateMistAdminSessionRequest{
		Token:          mint.GetToken(),
		ExpectedNodeId: "", // caller bug — fail closed, not match-any
	})
	if resp.GetValid() {
		t.Error("empty expected_node_id must fail closed")
	}
}

func TestValidateMistAdminSession_RejectsEmptyToken(t *testing.T) {
	srv := newMistAdminTestServer(t)
	resp, err := srv.ValidateMistAdminSession(context.Background(), &pb.ValidateMistAdminSessionRequest{
		Token:          "",
		ExpectedNodeId: "edge-us-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetValid() {
		t.Error("empty token must be rejected")
	}
}
