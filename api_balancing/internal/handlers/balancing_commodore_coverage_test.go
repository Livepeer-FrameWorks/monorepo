package handlers

import (
	"context"
	"net"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/triggers"

	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	qmcli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
)

func TestRefreshBillingStatusAfterPaymentPreservesInactiveTenant(t *testing.T) {
	balancingTestEnv(t)
	startQuartermasterFake(t, &fakeTenantService{
		validate: func(context.Context, *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
			return &quartermasterpb.ValidateTenantResponse{
				Valid: false, IsActive: false, TenantId: "tenant-inactive", BillingModel: "postpaid",
			}, nil
		},
	})

	billing := refreshBillingStatusAfterPayment(context.Background(), "demo", "tenant-inactive")
	if billing == nil || billing.State != triggers.BillingStatusDenied || billing.DeniedReason != "tenant_inactive" {
		t.Fatalf("post-payment refresh = %+v, want inactive-tenant denial", billing)
	}
}

// These tests unlock the two handleStreamBalancing branches that wave 2 could
// not reach because they are gated on a SUCCESSFUL Commodore resolution: the
// resolved target must carry a non-empty TenantID (prepaid/402 gate) or a
// FixedNode (VOD artifact pinning). Both are settable here because
// control.CommodoreClient — the concrete global ResolveStream reads — is an
// exported package var that a real client dialing a localhost fake can replace.

// commodoreBalancingFake is an in-process Commodore InternalService double whose
// only two RPCs (the ones handleStreamBalancing's resolution path exercises) are
// settable funcs. Unset RPCs return an empty/not-found response.
type commodoreBalancingFake struct {
	commodorepb.UnimplementedInternalServiceServer

	internalName     func(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error)
	artifactInternal func(context.Context, *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error)
	playbackID       func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error)
	playbackPolicy   func(context.Context, *commodorepb.ResolvePlaybackPolicyRequest) (*commodorepb.ResolvePlaybackPolicyResponse, error)
}

func (f *commodoreBalancingFake) ResolvePlaybackPolicy(ctx context.Context, req *commodorepb.ResolvePlaybackPolicyRequest) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
	if f.playbackPolicy != nil {
		return f.playbackPolicy(ctx, req)
	}
	return f.UnimplementedInternalServiceServer.ResolvePlaybackPolicy(ctx, req)
}

func (f *commodoreBalancingFake) ResolvePlaybackID(ctx context.Context, req *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
	if f.playbackID != nil {
		return f.playbackID(ctx, req)
	}
	return &commodorepb.ResolvePlaybackIDResponse{}, nil
}

func (f *commodoreBalancingFake) ResolveInternalName(ctx context.Context, req *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
	if f.internalName != nil {
		return f.internalName(ctx, req)
	}
	return &commodorepb.ResolveInternalNameResponse{}, nil
}

func (f *commodoreBalancingFake) ResolveArtifactInternalName(ctx context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
	if f.artifactInternal != nil {
		return f.artifactInternal(ctx, req)
	}
	return &commodorepb.ResolveArtifactInternalNameResponse{}, nil
}

// startBalancingCommodoreFake serves fake on a localhost gRPC listener, builds a
// real *commodore.GRPCClient against it, and points BOTH the resolution-path
// global (control.CommodoreClient, read by ResolveStream) and the
// handlers-package global (commodoreClient, read on the telemetry path) at it.
// Everything is restored on cleanup.
func startBalancingCommodoreFake(t *testing.T, fake *commodoreBalancingFake) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := commodorecli.NewGRPCClient(commodorecli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		ServiceToken:  "test-service-token",
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("commodore client: %v", err)
	}

	prevControl := control.CommodoreClient
	prevHandlers := commodoreClient
	control.CommodoreClient = client
	commodoreClient = client
	t.Cleanup(func() {
		control.CommodoreClient = prevControl
		commodoreClient = prevHandlers
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// fakeTenantService is a Quartermaster TenantService double serving a single
// settable ValidateTenant func. getBillingStatus falls through to
// quartermasterClient.ValidateTenant when triggerProcessor is nil, so this is
// the seam that drives the prepaid-suspended billing decision.
type fakeTenantService struct {
	quartermasterpb.UnimplementedTenantServiceServer
	validate func(context.Context, *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error)
}

func (f *fakeTenantService) ValidateTenant(ctx context.Context, req *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
	if f.validate != nil {
		return f.validate(ctx, req)
	}
	return &quartermasterpb.ValidateTenantResponse{}, nil
}

// startQuartermasterFake stands up a localhost QM gRPC server exposing the fake
// TenantService, builds a real *qmclient.GRPCClient, and points the
// handlers-package quartermasterClient global at it. Restored on cleanup.
func startQuartermasterFake(t *testing.T, fake *fakeTenantService) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	quartermasterpb.RegisterTenantServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := qmcli.NewGRPCClient(qmcli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("quartermaster client: %v", err)
	}

	prev := quartermasterClient
	quartermasterClient = client
	t.Cleanup(func() {
		quartermasterClient = prev
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

func startHealthyQuartermasterFake(t *testing.T) {
	t.Helper()
	startQuartermasterFake(t, &fakeTenantService{
		validate: func(_ context.Context, req *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
			return &quartermasterpb.ValidateTenantResponse{
				Valid:        true,
				IsActive:     true,
				TenantId:     req.GetTenantId(),
				BillingModel: "postpaid",
			}, nil
		},
	})
}

// seedArtifactNode makes nodeID/host an ACTIVE storage node that holds the
// artifact identified by clipHash, so applyArtifactPlacement (called from
// ResolveStream's vod+ branch) finds it via FindNodesByArtifactHash and pins
// target.FixedNode to this node's host. The node must be probe-verified
// (active:true) because FindNodesByArtifactHash skips non-active nodes.
// artifactHolderCluster is a platform-shared cluster the seeded artifact holder belongs to. applyArtifactPlacement now
// gates FixedNode by ClusterAccessibleForTenant, so a holder must be in a cluster ENTITLED to the resolved tenant, not
// merely present; a platform-shared cluster is entitled to any tenant, which is what these fixed-node fixtures need.
const artifactHolderCluster = "art-shared-edge"

func seedArtifactNode(t *testing.T, sm *state.StreamStateManager, nodeID, host, clipHash string) {
	t.Helper()
	seedNodeWithStream(t, sm, seedNode{
		nodeID: nodeID, host: host, active: true,
		ramMax: 100, ramCur: 10,
	}, "", 0, 0, 0)
	control.AddPlatformSharedCluster(artifactHolderCluster)
	sm.SetNodeConnectionInfo(context.Background(), nodeID, host, "", artifactHolderCluster, nil)
	sm.SetNodeArtifacts(nodeID, []*ipcpb.StoredArtifact{
		{ClipHash: clipHash, FilePath: "/data/" + clipHash + ".mp4", StreamName: "vod+art"},
	}, state.ArtifactReportOrder{Fence: 1, Seq: 1})
}
