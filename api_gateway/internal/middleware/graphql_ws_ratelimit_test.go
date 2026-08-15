package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gin-gonic/gin"
	"github.com/vektah/gqlparser/v2/ast"
)

func wsOperationContext(t *testing.T, clientIP, tenantID, fieldName string) context.Context {
	t.Helper()
	ctx := context.Background()
	if clientIP != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, clientIP)
	}
	if tenantID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	}
	// No gin context: this is what a WebSocket-borne operation looks like.
	return graphql.WithOperationContext(ctx, &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation:    ast.Query,
			SelectionSet: ast.SelectionSet{&ast.Field{Name: fieldName}},
		},
	})
}

func newTestRateLimiter(t *testing.T) *RateLimiter {
	t.Helper()
	return NewRateLimiter(RateLimitConfig{Logger: logging.NewLogger()})
}

// runOp drives the middleware once and reports whether the operation was
// allowed through.
func runOp(mw graphql.OperationMiddleware, ctx context.Context) bool {
	reached := false
	handler := mw(ctx, func(ctx context.Context) graphql.ResponseHandler {
		reached = true
		return func(context.Context) *graphql.Response { return &graphql.Response{} }
	})
	handler(ctx)
	return reached
}

// The whole point of the WS hook: an anonymous socket calling a public
// allowlisted resolver must eventually be refused. Anonymous callers bucket as
// "public:<ip>", which is not a tenant — routing that through tenant-limit
// lookup yields (0,0), and Allow treats a nonpositive limit as "allow", which
// silently disabled this limiter for exactly the traffic it exists to bound.
func TestGraphQLOperationRateLimitThrottlesAnonymousWebSocket(t *testing.T) {
	rl := newTestRateLimiter(t)
	defer rl.Stop()

	// A tenant-limit lookup that behaves like Quartermaster does for a
	// synthetic public bucket: unknown tenant, no limits.
	tenantLimits := func(string) (int, int) { return 0, 0 }
	mw := GraphQLOperationRateLimit(rl, tenantLimits)

	ctx := wsOperationContext(t, "203.0.113.9", "", "resolveIngestEndpoint")

	limit, burst := publicRateLimits()
	allowedCount := 0
	for i := 0; i < limit+burst+10; i++ {
		if runOp(mw, ctx) {
			allowedCount++
		}
	}

	if allowedCount > limit+burst {
		t.Fatalf("anonymous WS operations were not throttled: %d allowed, public limit is %d+%d",
			allowedCount, limit, burst)
	}
	if allowedCount == 0 {
		t.Fatal("anonymous WS operations were entirely blocked; the public allowance should let some through")
	}
}

// Separate callers must not share an allowance.
func TestGraphQLOperationRateLimitBucketsPerClientIP(t *testing.T) {
	rl := newTestRateLimiter(t)
	defer rl.Stop()
	mw := GraphQLOperationRateLimit(rl, func(string) (int, int) { return 0, 0 })

	limit, burst := publicRateLimits()
	exhausted := wsOperationContext(t, "203.0.113.9", "", "resolveIngestEndpoint")
	for i := 0; i < limit+burst+10; i++ {
		runOp(mw, exhausted)
	}

	fresh := wsOperationContext(t, "198.51.100.4", "", "resolveIngestEndpoint")
	if !runOp(mw, fresh) {
		t.Fatal("a different client IP was refused on someone else's exhausted bucket")
	}
}

// HTTP operations are charged by the HTTP middleware before gqlgen runs;
// charging them again here would halve every caller's real allowance.
func TestGraphQLOperationRateLimitSkipsHTTPOperations(t *testing.T) {
	rl := newTestRateLimiter(t)
	defer rl.Stop()
	mw := GraphQLOperationRateLimit(rl, func(string) (int, int) { return 0, 0 })

	limit, burst := publicRateLimits()
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := wsOperationContext(t, "203.0.113.9", "", "resolveIngestEndpoint")
	ctx = context.WithValue(ctx, ctxkeys.KeyGinContext, ginCtx)

	for i := 0; i < limit+burst+10; i++ {
		if !runOp(mw, ctx) {
			t.Fatal("an HTTP-borne operation was throttled here as well as by the HTTP middleware")
		}
	}
}

// Introspection executes through the same GraphQL engine as any other query;
// exempting it would leave an unbounded CPU path on a long-lived socket.
func TestGraphQLOperationRateLimitThrottlesIntrospection(t *testing.T) {
	rl := newTestRateLimiter(t)
	defer rl.Stop()
	mw := GraphQLOperationRateLimit(rl, func(string) (int, int) { return 0, 0 })

	ctx := wsOperationContext(t, "203.0.113.9", "", "__schema")
	limit, burst := publicRateLimits()
	allowed := 0
	for i := 0; i < limit+burst+10; i++ {
		if runOp(mw, ctx) {
			allowed++
		}
	}
	if allowed > limit+burst {
		t.Fatalf("introspection bypassed the operation limiter: %d allowed, limit is %d+%d", allowed, limit, burst)
	}
}

// Public buckets must not be resolved through tenant lookup — that both fails
// open and costs a Quartermaster validation RPC per operation.
func TestRateLimitsForBucketUsesPublicLimitsForAnonymous(t *testing.T) {
	tenantCalls := 0
	tenantLimits := func(string) (int, int) { tenantCalls++; return 0, 0 }

	limit, burst := RateLimitsForBucket("public:203.0.113.9", tenantLimits)
	wantLimit, wantBurst := publicRateLimits()
	if limit != wantLimit || burst != wantBurst {
		t.Fatalf("public bucket limits = (%d,%d), want (%d,%d)", limit, burst, wantLimit, wantBurst)
	}
	if tenantCalls != 0 {
		t.Errorf("public bucket consulted tenant limits %d times", tenantCalls)
	}

	if limit, burst := RateLimitsForBucket("tenant-abc", func(string) (int, int) { return 42, 7 }); limit != 42 || burst != 7 {
		t.Fatalf("tenant bucket limits = (%d,%d), want (42,7)", limit, burst)
	}
}
