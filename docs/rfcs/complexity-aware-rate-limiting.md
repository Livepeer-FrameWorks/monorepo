# RFC: Complexity-Aware Rate Limiting

**Status:** Proposed
**Author:** @stronk
**Created:** 2026-02-02

## Summary

Modify the GraphQL API rate limiter to deduct tokens based on query complexity instead of flat 1-token-per-request. This aligns with Shopify's cost-based rate limiting model.

## Motivation

### Current Behavior

The rate limiter in `api_gateway/internal/middleware/ratelimit.go` uses a token bucket algorithm that charges exactly 1 token per request:

```go
bucket.tokens -= 1.0  // Line 157
```

This means a simple query like `{ tenant { id } }` costs the same as a complex analytics query fetching 1000+ rows.

### Problems

1. **Resource asymmetry**: Expensive queries consume more server resources but don't cost more rate limit tokens
2. **No optimization incentive**: Clients have no reason to reduce query complexity
3. **Unfair throttling**: A tenant making 100 simple queries gets throttled the same as one making 100 expensive queries

### Industry Reference

Shopify pioneered cost-based GraphQL rate limiting:

- Each query has a "cost" based on complexity
- Costs are deducted from a point bucket
- Complex queries drain the bucket faster
- Reference: [Shopify Engineering Blog](https://shopify.engineering/rate-limiting-graphql-apis-calculating-query-complexity)

## Proposal

### Architecture Change

```
Current:
┌─────────────────┐     ┌─────────────────┐
│ Rate Limiter    │ ──► │ GraphQL Handler │
│ (deduct 1)      │     │ (calc complexity)│
└─────────────────┘     └─────────────────┘

Proposed:
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│ Rate Limiter    │ ──► │ GraphQL Handler │ ──► │ Post-Exec Hook  │
│ (gate: bucket>0)│     │ (calc complexity)│     │ (deduct cost)   │
└─────────────────┘     └─────────────────┘     └─────────────────┘
```

### How It Works

1. **Pre-execution gate**: Check if bucket > 0. If not, reject with 429.
2. **Query executes**: gqlgen calculates complexity during validation
3. **Post-execution deduction**: Deduct `complexity / COST_DIVISOR` from bucket
4. **Bucket can go negative**: First expensive query succeeds, future requests blocked until recovery

### Configuration

New environment variable:

```bash
# Complexity points per rate limit token
# Higher = more lenient (more complex queries allowed)
# Default: 10 (complexity 100 = 10 tokens)
GRAPHQL_COMPLEXITY_COST_DIVISOR=10
```

### Impact Analysis

With `COST_DIVISOR=10` and rate limit of 1000 tokens/min:

| Query Type               | Complexity | Token Cost | Max Queries/Min |
| ------------------------ | ---------- | ---------- | --------------- |
| Simple (`tenant { id }`) | ~10        | 1          | 1000            |
| List streams             | ~50        | 5          | 200             |
| Dashboard overview       | ~254       | 25         | 40              |
| Analytics export         | ~1347      | 135        | 7               |

## Implementation

### Files to Modify

1. **`api_gateway/internal/middleware/ratelimit.go`**
   - Add `DeductCost(tenantID string, cost float64)` method
   - Modify `Allow()` to only check bucket > 0, not deduct

2. **`api_gateway/cmd/bridge/main.go`**
   - In `AroundResponses` hook, call `DeductCost()` with complexity
   - Add `GRAPHQL_COMPLEXITY_COST_DIVISOR` config loading

3. **`config/env/base.env`**
   - Add `GRAPHQL_COMPLEXITY_COST_DIVISOR=10`

### Code Changes

#### ratelimit.go - New Method

```go
// DeductCost removes tokens from a tenant's bucket based on query cost.
// Called after query execution with actual complexity.
// Bucket may go negative; future requests blocked until recovery.
func (rl *RateLimiter) DeductCost(tenantID string, cost float64) {
    bucket := rl.getOrCreateBucket(tenantID)
    bucket.mu.Lock()
    defer bucket.mu.Unlock()
    bucket.tokens -= cost
    // Negative is OK - blocks future requests until refill
}
```

#### main.go - AroundResponses Hook

```go
gqlHandler.AroundResponses(func(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
    resp := next(ctx)
    if resp != nil {
        if ginCtx, ok := ctx.Value("GinContext").(*gin.Context); ok && ginCtx != nil {
            if stats := extension.GetComplexityStats(ctx); stats != nil {
                ginCtx.Set("graphql_complexity", stats.Complexity)

                // Deduct from rate limit based on complexity
                if tenantID, exists := ginCtx.Get("tenant_id"); exists && rateLimiter != nil {
                    cost := float64(stats.Complexity) / float64(complexityCostDivisor)
                    rateLimiter.DeductCost(tenantID.(string), cost)
                }
            }
        }
    }
    return resp
})
```

## Rollout Plan

### Phase 1: Shadow Mode

- Calculate and log what cost WOULD be deducted
- No actual rate limiting change
- Monitor P99 costs, identify outliers

### Phase 2: Warn Mode

- Deduct costs but only log warnings when bucket goes negative
- Don't actually reject requests
- Give clients time to optimize

### Phase 3: Enforce

- Full enforcement with configurable divisor
- Start with lenient divisor (e.g., 20), tune down

## Alternatives Considered

### 1. Pre-execution Complexity Check

Parse query and calculate complexity before execution. Rejected because:

- Duplicates gqlgen's parsing work
- Complexity calculation requires schema context (resolvers)
- More code to maintain

### 2. Per-Query Type Limits

Define fixed costs per operation name. Rejected because:

- Doesn't account for pagination parameters
- Requires manual mapping maintenance
- Less accurate than actual complexity

### 3. No Change (Status Quo)

Keep 1-token-per-request. Rejected because:

- Doesn't solve the resource asymmetry problem
- Industry is moving toward cost-based models

## Open Questions

1. **Divisor value**: What's the right default? 10 seems reasonable but needs tuning.
2. **Non-GraphQL endpoints**: Should REST endpoints also have variable costs?
3. **Subscription handling**: Subscriptions already have separate tracking - integrate?

## References

- [Shopify: Rate Limiting GraphQL APIs](https://shopify.engineering/rate-limiting-graphql-apis-calculating-query-complexity)
- Current complexity calculation: `api_gateway/graph/complexity.go`
- Current rate limiter: `api_gateway/internal/middleware/ratelimit.go`
