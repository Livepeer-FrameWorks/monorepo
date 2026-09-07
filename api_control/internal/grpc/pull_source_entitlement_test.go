package grpc

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type pullSourceQuartermasterFake struct {
	quartermasterpb.UnimplementedClusterServiceServer

	entitlementByTenant map[string][]*clusterpeerpb.TenantClusterPeer
	requestedTenant     []string
}

func requireCommodoreServiceCredential(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer commodore-service-token" {
		return status.Errorf(codes.Unauthenticated, "unexpected authorization metadata: %v", got)
	}
	if len(md.Get("x-user-id")) != 0 || len(md.Get("x-tenant-id")) != 0 {
		return status.Error(codes.Unauthenticated, "caller identity leaked onto service request")
	}
	return nil
}

func (f *pullSourceQuartermasterFake) GetTenantEntitlement(ctx context.Context, req *quartermasterpb.GetTenantEntitlementRequest) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	if err := requireCommodoreServiceCredential(ctx); err != nil {
		return nil, err
	}
	f.requestedTenant = append(f.requestedTenant, req.GetTenantId())
	return &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: f.entitlementByTenant[req.GetTenantId()]}, nil
}

func startPullSourceQuartermasterFake(t *testing.T, fake *pullSourceQuartermasterFake) *qmclient.GRPCClient {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	quartermasterpb.RegisterClusterServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:           lis.Addr().String(),
		AllowInsecure:      true,
		Logger:             logging.NewLogger(),
		Timeout:            5 * time.Second,
		ServiceToken:       "commodore-service-token",
		PreferServiceToken: true,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("quartermaster client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return client
}

func TestPullSourceValidationUsesOnlyTenantEntitledClusters(t *testing.T) {
	fake := &pullSourceQuartermasterFake{
		entitlementByTenant: map[string][]*clusterpeerpb.TenantClusterPeer{
			"tenant-1": {
				{ClusterId: "entitled", ClusterType: "edge", AllowPrivatePullSources: true},
				{ClusterId: "disabled", ClusterType: "edge", AllowPrivatePullSources: false},
			},
		},
	}
	server := &CommodoreServer{
		logger:              logrus.New(),
		quartermasterClient: startPullSourceQuartermasterFake(t, fake),
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")

	if _, allowed, err := server.validatePullSourceEligibility(ctx, "https://10.0.0.1/live.m3u8", []string{"entitled"}); err != nil || len(allowed) != 1 || allowed[0] != "entitled" {
		t.Fatalf("entitled private pull = allowed %v, err %v", allowed, err)
	}

	for _, clusterID := range []string{"foreign", "disabled"} {
		_, _, err := server.validatePullSourceEligibility(ctx, "https://10.0.0.1/live.m3u8", []string{clusterID})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("cluster %q status = %v, want InvalidArgument", clusterID, status.Code(err))
		}
		message := status.Convert(err).Message()
		if !strings.Contains(message, "not eligible for pull source placement") {
			t.Fatalf("cluster %q denial = %q", clusterID, message)
		}
		if strings.Contains(message, "registered media") || strings.Contains(message, "allow_private_pull_sources") {
			t.Fatalf("cluster %q denial leaks fleet metadata: %q", clusterID, message)
		}
	}

	if len(fake.requestedTenant) != 3 {
		t.Fatalf("entitlement calls = %d, want one per validation", len(fake.requestedTenant))
	}
	for _, tenantID := range fake.requestedTenant {
		if tenantID != "tenant-1" {
			t.Fatalf("tenant-bound lookup used %q", tenantID)
		}
	}
}
