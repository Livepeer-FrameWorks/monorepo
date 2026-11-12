package middleware

import (
	"context"
	"testing"

	"github.com/99designs/gqlgen/graphql"
)

func assertTerminalGraphQLResponse(t *testing.T, ctx context.Context, handler graphql.ResponseHandler, code string) {
	t.Helper()
	response := handler(ctx)
	if response == nil || len(response.Errors) != 1 {
		t.Fatalf("first response = %+v, want one error", response)
	}
	if code != "" && response.Errors[0].Extensions["code"] != code {
		t.Fatalf("error extensions = %+v, want code %s", response.Errors[0].Extensions, code)
	}
	for call := 2; call <= 4; call++ {
		if response := handler(ctx); response != nil {
			t.Fatalf("terminal response repeated on call %d; WebSocket operations must finish after one error", call)
		}
	}
}

func TestGraphQLAuthDenialTerminates(t *testing.T) {
	ctx := wsAuthOperationContext(`subscription { name }`, "")
	handler := GraphQLOperationAuth()(ctx, func(context.Context) graphql.ResponseHandler {
		t.Fatal("unauthenticated protected operation reached its resolver")
		return nil
	})
	assertTerminalGraphQLResponse(t, ctx, handler, "UNAUTHORIZED")
}

func TestGraphQLAccessDenialTerminates(t *testing.T) {
	for _, test := range []struct {
		name    string
		checker fakeBillingChecker
		code    string
	}{
		{name: "unavailable", checker: fakeBillingChecker{err: context.DeadlineExceeded}, code: "BILLING_STATUS_UNAVAILABLE"},
		{name: "suspended", checker: fakeBillingChecker{isSuspended: true}, code: "ACCOUNT_SUSPENDED"},
		{name: "unfunded", checker: fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true}, code: "INSUFFICIENT_BALANCE"},
		{name: "missing tier", checker: fakeBillingChecker{billingModel: "postpaid"}, code: "BILLING_STATUS_UNAVAILABLE"},
		{name: "provider setup", checker: fakeBillingChecker{billingModel: "postpaid", tierName: "supporter"}, code: "PAYMENT_SETUP_REQUIRED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := wsMutationContext("tenant-1", "createClip")
			handler := GraphQLOperationAccess(test.checker)(ctx, func(context.Context) graphql.ResponseHandler {
				t.Fatal("denied rated work reached its resolver")
				return nil
			})
			assertTerminalGraphQLResponse(t, ctx, handler, test.code)
		})
	}
}

func TestGraphQLRateLimitDenialTerminates(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Settings: func() AccessSettings {
		return AccessSettings{PublicLimitPerMinute: 1, PublicBurst: 1}
	}})
	t.Cleanup(rl.Stop)
	ctx := wsOperationContext(t, "203.0.113.19", "", "resolveIngestEndpoint")
	mw := GraphQLOperationRateLimit(rl, nil)
	for range 3 {
		runOp(mw, ctx)
	}
	handler := mw(ctx, func(context.Context) graphql.ResponseHandler {
		t.Fatal("rate-limited operation reached its resolver")
		return nil
	})
	assertTerminalGraphQLResponse(t, ctx, handler, "")
}

func TestGraphQLDeniedOperationStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(wsAuthOperationContext(`subscription { name }`, ""))
	t.Cleanup(cancel)
	handler := GraphQLOperationAuth()(ctx, func(context.Context) graphql.ResponseHandler {
		t.Fatal("unauthenticated operation reached its resolver")
		return nil
	})
	if response := handler(ctx); response == nil {
		t.Fatal("missing initial denial")
	}
	cancel()
	if response := handler(ctx); response != nil {
		t.Fatal("cancelled denied operation continues producing responses")
	}
}
