package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	foghornclient "frameworks/pkg/clients/foghorn"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type testTenantControlServer struct {
	pb.UnimplementedTenantControlServiceServer
	terminateResp  *pb.TerminateTenantStreamsResponse
	invalidateResp *pb.InvalidateTenantCacheResponse
}

func (s *testTenantControlServer) TerminateTenantStreams(context.Context, *pb.TerminateTenantStreamsRequest) (*pb.TerminateTenantStreamsResponse, error) {
	return s.terminateResp, nil
}

func (s *testTenantControlServer) InvalidateTenantCache(context.Context, *pb.InvalidateTenantCacheRequest) (*pb.InvalidateTenantCacheResponse, error) {
	return s.invalidateResp, nil
}

func startTenantControlTestServer(t *testing.T, svc pb.TenantControlServiceServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	pb.RegisterTenantControlServiceServer(srv, svc)
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
		clusterPeers: []*pb.TenantClusterPeer{{ClusterId: "bad-cluster", FoghornGrpcAddr: "bad host:50051"}},
		resolvedAt:   time.Now(),
	})

	_, err := server.TerminateTenantStreams(context.Background(), &pb.TerminateTenantStreamsRequest{TenantId: "tenant-1", Reason: "suspended"})
	if err == nil {
		t.Fatal("expected error")
	}
	st := status.Convert(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected unavailable, got %v", st.Code())
	}
}

func TestTerminateTenantStreams_ReturnsErrorOnPartialFanoutFailure(t *testing.T) {
	goodAddr := startTenantControlTestServer(t, &testTenantControlServer{
		terminateResp: &pb.TerminateTenantStreamsResponse{StreamsTerminated: 2, SessionsTerminated: 3, StreamNames: []string{"a", "b"}},
	})

	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "cluster-ok", FoghornGrpcAddr: goodAddr},
			{ClusterId: "cluster-down", FoghornGrpcAddr: "127.0.0.1:1"},
		},
		resolvedAt: time.Now(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := server.TerminateTenantStreams(ctx, &pb.TerminateTenantStreamsRequest{TenantId: "tenant-1", Reason: "suspended"})
	if err == nil {
		t.Fatal("expected error")
	}
	st := status.Convert(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected unavailable, got %v", st.Code())
	}
}

func TestInvalidateTenantCache_DeduplicatesTargets(t *testing.T) {
	goodAddr := startTenantControlTestServer(t, &testTenantControlServer{
		invalidateResp: &pb.InvalidateTenantCacheResponse{EntriesInvalidated: 5},
	})

	server := newTenantFanoutTestServer(t, &clusterRoute{
		clusterID:   "cluster-primary",
		foghornAddr: goodAddr,
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "cluster-primary", FoghornGrpcAddr: goodAddr},
			{ClusterId: "cluster-primary", FoghornGrpcAddr: goodAddr},
		},
		resolvedAt: time.Now(),
	})

	resp, err := server.InvalidateTenantCache(context.Background(), &pb.InvalidateTenantCacheRequest{TenantId: "tenant-1", Reason: "reactivated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EntriesInvalidated != 5 {
		t.Fatalf("expected single target invalidation count, got %d", resp.EntriesInvalidated)
	}
}
