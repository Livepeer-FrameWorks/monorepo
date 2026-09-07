package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/prometheus/client_golang/prometheus"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type serviceOnlyWebhookStub struct {
	purserpb.UnimplementedWebhookServiceServer
	calls int
}

func (s *serviceOnlyWebhookStub) ProcessWebhook(context.Context, *sharedpb.WebhookRequest) (*sharedpb.WebhookResponse, error) {
	s.calls++
	return &sharedpb.WebhookResponse{Success: true}, nil
}

func TestPurserServiceOnlyMethodsRejectSessionJWTAndAcceptServiceToken(t *testing.T) {
	secret := []byte("round-eight-purser-jwt-secret")
	session, err := auth.GenerateJWT("user-1", "tenant-1", "owner@example.com", "owner", secret)
	if err != nil {
		t.Fatal(err)
	}
	interceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken:         "service-secret",
		JWTSecret:            secret,
		DelegatedJWTAudience: "purser",
		ServiceOnlyMethods:   purserServiceOnlyMethods(),
	})
	handler := func(ctx context.Context, _ any) (any, error) {
		return ctxkeys.GetAuthType(ctx), nil
	}
	for _, method := range purserServiceOnlyMethods() {
		t.Run(method, func(t *testing.T) {
			info := &grpcpkg.UnaryServerInfo{FullMethod: method}
			jwtCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+session))
			if _, err := interceptor(jwtCtx, nil, info, handler); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("session status = %v, want PermissionDenied", status.Code(err))
			}
			serviceCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer service-secret"))
			got, err := interceptor(serviceCtx, nil, info, handler)
			if err != nil || got != "service" {
				t.Fatalf("service result = %v, err %v", got, err)
			}
		})
	}
}

func TestPurserProductionInterceptorChainRejectsSessionWebhookOverWire(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("round-eight-purser-wire-secret")
	metrics := &ServerMetrics{
		GRPCRequests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_purser_service_only_requests_total"}, []string{"method", "status"}),
		GRPCDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "test_purser_service_only_duration_seconds"}, []string{"method"}),
	}
	cfg := GRPCServerConfig{
		DB: nil, Logger: logging.NewLogger(), ServiceToken: "service-secret", JWTSecret: secret,
		Metrics: metrics, AllowInsecure: true,
	}
	server := grpcpkg.NewServer(grpcpkg.ChainUnaryInterceptor(purserUnaryInterceptors(cfg)...))
	stub := &serviceOnlyWebhookStub{}
	purserpb.RegisterWebhookServiceServer(server, stub)
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
	client := purserpb.NewWebhookServiceClient(conn)

	session, err := auth.GenerateJWT("user-1", "tenant-1", "owner@example.com", "owner", secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jwtCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+session))
	if _, err := client.ProcessWebhook(jwtCtx, &sharedpb.WebhookRequest{Provider: "mollie"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("session webhook status = %v, want PermissionDenied", status.Code(err))
	}
	serviceCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer service-secret"))
	if _, err := client.ProcessWebhook(serviceCtx, &sharedpb.WebhookRequest{Provider: "mollie"}); err != nil {
		t.Fatalf("service webhook denied: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", stub.calls)
	}
}
