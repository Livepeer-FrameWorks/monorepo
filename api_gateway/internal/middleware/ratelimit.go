//nolint:errcheck // Response replay writes to Gin's already-owned writer; transport errors terminate the request.
package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/accesspolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"
	x402 "github.com/Livepeer-FrameWorks/monorepo/pkg/x402"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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

// BillingAccessStatus is the one coherent billing snapshot used for an access
// decision. Fetching its fields independently can mix cache generations and,
// on lookup failure, used to turn an unknown tenant into implicit postpaid.
type BillingAccessStatus struct {
	BillingModel       string
	TierName           string
	CollectionReady    bool
	CollectionProvider string
	IsBalanceNegative  bool
	IsSuspended        bool
}

// BillingChecker provides billing status checks for the rate limit middleware.
type BillingChecker interface {
	GetBillingAccessStatus(tenantID string) (BillingAccessStatus, error)
}

// X402Provider provides x402 payment requirements for 402 responses
type X402Provider interface {
	GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error)
}

// X402Settler handles x402 payment settlement
type X402Settler interface {
	VerifyX402Payment(ctx context.Context, tenantID string, payment *x402pb.X402PaymentPayload, clientIP string) (*purserpb.VerifyX402PaymentResponse, error)
	SettleX402Payment(ctx context.Context, tenantID string, payment *x402pb.X402PaymentPayload, clientIP string) (*purserpb.SettleX402PaymentResponse, error)
	GetTenantAdmissionStatus(ctx context.Context, tenantID string) (*purserpb.GetTenantAdmissionStatusResponse, error)
	ClaimX402MutationResult(ctx context.Context, req *purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error)
	CompleteX402MutationResult(ctx context.Context, req *purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error)
}

type AccessRequest struct {
	TenantID          string
	ClientIP          string
	Path              string
	OperationName     string
	OperationNames    []string
	OperationType     string
	XPayment          string
	PublicAllowlisted bool

	// RateLimitTenantID, when non-nil, is the CALLER's own identity used solely for
	// the rate-limit bucket, decoupled from TenantID (which drives billing/owner
	// attribution). An empty string means an anonymous caller → per-IP public
	// bucket. Nil (the default) means "share TenantID for both". This exists so a
	// public caller resolving an owned resource (e.g. anonymous viewer playback)
	// is throttled on its own IP bucket and cannot exhaust the owner's tenant bucket.
	RateLimitTenantID *string
}

type AccessDecision struct {
	Allowed     bool
	Status      int
	Headers     map[string]string
	Body        map[string]any
	X402Settled bool
	X402QuoteID string
}

// Suspended tenants get only the account and payment-recovery surface. This is
// deliberately narrower than zero-balance prepaid access: suspension is an
// independent enforcement state and must not reopen control-plane mutation
// access merely because those mutations do not immediately incur usage.
var suspendedRecoveryAllowlist = map[string]bool{
	"prepaidBalance":                true,
	"balanceTransactionsConnection": true,
	"createCardTopup":               true,
	"createCryptoTopup":             true,
	"billingDetails":                true,
	"updateBillingDetails":          true,
	"billingStatus":                 true,
	"invoicesConnection":            true,
	"billingTiers":                  true,
	"mollieMandates":                true,
	"cryptoTopupStatus":             true,
	"createPayment":                 true,
	"createStripeCheckout":          true,
	"createStripeBillingPortal":     true,
	"createMollieFirstPayment":      true,
	"createMollieSubscription":      true,
	"submitX402Payment":             true,
	"changeBillingTier":             true,
	"me":                            true,
	"tenant":                        true,
	"logout":                        true,
	"linkEmail":                     true,
	"promoteToPaid":                 true,

	"mcp:initialize":                            true,
	"mcp:tools/list":                            true,
	"mcp:resources/list":                        true,
	"mcp:resources/templates/list":              true,
	"mcp:prompts/list":                          true,
	"mcp:prompts/get":                           true,
	"mcp:tools/call:update_billing_details":     true,
	"mcp:tools/call:topup_balance":              true,
	"mcp:tools/call:check_topup":                true,
	"mcp:tools/call:pay_invoice":                true,
	"mcp:tools/call:get_payment_options":        true,
	"mcp:tools/call:submit_payment":             true,
	"mcp:resources/read:account://status":       true,
	"mcp:resources/read:billing://balance":      true,
	"mcp:resources/read:billing://pricing":      true,
	"mcp:resources/read:billing://transactions": true,
	"mcp:resources/read:billing://invoices":     true,
	"mcp:resources/read:billing://payments":     true,
	"mcp:resources/read:billing://documents":    true,
}

// RateLimitMiddlewareWithX402 combines billing enforcement, payment
// requirements, and x402 payment-signature settlement.
func RateLimitMiddlewareWithX402(rl *RateLimiter, getLimits func(tenantID string) (limit, burst int), billingChecker BillingChecker, x402Provider X402Provider, x402Settler X402Settler, x402Resolver x402.CommodoreClient, tp *TrustedProxies) gin.HandlerFunc {
	return rateLimitMiddlewareInternal(rl, getLimits, billingChecker, x402Provider, x402Settler, x402Resolver, tp)
}

// PublicOperationRateLimitMiddleware applies the shared per-IP public bucket to
// non-GraphQL endpoints that must remain reachable without authentication.
func PublicOperationRateLimitMiddleware(rl *RateLimiter, tp *TrustedProxies, operation string) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := ClientIPFromRequestWithTrust(c.Request, tp)
		var logger logging.Logger
		if rl != nil {
			logger = rl.config.Logger
		}
		decision := EvaluateAccess(c.Request.Context(), AccessRequest{
			ClientIP:          clientIP,
			Path:              c.Request.URL.Path,
			OperationName:     operation,
			PublicAllowlisted: true,
		}, rl, nil, nil, nil, nil, nil, logger)
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

// graphqlRequest represents a minimal GraphQL request for operation extraction
type graphqlRequest struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
}

type graphqlAccess struct {
	OperationName string
	OperationType string
	Fields        []string
	Variables     map[string]interface{}
}

// rateLimitMiddlewareInternal is the internal implementation with optional x402 support
func rateLimitMiddlewareInternal(rl *RateLimiter, getLimits func(tenantID string) (limit, burst int), billingChecker BillingChecker, x402Provider X402Provider, x402Settler X402Settler, x402Resolver x402.CommodoreClient, tp *TrustedProxies) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, _ := c.Get(string(ctxkeys.KeyTenantID))
		tenantIDStr, _ := tenantID.(string)
		publicAllowlisted := false
		if v, ok := c.Get(string(ctxkeys.KeyPublicAllowlisted)); ok {
			if allowed, ok := v.(bool); ok {
				publicAllowlisted = allowed
			}
		}
		access := extractGraphQLAccess(c)
		opName, variables := access.OperationName, access.Variables
		if len(access.Fields) == 1 {
			opName = access.Fields[0]
		}
		resourcePath := c.Request.URL.Path
		if opName != "" && strings.Contains(strings.ToLower(resourcePath), "graphql") {
			if resource := graphqlResourcePath(opName, variables); resource != "" {
				resourcePath = resource
			} else {
				resourcePath = "graphql://" + opName
			}
		}
		if GetX402PaymentHeader(c.Request) != "" && access.OperationType == "mutation" {
			strategy, registered := accesspolicy.GraphQLX402MutationStrategy(opName)
			if !registered || strategy != accesspolicy.X402OwnerIdempotency {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{
					"error": "x402_mutation_requires_topup", "code": "X402_MUTATION_DIRECT_EXECUTION_UNSUPPORTED",
					"message": "this mutation does not yet have owner-level idempotency; use x402 to top up, then retry without the payment header",
				})
				return
			}
		}

		// Resolved once and published on the context so downstream resolvers
		// score the same address this request is limited by. Reading gin's
		// ClientIP() later would reintroduce a spoofable second identity.
		clientIP := ClientIPFromRequestWithTrust(c.Request, tp)
		if clientIP != "" {
			c.Set(string(ctxkeys.KeyClientIP), clientIP)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkeys.KeyClientIP, clientIP))
		}

		decision := EvaluateAccess(c.Request.Context(), AccessRequest{
			TenantID:          tenantIDStr,
			ClientIP:          clientIP,
			Path:              resourcePath,
			OperationName:     opName,
			OperationNames:    access.Fields,
			OperationType:     access.OperationType,
			XPayment:          GetX402PaymentHeader(c.Request),
			PublicAllowlisted: publicAllowlisted,
		}, rl, getLimits, billingChecker, x402Provider, x402Settler, x402Resolver, rl.config.Logger)

		for key, value := range decision.Headers {
			c.Header(key, value)
		}

		if !decision.Allowed {
			c.AbortWithStatusJSON(decision.Status, decision.Body)
			return
		}

		// Settlement and operation execution are separate failure domains. For a
		// paid GraphQL mutation, claim a durable result slot before handing the
		// request to any resolver. A retry either replays that exact result or
		// stops at an in-progress/unknown claim; it never invokes the mutation a
		// second time.
		if decision.X402Settled && access.OperationType == "mutation" {
			if handled := handlePaidHTTPMutationIdempotency(c, x402Settler, tenantIDStr, decision.X402QuoteID, opName, rl.config.Logger); handled {
				return
			}
		}

		c.Next()
	}
}

func EvaluateAccess(ctx context.Context, req AccessRequest, rl *RateLimiter, getLimits func(tenantID string) (limit, burst int), billingChecker BillingChecker, x402Provider X402Provider, x402Settler X402Settler, x402Resolver x402.CommodoreClient, logger logging.Logger) AccessDecision {
	// Normalize the client IP BEFORE building any "public:<ip>" bucket key, so an
	// empty IP yields "public:unknown" (still classified public) rather than a bare
	// "public:" that isPublicTenant would reject — which would drop the caller out
	// of the public throttle.
	if req.ClientIP == "" {
		req.ClientIP = "unknown"
	}
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = "public:" + req.ClientIP
	}

	headers := map[string]string{}
	tenantIDStr := tenantID
	isPublic := isPublicTenant(tenantIDStr)
	x402Processed := false
	x402Settled := false
	x402QuoteID := ""

	// Rate-limit identity is separate from the billing/owner identity (tenantIDStr):
	// a caller can supply its own tenant (empty → per-IP public bucket) so an
	// anonymous request resolving an OWNED resource is bucketed on its own IP, not
	// the resource owner's tenant. Defaults to the billing identity when unset.
	rlTenant := tenantIDStr
	if req.RateLimitTenantID != nil {
		rlTenant = *req.RateLimitTenantID
		if rlTenant == "" {
			rlTenant = "public:" + req.ClientIP
		}
	}
	rlIsPublic := isPublicTenant(rlTenant)

	if req.XPayment != "" && x402Settler != nil {
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
			if settleErr.Code == x402.ErrSettlementPending {
				headers["Retry-After"] = "3"
				return AccessDecision{
					Allowed: false,
					Status:  http.StatusServiceUnavailable,
					Body: map[string]any{
						"error": "settlement_pending", "code": "SETTLEMENT_PENDING",
						"message": settleErr.Message, "transaction": settleErr.TxHash,
						"network": settleErr.Network, "retry_same_payment": true,
					},
					Headers: headers,
				}
			}
			if settleErr.Code == x402.ErrBillingDetailsRequired {
				return AccessDecision{
					Allowed: false,
					Status:  http.StatusPaymentRequired,
					Body: map[string]any{
						"error":           "billing_details_required",
						"message":         settleErr.Message,
						"code":            "BILLING_DETAILS_REQUIRED",
						"topup_url":       "/account/billing",
						"required_fields": []string{"name", "email", "street", "city", "postal_code", "country"},
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
					"code":      settleErr.MachineCode(),
					"topup_url": "/account/billing",
				},
				Headers: headers,
			}
		}
		if settleResult != nil && settleResult.Settle != nil && settleResult.Settle.Success {
			x402Processed = true
			x402Settled = true
			x402QuoteID = settleResult.QuoteID
			if settleResult.X402Version == 2 {
				if paymentResponse, encodeErr := x402.EncodePaymentResponseHeader(settleResult, settleResult.Network); encodeErr == nil {
					headers[x402.PaymentResponseHeader] = paymentResponse
				} else if logger != nil {
					logger.WithError(encodeErr).Warn("Failed to encode x402 payment response header")
				}
			}
		}
	}

	if isPublic && !req.PublicAllowlisted {
		response, paymentRequired := build402ResponseWithHeader(ctx, "", req.OperationName, req.Path, x402Provider, logger)
		if paymentRequired != "" {
			headers[x402.PaymentRequiredHeader] = paymentRequired
		}
		return AccessDecision{
			Allowed: false,
			Status:  http.StatusPaymentRequired,
			Body:    response,
			Headers: headers,
		}
	}

	if !isPublic {
		billingModel := ""
		tierName := ""
		collectionReady := false
		isBalanceNegative := false
		isSuspended := false
		unfundedAllowed, accessClassKnown := requestAllowedWithoutFunds(req)

		if x402Processed {
			if x402Settler == nil {
				return billingStatusUnavailableDecision(headers)
			}
			statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			freshStatus, err := x402Settler.GetTenantAdmissionStatus(statusCtx, tenantIDStr)
			cancel()
			if err != nil || freshStatus == nil {
				if logger != nil {
					logger.WithFields(logging.Fields{
						"tenant_id": tenantIDStr,
						"error":     err,
					}).Warn("Failed to recheck billing status after x402 settlement")
				}
				return billingStatusUnavailableDecision(headers)
			}
			billingModel = freshStatus.GetBillingModel()
			tierName = freshStatus.GetTierName()
			collectionReady = freshStatus.GetCollectionReady()
			isBalanceNegative = freshStatus.GetIsBalanceNegative()
			isSuspended = freshStatus.GetIsSuspended()
		} else if billingChecker != nil {
			status, err := billingChecker.GetBillingAccessStatus(tenantIDStr)
			if err != nil {
				if logger != nil {
					logger.WithFields(logging.Fields{
						"tenant_id": tenantIDStr,
						"operation": req.OperationName,
						"error":     err,
					}).Warn("Failed to resolve billing status")
				}
				// Billing dependency failure must never create implicit postpaid
				// access to rated work. Reads and safe control remain usable.
				if !accessClassKnown || !unfundedAllowed {
					return billingStatusUnavailableDecision(headers)
				}
			} else {
				billingModel = status.BillingModel
				tierName = status.TierName
				collectionReady = status.CollectionReady
				isBalanceNegative = status.IsBalanceNegative
				isSuspended = status.IsSuspended
			}
		}

		blockedBySuspension := isSuspended && !suspendedRequestAllowed(req)
		if billingModel == "prepaid" && isBalanceNegative && !accessClassKnown {
			return accessPolicyUnavailableDecision(headers, req)
		}
		blockedByBalance := billingModel == "prepaid" && isBalanceNegative && !unfundedAllowed
		if billingModel == "postpaid" && !unfundedAllowed {
			if tierName == "" {
				return billingStatusUnavailableDecision(headers)
			}
			if !strings.EqualFold(tierName, "free") && !collectionReady {
				return postpaidCollectionRequiredDecision(headers)
			}
		}
		if blockedBySuspension || blockedByBalance {
			if logger != nil {
				logger.WithFields(logging.Fields{
					"tenant_id":     tenantIDStr,
					"billing_model": billingModel,
					"is_suspended":  isSuspended,
					"operation":     req.OperationName,
					"path":          req.Path,
				}).Warn("Insufficient balance (402 Payment Required)")
			}

			response, paymentRequired := build402ResponseWithHeader(ctx, tenantIDStr, req.OperationName, req.Path, x402Provider, logger)
			if paymentRequired != "" {
				headers[x402.PaymentRequiredHeader] = paymentRequired
			}
			return AccessDecision{
				Allowed: false,
				Status:  http.StatusPaymentRequired,
				Body:    response,
				Headers: headers,
			}
		}
	}

	// Public (unauthenticated) callers are still rate-limited, keyed per client IP
	// via the "public:<ip>" bucket. This bounds abuse of the anonymous allowlisted
	// endpoints (resolveIngestEndpoint, resolveViewerEndpoint, networkStatus, the
	// public orchestrator topology fields, and the walletLogin/bootstrapEdge
	// mutations) that would otherwise be unmetered. Limits are fixed, not
	// tenant-derived. A payment never bypasses abuse controls; settlement and
	// request-rate accounting are separate concerns.
	if rlIsPublic {
		if rl == nil {
			return AccessDecision{Allowed: true, Headers: headers, X402Settled: x402Settled, X402QuoteID: x402QuoteID}
		}
		limit, burst := publicRateLimits()
		allowed, remaining, resetSeconds := rl.Allow(rlTenant, limit, burst)
		headers["X-RateLimit-Limit"] = strconv.Itoa(limit)
		headers["X-RateLimit-Remaining"] = strconv.Itoa(remaining)
		headers["X-RateLimit-Reset"] = strconv.Itoa(resetSeconds)
		if !allowed {
			if logger != nil {
				logger.WithFields(logging.Fields{
					"client_ip":     req.ClientIP,
					"limit":         limit,
					"reset_seconds": resetSeconds,
					"path":          req.Path,
				}).Warn("Public rate limit exceeded")
			}
			return rateLimitExceededDecision(limit, resetSeconds, headers)
		}
		return AccessDecision{Allowed: true, Headers: headers, X402Settled: x402Settled, X402QuoteID: x402QuoteID}
	}

	limit, burst := 0, 0
	if getLimits != nil {
		limit, burst = getLimits(rlTenant)
	}

	allowed, remaining, resetSeconds := rl.Allow(rlTenant, limit, burst)
	headers["X-RateLimit-Limit"] = strconv.Itoa(limit)
	headers["X-RateLimit-Remaining"] = strconv.Itoa(remaining)
	headers["X-RateLimit-Reset"] = strconv.Itoa(resetSeconds)

	if !allowed {
		if logger != nil {
			logger.WithFields(logging.Fields{
				"tenant_id":     rlTenant,
				"reset_seconds": resetSeconds,
				"limit":         limit,
				"path":          req.Path,
			}).Warn("Rate limit exceeded")
		}
		return rateLimitExceededDecision(limit, resetSeconds, headers)
	}

	return AccessDecision{
		Allowed:     true,
		Headers:     headers,
		X402Settled: x402Settled,
		X402QuoteID: x402QuoteID,
	}
}

const maxPaidMutationCaptureBytes = 2 << 20

type paidMutationCaptureWriter struct {
	gin.ResponseWriter
	body     bytes.Buffer
	overflow bool
}

func (w *paidMutationCaptureWriter) Write(data []byte) (int, error) {
	if !w.overflow {
		remaining := maxPaidMutationCaptureBytes - w.body.Len()
		if len(data) <= remaining {
			_, _ = w.body.Write(data)
		} else {
			w.overflow = true
			w.body.Reset()
		}
	}
	return w.ResponseWriter.Write(data)
}

func (w *paidMutationCaptureWriter) WriteString(data string) (int, error) {
	if !w.overflow {
		remaining := maxPaidMutationCaptureBytes - w.body.Len()
		if len(data) <= remaining {
			_, _ = w.body.WriteString(data)
		} else {
			w.overflow = true
			w.body.Reset()
		}
	}
	return w.ResponseWriter.WriteString(data)
}

func paidMutationFingerprint(method, path string, body []byte) string {
	digest := sha256.Sum256(bytes.Join([][]byte{[]byte(method), []byte(path), body}, []byte{0}))
	return fmt.Sprintf("%x", digest[:])
}

// handlePaidHTTPMutationIdempotency returns true when it has already produced
// the response. A false result means the caller owns the claim and should stop
// this middleware after c.Next returns.
func handlePaidHTTPMutationIdempotency(c *gin.Context, store X402Settler, tenantID, quoteID, operation string, logger logging.Logger) bool {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(key) < 8 || len(key) > 255 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error":   "idempotency_key_required",
			"code":    "IDEMPOTENCY_KEY_REQUIRED",
			"message": "paid GraphQL mutations require an Idempotency-Key containing 8-255 characters",
		})
		return true
	}
	if quoteID == "" {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error":   "paid_mutation_quote_missing",
			"code":    "PAID_MUTATION_QUOTE_MISSING",
			"message": "the settled payment is not bound to an x402 v2 quote",
		})
		return true
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "request_body_unreadable"})
		return true
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	fingerprint := paidMutationFingerprint(c.Request.Method, c.Request.URL.RequestURI(), body)
	claimCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	claim, err := store.ClaimX402MutationResult(claimCtx, &purserpb.ClaimX402MutationResultRequest{
		TenantId: tenantID, QuoteId: quoteID, IdempotencyKey: key,
		RequestFingerprint: fingerprint, Protocol: "http", Operation: operation,
	})
	cancel()
	if err != nil {
		statusCode := http.StatusServiceUnavailable
		if grpcstatus.Code(err) == codes.AlreadyExists || grpcstatus.Code(err) == codes.FailedPrecondition {
			statusCode = http.StatusConflict
		}
		c.AbortWithStatusJSON(statusCode, gin.H{
			"error":   "paid_mutation_idempotency_rejected",
			"code":    "PAID_MUTATION_IDEMPOTENCY_REJECTED",
			"message": err.Error(),
		})
		return true
	}
	switch claim.GetState() {
	case "completed":
		if claim.GetContentType() != "" {
			c.Header("Content-Type", claim.GetContentType())
		}
		statusCode := int(claim.GetStatusCode())
		if statusCode < 100 {
			statusCode = http.StatusOK
		}
		c.Status(statusCode)
		_, _ = c.Writer.Write(claim.GetResult())
		c.Abort()
		return true
	case "in_progress":
		c.Header("Retry-After", "2")
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error":   "paid_mutation_in_progress",
			"code":    "PAID_MUTATION_IN_PROGRESS",
			"message": "the paid mutation outcome is still being recorded; retry with the same key",
		})
		return true
	case "operator_review":
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error": "paid_mutation_operator_review", "code": "PAID_MUTATION_OPERATOR_REVIEW",
			"message": "the mutation outcome is ambiguous and is being reviewed; do not execute it again",
		})
		return true
	case "claimed":
		capture := &paidMutationCaptureWriter{ResponseWriter: c.Writer}
		c.Writer = capture
		c.Next()
		if capture.overflow {
			envelope := []byte(`{"error":"paid_mutation_result_not_replayable","code":"PAID_MUTATION_RESULT_NOT_REPLAYABLE","message":"the original mutation completed but its response exceeded the durable replay limit; inspect the resource state"}`)
			_ = completePaidMutationResult(store, &purserpb.CompleteX402MutationResultRequest{
				TenantId: tenantID, QuoteId: quoteID, IdempotencyKey: key,
				RequestFingerprint: fingerprint, Result: envelope,
				ContentType: "application/json", StatusCode: http.StatusConflict,
			})
			if logger != nil {
				logger.WithField("operation", operation).Warn("Paid mutation response exceeded durable replay limit; stored non-replayable terminal envelope")
			}
			return true
		}
		completeErr := completePaidMutationResult(store, &purserpb.CompleteX402MutationResultRequest{
			TenantId: tenantID, QuoteId: quoteID, IdempotencyKey: key,
			RequestFingerprint: fingerprint, Result: capture.body.Bytes(),
			ContentType: c.Writer.Header().Get("Content-Type"), StatusCode: int32(c.Writer.Status()),
		})
		if completeErr != nil && logger != nil {
			logger.WithError(completeErr).WithField("operation", operation).Error("Failed to persist paid mutation result after retries; claim will enter operator review")
		}
		return true
	default:
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "invalid paid mutation claim state"})
		return true
	}
}

func completePaidMutationResult(store X402Settler, req *purserpb.CompleteX402MutationResultRequest) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		completeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, lastErr = store.CompleteX402MutationResult(completeCtx, req)
		cancel()
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func billingStatusUnavailableDecision(headers map[string]string) AccessDecision {
	return AccessDecision{
		Allowed: false,
		Status:  http.StatusServiceUnavailable,
		Body: map[string]any{
			"error":   "billing_status_unavailable",
			"message": "billing status is temporarily unavailable; rated work was not started, retry safely",
			"code":    "BILLING_STATUS_UNAVAILABLE",
		},
		Headers: headers,
	}
}

func accessPolicyUnavailableDecision(headers map[string]string, req AccessRequest) AccessDecision {
	return AccessDecision{
		Allowed: false,
		Status:  http.StatusServiceUnavailable,
		Body: map[string]any{
			"error":     "access_policy_unclassified",
			"message":   "operation access policy is unavailable; no rated work was started",
			"code":      "ACCESS_POLICY_UNCLASSIFIED",
			"operation": req.OperationName,
		},
		Headers: headers,
	}
}

func postpaidCollectionRequiredDecision(headers map[string]string) AccessDecision {
	return AccessDecision{
		Allowed: false,
		Status:  http.StatusPaymentRequired,
		Body: map[string]any{
			"error":   "payment_setup_required",
			"message": "confirmed Stripe or Mollie collection setup is required for this paid tier",
			"code":    "PAYMENT_SETUP_REQUIRED",
		},
		Headers: headers,
	}
}

// rateLimitExceededDecision builds the shared 429 Too Many Requests response for
// both authenticated (per-tenant) and public (per-IP) rate-limit rejections.
func rateLimitExceededDecision(limit, resetSeconds int, headers map[string]string) AccessDecision {
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

// Public (unauthenticated) request rate limits, applied per client IP. Kept low
// since legitimate public callers hit these endpoints once per ingest/playback
// start, not per frame. Overridable via env for operators behind shared NAT.
const (
	defaultPublicRateLimitPerMinute = 60
	defaultPublicRateLimitBurst     = 30
)

// publicRateLimits returns the per-IP limit/burst for unauthenticated callers,
// honoring PUBLIC_RATE_LIMIT_PER_MINUTE / PUBLIC_RATE_LIMIT_BURST when set to a
// positive integer, otherwise the defaults.
func publicRateLimits() (limit, burst int) {
	limit, burst = defaultPublicRateLimitPerMinute, defaultPublicRateLimitBurst
	if v := strings.TrimSpace(os.Getenv("PUBLIC_RATE_LIMIT_PER_MINUTE")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("PUBLIC_RATE_LIMIT_BURST")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			burst = n
		}
	}
	return limit, burst
}

// build402Response builds the 402 Payment Required response
// Includes both human flow (topup_url) and x402 machine flow (accepts block)
func build402Response(ctx context.Context, tenantID, operationName, resourcePath string, x402Provider X402Provider, logger logging.Logger) map[string]any {
	response, _ := build402ResponseWithHeader(ctx, tenantID, operationName, resourcePath, x402Provider, logger)
	return response
}

func build402ResponseWithHeader(ctx context.Context, tenantID, operationName, resourcePath string, x402Provider X402Provider, logger logging.Logger) (map[string]any, string) {
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
			if requirements.Error != "" {
				response["message"] = requirements.Error
				if requirements.ErrorCode != "" {
					response["code"] = requirements.ErrorCode
				}
				if len(requirements.RequiredFields) > 0 {
					response["required_fields"] = requirements.RequiredFields
				}
				return response, ""
			}
			response["x402Version"] = requirements.X402Version

			// Convert accepts to map slice
			accepts := make([]map[string]any, 0, len(requirements.Accepts))
			for _, req := range requirements.Accepts {
				accepted := map[string]any{
					"scheme":            req.Scheme,
					"network":           req.Network,
					"payTo":             req.PayTo,
					"asset":             req.Asset,
					"maxTimeoutSeconds": req.MaxTimeoutSeconds,
				}
				if requirements.X402Version == 2 {
					accepted["amount"] = req.Amount
					if len(req.ExtraJson) > 0 {
						accepted["extra"] = json.RawMessage(req.ExtraJson)
					}
				} else {
					accepted["maxAmountRequired"] = req.MaxAmountRequired
					accepted["resource"] = req.Resource
					accepted["description"] = req.Description
				}
				accepts = append(accepts, accepted)
			}
			response["accepts"] = accepts
			if requirements.X402Version != 2 {
				return response, ""
			}
			response["resource"] = map[string]any{
				"url":         requirements.ResourceUrl,
				"description": requirements.ResourceDescription,
				"mimeType":    requirements.ResourceMimeType,
			}
			paymentRequired, encodeErr := x402.EncodePaymentRequiredHeader(requirements)
			if encodeErr != nil {
				if logger != nil {
					logger.WithError(encodeErr).Warn("Failed to encode x402 payment-required header")
				}
				return response, ""
			}
			return response, paymentRequired
		}
	}

	return response, ""
}

// extractGraphQLRequest reads the operationName + variables from a GraphQL request body.
// Returns empty values if not found or on error.
func extractGraphQLRequest(c *gin.Context) (string, map[string]interface{}) {
	access := extractGraphQLAccess(c)
	return access.OperationName, access.Variables
}

func extractGraphQLAccess(c *gin.Context) graphqlAccess {
	// Only process POST requests with JSON body
	if c.Request.Method != "POST" || c.Request.Body == nil {
		return graphqlAccess{}
	}

	// Read body (we need to restore it after reading)
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return graphqlAccess{}
	}
	// Restore the body for downstream handlers
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var req graphqlRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return graphqlAccess{}
	}

	operationType, fields := parseGraphQLTopLevelFields(req.Query, req.OperationName)
	return graphqlAccess{
		OperationName: req.OperationName,
		OperationType: operationType,
		Fields:        fields,
		Variables:     req.Variables,
	}
}

func requestAllowedWithoutFunds(req AccessRequest) (allowed, classified bool) {
	if req.OperationType == "query" || req.OperationType == "subscription" {
		return true, true
	}
	names := req.OperationNames
	if len(names) == 0 && req.OperationName != "" {
		names = []string{req.OperationName}
	}
	if len(names) == 0 {
		return false, false
	}
	for _, name := range names {
		class, ok := accesspolicy.OperationClass(req.OperationType, name)
		if !ok {
			return false, false
		}
		if !class.UnfundedAllowed() {
			allowed = false
		} else if !classified {
			allowed = true
		}
		classified = true
	}
	return allowed, classified
}

func suspendedRequestAllowed(req AccessRequest) bool {
	names := req.OperationNames
	if len(names) == 0 && req.OperationName != "" {
		names = []string{req.OperationName}
	}
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !suspendedOperationAllowed(name) {
			return false
		}
	}
	return true
}

func suspendedOperationAllowed(name string) bool {
	if suspendedRecoveryAllowlist[name] {
		return true
	}
	return strings.HasPrefix(name, "mcp:resources/read:billing://invoices/") ||
		strings.HasPrefix(name, "mcp:resources/read:billing://payments/") ||
		strings.HasPrefix(name, "mcp:resources/read:billing://documents/")
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

// RateLimitBucketKey returns the throttle identity for a caller: its tenant
// when authenticated, otherwise a per-IP public bucket. Exported so transports
// that cannot use the HTTP middleware — GraphQL over WebSocket, which
// authenticates later in the connection InitFunc — bucket callers exactly the
// same way, instead of inventing a second convention.
func RateLimitBucketKey(tenantID, clientIP string) string {
	if tenantID != "" {
		return tenantID
	}
	if clientIP == "" {
		clientIP = "unknown"
	}
	return "public:" + clientIP
}

// =============================================================================
// TENANT RATE LIMIT CACHE
// =============================================================================
// Caches tenant rate limits fetched from Quartermaster to avoid per-request gRPC calls.
// Single source of truth: Quartermaster (which reads from quartermaster.tenants table)

// TenantValidator is the interface for validating tenants and fetching rate limits
type TenantValidator interface {
	ValidateTenant(ctx context.Context, tenantID, userID string) (*quartermasterpb.ValidateTenantResponse, error)
}

// TenantRateLimits holds cached rate limit and billing info for a tenant
type TenantRateLimits struct {
	Limit                    int
	Burst                    int
	BillingModel             string // "postpaid" or "prepaid"
	TierName                 string
	CollectionReady          bool
	CollectionProvider       string
	BillingStatusUnavailable bool
	IsSuspended              bool // true if tenant suspended (balance < -$10)
	IsBalanceNegative        bool // true if balance <= 0 (should return 402)
	FetchedAt                time.Time
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
	info, _ := tc.getTenantInfo(tenantID)
	if info == nil {
		return 0, 0
	}
	return info.Limit, info.Burst
}

// getTenantInfo returns cached tenant info, fetching from Quartermaster if stale/missing.
func (tc *TenantCache) getTenantInfo(tenantID string) (*TenantRateLimits, error) {
	// Check cache first
	if cached, ok := tc.cache.Load(tenantID); ok {
		limits := cached.(*TenantRateLimits) //nolint:errcheck // type guaranteed by sync.Map usage
		// Use shorter TTL for prepaid tenants (faster enforcement)
		ttl := tc.cacheTTLPostpaid
		if limits.BillingModel == "prepaid" {
			ttl = tc.cacheTTLPrepaid
		}
		if time.Since(limits.FetchedAt) < ttl {
			return limits, nil
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
		return nil, fmt.Errorf("validate tenant: %w", err)
	}

	if resp == nil || !resp.Valid {
		return nil, fmt.Errorf("tenant %q was not valid", tenantID)
	}

	// Cache the result with billing info
	limits := &TenantRateLimits{
		Limit:                    int(resp.RateLimitPerMinute),
		Burst:                    int(resp.RateLimitBurst),
		BillingModel:             resp.BillingModel,
		TierName:                 resp.TierName,
		CollectionReady:          resp.CollectionReady,
		CollectionProvider:       resp.CollectionProvider,
		BillingStatusUnavailable: resp.BillingStatusUnavailable,
		IsSuspended:              resp.IsSuspended,
		IsBalanceNegative:        resp.IsBalanceNegative,
		FetchedAt:                time.Now(),
	}
	tc.cache.Store(tenantID, limits)

	return limits, nil
}

// GetBillingAccessStatus returns one coherent snapshot. Lookup failure is
// explicit so callers can keep safe control available without accidentally
// admitting rated work as postpaid.
func (tc *TenantCache) GetBillingAccessStatus(tenantID string) (BillingAccessStatus, error) {
	info, err := tc.getTenantInfo(tenantID)
	if err != nil {
		return BillingAccessStatus{}, err
	}
	if info.BillingStatusUnavailable {
		return BillingAccessStatus{}, fmt.Errorf("billing authority unavailable for tenant %q", tenantID)
	}
	return BillingAccessStatus{
		BillingModel:       info.BillingModel,
		TierName:           info.TierName,
		CollectionReady:    info.CollectionReady,
		CollectionProvider: info.CollectionProvider,
		IsBalanceNegative:  info.IsBalanceNegative,
		IsSuspended:        info.IsSuspended,
	}, nil
}

// GetLimitsFunc returns a function suitable for use with RateLimitMiddleware
func (tc *TenantCache) GetLimitsFunc() func(tenantID string) (limit, burst int) {
	return tc.GetLimits
}
