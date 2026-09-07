package grpc

import (
	"context"
	"net"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func analyticsTokenContext(tenant string, permissions ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
}

func TestAPITokenAnalyticsAuthorizationRequiresScopeAndTenantBinding(t *testing.T) {
	interceptor := apiTokenAuthorizationInterceptor()
	info := &grpcpkg.UnaryServerInfo{FullMethod: "/periscope.AggregatedAnalyticsService/GetTenantDailyStats"}
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	for _, tc := range []struct {
		name string
		ctx  context.Context
		req  any
	}{
		{"missing scope", analyticsTokenContext("tenant-1", "streams:read"), &periscopepb.GetTenantDailyStatsRequest{TenantId: "tenant-1"}},
		{"coarse read scope", analyticsTokenContext("tenant-1", "read"), &periscopepb.GetTenantDailyStatsRequest{TenantId: "tenant-1"}},
		{"cross tenant", analyticsTokenContext("tenant-1", "analytics:read"), &periscopepb.GetTenantDailyStatsRequest{TenantId: "tenant-2"}},
		{"unbound request", analyticsTokenContext("tenant-1", "analytics:read"), &periscopepb.ListTenantActivityRequest{TenantIds: []string{"tenant-1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := interceptor(tc.ctx, tc.req, info, handler); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("error = %v, want PermissionDenied", err)
			}
		})
	}
	if got, err := interceptor(analyticsTokenContext("tenant-1", "analytics:read"), &periscopepb.GetTenantDailyStatsRequest{TenantId: "tenant-1"}, info, handler); err != nil || got != "ok" {
		t.Fatalf("authorized request = (%v, %v)", got, err)
	}
}

type delegatedAnalyticsStub struct {
	periscopepb.UnimplementedAggregatedAnalyticsServiceServer
	calls int
}

func (s *delegatedAnalyticsStub) GetTenantDailyStats(context.Context, *periscopepb.GetTenantDailyStatsRequest) (*periscopepb.GetTenantDailyStatsResponse, error) {
	s.calls++
	return &periscopepb.GetTenantDailyStatsResponse{}, nil
}

func TestDelegatedAPITokenIsAuthorizedAndConsumedAtGRPCBoundary(t *testing.T) {
	const tenantID = "tenant-1"
	secret := []byte("periscope-delegation-secret")
	token, err := auth.GenerateDelegatedAPITokenJWT(
		"user-1",
		tenantID,
		"user@example.com",
		"member",
		"api-token-1",
		[]string{"analytics:read"},
		"periscope",
		secret,
	)
	if err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`INSERT INTO periscope\.delegated_jwt_replays`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`INSERT INTO periscope\.delegated_jwt_replays`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stub := &delegatedAnalyticsStub{}
	server := grpcpkg.NewServer(grpcpkg.ChainUnaryInterceptor(
		middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
			JWTSecret:            secret,
			DelegatedJWTAudience: "periscope",
			MetadataPolicy:       middleware.MetadataPolicyDeny,
		}),
		apiTokenAuthorizationInterceptor(),
		middleware.DelegatedJWTReplayInterceptor(db, "periscope"),
	))
	periscopepb.RegisterAggregatedAnalyticsServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpcpkg.NewClient(listener.Addr().String(), grpcpkg.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := periscopepb.NewAggregatedAnalyticsServiceClient(conn)
	callCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	req := &periscopepb.GetTenantDailyStatsRequest{TenantId: tenantID}
	if _, err := client.GetTenantDailyStats(callCtx, req); err != nil {
		t.Fatalf("first delegated call: %v", err)
	}
	if _, err := client.GetTenantDailyStats(callCtx, req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("replayed delegated call error = %v, want Unauthenticated", err)
	}
	if stub.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", stub.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
