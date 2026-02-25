package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/globalid"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	x402 "frameworks/pkg/x402"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RateLimitConfig configures the rate limiter
type RateLimitConfig struct {
	// Logger for rate limit events
	Logger logging.Logger
	// CleanupInterval is how often to clean up expired entries (default: 1 minute)
	CleanupInterval time.Duration
}

// RateLimiter implements a sliding window rate limiter
type RateLimiter struct {
	config  RateLimitConfig
	buckets sync.Map // map[tenantID]*tokenBucket
	stopCh  chan struct{}
}

// tokenBucket tracks request counts for a tenant
type tokenBucket struct {
	mu          sync.Mutex
	tokens      float64   // Current available tokens
	lastUpdate  time.Time // Last time tokens were updated
	limit       int       // Requests per minute
	burst       int       // Burst allowance
	lastRequest time.Time // For cleanup
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(config RateLimitConfig) *RateLimiter {
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = time.Minute
	}

	rl := &RateLimiter{
		config: config,
		stopCh: make(chan struct{}),
	}

	// Start cleanup goroutine
	go rl.cleanupLoop()

	return rl
}

// Stop stops the rate limiter cleanup goroutine
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// cleanupLoop periodically removes stale buckets
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.cleanup()
		case <-rl.stopCh:
			return
		}
	}
}

// cleanup removes buckets that haven't been used in 5 minutes
func (rl *RateLimiter) cleanup() {
	threshold := time.Now().Add(-5 * time.Minute)
	rl.buckets.Range(func(key, value interface{}) bool {
		bucket := value.(*tokenBucket) //nolint:errcheck // type guaranteed by sync.Map usage
		bucket.mu.Lock()
		if bucket.lastRequest.Before(threshold) {
			bucket.mu.Unlock()
			rl.buckets.Delete(key)
		} else {
			bucket.mu.Unlock()
		}
		return true
	})
}

// Allow checks if a request is allowed for the given tenant
// Returns: allowed, remaining tokens, reset time (seconds until bucket refills)
// IMPORTANT: limit and burst must be provided by the caller (from Quartermaster)
func (rl *RateLimiter) Allow(tenantID string, limit, burst int) (allowed bool, remaining int, resetSeconds int) {
	// Limits must come from Quartermaster - no fallbacks here
	if limit <= 0 || burst <= 0 {
		// This should never happen if properly configured
		// Log and allow the request rather than block incorrectly
		if rl.config.Logger != nil {
			rl.config.Logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"limit":     limit,
				"burst":     burst,
			}).Error("Rate limit called with invalid limits - check Quartermaster integration")
		}
		return true, 0, 0
	}

	// Get or create bucket for tenant
	bucketI, _ := rl.buckets.LoadOrStore(tenantID, &tokenBucket{
		tokens:      float64(limit + burst), // Start with full bucket
		lastUpdate:  time.Now(),
		limit:       limit,
		burst:       burst,
		lastRequest: time.Now(),
	})
	bucket := bucketI.(*tokenBucket) //nolint:errcheck // type guaranteed by sync.Map usage

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	now := time.Now()
	bucket.lastRequest = now

	// Update limit/burst if changed
	if bucket.limit != limit || bucket.burst != burst {
		bucket.limit = limit
		bucket.burst = burst
	}

	// Calculate tokens to add since last update (token bucket algorithm)
	// Rate: limit tokens per minute = limit/60 tokens per second
	elapsed := now.Sub(bucket.lastUpdate).Seconds()
	tokensToAdd := elapsed * float64(limit) / 60.0
	bucket.tokens += tokensToAdd
	bucket.lastUpdate = now

	// Cap tokens at limit + burst
	maxTokens := float64(limit + burst)
	if bucket.tokens > maxTokens {
		bucket.tokens = maxTokens
	}

	// Check if we have tokens available
	if bucket.tokens >= 1.0 {
		bucket.tokens -= 1.0
		remaining = int(bucket.tokens)
		// Calculate reset time (time until bucket is full again)
		tokensNeeded := maxTokens - bucket.tokens
		resetSeconds = int(tokensNeeded * 60.0 / float64(limit))
		return true, remaining, resetSeconds
	}

	// Rate limited - calculate when tokens will be available
	tokensNeeded := 1.0 - bucket.tokens
	secondsUntilToken := tokensNeeded * 60.0 / float64(limit)
	resetSeconds = int(secondsUntilToken) + 1

	return false, 0, resetSeconds
}

// BillingChecker provides billing status checks for the rate limit middleware
type BillingChecker interface {
	IsBalanceNegative(tenantID string) bool
	IsSuspended(tenantID string) bool
	GetBillingModel(tenantID string) string
}

// X402Provider provides x402 payment requirements for 402 responses
type X402Provider interface {
	GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*pb.PaymentRequirements, error)
}

// X402Settler handles x402 payment settlement
type X402Settler interface {
	VerifyX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.VerifyX402PaymentResponse, error)
	SettleX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.SettleX402PaymentResponse, error)
}

type AccessRequest struct {
	TenantID          string
	ClientIP          string
	Path              string
	OperationName     string
	XPayment          string
	PublicAllowlisted bool
	X402Processed     bool
	X402AuthOnly      bool
}

type AccessDecision struct {
	Allowed bool
	Status  int
	Headers map[string]string
	Body    map[string]any
}

// Operations that are ALWAYS allowed even with negative prepaid balance.
// These are essential for users to check their status and top up.
// Everything else returns 402 for prepaid accounts with balance <= 0.
var prepaidAllowlist = map[string]bool{
	// Balance & billing (must be able to check and top up)
	"prepaidBalance":                true,
	"balanceTransactionsConnection": true,
	"createCardTopup":               true,
	"createCryptoTopup":             true,
	"billingDetails":                true,
	"updateBillingDetails":          true,
	"billingStatus":                 true,
	"invoicesConnection":            true,
	"billingTiers":                  true,

	// MCP billing/account tools/resources
	"mcp:tools/call:update_billing_details":     true,
	"mcp:tools/call:topup_balance":              true,
	"mcp:tools/call:check_topup":                true,
	"mcp:resources/read:account://status":       true,
	"mcp:resources/read:billing://balance":      true,
	"mcp:resources/read:billing://pricing":      true,
	"mcp:resources/read:billing://transactions": true,

	// Account essentials (must be able to see/manage account)
	"me":            true,
	"logout":        true,
	"tenant":        true,
	"linkEmail":     true,
	"promoteToPaid": true,

	// Introspection (for tooling)
	"__schema": true,
	"__type":   true,

	// MCP core discovery
	"mcp:initialize":               true,
	"mcp:tools/list":               true,
	"mcp:resources/list":           true,
	"mcp:resources/templates/list": true,
	"mcp:prompts/list":             true,
	"mcp:prompts/get":              true,
}

// RateLimitMiddlewareWithX402 creates a Gin middleware with full x402 support
// Includes both billing checks, x402 payment requirements in 402 responses,
// AND X-PAYMENT header handling for settling x402 payments
func RateLimitMiddlewareWithX402(rl *RateLimiter, getLimits func(tenantID string) (limit, burst int), billingChecker BillingChecker, x402Provider X402Provider, x402Settler X402Settler, x402Resolver x402.CommodoreClient) gin.HandlerFunc {
	return rateLimitMiddlewareInternal(rl, getLimits, billingChecker, x402Provider, x402Settler, x402Resolver)
}

// graphqlRequest represents a minimal GraphQL request for operation extraction
type graphqlRequest struct {
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
}

// rateLimitMiddlewareInternal is the internal implementation with optional x402 support
func rateLimitMiddlewareInternal(rl *RateLimiter, getLimits func(tenantID string) (limit, burst int), billingChecker BillingChecker, x402Provider X402Provider, x402Settler X402Settler, x402Resolver x402.CommodoreClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, _ := c.Get(string(ctxkeys.KeyTenantID))
		tenantIDStr, _ := tenantID.(string)
		publicAllowlisted := false
		x402Processed := false
		x402AuthOnly := false
		if v, ok := c.Get(string(ctxkeys.KeyPublicAllowlisted)); ok {
			if allowed, ok := v.(bool); ok {
				publicAllowlisted = allowed
			}
		}
		if v, ok := c.Get(string(ctxkeys.KeyX402Processed)); ok {
			if processed, ok := v.(bool); ok {
				x402Processed = processed
			}
		}
		if v, ok := c.Get(string(ctxkeys.KeyX402AuthOnly)); ok {
			if authOnly, ok := v.(bool); ok {
				x402AuthOnly = authOnly
			}
		}
		opName, variables := extractGraphQLRequest(c)
		resourcePath := c.Request.URL.Path
		if opName != "" && strings.Contains(strings.ToLower(resourcePath), "graphql") {
			if resource := graphqlResourcePath(opName, variables); resource != "" {
				resourcePath = resource
			} else {
				resourcePath = "graphql://" + opName
			}
		}

		decision := EvaluateAccess(c.Request.Context(), AccessRequest{
			TenantID:          tenantIDStr,
			ClientIP:          c.ClientIP(),
			Path:              resourcePath,
			OperationName:     opName,
			XPayment:          GetX402PaymentHeader(c.Request),
			PublicAllowlisted: publicAllowlisted,
			X402Processed:     x402Processed,
			X402AuthOnly:      x402AuthOnly,
		}, rl, getLimits, billingChecker, x402Provider, x402Settler, x402Resolver, rl.config.Logger)

		for key, value := range decision.Headers {
			c.Header(key, value)
		}

		if !decision.Allowed {
			c.AbortWithStatusJSON(decision.Status, decision.Body)
			return
		}

		c.Next()
	}
}

func EvaluateAccess(ctx context.Context, req AccessRequest, rl *RateLimiter, getLimits func(tenantID string) (limit, burst int), billingChecker BillingChecker, x402Provider X402Provider, x402Settler X402Settler, x402Resolver x402.CommodoreClient, logger logging.Logger) AccessDecision {
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = "public:" + req.ClientIP
	}
	if req.ClientIP == "" {
		req.ClientIP = "unknown"
	}

	headers := map[string]string{}
	tenantIDStr := tenantID
	isPublic := isPublicTenant(tenantIDStr)
	x402Paid := req.X402Processed && !req.X402AuthOnly

	if req.XPayment != "" && x402Settler != nil && !req.X402Processed {
		settleResult, settleErr := x402.SettleX402Payment(ctx, x402.SettlementOptions{
			PaymentHeader:          req.XPayment,
			Resource:               req.Path,
			AuthTenantID:           tenantIDStr,
			ClientIP:               req.ClientIP,
			Purser:                 x402Settler,
			Commodore:              x402Resolver,
			AllowUnresolvedCreator: true,
			Logger:                 logger,
		})
		if settleErr != nil {
			if settleErr.Code == x402.ErrBillingDetailsRequired {
				return AccessDecision{
					Allowed: false,
					Status:  http.StatusPaymentRequired,
					Body: map[string]any{
						"error":           "billing_details_required",
						"message":         settleErr.Message,
						"code":            "BILLING_DETAILS_REQUIRED",
						"topup_url":       "/account/billing",
						"required_fields": []string{"email", "street", "city", "postal_code", "country"},
					},
					Headers: headers,
				}
			}
			message := settleErr.Message
			if message == "" {
				message = "payment failed"
			}
			return AccessDecision{
				Allowed: false,
				Status:  http.StatusPaymentRequired,
				Body: map[string]any{
					"error":     "payment_failed",
					"message":   message,
					"code":      "X402_PAYMENT_FAILED",
					"topup_url": "/account/billing",
				},
				Headers: headers,
			}
		}
		if settleResult != nil && settleResult.Settle != nil && settleResult.Settle.Success {
			x402Paid = true
		}
	}

	if isPublic && !req.PublicAllowlisted && !x402Paid {
		response := build402Response(ctx, "", req.OperationName, req.Path, x402Provider, logger)
		return AccessDecision{
			Allowed: false,
			Status:  http.StatusPaymentRequired,
			Body:    response,
			Headers: headers,
		}
	}

	if billingChecker != nil && !isPublic {
		billingModel := billingChecker.GetBillingModel(tenantIDStr)
		if billingModel == "prepaid" && billingChecker.IsBalanceNegative(tenantIDStr) {
			if !prepaidAllowlist[req.OperationName] && !x402Paid {
				if logger != nil {
					logger.WithFields(logging.Fields{
						"tenant_id":     tenantIDStr,
						"billing_model": billingModel,
						"operation":     req.OperationName,
						"path":          req.Path,
					}).Warn("Insufficient balance (402 Payment Required)")
				}

				response := build402Response(ctx, tenantIDStr, req.OperationName, req.Path, x402Provider, logger)
				return AccessDecision{
					Allowed: false,
					Status:  http.StatusPaymentRequired,
					Body:    response,
					Headers: headers,
				}
			}
		}
	}

	limit, burst := 0, 0
	if getLimits != nil && !isPublic {
		limit, burst = getLimits(tenantIDStr)
	}

	if isPublic {
		return AccessDecision{Allowed: true, Headers: headers}
	}

	allowed, remaining, resetSeconds := rl.Allow(tenantIDStr, limit, burst)
	headers["X-RateLimit-Limit"] = strconv.Itoa(limit)
	headers["X-RateLimit-Remaining"] = strconv.Itoa(remaining)
	headers["X-RateLimit-Reset"] = strconv.Itoa(resetSeconds)

	if !allowed {
		if logger != nil {
			logger.WithFields(logging.Fields{
				"tenant_id":     tenantIDStr,
				"limit":         limit,
				"reset_seconds": resetSeconds,
				"path":          req.Path,
			}).Warn("Rate limit exceeded")
		}
		headers["Retry-After"] = strconv.Itoa(resetSeconds)
		docsURL := strings.TrimSpace(os.Getenv("DOCS_PUBLIC_URL"))
		response := map[string]any{
			"error":       "rate_limit_exceeded",
			"message":     "Too many requests. Please retry after the specified time.",
			"limit":       limit,
			"retry_after": resetSeconds,
		}
		if docsURL != "" {
			response["documentation"] = docsURL + "/api/rate-limits"
		}
		return AccessDecision{
			Allowed: false,
			Status:  http.StatusTooManyRequests,
			Body:    response,
			Headers: headers,
		}
	}

	return AccessDecision{
		Allowed: true,
		Headers: headers,
	}
}

// build402Response builds the 402 Payment Required response
// Includes both human flow (topup_url) and x402 machine flow (accepts block)
func build402Response(ctx context.Context, tenantID, operationName, resourcePath string, x402Provider X402Provider, logger logging.Logger) map[string]any {
	response := map[string]any{
		"error":     "insufficient_balance",
		"message":   "Insufficient balance - please top up to continue",
		"code":      "INSUFFICIENT_BALANCE",
		"operation": operationName,
		"topup_url": "/account/billing",
	}

	// Include x402 payment requirements if provider is available
	if x402Provider != nil {
		reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		requirements, err := x402Provider.GetPaymentRequirements(reqCtx, tenantID, resourcePath)
		if err != nil {
			if logger != nil {
				logger.WithFields(logging.Fields{
					"tenant_id": tenantID,
					"error":     err,
				}).Warn("Failed to get x402 payment requirements")
			}
			// Continue without x402 - human flow still works
		} else if requirements != nil {
			response["x402Version"] = requirements.X402Version

			// Convert accepts to map slice
			accepts := make([]map[string]any, 0, len(requirements.Accepts))
			for _, req := range requirements.Accepts {
				accepts = append(accepts, map[string]any{
					"scheme":            req.Scheme,
					"network":           req.Network,
					"maxAmountRequired": req.MaxAmountRequired,
					"payTo":             req.PayTo,
					"asset":             req.Asset,
					"maxTimeoutSeconds": req.MaxTimeoutSeconds,
					"resource":          req.Resource,
					"description":       req.Description,
				})
			}
			response["accepts"] = accepts
		}
	}

	return response
}

// handleX402Payment parses and settles an x402 payment from the X-PAYMENT header
// The header contains a base64-encoded JSON payload per the x402 spec
// extractGraphQLRequest reads the operationName + variables from a GraphQL request body.
// Returns empty values if not found or on error.
func extractGraphQLRequest(c *gin.Context) (string, map[string]interface{}) {
	// Only process POST requests with JSON body
	if c.Request.Method != "POST" || c.Request.Body == nil {
		return "", nil
	}

	// Read body (we need to restore it after reading)
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "", nil
	}
	// Restore the body for downstream handlers
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var req graphqlRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return "", nil
	}

	return req.OperationName, req.Variables
}

func graphqlResourcePath(operation string, variables map[string]interface{}) string {
	if operation == "" {
		return ""
	}

	switch strings.ToLower(operation) {
	case "resolveviewerendpoint":
		if contentID := getGraphQLString(variables, "contentId", "contentID", "content_id"); contentID != "" {
			return "viewer://" + contentID
		}
	case "updatestream", "deletestream", "refreshstreamkey":
		if streamID := getGraphQLString(variables, "id", "streamId", "streamID", "stream_id"); streamID != "" {
			return "stream://" + streamID
		}
	case "createclip":
		if streamID := getGraphQLNestedString(variables, "input", "streamId", "streamID", "stream_id"); streamID != "" {
			return "stream://" + streamID
		}
	case "deleteclip":
		if clipID := getGraphQLString(variables, "id"); clipID != "" {
			if clipHash := graphqlClipResourceID(clipID); clipHash != "" {
				return "clip://" + clipHash
			}
		}
	case "startdvr":
		if streamID := getGraphQLString(variables, "streamId", "streamID", "stream_id", "id"); streamID != "" {
			return "stream://" + streamID
		}
	case "stopdvr", "deletedvr":
		if dvrHash := getGraphQLString(variables, "dvrHash", "dvr_hash"); dvrHash != "" {
			return "dvr://" + dvrHash
		}
	case "deletevodasset":
		if vodID := getGraphQLString(variables, "id", "vodId", "vodID", "vod_id"); vodID != "" {
			return "vod://" + vodID
		}
	}

	return ""
}

func getGraphQLString(variables map[string]interface{}, keys ...string) string {
	if variables == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := variables[key]; ok {
			if str, ok := value.(string); ok && str != "" {
				return str
			}
		}
	}
	return ""
}

func getGraphQLNestedString(variables map[string]interface{}, parent string, keys ...string) string {
	if variables == nil {
		return ""
	}
	raw, ok := variables[parent]
	if !ok {
		return ""
	}
	nested, ok := raw.(map[string]interface{})
	if !ok {
		return ""
	}
	return getGraphQLString(nested, keys...)
}

func graphqlClipResourceID(input string) string {
	if input == "" {
		return ""
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeClip {
			return ""
		}
		if _, err := uuid.Parse(id); err == nil {
			return ""
		}
		return id
	}
	return input
}

// isPublicTenant returns true if the tenant ID represents a public/unauthenticated request
func isPublicTenant(tenantID string) bool {
	return len(tenantID) > 7 && tenantID[:7] == "public:"
}

// =============================================================================
// TENANT RATE LIMIT CACHE
// =============================================================================
// Caches tenant rate limits fetched from Quartermaster to avoid per-request gRPC calls.
// Single source of truth: Quartermaster (which reads from quartermaster.tenants table)

// TenantValidator is the interface for validating tenants and fetching rate limits
type TenantValidator interface {
	ValidateTenant(ctx context.Context, tenantID, userID string) (*pb.ValidateTenantResponse, error)
}

// TenantRateLimits holds cached rate limit and billing info for a tenant
type TenantRateLimits struct {
	Limit             int
	Burst             int
	BillingModel      string // "postpaid" or "prepaid"
	IsSuspended       bool   // true if tenant suspended (balance < -$10)
	IsBalanceNegative bool   // true if balance <= 0 (should return 402)
	FetchedAt         time.Time
}

// TenantCache caches tenant rate limits from Quartermaster
type TenantCache struct {
	client           TenantValidator
	logger           logging.Logger
	cache            sync.Map // map[tenantID]*TenantRateLimits
	cacheTTLPostpaid time.Duration
	cacheTTLPrepaid  time.Duration
}

// NewTenantCache creates a new tenant cache
func NewTenantCache(client TenantValidator, logger logging.Logger) *TenantCache {
	return &TenantCache{
		client:           client,
		logger:           logger,
		cacheTTLPostpaid: 5 * time.Minute, // Postpaid tenants: 5 minute cache
		cacheTTLPrepaid:  1 * time.Minute, // Prepaid tenants: 1 minute cache (faster enforcement)
	}
}

// GetLimits returns the rate limits for a tenant, fetching from Quartermaster if not cached
func (tc *TenantCache) GetLimits(tenantID string) (limit, burst int) {
	info := tc.getTenantInfo(tenantID)
	if info == nil {
		return 0, 0
	}
	return info.Limit, info.Burst
}

// getTenantInfo returns cached tenant info, fetching from Quartermaster if stale/missing
func (tc *TenantCache) getTenantInfo(tenantID string) *TenantRateLimits {
	// Check cache first
	if cached, ok := tc.cache.Load(tenantID); ok {
		limits := cached.(*TenantRateLimits) //nolint:errcheck // type guaranteed by sync.Map usage
		// Use shorter TTL for prepaid tenants (faster enforcement)
		ttl := tc.cacheTTLPostpaid
		if limits.BillingModel == "prepaid" {
			ttl = tc.cacheTTLPrepaid
		}
		if time.Since(limits.FetchedAt) < ttl {
			return limits
		}
	}

	// Fetch from Quartermaster
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := tc.client.ValidateTenant(ctx, tenantID, "")
	if err != nil {
		if tc.logger != nil {
			tc.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"error":     err,
			}).Warn("Failed to fetch tenant info from Quartermaster")
		}
		return nil
	}

	if resp == nil || !resp.Valid {
		return nil
	}

	// Cache the result with billing info
	limits := &TenantRateLimits{
		Limit:             int(resp.RateLimitPerMinute),
		Burst:             int(resp.RateLimitBurst),
		BillingModel:      resp.BillingModel,
		IsSuspended:       resp.IsSuspended,
		IsBalanceNegative: resp.IsBalanceNegative,
		FetchedAt:         time.Now(),
	}
	tc.cache.Store(tenantID, limits)

	return limits
}

// IsTenantSuspended checks if a tenant is suspended (balance < -$10)
func (tc *TenantCache) IsTenantSuspended(tenantID string) bool {
	info := tc.getTenantInfo(tenantID)
	if info == nil {
		return false // Fail open - don't block on lookup failure
	}
	return info.IsSuspended
}

// IsSuspended implements BillingChecker interface
func (tc *TenantCache) IsSuspended(tenantID string) bool {
	return tc.IsTenantSuspended(tenantID)
}

// IsBalanceNegative checks if a tenant's balance is <= 0 (should return 402)
func (tc *TenantCache) IsBalanceNegative(tenantID string) bool {
	info := tc.getTenantInfo(tenantID)
	if info == nil {
		return false // Fail open - don't block on lookup failure
	}
	return info.IsBalanceNegative
}

// GetBillingModel returns the billing model for a tenant ("postpaid" or "prepaid")
func (tc *TenantCache) GetBillingModel(tenantID string) string {
	info := tc.getTenantInfo(tenantID)
	if info == nil {
		return "postpaid" // Default to postpaid
	}
	return info.BillingModel
}

// GetLimitsFunc returns a function suitable for use with RateLimitMiddleware
func (tc *TenantCache) GetLimitsFunc() func(tenantID string) (limit, burst int) {
	return tc.GetLimits
}
