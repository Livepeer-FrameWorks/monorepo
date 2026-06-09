package commodore

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func captureInterceptorMD(t *testing.T, serviceToken string, ctx context.Context) metadata.MD {
	t.Helper()
	var captured metadata.MD
	invoker := func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		captured = md
		return nil
	}
	if err := authInterceptor(serviceToken)(ctx, "/svc/Method", nil, nil, nil, invoker); err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	return captured
}

func TestAuthInterceptorSetsUserContextFields(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, "jwt-abc")
	ctx = context.WithValue(ctx, ctxkeys.KeyDemoMode, true)
	ctx = context.WithValue(ctx, ctxkeys.KeyXPayment, "pay-token")

	md := captureInterceptorMD(t, "svc-tok", ctx)

	if got := md.Get("x-user-id"); len(got) != 1 || got[0] != "user-1" {
		t.Fatalf("x-user-id = %v, want [user-1]", got)
	}
	if got := md.Get("x-tenant-id"); len(got) != 1 || got[0] != "tenant-1" {
		t.Fatalf("x-tenant-id = %v, want [tenant-1]", got)
	}
	if got := md.Get("x-demo-mode"); len(got) != 1 || got[0] != "true" {
		t.Fatalf("x-demo-mode = %v, want [true]", got)
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer jwt-abc" {
		t.Fatalf("authorization = %v, want [Bearer jwt-abc] (user JWT preferred)", got)
	}
	if got := md.Get("x-payment"); len(got) != 1 || got[0] != "pay-token" {
		t.Fatalf("x-payment = %v, want [pay-token]", got)
	}
}

func TestAuthInterceptorOmitsEmptyFields(t *testing.T) {
	md := captureInterceptorMD(t, "", context.Background())

	if got := md.Get("x-user-id"); len(got) != 0 {
		t.Fatalf("expected no x-user-id, got %v", got)
	}
	if got := md.Get("x-tenant-id"); len(got) != 0 {
		t.Fatalf("expected no x-tenant-id, got %v", got)
	}
	if got := md.Get("x-demo-mode"); len(got) != 0 {
		t.Fatalf("expected no x-demo-mode, got %v", got)
	}
	if got := md.Get("authorization"); len(got) != 0 {
		t.Fatalf("expected no authorization without jwt or service token, got %v", got)
	}
	if got := md.Get("x-payment"); len(got) != 0 {
		t.Fatalf("expected no x-payment, got %v", got)
	}
}

func TestAuthInterceptorFallsBackToServiceToken(t *testing.T) {
	md := captureInterceptorMD(t, "svc-tok", context.Background())
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer svc-tok" {
		t.Fatalf("authorization = %v, want [Bearer svc-tok] (service token fallback)", got)
	}
}

func TestAuthInterceptorEmptyXPaymentNotSet(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyXPayment, "")
	md := captureInterceptorMD(t, "", ctx)
	if got := md.Get("x-payment"); len(got) != 0 {
		t.Fatalf("empty x-payment must not be set, got %v", got)
	}
}

func TestNewGRPCClientDefaultsTimeout(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Close(); err != nil {
		t.Fatalf("Close on connected client should succeed, got %v", err)
	}
}

func TestCloseNilConn(t *testing.T) {
	c := &GRPCClient{}
	if err := c.Close(); err != nil {
		t.Fatalf("Close with nil conn must return nil, got %v", err)
	}
}

func TestValidateStreamKeyCachesValidResponse(t *testing.T) {
	c := cache.New(cache.Options{
		TTL:                  time.Hour,
		StaleWhileRevalidate: time.Hour,
		NegativeTTL:          time.Minute,
		MaxEntries:           10,
	}, cache.MetricsHooks{})

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{resp: &commodorepb.ValidateStreamKeyResponse{Valid: true, IsRecordingEnabled: true}}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		Cache:         c,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	key := buildValidateStreamKeyCacheKey("sk_live_xyz", "")
	if _, ok := c.Peek(key); ok {
		t.Fatal("cache should be empty before call")
	}
	if _, err := client.ValidateStreamKey(context.Background(), "sk_live_xyz"); err != nil {
		t.Fatalf("ValidateStreamKey: %v", err)
	}
	cached, ok := c.Peek(key)
	if !ok {
		t.Fatal("valid response should be cached after a successful call")
	}
	if resp, ok := cached.(*commodorepb.ValidateStreamKeyResponse); !ok || !resp.GetValid() {
		t.Fatalf("cached value not the valid response: %#v", cached)
	}
}

func TestValidateStreamKeyDoesNotCacheInvalidResponse(t *testing.T) {
	c := cache.New(cache.Options{
		TTL:                  time.Hour,
		StaleWhileRevalidate: time.Hour,
		NegativeTTL:          time.Minute,
		MaxEntries:           10,
	}, cache.MetricsHooks{})

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{resp: &commodorepb.ValidateStreamKeyResponse{Valid: false}}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		Cache:         c,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.ValidateStreamKey(context.Background(), "sk_invalid"); err != nil {
		t.Fatalf("ValidateStreamKey: %v", err)
	}
	if _, ok := c.Peek(buildValidateStreamKeyCacheKey("sk_invalid", "")); ok {
		t.Fatal("invalid response must not be cached")
	}
}
