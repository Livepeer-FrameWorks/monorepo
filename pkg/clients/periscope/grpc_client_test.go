package periscope

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type retryDelegationStub struct {
	periscopepb.UnimplementedAggregatedAnalyticsServiceServer
	secret []byte
	mu     sync.Mutex
	jtis   []string
}

func (s *retryDelegationStub) GetTenantDailyStats(ctx context.Context, _ *periscopepb.GetTenantDailyStatsRequest) (*periscopepb.GetTenantDailyStatsResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	header := md.Get("authorization")
	if len(header) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing assertion")
	}
	claims, err := auth.ValidateJWT(strings.TrimPrefix(header[0], "Bearer "), s.secret)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	s.mu.Lock()
	s.jtis = append(s.jtis, claims.ID)
	attempt := len(s.jtis)
	s.mu.Unlock()
	if attempt == 1 {
		return nil, status.Error(codes.Unavailable, "connection reset during retry test")
	}
	return &periscopepb.GetTenantDailyStatsResponse{}, nil
}

func TestFailsafeRetryMintsFreshPeriscopeDelegation(t *testing.T) {
	secret := []byte("periscope-delegation-secret")
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stub := &retryDelegationStub{secret: secret}
	server := grpc.NewServer()
	periscopepb.RegisterAggregatedAnalyticsServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	client, err := NewGRPCClient(GRPCConfig{GRPCAddr: listener.Addr().String(), Logger: logging.NewLogger(), DelegatedJWTSecret: secret, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"analytics:read"})
	if _, err := client.GetTenantDailyStats(ctx, "tenant-1", 7); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.jtis) < 2 || stub.jtis[0] == stub.jtis[1] {
		t.Fatalf("retry JTIs = %v, want distinct assertions", stub.jtis)
	}
}

func TestAuthInterceptorMintsPeriscopeDelegation(t *testing.T) {
	secret := []byte("periscope-delegation-secret")
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"analytics:read"})
	err := authInterceptor("service-token", secret)(ctx, "/periscope.AggregatedAnalyticsService/GetTenantDailyStats", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			header := md.Get("authorization")
			if len(header) != 1 || !strings.HasPrefix(header[0], "Bearer ") {
				t.Fatalf("authorization = %v", header)
			}
			claims, err := auth.ValidateJWT(strings.TrimPrefix(header[0], "Bearer "), secret)
			if err != nil {
				t.Fatal(err)
			}
			if claims.AuthType != "api_token" || claims.TenantID != "tenant-1" || len(claims.Audience) != 1 || claims.Audience[0] != "periscope" {
				t.Fatalf("delegated claims = %+v", claims)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

type workloadAuthStub struct {
	periscopepb.UnimplementedAggregatedAnalyticsServiceServer
	serviceCall chan bool
}

type networkLiveStatsAuthStub struct {
	periscopepb.UnimplementedPlatformAnalyticsServiceServer
	serviceCall chan bool
}

func (s *networkLiveStatsAuthStub) GetNetworkLiveStats(ctx context.Context, _ *periscopepb.GetNetworkLiveStatsRequest) (*periscopepb.GetNetworkLiveStatsResponse, error) {
	isService := middleware.IsServiceCall(ctx)
	s.serviceCall <- isService
	if !isService {
		return nil, status.Error(codes.PermissionDenied, "service credentials required")
	}
	return &periscopepb.GetNetworkLiveStatsResponse{}, nil
}

func (s *workloadAuthStub) GetClusterWorkload(ctx context.Context, req *periscopepb.GetClusterWorkloadRequest) (*periscopepb.GetClusterWorkloadResponse, error) {
	isService := middleware.IsServiceCall(ctx)
	s.serviceCall <- isService
	if !isService {
		return nil, status.Error(codes.PermissionDenied, "service credentials required")
	}
	return &periscopepb.GetClusterWorkloadResponse{Rows: []*periscopepb.ClusterWorkload{{
		ClusterId: req.GetClusterIds()[0],
		NodeId:    "edge-1",
		WorkKind:  "viewer",
	}}}, nil
}

func TestGetClusterWorkloadUsesServiceCredentialsWithUserContext(t *testing.T) {
	const (
		serviceToken = "periscope-service-token"
		jwtSecret    = "jwt-secret"
	)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stub := &workloadAuthStub{serviceCall: make(chan bool, 1)}
	server := grpc.NewServer(grpc.UnaryInterceptor(middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken:   serviceToken,
		JWTSecret:      []byte(jwtSecret),
		MetadataPolicy: middleware.MetadataPolicyDeny,
	})))
	periscopepb.RegisterAggregatedAnalyticsServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	jwt, err := auth.GenerateJWT("user-1", "tenant-1", "user@example.com", "member", []byte(jwtSecret))
	if err != nil {
		t.Fatalf("generate jwt: %v", err)
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, jwt)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	resp, err := client.GetClusterWorkload(ctx, "tenant-1", []string{"cluster-1"}, nil)
	if err != nil {
		t.Fatalf("GetClusterWorkload: %v", err)
	}
	if len(resp.GetRows()) != 1 || resp.GetRows()[0].GetClusterId() != "cluster-1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if isService := <-stub.serviceCall; !isService {
		t.Fatal("workload request reached Periscope with user credentials")
	}
}

func TestGetNetworkLiveStatsUsesServiceCredentialsWithAPITokenContext(t *testing.T) {
	const serviceToken = "periscope-service-token"
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stub := &networkLiveStatsAuthStub{serviceCall: make(chan bool, 1)}
	server := grpc.NewServer(grpc.UnaryInterceptor(middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: serviceToken, MetadataPolicy: middleware.MetadataPolicyDeny,
	})))
	periscopepb.RegisterPlatformAnalyticsServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr: listener.Addr().String(), Logger: logging.NewLogger(), ServiceToken: serviceToken, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"analytics:read"})
	if _, err := client.GetNetworkLiveStats(ctx); err != nil {
		t.Fatal(err)
	}
	if isService := <-stub.serviceCall; !isService {
		t.Fatal("network live stats request reached Periscope with caller credentials")
	}
}

func TestBuildTimeRange(t *testing.T) {
	t.Run("nil opts returns nil", func(t *testing.T) {
		if got := buildTimeRange(nil); got != nil {
			t.Fatalf("expected nil time range, got %#v", got)
		}
	})

	t.Run("builds proto range from opts", func(t *testing.T) {
		start := time.Date(2026, 2, 1, 1, 2, 3, 0, time.UTC)
		end := time.Date(2026, 2, 1, 2, 3, 4, 0, time.UTC)
		got := buildTimeRange(&TimeRangeOpts{
			StartTime: start,
			EndTime:   end,
		})
		if got == nil {
			t.Fatal("expected non-nil time range")
		}
		if !got.Start.AsTime().Equal(start) {
			t.Fatalf("expected start %s, got %s", start, got.Start.AsTime())
		}
		if !got.End.AsTime().Equal(end) {
			t.Fatalf("expected end %s, got %s", end, got.End.AsTime())
		}
	})
}

func TestBuildCursorPagination(t *testing.T) {
	after := "after-cursor"
	before := "before-cursor"

	t.Run("nil opts use default first limit", func(t *testing.T) {
		got := buildCursorPagination(nil)
		if got.First != int32(pagination.DefaultLimit) {
			t.Fatalf("expected first=%d, got %d", pagination.DefaultLimit, got.First)
		}
		if got.Last != 0 || got.After != nil || got.Before != nil {
			t.Fatalf("unexpected default pagination: %#v", got)
		}
	})

	t.Run("copies explicit cursor fields", func(t *testing.T) {
		got := buildCursorPagination(&CursorPaginationOpts{
			First:  25,
			After:  &after,
			Last:   5,
			Before: &before,
		})
		if got.First != 25 || got.Last != 5 {
			t.Fatalf("unexpected limits: first=%d last=%d", got.First, got.Last)
		}
		if got.GetAfter() != after || got.GetBefore() != before {
			t.Fatalf("unexpected cursor fields: after=%q before=%q", got.GetAfter(), got.GetBefore())
		}
	})
}

func TestRequireTenantID(t *testing.T) {
	cases := []struct {
		name      string
		tenantID  string
		wantError bool
	}{
		{name: "empty tenant", tenantID: "", wantError: true},
		{name: "non-empty tenant", tenantID: "tenant-1", wantError: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireTenantID(tc.tenantID)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), "tenantID required") {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
