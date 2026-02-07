package middleware

import (
	"context"
	"testing"

	"frameworks/pkg/auth"
	"frameworks/pkg/ctxkeys"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
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
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
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
