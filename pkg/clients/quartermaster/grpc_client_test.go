package quartermaster

import (
	"context"
	"net"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestOutgoingAuthTokenDefaultsToUserJWT(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyServiceToken, "context-service")

	got, err := outgoingAuthTokenForRPC(ctx, "configured-service", nil)
	if err != nil || got != "user-jwt" {
		t.Fatalf("outgoingAuthTokenForRPC() = %q, %v; want user JWT", got, err)
	}
}

func TestOutgoingAuthTokenUsesOnlyQuartermasterDelegation(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDelegatedJWTs, map[string]string{
		"quartermaster": "delegated-quartermaster",
		"commodore":     "delegated-commodore",
	})
	got, err := outgoingAuthTokenForRPC(ctx, "service-token", nil)
	if err != nil || got != "delegated-quartermaster" {
		t.Fatalf("outgoingAuthTokenForRPC() = %q, %v; want Quartermaster delegation", got, err)
	}
}

func TestOutgoingAuthTokenMissingDelegationSecretHasAuthStatus(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-1")
	_, err := outgoingAuthTokenForRPC(ctx, "service-token", nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("status = %v, want Unauthenticated", status.Code(err))
	}
}

func TestAuthInterceptorPreferredServiceTokenStripsUserMetadata(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	err := authInterceptor("service-token", true)(ctx, "/quartermaster.Test/Method", nil, nil, nil,
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("missing outgoing metadata")
			}
			if got := first(md.Get("authorization")); got != "Bearer service-token" {
				t.Fatalf("authorization = %q, want service token", got)
			}
			if got := first(md.Get("x-user-id")); got != "" {
				t.Fatalf("x-user-id = %q, want pure service identity", got)
			}
			if got := first(md.Get("x-tenant-id")); got != "" {
				t.Fatalf("x-tenant-id = %q, want pure service identity", got)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("authInterceptor() error = %v", err)
	}
}

func TestAuthInterceptorForcedServiceAuthStripsUserMetadata(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer stale-user", "x-user-id", "stale-user", "x-tenant-id", "stale-tenant", "x-payment", "payment-proof"))
	ctx = withServiceAuth(ctx)

	err := authInterceptor("service-token", false)(ctx, "/quartermaster.Test/Method", nil, nil, nil,
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("missing outgoing metadata")
			}
			if got := first(md.Get("authorization")); got != "Bearer service-token" {
				t.Fatalf("authorization = %q, want service token", got)
			}
			if got := first(md.Get("x-user-id")); got != "" {
				t.Fatalf("x-user-id = %q, want stripped", got)
			}
			if got := first(md.Get("x-tenant-id")); got != "" {
				t.Fatalf("x-tenant-id = %q, want stripped", got)
			}
			if got := first(md.Get("x-payment")); got != "payment-proof" {
				t.Fatalf("x-payment = %q, want preserved", got)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("authInterceptor() error = %v", err)
	}
}

func TestAuthInterceptorForcedServiceAuthNeverFallsBackToDelegatedJWT(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDelegatedJWTs, map[string]string{"quartermaster": "delegated-user"})
	ctx = withServiceAuth(ctx)
	invoked := false
	err := authInterceptor("", false)(ctx, "/quartermaster.Test/Method", nil, nil, nil,
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			invoked = true
			return nil
		})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("status = %v, want Unauthenticated", status.Code(err))
	}
	if invoked {
		t.Fatal("forced service call reached transport without a service token")
	}
}

func TestAuthInterceptorPreferServiceTokenNeverForwardsCallerWhenTokenMissing(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	invoked := false
	err := authInterceptor("", true)(ctx, "/quartermaster.Test/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			invoked = true
			return nil
		})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("status = %v, want Unauthenticated", status.Code(err))
	}
	if invoked {
		t.Fatal("PreferServiceToken fell back to caller credentials")
	}
}

type clusterOwnershipLookupStub struct {
	quartermasterpb.UnimplementedClusterServiceServer
}

func (clusterOwnershipLookupStub) GetCluster(context.Context, *quartermasterpb.GetClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	return &quartermasterpb.ClusterResponse{}, nil
}

func TestGetClusterAsServiceUsesServiceCredentialOverWire(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan metadata.MD, 1)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		captured <- md.Copy()
		return handler(ctx, req)
	}))
	quartermasterpb.RegisterClusterServiceServer(server, clusterOwnershipLookupStub{})
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
	if _, err := client.GetClusterAsService(ctx, "cluster-1"); err != nil {
		t.Fatal(err)
	}
	md := <-captured
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer service-token" {
		t.Fatalf("authorization = %v", got)
	}
	if len(md.Get("x-user-id")) != 0 || len(md.Get("x-tenant-id")) != 0 {
		t.Fatalf("caller identity leaked into service-only RPC: %v", md)
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
