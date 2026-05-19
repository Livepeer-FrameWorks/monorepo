package quartermaster

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestOutgoingAuthTokenDefaultsToUserJWT(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyServiceToken, "context-service")

	if got := outgoingAuthToken(ctx, "configured-service", false); got != "user-jwt" {
		t.Fatalf("outgoingAuthToken() = %q, want user JWT", got)
	}
}

func TestOutgoingAuthTokenCanPreferConfiguredServiceToken(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyServiceToken, "context-service")

	if got := outgoingAuthToken(ctx, "configured-service", true); got != "configured-service" {
		t.Fatalf("outgoingAuthToken() = %q, want configured service token", got)
	}
}

func TestAuthInterceptorCanSendServiceTokenWithUserMetadata(t *testing.T) {
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
			if got := first(md.Get("x-user-id")); got != "user-1" {
				t.Fatalf("x-user-id = %q, want user metadata", got)
			}
			if got := first(md.Get("x-tenant-id")); got != "tenant-1" {
				t.Fatalf("x-tenant-id = %q, want tenant metadata", got)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("authInterceptor() error = %v", err)
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
