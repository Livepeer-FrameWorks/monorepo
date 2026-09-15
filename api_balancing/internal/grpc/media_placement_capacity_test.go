package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCapacityPreviewRPCRequiresServiceBeforeDependencies(t *testing.T) {
	server := &FoghornGRPCServer{}
	for _, identity := range []string{"", "jwt", "api_token", "wallet"} {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, identity)
		if _, err := server.ObserveMediaPlacementCapacity(ctx, nil); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%q reached capacity dependencies: %v", identity, err)
		}
		if _, err := server.ObserveMediaPlacementPushSource(ctx, nil); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%q reached source dependencies: %v", identity, err)
		}
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := server.ObserveMediaPlacementPushSource(ctx, &placementpb.PushSourcePreviewQuery{TenantId: "tenant", ControlCellId: "cell", ClusterId: "cluster", InternalName: "stream"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing source observer succeeded: %v", err)
	}
	if _, err := server.ObserveMediaPlacementCapacity(ctx, &placementpb.CapacityPreviewQuery{TenantId: "tenant", ControlCellId: "cell", ClusterIds: []string{"cluster"}, Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing observer became a successful preview: %v", err)
	}
}

type capacityRPCOwner struct{ publisher bool }

func (owner capacityRPCOwner) GetTenantEntitlement(_ context.Context, _ string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	return &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"cluster"}, EffectiveAccess: []*clusterpb.TenantClusterPeer{{ClusterId: "cluster", ClusterType: "edge", ClusterClass: "platform_official", ControlCellId: "cell", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, MediaConsent: &placementpb.CapacityConsent{AllowServe: true, AllowIngest: owner.publisher}}}}, nil
}

func (owner capacityRPCOwner) GetMediaPlacementInventory(_ context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	out := &quartermasterpb.MediaPlacementInventory{TenantId: req.TenantId, ControlCellId: req.ControlCellId, ClusterIds: req.ClusterIds, Complete: true, ObservedAt: timestamppb.Now()}
	if owner.publisher {
		out.Nodes = []*quartermasterpb.MediaPlacementInventoryNode{{NodeId: "publisher", ClusterId: "cluster", AdmissionEnabled: true}}
	}
	return out, nil
}

type sourceRPCRegistry struct{}

func (sourceRPCRegistry) SourceSnapshot(_ context.Context, tenant, internal string) (control.StreamEntry, bool, error) {
	if tenant != "tenant" || internal != "stream" {
		return control.StreamEntry{}, false, nil
	}
	return control.StreamEntry{TenantID: tenant, InternalName: internal, IngestMode: control.IngestPush, Locations: map[string]control.Location{"cell": {SourceActive: true, OwnerNodeID: "publisher", SourceGeneration: "generation", SourceRevision: 9}}}, true, nil
}

func TestCapacityPreviewRPCRegistrationAndWire(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpcgo.NewServer(grpcgo.UnaryInterceptor(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, handler grpcgo.UnaryHandler) (any, error) {
		return handler(context.WithValue(ctx, ctxkeys.KeyAuthType, "service"), req)
	}))
	s := &FoghornGRPCServer{}
	s.SetPlacementCapacityObserver(&balancer.PlacementCapacityObserver{CellID: "cell", Owner: func() balancer.PlacementCapacityOwner { return capacityRPCOwner{} }, Snapshot: func() *state.BalancerSnapshot { return &state.BalancerSnapshot{} }})
	now := time.Now().UTC()
	sourceSnapshot := &state.BalancerSnapshot{Nodes: []state.EnhancedBalancerNodeSnapshot{{NodeID: "publisher", ClusterID: "cluster", IsActive: true, Host: "https://publisher.example", LastHeartbeat: now, OutputsObservedAt: now, Outputs: map[string]any{"DTSC": "dtsc://HOST:14200/$"}, Streams: map[string]state.BalancerStreamSummary{"stream": {TenantID: "tenant", Status: "live", BufferState: "FULL", Playable: true, Inputs: 1, ObservedAt: now}}}}}
	s.SetPlacementPushSourceObserver(&federation.PlacementPushSourceObserver{Inventory: &balancer.PlacementCapacityObserver{CellID: "cell", Owner: func() balancer.PlacementCapacityOwner { return capacityRPCOwner{publisher: true} }, Snapshot: func() *state.BalancerSnapshot { return sourceSnapshot }}, Registry: sourceRPCRegistry{}})
	s.RegisterServices(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	t.Cleanup(func() { _ = listener.Close() })
	conn, err := grpcgo.NewClient("passthrough:///capacity-preview", grpcgo.WithTransportCredentials(insecure.NewCredentials()), grpcgo.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := foghornpb.NewMediaPlacementControlServiceClient(conn).ObserveMediaPlacementCapacity(ctx, &placementpb.CapacityPreviewQuery{TenantId: "tenant", ControlCellId: "cell", ClusterIds: []string{"cluster"}, Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls"})
	if err != nil || !out.GetComplete() || len(out.GetCandidates()) != 0 || out.GetTenantId() != "tenant" || out.GetControlCellId() != "cell" {
		t.Fatalf("registered capacity RPC failed to preserve empty authorized inventory: %+v %v", out, err)
	}
	source, err := foghornpb.NewMediaPlacementControlServiceClient(conn).ObserveMediaPlacementPushSource(ctx, &placementpb.PushSourcePreviewQuery{TenantId: "tenant", ControlCellId: "cell", ClusterId: "cluster", InternalName: "stream"})
	if err != nil || source.GetNodeId() != "publisher" || !source.GetPullAvailable() || source.GetGeneration() != "generation" || source.GetRevision() != 9 || !source.GetConsent().GetAllowIngest() || source.GetScope().GetInternalName() != "stream" {
		t.Fatalf("registered source RPC lost publisher evidence: %+v %v", source, err)
	}
}
