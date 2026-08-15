package middleware

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gin-gonic/gin"
)

// RateLimitsForBucket resolves the limits for a bucket key produced by
// RateLimitBucketKey.
//
// Anonymous callers are bucketed as "public:<ip>", which is not a tenant:
// asking Quartermaster to validate it returns (0, 0), and RateLimiter.Allow
// treats a nonpositive limit as "misconfigured, allow" — so routing an
// anonymous caller through tenant lookup silently disables the limit it was
// meant to enforce, and costs a validation RPC per operation. Public buckets
// therefore use the fixed public limits, exactly as the HTTP path does.
func RateLimitsForBucket(bucket string, tenantLimits func(string) (int, int)) (limit, burst int) {
	if isPublicTenant(bucket) {
		return publicRateLimits()
	}
	if tenantLimits == nil {
		return 0, 0
	}
	return tenantLimits(bucket)
}

// GraphQLOperationRateLimit throttles GraphQL operations that did not pass
// through the HTTP rate-limit middleware.
//
// The HTTP middleware cannot cover WebSocket traffic: a WS connection
// authenticates later, in the transport's InitFunc, so limiting at upgrade time
// would bucket every caller as anonymous. But GraphQL over WS executes queries
// — including the public allowlisted ones — so without a check here an
// anonymous socket can call an uncached resolver without limit.
//
// Operations carrying a gin context arrived over HTTP and were already charged.
func GraphQLOperationRateLimit(rl *RateLimiter, tenantLimits func(string) (int, int)) graphql.OperationMiddleware {
	return func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
			return next(ctx)
		}
		if rl == nil || !graphql.HasOperationContext(ctx) {
			return next(ctx)
		}
		bucket := RateLimitBucketKey(ctxkeys.GetTenantID(ctx), ctxkeys.GetClientIP(ctx))
		limit, burst := RateLimitsForBucket(bucket, tenantLimits)
		if allowed, _, resetSeconds := rl.Allow(bucket, limit, burst); !allowed {
			return func(ctx context.Context) *graphql.Response {
				return graphql.ErrorResponse(ctx, "rate limit exceeded, retry in %ds", resetSeconds)
			}
		}
		return next(ctx)
	}
}
