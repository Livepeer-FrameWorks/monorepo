package purser

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestServiceOnlyPurserCallReplacesCallerCredentialAndIdentity(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "caller-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"authorization", "Bearer stale",
		"x-user-id", "stale-user",
		"x-tenant-id", "stale-tenant",
		"x-trace-id", "trace-1",
	))

	called := false
	err := authInterceptor("service-secret", false)(withServiceAuth(ctx), "/purser.X402Service/SettleX402Payment", nil, nil, nil,
		func(callCtx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			called = true
			md, _ := metadata.FromOutgoingContext(callCtx)
			if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer service-secret" {
				t.Fatalf("authorization = %v", got)
			}
			if got := md.Get("x-user-id"); len(got) != 0 {
				t.Fatalf("x-user-id leaked onto service call: %v", got)
			}
			if got := md.Get("x-tenant-id"); len(got) != 0 {
				t.Fatalf("x-tenant-id leaked onto service call: %v", got)
			}
			if got := md.Get("x-trace-id"); len(got) != 1 || got[0] != "trace-1" {
				t.Fatalf("unrelated metadata = %v", got)
			}
			return nil
		})
	if err != nil || !called {
		t.Fatalf("service call = called %v, err %v", called, err)
	}
}

func TestPurserMissingDelegationSecretHasAuthStatus(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-1")
	err := authInterceptor("service-token", false)(ctx, "/purser.BillingService/GetSubscription", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			t.Fatal("invoker called")
			return nil
		})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("status = %v, want Unauthenticated", status.Code(err))
	}
}

func TestServiceOnlyPurserCallRequiresConfiguredToken(t *testing.T) {
	err := authInterceptor("", false)(withServiceAuth(context.Background()), "/purser.WebhookService/ProcessWebhook", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			t.Fatal("invoker called")
			return nil
		})
	if err == nil {
		t.Fatal("missing service token was accepted")
	}
}

func TestPreferredServiceTokenPurserCallStripsDelegatedCallerIdentity(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, "delegated-commodore")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	called := false
	err := authInterceptor("service-secret", true)(ctx, "/purser.BillingService/GetTenantAdmissionStatus", nil, nil, nil,
		func(callCtx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			called = true
			md, _ := metadata.FromOutgoingContext(callCtx)
			if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer service-secret" {
				t.Fatalf("authorization = %v", got)
			}
			if got := md.Get("x-user-id"); len(got) != 0 {
				t.Fatalf("x-user-id leaked onto service call: %v", got)
			}
			if got := md.Get("x-tenant-id"); len(got) != 0 {
				t.Fatalf("x-tenant-id leaked onto service call: %v", got)
			}
			return nil
		})
	if err != nil || !called {
		t.Fatalf("preferred service call = called %v, err %v", called, err)
	}
}
