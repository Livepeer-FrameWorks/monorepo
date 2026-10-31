package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type testTenantControlServer struct {
	foghornpb.UnimplementedTenantControlServiceServer
	terminateResp  *foghorncontrolpb.TerminateTenantStreamsResponse
	invalidateResp *foghorncontrolpb.InvalidateTenantCacheResponse
	invalidateErr  error
}

func (s *testTenantControlServer) TerminateTenantStreams(context.Context, *foghorncontrolpb.TerminateTenantStreamsRequest) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	return s.terminateResp, nil
}

func (s *testTenantControlServer) InvalidateTenantCache(context.Context, *foghorncontrolpb.InvalidateTenantCacheRequest) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
	return s.invalidateResp, s.invalidateErr
}

func startTenantControlTestServer(t *testing.T, svc foghornpb.TenantControlServiceServer) string {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	foghornpb.RegisterTenantControlServiceServer(srv, svc)
	go func() {
		_ = srv.Serve(lis)
	}()

	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
	})

	return lis.Addr().String()
}

func newTenantFanoutTestServer(t *testing.T, route *clusterRoute) *CommodoreServer {
	t.Helper()
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logrus.New()})
	t.Cleanup(func() { _ = pool.Close() })

	return &CommodoreServer{
		logger:        logrus.New(),
		foghornPool:   pool,
		routeCache:    map[string]*clusterRoute{"tenant-1": route},
		routeCacheTTL: time.Minute,
	}
}

func TestTerminateTenantStreams_FailsWhenAllDialAttemptsFail(t *testing.T) {
	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "bad-cluster", FoghornGrpcAddr: "bad host:50051"}},
		resolvedAt:   time.Now(),
	})

	_, err := server.TerminateTenantStreams(serviceCtx(), &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1", Reason: "suspended"})
	if err == nil {
		t.Fatal("expected error")
	}
	st := status.Convert(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected unavailable, got %v", st.Code())
	}
}

func TestTerminateTenantStreams_PartialResultsRemainPending(t *testing.T) {
	goodAddr := startTenantControlTestServer(t, &testTenantControlServer{
		terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 2, SessionsTerminated: 3, StreamNames: []string{"a", "b"}},
	})

	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "cluster-ok", FoghornGrpcAddr: goodAddr},
			{ClusterId: "cluster-down", FoghornGrpcAddr: "127.0.0.1:1"},
		},
		resolvedAt: time.Now(),
	})

	ctx, cancel := context.WithTimeout(serviceCtx(), 150*time.Millisecond)
	defer cancel()

	_, err := server.TerminateTenantStreams(ctx, &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1", Reason: "suspended"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("partial delivery must stay pending: %v", err)
	}
	resp := grpcutil.StreamStopProgress(err)
	if resp == nil {
		t.Fatalf("missing partial counts: %v", err)
	}
	if resp.StreamsTerminated != 2 {
		t.Fatalf("expected 2 streams terminated from successful cluster, got %d", resp.StreamsTerminated)
	}
}

func TestInvalidateTenantCache_DeduplicatesTargets(t *testing.T) {
	goodAddr := startTenantControlTestServer(t, &testTenantControlServer{
		invalidateResp: &foghorncontrolpb.InvalidateTenantCacheResponse{EntriesInvalidated: 5},
	})

	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterID:   "cluster-primary",
		foghornAddr: goodAddr,
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "cluster-primary", FoghornGrpcAddr: goodAddr},
			{ClusterId: "cluster-primary", FoghornGrpcAddr: goodAddr},
		},
		resolvedAt: time.Now(),
	})

	resp, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1", Reason: "reactivated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EntriesInvalidated != 5 {
		t.Fatalf("expected single target invalidation count, got %d", resp.EntriesInvalidated)
	}
}

func TestTerminateTenantStreams_DeduplicatesByAddrWhenClusterIDMissing(t *testing.T) {
	goodAddr := startTenantControlTestServer(t, &testTenantControlServer{
		terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 7, SessionsTerminated: 9},
	})

	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "", FoghornGrpcAddr: goodAddr},
			{ClusterId: "", FoghornGrpcAddr: goodAddr},
		},
		resolvedAt: time.Now(),
	})

	resp, err := server.TerminateTenantStreams(serviceCtx(), &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant-1", Reason: "suspended"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StreamsTerminated != 7 || resp.SessionsTerminated != 9 {
		t.Fatalf("expected single-target result (7/9), got %d/%d", resp.StreamsTerminated, resp.SessionsTerminated)
	}
}

func TestInvalidateTenantCache_NoTargetsIsSuccessful(t *testing.T) {
	server := newTenantFanoutTestServer(t, &clusterRoute{resolvedAt: time.Now()})
	response, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1", Reason: "tenant closed"})
	if err != nil || response.GetEntriesInvalidated() != 0 {
		t.Fatalf("closed tenant cache invalidation=%v err=%v", response, err)
	}
}

func TestInvalidateTenantCacheDiscoveryGapIsNotEmptyTenant(t *testing.T) {
	for _, test := range []struct {
		name  string
		route *clusterRoute
		want  codes.Code
	}{
		{"no clusters", &clusterRoute{}, codes.OK},
		{"primary without discovery", &clusterRoute{clusterID: "cell"}, codes.Unavailable},
		{"official without discovery", &clusterRoute{officialClusterID: "cell"}, codes.Unavailable},
		{"unhealthy entitlement", &clusterRoute{admissionPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cell"}}}, codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.route.resolvedAt = time.Now()
			server := newTenantFanoutTestServer(t, test.route)
			_, callErr := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1"})
			if status.Code(callErr) != test.want {
				t.Fatalf("invalidation result=%v want=%s", callErr, test.want)
			}
		})
	}
}

func TestInvalidateTenantCacheRefusesPartialDelivery(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "undiscoverable cell", false: "failed RPC"}[missing], func(t *testing.T) {
			good := startTenantControlTestServer(t, &testTenantControlServer{invalidateResp: &foghorncontrolpb.InvalidateTenantCacheResponse{EntriesInvalidated: 5}})
			bad := ""
			if !missing {
				bad = startTenantControlTestServer(t, &testTenantControlServer{invalidateErr: status.Error(codes.Unavailable, "cell disconnected")})
			}
			server := newTenantFanoutTestServer(t, &clusterRoute{clusterID: "good", foghornAddr: good, clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "bad", FoghornGrpcAddr: bad}}, resolvedAt: time.Now()})
			if _, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1"}); status.Code(err) != codes.Unavailable {
				t.Fatalf("partial delivery reported success: %v", err)
			}
		})
	}
}
