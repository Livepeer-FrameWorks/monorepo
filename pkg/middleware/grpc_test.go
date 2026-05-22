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
		ServiceToken: "service-token",
		JWTSecret:    []byte("secret"),
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
