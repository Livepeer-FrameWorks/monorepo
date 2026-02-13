package grpc

import (
	"context"
	"testing"
	"time"

	foghornclient "frameworks/pkg/clients/foghorn"
	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestPool(t *testing.T) *foghornclient.FoghornPool {
	t.Helper()
	l := logrus.New()
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: l})
	t.Cleanup(func() { pool.Close() })
	return pool
}

func TestResolveFoghornForTenant(t *testing.T) {
	tests := []struct {
		name    string
		server  *CommodoreServer
		want    codes.Code
		wantMsg string
	}{
		{
			name: "quartermaster_unavailable",
			server: &CommodoreServer{
				logger:        logrus.New(),
				routeCache:    make(map[string]*clusterRoute),
				routeCacheTTL: 5 * time.Minute,
			},
			want:    codes.Unavailable,
			wantMsg: "quartermaster not available for cluster routing",
		},
		{
			name: "cache_expired_quartermaster_unavailable",
			server: &CommodoreServer{
				logger: logrus.New(),
				routeCache: map[string]*clusterRoute{
					"tenant-1": {
						clusterID:   "cluster-1",
						foghornAddr: "foghorn:50051",
						resolvedAt:  time.Now().Add(-10 * time.Minute),
					},
				},
				routeCacheTTL: 5 * time.Minute,
			},
			want:    codes.Unavailable,
			wantMsg: "quartermaster not available for cluster routing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := test.server.resolveFoghornForTenant(context.Background(), "tenant-1")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error, got %v", err)
			}
			if st.Code() != test.want {
				t.Fatalf("expected code %v, got %v: %s", test.want, st.Code(), st.Message())
			}
			if st.Message() != test.wantMsg {
				t.Fatalf("expected message %q, got %q", test.wantMsg, st.Message())
			}
		})
	}
}

func TestResolveClusterRouteForTenant(t *testing.T) {
	tests := []struct {
		name    string
		server  *CommodoreServer
		want    codes.Code
		wantMsg string
	}{
		{
			name: "quartermaster_unavailable",
			server: &CommodoreServer{
				logger:        logrus.New(),
				routeCache:    make(map[string]*clusterRoute),
				routeCacheTTL: 5 * time.Minute,
			},
			want:    codes.Unavailable,
			wantMsg: "quartermaster not available for cluster routing",
		},
		{
			name: "cache_expired_quartermaster_unavailable",
			server: &CommodoreServer{
				logger: logrus.New(),
				routeCache: map[string]*clusterRoute{
					"tenant-1": {
						clusterID:   "cluster-1",
						foghornAddr: "foghorn:50051",
						resolvedAt:  time.Now().Add(-10 * time.Minute),
					},
				},
				routeCacheTTL: 5 * time.Minute,
			},
			want:    codes.Unavailable,
			wantMsg: "quartermaster not available for cluster routing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.server.resolveClusterRouteForTenant(context.Background(), "tenant-1")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error, got %v", err)
			}
			if st.Code() != test.want {
				t.Fatalf("expected code %v, got %v: %s", test.want, st.Code(), st.Message())
			}
			if st.Message() != test.wantMsg {
				t.Fatalf("expected message %q, got %q", test.wantMsg, st.Message())
			}
		})
	}
}

func TestResolveClusterRouteForTenant_IndependentOfFoghorn(t *testing.T) {
	server := &CommodoreServer{
		logger: logrus.New(),
		routeCache: map[string]*clusterRoute{
			"tenant-1": {
				clusterID:   "cluster-1",
				foghornAddr: "foghorn:50051",
				clusterSlug: "us-west",
				baseURL:     "frameworks.network",
				resolvedAt:  time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
		// No foghornPool — resolveClusterRouteForTenant must not touch it
	}

	route, err := server.resolveClusterRouteForTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.clusterID != "cluster-1" {
		t.Fatalf("expected cluster-1, got %s", route.clusterID)
	}
	if route.clusterSlug != "us-west" {
		t.Fatalf("expected us-west, got %s", route.clusterSlug)
	}
	if route.baseURL != "frameworks.network" {
		t.Fatalf("expected frameworks.network, got %s", route.baseURL)
	}
}

func TestResolveFoghornForTenant_EmptyAddr_EvictsAndRetries(t *testing.T) {
	server := &CommodoreServer{
		logger: logrus.New(),
		routeCache: map[string]*clusterRoute{
			"tenant-1": {
				clusterID:   "cluster-1",
				foghornAddr: "",
				resolvedAt:  time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
	}

	_, _, err := server.resolveFoghornForTenant(context.Background(), "tenant-1")
	if err == nil {
		t.Fatal("expected error for empty foghorn addr")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", st.Code())
	}
	if st.Message() != "quartermaster not available for cluster routing" {
		t.Fatalf("expected quartermaster fallback message, got %q", st.Message())
	}

	server.routeCacheMu.RLock()
	_, exists := server.routeCache["tenant-1"]
	server.routeCacheMu.RUnlock()
	if exists {
		t.Fatal("route cache entry should be evicted after failed resolution")
	}
}

func TestNormalizeClusterRoute_FallbacksForLegacyQuartermaster(t *testing.T) {
	route := &clusterRoute{
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "cluster-peer", FoghornGrpcAddr: "foghorn-peer:50051"},
		},
	}

	normalizeClusterRoute(route)

	if route.clusterID != "cluster-peer" {
		t.Fatalf("expected clusterID fallback from peer, got %q", route.clusterID)
	}
	if route.foghornAddr != "foghorn-peer:50051" {
		t.Fatalf("expected foghornAddr fallback from peer, got %q", route.foghornAddr)
	}
}

func TestFoghornPoolKey_FallsBackToAddrWhenClusterMissing(t *testing.T) {
	if got := foghornPoolKey("cluster-1", "foghorn:50051"); got != "cluster-1" {
		t.Fatalf("expected cluster key, got %q", got)
	}
	if got := foghornPoolKey("", "foghorn:50051"); got != "foghorn:50051" {
		t.Fatalf("expected addr key fallback, got %q", got)
	}
}

func TestResolveViewerEndpoint_FailsClosedWhenQuartermasterUnavailable(t *testing.T) {
	server := &CommodoreServer{
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: 5 * time.Minute,
	}

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	_, err := server.ResolveViewerEndpoint(ctx, &pb.ViewerEndpointRequest{ContentId: "stream-1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", st.Code())
	}
	if st.Message() != "quartermaster not available for cluster routing" {
		t.Fatalf("unexpected message: %q", st.Message())
	}
}

func TestResolveAddrFromRoute_PrimaryCluster(t *testing.T) {
	route := &clusterRoute{
		clusterID:   "cluster-primary",
		foghornAddr: "foghorn-primary:50051",
	}
	got := resolveAddrFromRoute(route, "cluster-primary")
	if got != "foghorn-primary:50051" {
		t.Fatalf("expected foghorn-primary:50051, got %q", got)
	}
}

func TestResolveAddrFromRoute_OfficialCluster(t *testing.T) {
	route := &clusterRoute{
		clusterID:               "cluster-primary",
		foghornAddr:             "foghorn-primary:50051",
		officialClusterID:       "cluster-official",
		officialFoghornGrpcAddr: "foghorn-official:50051",
	}
	got := resolveAddrFromRoute(route, "cluster-official")
	if got != "foghorn-official:50051" {
		t.Fatalf("expected foghorn-official:50051, got %q", got)
	}
}

func TestResolveAddrFromRoute_PeerCluster(t *testing.T) {
	route := &clusterRoute{
		clusterID:   "cluster-primary",
		foghornAddr: "foghorn-primary:50051",
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "cluster-peer-1", FoghornGrpcAddr: "foghorn-peer1:50051"},
			{ClusterId: "cluster-peer-2", FoghornGrpcAddr: "foghorn-peer2:50051"},
		},
	}
	got := resolveAddrFromRoute(route, "cluster-peer-2")
	if got != "foghorn-peer2:50051" {
		t.Fatalf("expected foghorn-peer2:50051, got %q", got)
	}
}

func TestResolveAddrFromRoute_PrimaryWithoutAddrFallsBackToPeers(t *testing.T) {
	route := &clusterRoute{
		clusterID:   "cluster-primary",
		foghornAddr: "",
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "cluster-primary", FoghornGrpcAddr: "foghorn-primary-from-peer:50051"},
		},
	}
	got := resolveAddrFromRoute(route, "cluster-primary")
	if got != "foghorn-primary-from-peer:50051" {
		t.Fatalf("expected peer fallback address, got %q", got)
	}
}

func TestResolveAddrFromRoute_UnknownCluster(t *testing.T) {
	route := &clusterRoute{
		clusterID:   "cluster-primary",
		foghornAddr: "foghorn-primary:50051",
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "cluster-peer-1", FoghornGrpcAddr: "foghorn-peer1:50051"},
		},
	}
	got := resolveAddrFromRoute(route, "cluster-unknown")
	if got != "" {
		t.Fatalf("expected empty string for unknown cluster, got %q", got)
	}
}

func TestResolveFoghornForCluster_CacheHit(t *testing.T) {
	pool := newTestPool(t)
	server := &CommodoreServer{
		logger:      logrus.New(),
		foghornPool: pool,
		routeCache: map[string]*clusterRoute{
			"tenant-1": {
				clusterID:   "cluster-primary",
				foghornAddr: "foghorn-primary:50051",
				clusterPeers: []*pb.TenantClusterPeer{
					{ClusterId: "cluster-peer-1", FoghornGrpcAddr: "foghorn-peer1:50051"},
				},
				resolvedAt: time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
	}

	// Pool dial will succeed lazily (gRPC uses lazy connection), so we get a client back
	client, err := server.resolveFoghornForCluster(context.Background(), "cluster-peer-1", "tenant-1")
	if err != nil {
		// If it fails, it should NOT be NotFound (address was in cache)
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.NotFound {
			t.Fatal("should have found the peer address in cache, got NotFound")
		}
		return
	}
	if client == nil {
		t.Fatal("expected non-nil client from cache hit")
	}
}

func TestResolveFoghornForCluster_EvictsOnMiss(t *testing.T) {
	server := &CommodoreServer{
		logger: logrus.New(),
		routeCache: map[string]*clusterRoute{
			"tenant-1": {
				clusterID:   "cluster-primary",
				foghornAddr: "foghorn-primary:50051",
				clusterPeers: []*pb.TenantClusterPeer{
					{ClusterId: "cluster-peer-1", FoghornGrpcAddr: "foghorn-peer1:50051"},
				},
				resolvedAt: time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
		// No quartermasterClient — retry after eviction will fail
	}

	_, err := server.resolveFoghornForCluster(context.Background(), "cluster-unknown", "tenant-1")
	if err == nil {
		t.Fatal("expected error for unknown cluster")
	}

	// Cache should have been evicted during retry
	server.routeCacheMu.RLock()
	_, exists := server.routeCache["tenant-1"]
	server.routeCacheMu.RUnlock()
	if exists {
		t.Fatal("route cache entry should have been evicted on miss")
	}
}

func TestResolveFoghornForArtifact_EmptyOriginFallsToPrimary(t *testing.T) {
	server := &CommodoreServer{
		logger: logrus.New(),
		routeCache: map[string]*clusterRoute{
			"tenant-1": {
				clusterID:   "cluster-primary",
				foghornAddr: "",
				resolvedAt:  time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
	}

	// Empty originClusterID → falls through to resolveFoghornForTenant
	_, err := server.resolveFoghornForArtifact(context.Background(), "tenant-1", "")
	if err == nil {
		t.Fatal("expected error (no pool), got nil")
	}
	// Should fail via resolveFoghornForTenant path (empty addr → evict → no QM)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable from tenant fallback, got %v", st.Code())
	}
}

func TestResolveFoghornForArtifact_RoutesToOriginCluster(t *testing.T) {
	pool := newTestPool(t)
	server := &CommodoreServer{
		logger:      logrus.New(),
		foghornPool: pool,
		routeCache: map[string]*clusterRoute{
			"tenant-1": {
				clusterID:   "cluster-primary",
				foghornAddr: "foghorn-primary:50051",
				clusterPeers: []*pb.TenantClusterPeer{
					{ClusterId: "cluster-origin", FoghornGrpcAddr: "foghorn-origin:50051"},
				},
				resolvedAt: time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
	}

	// Non-empty originClusterID → routes to that cluster via resolveFoghornForCluster
	client, err := server.resolveFoghornForArtifact(context.Background(), "tenant-1", "cluster-origin")
	if err != nil {
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.NotFound {
			t.Fatal("should have resolved origin cluster address, got NotFound")
		}
		return
	}
	if client == nil {
		t.Fatal("expected non-nil client for origin cluster")
	}
}

func TestResolveIngestEndpoint_FailsClosedWhenQuartermasterUnavailable(t *testing.T) {
	server := &CommodoreServer{
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: 5 * time.Minute,
	}

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	_, err := server.ResolveIngestEndpoint(ctx, &pb.IngestEndpointRequest{StreamKey: "sk_test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", st.Code())
	}
	if st.Message() != "quartermaster not available for cluster routing" {
		t.Fatalf("unexpected message: %q", st.Message())
	}
}
