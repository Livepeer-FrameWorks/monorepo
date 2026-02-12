package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
