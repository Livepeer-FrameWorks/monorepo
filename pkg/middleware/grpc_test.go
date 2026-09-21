package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGRPCAuthInterceptor_SetsAuthTypeForServiceToken(t *testing.T) {
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{
		ServiceToken:   "service-token",
		JWTSecret:      []byte("secret"),
		MetadataPolicy: MetadataPolicyAllow,
	})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer service-token",
		"x-tenant-id":   "tenant-a",
	}))

	info := &grpc.UnaryServerInfo{FullMethod: "/skipper.SkipperChatService/Chat"}
	handler := func(ctx context.Context, req any) (any, error) {
		if got := ctxkeys.GetAuthType(ctx); got != "service" {
			t.Fatalf("expected auth type service, got %q", got)
		}
		if got := ctxkeys.GetTenantID(ctx); got != "tenant-a" {
			t.Fatalf("expected tenant_id tenant-a, got %q", got)
		}
		return struct{}{}, nil
	}

	if _, err := interceptor(ctx, struct{}{}, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsServiceCallRequiresValidatedServiceMarker(t *testing.T) {
	if IsServiceCall(context.Background()) {
		t.Fatal("empty context was trusted as a service call")
	}
	jwtCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	if IsServiceCall(jwtCtx) {
		t.Fatal("JWT context was trusted as a service call")
	}
	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if !IsServiceCall(serviceCtx) {
		t.Fatal("validated service context was rejected")
	}
}

func TestGRPCAuthInterceptor_SetsAuthTypeForJWT(t *testing.T) {
	secret := []byte("secret")
	token, err := auth.GenerateJWT("user-a", "tenant-a", "user@example.com", "member", secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{
		ServiceToken: "service-token",
		JWTSecret:    secret,
	})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer " + token,
	}))

	info := &grpc.UnaryServerInfo{FullMethod: "/skipper.SkipperChatService/ListConversations"}
	handler := func(ctx context.Context, req any) (any, error) {
		if got := ctxkeys.GetAuthType(ctx); got != "jwt" {
			t.Fatalf("expected auth type jwt, got %q", got)
		}
		if got := ctxkeys.GetUserID(ctx); got != "user-a" {
			t.Fatalf("expected user_id user-a, got %q", got)
		}
		return struct{}{}, nil
	}

	if _, err := interceptor(ctx, struct{}{}, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGRPCAuthInterceptorValidatesDelegatedAPITokenAudienceAndScopes(t *testing.T) {
	secret := []byte("secret")
	token, err := auth.GenerateDelegatedAPITokenJWT("user-a", "tenant-a", "", "owner", "token-a", []string{"infrastructure:write"}, "quartermaster", secret)
	if err != nil {
		t.Fatal(err)
	}
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{JWTSecret: secret, DelegatedJWTAudience: "quartermaster"})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{"authorization": "Bearer " + token}))
	_, err = interceptor(ctx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/quartermaster.ClusterService/UpdateCluster"}, func(ctx context.Context, _ any) (any, error) {
		if got := ctxkeys.GetAuthType(ctx); got != "api_token" {
			t.Fatalf("auth type = %q", got)
		}
		if got := ctxkeys.GetPermissions(ctx); len(got) != 1 || got[0] != "infrastructure:write" {
			t.Fatalf("permissions = %#v", got)
		}
		if got := ctxkeys.GetAPITokenID(ctx); got != "token-a" {
			t.Fatalf("API token ID = %q, want token-a", got)
		}
		if ctxkeys.GetJWTID(ctx) == "" {
			t.Fatal("delegated JTI was not retained for replay protection")
		}
		if _, ok := ctxkeys.GetJWTExpiresAt(ctx); !ok {
			t.Fatal("delegated expiry was not retained for replay protection")
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("delegated token rejected: %v", err)
	}

	wrongAudience := GRPCAuthInterceptor(GRPCAuthConfig{JWTSecret: secret, DelegatedJWTAudience: "commodore"})
	if _, err := wrongAudience(ctx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/quartermaster.ClusterService/UpdateCluster"}, func(context.Context, any) (any, error) {
		return struct{}{}, nil
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong audience status = %v", status.Code(err))
	}

	ordinaryService := GRPCAuthInterceptor(GRPCAuthConfig{JWTSecret: secret})
	if _, err := ordinaryService(ctx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/other.Service/Read"}, func(context.Context, any) (any, error) {
		return struct{}{}, nil
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("delegated token without configured audience status = %v", status.Code(err))
	}
}

func TestGRPCAuthInterceptorRejectsJWTForServiceOnlyMethod(t *testing.T) {
	secret := []byte("secret")
	token, err := auth.GenerateJWT("user-a", "tenant-a", "user@example.com", "member", secret)
	if err != nil {
		t.Fatal(err)
	}
	const method = "/commodore.InternalService/ResolvePullSourceByInternalName"
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{
		ServiceToken: "service-token", JWTSecret: secret, ServiceOnlyMethods: []string{method},
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer " + token,
	}))
	called := false
	_, err = interceptor(ctx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: method}, func(context.Context, any) (any, error) {
		called = true
		return struct{}{}, nil
	})
	if status.Code(err) != codes.PermissionDenied || called {
		t.Fatalf("service-only JWT result err=%v called=%v", err, called)
	}

	serviceCtx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer service-token",
	}))
	_, err = interceptor(serviceCtx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: method}, func(context.Context, any) (any, error) {
		called = true
		return struct{}{}, nil
	})
	if err != nil || !called {
		t.Fatalf("service caller rejected err=%v called=%v", err, called)
	}
}

func TestGRPCAuthInterceptorRejectsAudienceWithoutAPITokenType(t *testing.T) {
	secret := []byte("secret")
	token, _, err := auth.GenerateMistAdminSessionJWT("user-a", "tenant-a", "owner", "edge-a", "cluster-a", 0, secret)
	if err != nil {
		t.Fatal(err)
	}
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{
		JWTSecret: secret, DelegatedJWTAudience: "commodore",
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer " + token,
	}))
	called := false
	_, err = interceptor(ctx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/commodore.DeveloperService/ListAPITokens"}, func(context.Context, any) (any, error) {
		called = true
		return struct{}{}, nil
	})
	if status.Code(err) != codes.Unauthenticated || called {
		t.Fatalf("audience-only JWT result err=%v called=%v", err, called)
	}
}

func TestGRPCStreamAuthInterceptorRejectsJWTForServiceOnlyMethod(t *testing.T) {
	secret := []byte("secret")
	token, err := auth.GenerateJWT("user-a", "tenant-a", "user@example.com", "member", secret)
	if err != nil {
		t.Fatal(err)
	}
	const method = "/foghorn.HelmsmanControl/Connect"
	interceptor := GRPCStreamAuthInterceptor(GRPCAuthConfig{
		ServiceToken: "service-token", JWTSecret: secret, ServiceOnlyMethods: []string{method},
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer " + token,
	}))
	called := false
	err = interceptor(nil, &fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: method}, func(any, grpc.ServerStream) error {
		called = true
		return nil
	})
	if status.Code(err) != codes.PermissionDenied || called {
		t.Fatalf("service-only stream JWT result err=%v called=%v", err, called)
	}
}

func newMetricsTestVecs() (*prometheus.CounterVec, *prometheus.HistogramVec) {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_grpc_requests_total"}, []string{"method", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "test_grpc_request_duration_seconds"}, []string{"method"})
	return requests, duration
}

func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("get counter: %v", err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

func histogramSampleCount(t *testing.T, vec *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()
	h, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("get histogram: %v", err)
	}
	var m dto.Metric
	if err := h.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestGRPCMetricsInterceptor_OK(t *testing.T) {
	requests, duration := newMetricsTestVecs()
	interceptor := GRPCMetricsInterceptor(requests, duration)
	info := &grpc.UnaryServerInfo{FullMethod: "/commodore.CommodoreService/Login"}
	handler := func(ctx context.Context, req any) (any, error) { return struct{}{}, nil }

	if _, err := interceptor(context.Background(), struct{}{}, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := counterValue(t, requests, "Login", "ok"); got != 1 {
		t.Fatalf("expected 1 ok increment, got %v", got)
	}
	if got := histogramSampleCount(t, duration, "Login"); got != 1 {
		t.Fatalf("expected 1 duration sample, got %v", got)
	}
}

func TestGRPCMetricsInterceptor_Unauthenticated(t *testing.T) {
	requests, duration := newMetricsTestVecs()
	interceptor := GRPCMetricsInterceptor(requests, duration)
	info := &grpc.UnaryServerInfo{FullMethod: "/commodore.CommodoreService/Login"}
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.Unauthenticated, "no token")
	}

	if _, err := interceptor(context.Background(), struct{}{}, info, handler); err == nil {
		t.Fatalf("expected error")
	}
	if got := counterValue(t, requests, "Login", "Unauthenticated"); got != 1 {
		t.Fatalf("expected 1 Unauthenticated increment, got %v", got)
	}
}

func TestGRPCMetricsInterceptor_UnknownError(t *testing.T) {
	requests, duration := newMetricsTestVecs()
	interceptor := GRPCMetricsInterceptor(requests, duration)
	info := &grpc.UnaryServerInfo{FullMethod: "/commodore.CommodoreService/Login"}
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, errors.New("boom")
	}

	if _, err := interceptor(context.Background(), struct{}{}, info, handler); err == nil {
		t.Fatalf("expected error")
	}
	if got := counterValue(t, requests, "Login", "Unknown"); got != 1 {
		t.Fatalf("expected 1 Unknown increment, got %v", got)
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func TestGRPCStreamMetricsInterceptor_Cases(t *testing.T) {
	cases := []struct {
		name       string
		handlerErr error
		wantStatus string
	}{
		{"ok", nil, "ok"},
		{"unauth", status.Error(codes.Unauthenticated, "denied"), "Unauthenticated"},
		{"unknown", errors.New("boom"), "Unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests, duration := newMetricsTestVecs()
			interceptor := GRPCStreamMetricsInterceptor(requests, duration)
			info := &grpc.StreamServerInfo{FullMethod: "/decklog.DecklogService/StreamEvents"}
			handler := func(srv any, stream grpc.ServerStream) error { return tc.handlerErr }
			stream := &fakeServerStream{ctx: context.Background()}

			err := interceptor(nil, stream, info, handler)
			if (err == nil) != (tc.handlerErr == nil) {
				t.Fatalf("error mismatch: %v vs %v", err, tc.handlerErr)
			}
			if got := counterValue(t, requests, "StreamEvents", tc.wantStatus); got != 1 {
				t.Fatalf("expected 1 %s increment, got %v", tc.wantStatus, got)
			}
			if got := histogramSampleCount(t, duration, "StreamEvents"); got != 1 {
				t.Fatalf("expected 1 duration sample, got %v", got)
			}
		})
	}
}
