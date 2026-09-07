package commodore

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestAuthInterceptorPrefersCommodoreDelegationOverServiceCredential(t *testing.T) {
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer stale-token",
		"x-user-id", "stale-user",
		"x-tenant-id", "stale-tenant",
		"x-correlation-id", "correlation-1",
	))
	ctx = context.WithValue(ctx, ctxkeys.KeyDelegatedJWTs, map[string]string{
		"quartermaster": "wrong-audience", "commodore": "commodore-delegation",
	})
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	err := authInterceptor("service-token")(ctx, "/commodore.DeveloperService/CreateAPIToken", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer commodore-delegation" {
				t.Fatalf("authorization = %v", got)
			}
			if got := md.Get("x-user-id"); len(got) != 1 || got[0] != "user-1" {
				t.Fatalf("x-user-id = %v", got)
			}
			if got := md.Get("x-tenant-id"); len(got) != 1 || got[0] != "tenant-1" {
				t.Fatalf("x-tenant-id = %v", got)
			}
			if got := md.Get("x-correlation-id"); len(got) != 1 || got[0] != "correlation-1" {
				t.Fatalf("x-correlation-id = %v", got)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthInterceptorForcedServiceCredentialDropsCallerIdentity(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "caller-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	err := authInterceptor("service-token")(withServiceAuth(ctx), "/commodore.InternalService/TerminateTenantStreams", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer service-token" {
				t.Fatalf("authorization = %v", got)
			}
			if len(md.Get("x-user-id")) != 0 || len(md.Get("x-tenant-id")) != 0 {
				t.Fatalf("caller identity leaked into service-only RPC: %v", md)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

type tenantBillingLookupStub struct {
	commodorepb.UnimplementedInternalServiceServer
}

func (tenantBillingLookupStub) GetTenantUserCount(context.Context, *commodorepb.GetTenantUserCountRequest) (*commodorepb.GetTenantUserCountResponse, error) {
	return &commodorepb.GetTenantUserCountResponse{}, nil
}

func (tenantBillingLookupStub) GetTenantPrimaryUser(context.Context, *commodorepb.GetTenantPrimaryUserRequest) (*commodorepb.GetTenantPrimaryUserResponse, error) {
	return &commodorepb.GetTenantPrimaryUserResponse{}, nil
}

func TestTenantBillingLookupsUseServiceCredentialOverWire(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan metadata.MD, 2)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		captured <- md.Copy()
		return handler(ctx, req)
	}))
	commodorepb.RegisterInternalServiceServer(server, tenantBillingLookupStub{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr: listener.Addr().String(), ServiceToken: "service-token",
		AllowInsecure: true, Logger: logging.NewLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "caller-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "caller-user")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "caller-tenant")
	if _, err := client.GetTenantUserCount(ctx, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetTenantPrimaryUser(ctx, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		md := <-captured
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer service-token" {
			t.Fatalf("authorization = %v", got)
		}
		if len(md.Get("x-user-id")) != 0 || len(md.Get("x-tenant-id")) != 0 {
			t.Fatalf("caller identity leaked into service-only RPC: %v", md)
		}
	}
}

func TestBuildValidateStreamKeyCacheKey(t *testing.T) {
	tests := []struct {
		name      string
		streamKey string
		clusterID string
		want      string
	}{
		{
			name:      "default route",
			streamKey: "sk_live_abc",
			clusterID: "",
			want:      "commodore:validate:sk_live_abc",
		},
		{
			name:      "cluster-specific route",
			streamKey: "sk_live_abc",
			clusterID: "cluster-us-west",
			want:      "commodore:validate:sk_live_abc:cluster:cluster-us-west",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildValidateStreamKeyCacheKey(tt.streamKey, tt.clusterID); got != tt.want {
				t.Fatalf("buildValidateStreamKeyCacheKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildCheckStreamKeyCacheKeyIsTenantScoped(t *testing.T) {
	owner := buildCheckStreamKeyCacheKey("sk_live_abc", "tenant-owner")
	attacker := buildCheckStreamKeyCacheKey("sk_live_abc", "tenant-attacker")
	if owner == attacker {
		t.Fatalf("tenant-scoped key collision: %q", owner)
	}
	if got := buildCheckStreamKeyCacheKey("sk_live_abc", ""); got != "commodore:validate:sk_live_abc" {
		t.Fatalf("service cache key=%q", got)
	}
}

func TestInvalidateTenantCacheKeysEvictsOnlyMatchingTenant(t *testing.T) {
	clientCache := cache.New(cache.Options{TTL: time.Hour, NegativeTTL: time.Hour, MaxEntries: 20}, cache.MetricsHooks{})
	clientCache.SetDefault("tenant-1:resolve-internal:live-a", "a")
	clientCache.SetDefault("tenant-2:resolve-internal:live-b", "b")
	clientCache.SetDefault("commodore:internal:live-global-a", &commodorepb.ResolveInternalNameResponse{TenantId: "tenant-1"})
	clientCache.SetDefault("commodore:internal:live-global-b", &commodorepb.ResolveInternalNameResponse{TenantId: "tenant-2"})
	clientCache.SetDefault(buildValidateStreamKeyCacheKey("service-key-a", ""), &commodorepb.ValidateStreamKeyResponse{Valid: true, TenantId: "tenant-1"})
	clientCache.SetDefault(buildValidateStreamKeyCacheKey("service-key-b", ""), &commodorepb.ValidateStreamKeyResponse{Valid: true, TenantId: "tenant-2"})
	clientCache.SetDefault(buildCheckStreamKeyCacheKey("stream-key-a", "tenant-1"), &commodorepb.ValidateStreamKeyResponse{Valid: true})
	clientCache.SetDefault(buildCheckStreamKeyCacheKey("stream-key-b", "tenant-2"), &commodorepb.ValidateStreamKeyResponse{Valid: true})
	negativeKey := "commodore:internal:new-name"
	if _, ok, err := clientCache.Get(context.Background(), negativeKey, func(context.Context, string) (any, bool, error) {
		return nil, false, status.Error(codes.NotFound, "not found")
	}); ok || status.Code(err) != codes.NotFound {
		t.Fatalf("seed global negative = ok %v, err %v", ok, err)
	}
	client := &GRPCClient{cache: clientCache}

	client.InvalidateTenantCacheKeys("tenant-1")

	if _, ok := clientCache.Peek("tenant-1:resolve-internal:live-a"); ok {
		t.Fatal("matching tenant cache entry survived invalidation")
	}
	if _, ok := clientCache.Peek("commodore:internal:live-global-a"); ok {
		t.Fatal("globally keyed internal-name response survived tenant invalidation")
	}
	if _, ok := clientCache.Peek(buildCheckStreamKeyCacheKey("stream-key-a", "tenant-1")); ok {
		t.Fatal("tenant-scoped CheckStreamKey entry survived invalidation")
	}
	if _, ok := clientCache.Peek(buildValidateStreamKeyCacheKey("service-key-a", "")); ok {
		t.Fatal("service validation entry for tenant survived invalidation")
	}
	for _, entry := range clientCache.Snapshot() {
		if entry.Key == negativeKey {
			t.Fatal("globally keyed negative internal-name entry survived tenant invalidation")
		}
	}
	if _, ok := clientCache.Peek("tenant-2:resolve-internal:live-b"); !ok {
		t.Fatal("unrelated tenant cache entry was invalidated")
	}
	if _, ok := clientCache.Peek("commodore:internal:live-global-b"); !ok {
		t.Fatal("unrelated global internal-name response was invalidated")
	}
	if _, ok := clientCache.Peek(buildCheckStreamKeyCacheKey("stream-key-b", "tenant-2")); !ok {
		t.Fatal("unrelated CheckStreamKey entry was invalidated")
	}
	if _, ok := clientCache.Peek(buildValidateStreamKeyCacheKey("service-key-b", "")); !ok {
		t.Fatal("unrelated service validation entry was invalidated")
	}
}

type validateStreamKeyStub struct {
	commodorepb.UnimplementedInternalServiceServer
	calls       atomic.Int32
	checkCalls  atomic.Int32
	resp        *commodorepb.ValidateStreamKeyResponse
	err         error
	checkErr    error
	validateErr error
}

type resolveInternalNameStub struct {
	commodorepb.UnimplementedInternalServiceServer
	calls atomic.Int32
	resp  *commodorepb.ResolveInternalNameResponse
}

func (s *resolveInternalNameStub) ResolveInternalName(context.Context, *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
	s.calls.Add(1)
	return s.resp, nil
}

func TestResolveInternalNameCachesGloballyUniqueNameWithoutTenantContext(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	stub := &resolveInternalNameStub{resp: &commodorepb.ResolveInternalNameResponse{TenantId: "tenant-1", ServePolicyResolved: proto.Bool(true)}}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	clientCache := cache.New(cache.Options{TTL: time.Hour, StaleWhileRevalidate: time.Hour, MaxEntries: 10}, cache.MetricsHooks{})
	client, err := NewGRPCClient(GRPCConfig{GRPCAddr: listener.Addr().String(), Cache: clientCache, ServiceToken: "service-token", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for range 2 {
		if _, err := client.ResolveInternalName(context.Background(), "live-demo"); err != nil {
			t.Fatal(err)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("backend calls = %d, want 1 cached lookup", got)
	}
}

func (s *validateStreamKeyStub) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	s.calls.Add(1)
	if s.validateErr != nil {
		return nil, s.validateErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func (s *validateStreamKeyStub) CheckStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	s.checkCalls.Add(1)
	if s.checkErr != nil {
		return nil, s.checkErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func TestValidateStreamKeyDoesNotEscalateTenantCheckOnUnimplemented(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{
		checkErr: status.Error(codes.Unimplemented, "old Commodore"),
		resp:     &commodorepb.ValidateStreamKeyResponse{Valid: true, TenantId: "victim"},
	}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	clientCache := cache.New(cache.Options{TTL: time.Hour, MaxEntries: 10}, cache.MetricsHooks{})
	clientCache.SetDefault(buildCheckStreamKeyCacheKey("victim-key", "attacker"), &commodorepb.ValidateStreamKeyResponse{Valid: true, TenantId: "attacker"})
	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr: listener.Addr().String(), Cache: clientCache,
		ServiceToken: "service-token", AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "attacker")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-attacker")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, "user-jwt")
	got, err := client.ValidateStreamKey(ctx, "victim-key")
	if status.Code(err) != codes.Unavailable || got != nil {
		t.Fatalf("rolling fallback=(%+v,%v), want unavailable without fallback", got, err)
	}
	if stub.checkCalls.Load() != 1 || stub.calls.Load() != 0 {
		t.Fatalf("calls: CheckStreamKey=%d ValidateStreamKey=%d, want 1/0", stub.checkCalls.Load(), stub.calls.Load())
	}
	if _, ok := clientCache.Peek(buildCheckStreamKeyCacheKey("victim-key", "attacker")); ok {
		t.Fatal("generic rolling-upgrade denial was cached")
	}
}

func TestValidateStreamKeyRequiresExplicitServicePathWithoutTenant(t *testing.T) {
	client := &GRPCClient{}
	resp, err := client.ValidateStreamKey(context.Background(), "sk_live_foreign")
	if resp != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("tenantless interactive validation = (%v, %v), want Unauthenticated", resp, err)
	}
}

func TestValidateStreamKeyBypassesCache(t *testing.T) {
	c := cache.New(cache.Options{
		TTL:                  time.Hour,
		StaleWhileRevalidate: time.Hour,
		NegativeTTL:          time.Minute,
		MaxEntries:           10,
	}, cache.MetricsHooks{})
	stale := &commodorepb.ValidateStreamKeyResponse{Valid: true, IsRecordingEnabled: false}
	if _, ok, err := c.Get(context.Background(), "commodore:validate:sk_live_abc", func(context.Context, string) (interface{}, bool, error) {
		return stale, true, nil
	}); err != nil || !ok {
		t.Fatalf("failed to seed cache: ok=%v err=%v", ok, err)
	}

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{
		resp: &commodorepb.ValidateStreamKeyResponse{Valid: true, IsRecordingEnabled: true},
	}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		Cache:         c,
		ServiceToken:  "service-token",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	got, err := client.ValidateStreamKeyAsService(context.Background(), "sk_live_abc")
	if err != nil {
		t.Fatalf("ValidateStreamKey: %v", err)
	}
	if !got.GetIsRecordingEnabled() {
		t.Fatalf("ValidateStreamKey returned stale cached DVR-disabled response")
	}
	if stub.calls.Load() != 1 {
		t.Fatalf("ValidateStreamKey called backend %d times, want 1", stub.calls.Load())
	}
	if stub.checkCalls.Load() != 0 {
		t.Fatalf("tenantless service validation called CheckStreamKey %d times", stub.checkCalls.Load())
	}
}

func TestValidateStreamKeyCacheFallbackExcludesClaims(t *testing.T) {
	c := cache.New(cache.Options{
		TTL:                  time.Hour,
		StaleWhileRevalidate: time.Hour,
		NegativeTTL:          time.Minute,
		MaxEntries:           10,
	}, cache.MetricsHooks{})
	cached := &commodorepb.ValidateStreamKeyResponse{Valid: true, IsRecordingEnabled: true}
	c.SetDefault(buildValidateStreamKeyCacheKey("sk_live_abc", "demo-media"), cached)
	c.SetDefault(buildValidateStreamKeyCacheKey("sk_live_abc", ""), cached)

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{err: status.Error(codes.Unavailable, "commodore unavailable")}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		Cache:         c,
		ServiceToken:  "service-token",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	claimed, err := client.ValidateStreamKeyForClaim(context.Background(), "sk_live_abc", "demo-media", "conn-1")
	if err == nil || claimed != nil {
		t.Fatalf("claiming validation = (%v, %v), want backend error and no cached response", claimed, err)
	}

	got, err := client.ValidateStreamKeyAsService(context.Background(), "sk_live_abc")
	if err != nil {
		t.Fatalf("non-claiming validation did not use bounded cache: %v", err)
	}
	if got != cached {
		t.Fatalf("non-claiming validation did not return cached admission snapshot")
	}
	if stub.calls.Load() != 2 {
		t.Fatalf("ValidateStreamKey called backend %d times, want 2", stub.calls.Load())
	}
}

func TestValidateStreamKeyDoesNotUseCacheOnAuthorizationFailure(t *testing.T) {
	c := cache.New(cache.Options{TTL: time.Hour, MaxEntries: 10}, cache.MetricsHooks{})
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	c.SetDefault(buildCheckStreamKeyCacheKey("sk_live_abc", "tenant-1"), &commodorepb.ValidateStreamKeyResponse{Valid: true})

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{err: status.Error(codes.PermissionDenied, "denied")}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	client, err := NewGRPCClient(GRPCConfig{GRPCAddr: listener.Addr().String(), Cache: c, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	got, err := client.ValidateStreamKey(ctx, "sk_live_abc")
	if status.Code(err) != codes.PermissionDenied || got != nil {
		t.Fatalf("ValidateStreamKey = (%v, %v), want PermissionDenied without cached response", got, err)
	}
	if stub.checkCalls.Load() != 1 || stub.calls.Load() != 0 {
		t.Fatalf("calls: CheckStreamKey=%d ValidateStreamKey=%d", stub.checkCalls.Load(), stub.calls.Load())
	}
}
