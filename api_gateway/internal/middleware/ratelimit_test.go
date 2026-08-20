package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/x402"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRateLimiterAllowInvalidLimits(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	allowed, remaining, reset := rl.Allow("tenant-1", 0, 0)
	if !allowed {
		t.Fatal("expected request to be allowed with invalid limits")
	}
	if remaining != 0 || reset != 0 {
		t.Fatalf("expected zero remaining/reset, got %d/%d", remaining, reset)
	}
}

func TestRateLimiterAllowAndBlock(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	allowed, _, _ := rl.Allow("tenant-1", 1, 1)
	if !allowed {
		t.Fatal("expected first request to be allowed")
	}
	allowed, _, _ = rl.Allow("tenant-1", 1, 1)
	if !allowed {
		t.Fatal("expected second request to be allowed")
	}
	allowed, _, reset := rl.Allow("tenant-1", 1, 1)
	if allowed {
		t.Fatal("expected third request to be rate limited")
	}
	if reset <= 0 {
		t.Fatalf("expected reset seconds > 0, got %d", reset)
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	rl.Allow("tenant-1", 10, 5)
	bucketI, ok := rl.buckets.Load("tenant-1")
	if !ok {
		t.Fatal("expected bucket to exist")
	}
	bucket := bucketI.(*tokenBucket)
	bucket.mu.Lock()
	bucket.lastRequest = time.Now().Add(-6 * time.Minute)
	bucket.mu.Unlock()

	rl.cleanup()
	if _, ok := rl.buckets.Load("tenant-1"); ok {
		t.Fatal("expected bucket to be removed after cleanup")
	}
}

func TestEvaluateAccessPublicTenantSkipsGetLimits(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	getLimits := func(tenantID string) (int, int) {
		t.Fatalf("getLimits should not be called for public tenant, got %q", tenantID)
		return 0, 0
	}

	decision := EvaluateAccess(
		context.Background(),
		AccessRequest{
			TenantID:          "",
			ClientIP:          "172.18.0.1",
			Path:              "/graphql/",
			OperationName:     "serviceinstanceshealth",
			PublicAllowlisted: true,
		},
		rl,
		getLimits,
		nil, nil, nil, nil, nil,
	)

	if !decision.Allowed {
		t.Fatalf("expected public allowlisted request to be allowed, got status %d", decision.Status)
	}
}

// Public (unauthenticated) callers are throttled per client IP so key-authenticated
// endpoints like resolveIngestEndpoint can't be used as an unmetered oracle.
func TestEvaluateAccessPublicRateLimitedPerIP(t *testing.T) {
	t.Setenv("PUBLIC_RATE_LIMIT_PER_MINUTE", "1")
	t.Setenv("PUBLIC_RATE_LIMIT_BURST", "1")

	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	req := AccessRequest{
		TenantID:          "",
		ClientIP:          "203.0.113.7",
		Path:              "ingest://resolve",
		OperationName:     "resolveIngestEndpoint",
		PublicAllowlisted: true,
	}

	// Bucket starts full at limit+burst = 2 tokens; first two pass, third blocks.
	for i := 0; i < 2; i++ {
		if d := EvaluateAccess(context.Background(), req, rl, nil, nil, nil, nil, nil, nil); !d.Allowed {
			t.Fatalf("expected public request %d to be allowed, got status %d", i+1, d.Status)
		}
	}
	blocked := EvaluateAccess(context.Background(), req, rl, nil, nil, nil, nil, nil, nil)
	if blocked.Allowed {
		t.Fatal("expected public caller to be rate limited after exhausting bucket")
	}
	if blocked.Status != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", blocked.Status)
	}
	if blocked.Headers["Retry-After"] == "" {
		t.Fatal("expected Retry-After header on public rate-limited response")
	}

	// A different client IP has its own bucket and is unaffected.
	other := req
	other.ClientIP = "203.0.113.8"
	if d := EvaluateAccess(context.Background(), other, rl, nil, nil, nil, nil, nil, nil); !d.Allowed {
		t.Fatalf("expected separate IP to be allowed, got status %d", d.Status)
	}
}

// The public per-IP throttle is only as strong as its IP source. With no trusted
// proxies configured, a spoofed X-Forwarded-For must be ignored so an attacker
// can't mint a fresh bucket per request. This pins that the middleware keys on the
// trust-aware client IP, not gin's c.ClientIP() (which trusts XFF by default).
func TestRateLimitMiddlewareIgnoresSpoofedXFF(t *testing.T) {
	t.Setenv("PUBLIC_RATE_LIMIT_PER_MINUTE", "1")
	t.Setenv("PUBLIC_RATE_LIMIT_BURST", "1")

	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	tp, _ := ParseTrustedProxies("") // trust no proxies → XFF is untrusted
	handler := rateLimitMiddlewareInternal(rl, nil, nil, nil, nil, nil, tp)

	gin.SetMode(gin.TestMode)
	send := func(spoofedXFF string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(string(ctxkeys.KeyPublicAllowlisted), true)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", nil)
		req.RemoteAddr = "203.0.113.9:1234" // same real source every time
		req.Header.Set("X-Forwarded-For", spoofedXFF)
		c.Request = req
		handler(c)
		return w.Code
	}

	// Bucket = limit+burst = 2 tokens. First two pass; the third from the same real
	// source must be throttled despite each carrying a different spoofed XFF.
	if code := send("1.1.1.1"); code == http.StatusTooManyRequests {
		t.Fatalf("first request should pass, got %d", code)
	}
	if code := send("2.2.2.2"); code == http.StatusTooManyRequests {
		t.Fatalf("second request should pass, got %d", code)
	}
	if code := send("3.3.3.3"); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 (spoofed XFF must not create a new bucket), got %d", code)
	}
}

// A public caller resolving an OWNED resource (e.g. anonymous viewer playback via
// MCP, where TenantID is set to the stream owner for billing) must be throttled on
// its own per-IP bucket, not the owner's tenant bucket — otherwise anonymous
// traffic could exhaust a victim tenant's rate limit.
func TestEvaluateAccessDecouplesRateLimitFromOwnerTenant(t *testing.T) {
	t.Setenv("PUBLIC_RATE_LIMIT_PER_MINUTE", "1")
	t.Setenv("PUBLIC_RATE_LIMIT_BURST", "1")

	// The owner would get a huge bucket; if the limiter keyed on the owner we'd
	// never see a 429. It must key on the caller's public:<ip> bucket instead.
	getLimits := func(string) (int, int) { return 100000, 100000 }
	anon := ""

	ownedByAnon := func(ip string) AccessRequest {
		return AccessRequest{
			TenantID:          "owner-tenant", // billing/owner identity
			RateLimitTenantID: &anon,          // caller is anonymous → per-IP bucket
			ClientIP:          ip,
			Path:              "viewer://abc",
			OperationName:     "resolve_playback_endpoint",
			PublicAllowlisted: true,
		}
	}

	t.Run("anonymous caller is throttled on the IP bucket, not the owner bucket", func(t *testing.T) {
		rl := NewRateLimiter(RateLimitConfig{})
		defer rl.Stop()

		for i := 0; i < 2; i++ { // public bucket = limit+burst = 2 tokens
			if d := EvaluateAccess(context.Background(), ownedByAnon("203.0.113.5"), rl, getLimits, nil, nil, nil, nil, nil); !d.Allowed {
				t.Fatalf("request %d should pass, got %d", i+1, d.Status)
			}
		}
		if d := EvaluateAccess(context.Background(), ownedByAnon("203.0.113.5"), rl, getLimits, nil, nil, nil, nil, nil); d.Status != http.StatusTooManyRequests {
			t.Fatalf("expected 429 on IP bucket exhaustion, got %d", d.Status)
		}
	})

	t.Run("without decoupling the owner's large bucket applies (control)", func(t *testing.T) {
		rl := NewRateLimiter(RateLimitConfig{})
		defer rl.Stop()

		// Same owner tenant, but RateLimitTenantID nil → keyed on the owner bucket,
		// which getLimits sizes huge, so the 3rd request is NOT throttled.
		coupled := AccessRequest{TenantID: "owner-tenant", ClientIP: "203.0.113.6", Path: "viewer://abc", OperationName: "x"}
		for i := 0; i < 3; i++ {
			if d := EvaluateAccess(context.Background(), coupled, rl, getLimits, nil, nil, nil, nil, nil); !d.Allowed {
				t.Fatalf("owner-bucket request %d unexpectedly throttled: %d", i+1, d.Status)
			}
		}
	})
}

// A public caller with no resolvable client IP must still land in the public
// throttle (bucket "public:unknown"), not fall through to the authenticated path
// where nil limits fail open and the request goes unmetered.
func TestEvaluateAccessPublicWithEmptyClientIP(t *testing.T) {
	t.Setenv("PUBLIC_RATE_LIMIT_PER_MINUTE", "1")
	t.Setenv("PUBLIC_RATE_LIMIT_BURST", "1")

	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	req := AccessRequest{
		TenantID:          "",
		ClientIP:          "",
		Path:              "ingest://resolve",
		OperationName:     "resolveIngestEndpoint",
		PublicAllowlisted: true,
	}
	for i := 0; i < 2; i++ { // public bucket = 2 tokens
		if d := EvaluateAccess(context.Background(), req, rl, nil, nil, nil, nil, nil, nil); !d.Allowed {
			t.Fatalf("request %d should pass on the public bucket, got %d", i+1, d.Status)
		}
	}
	if d := EvaluateAccess(context.Background(), req, rl, nil, nil, nil, nil, nil, nil); d.Status != http.StatusTooManyRequests {
		t.Fatalf("expected 429 (empty-IP caller must still be public-throttled), got %d", d.Status)
	}
}

func TestEvaluateAccessRateLimitHeaders(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	getLimits := func(tenantID string) (int, int) {
		if tenantID != "tenant-1" {
			t.Fatalf("unexpected tenant id: %q", tenantID)
		}
		return 1, 1
	}

	req := AccessRequest{
		TenantID:      "tenant-1",
		ClientIP:      "172.18.0.1",
		Path:          "/graphql",
		OperationName: "streamsConnection",
	}

	for i := 0; i < 2; i++ {
		decision := EvaluateAccess(context.Background(), req, rl, getLimits, nil, nil, nil, nil, nil)
		if !decision.Allowed {
			t.Fatalf("expected request %d to be allowed, got status %d", i+1, decision.Status)
		}
		if decision.Headers["X-RateLimit-Limit"] == "" {
			t.Fatalf("expected rate limit headers on request %d", i+1)
		}
	}

	decision := EvaluateAccess(context.Background(), req, rl, getLimits, nil, nil, nil, nil, nil)
	if decision.Allowed {
		t.Fatal("expected request to be rate limited")
	}
	if decision.Status != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", decision.Status)
	}
	if decision.Headers["Retry-After"] == "" {
		t.Fatal("expected Retry-After header on rate limited response")
	}
}

type fakeBillingChecker struct {
	billingModel      string
	isBalanceNegative bool
	isSuspended       bool
}

func (f fakeBillingChecker) IsBalanceNegative(string) bool { return f.isBalanceNegative }
func (f fakeBillingChecker) IsSuspended(string) bool       { return f.isSuspended }
func (f fakeBillingChecker) GetBillingModel(string) string { return f.billingModel }

type fakeX402Provider struct {
	requirements *purserpb.PaymentRequirements
	err          error
}

type fakeX402Settler struct {
	status     *purserpb.GetTenantBillingStatusResponse
	statusErr  error
	claimFn    func(*purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error)
	completeFn func(*purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error)
}

func (f fakeX402Settler) VerifyX402Payment(context.Context, string, *x402pb.X402PaymentPayload, string) (*purserpb.VerifyX402PaymentResponse, error) {
	return &purserpb.VerifyX402PaymentResponse{Valid: true}, nil
}

func (f fakeX402Settler) SettleX402Payment(context.Context, string, *x402pb.X402PaymentPayload, string) (*purserpb.SettleX402PaymentResponse, error) {
	return &purserpb.SettleX402PaymentResponse{Success: true}, nil
}

func (f fakeX402Settler) GetTenantBillingStatus(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
	return f.status, f.statusErr
}

func (f fakeX402Settler) ClaimX402MutationResult(_ context.Context, req *purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error) {
	if f.claimFn != nil {
		return f.claimFn(req)
	}
	return &purserpb.ClaimX402MutationResultResponse{State: "claimed"}, nil
}

func (f fakeX402Settler) CompleteX402MutationResult(_ context.Context, req *purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error) {
	if f.completeFn != nil {
		return f.completeFn(req)
	}
	return &purserpb.CompleteX402MutationResultResponse{Completed: true}, nil
}

func (f fakeX402Provider) GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error) {
	return f.requirements, f.err
}

func TestBuild402ResponseIncludesRequirements(t *testing.T) {
	provider := fakeX402Provider{
		requirements: &purserpb.PaymentRequirements{
			X402Version: 1,
			Accepts: []*purserpb.PaymentRequirement{
				{
					Scheme:            "x402",
					Network:           "base",
					MaxAmountRequired: "100",
					PayTo:             "0xabc",
					Asset:             "USDC",
					MaxTimeoutSeconds: 120,
					Resource:          "viewer://content",
					Description:       "viewer endpoint",
				},
			},
		},
	}

	response := build402Response(context.Background(), "tenant-1", "resolveViewerEndpoint", "viewer://content", provider, nil)

	if response["x402Version"] != int32(1) {
		t.Fatalf("expected x402Version 1, got %#v", response["x402Version"])
	}
	accepts, ok := response["accepts"].([]map[string]any)
	if !ok || len(accepts) != 1 {
		t.Fatalf("expected accepts list, got %#v", response["accepts"])
	}
	expected := map[string]any{
		"scheme":            "x402",
		"network":           "base",
		"maxAmountRequired": "100",
		"payTo":             "0xabc",
		"asset":             "USDC",
		"maxTimeoutSeconds": int32(120),
		"resource":          "viewer://content",
		"description":       "viewer endpoint",
	}
	if !reflect.DeepEqual(expected, accepts[0]) {
		t.Fatalf("accepts mismatch: got %#v", accepts[0])
	}
}

func TestBuild402ResponseSkipsRequirementsOnError(t *testing.T) {
	provider := fakeX402Provider{err: context.DeadlineExceeded}

	response := build402Response(context.Background(), "tenant-1", "op", "/path", provider, nil)

	if _, ok := response["accepts"]; ok {
		t.Fatalf("expected accepts to be omitted on error, got %#v", response["accepts"])
	}
}

func TestBuild402ResponseEmitsOfficialV2Header(t *testing.T) {
	provider := fakeX402Provider{requirements: &purserpb.PaymentRequirements{
		X402Version:         2,
		ResourceUrl:         "https://api.example.com/graphql",
		ResourceDescription: "FrameWorks prepaid usage credit",
		ResourceMimeType:    "application/json",
		Accepts: []*purserpb.PaymentRequirement{{
			Scheme: "exact", Network: "eip155:8453", Amount: "5000000",
			PayTo: "0xabc", Asset: "0xasset", MaxTimeoutSeconds: 60,
			ExtraJson: []byte(`{"frameworks":{"quoteId":"quote-1"}}`),
		}},
	}}

	response, header := build402ResponseWithHeader(context.Background(), "tenant-1", "createStream", "graphql://createStream", provider, nil)
	if response["x402Version"] != int32(2) || header == "" {
		t.Fatalf("missing v2 response/header: response=%+v header=%q", response, header)
	}
	decoded, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(decoded, []byte(`"network":"eip155:8453"`)) || !bytes.Contains(decoded, []byte(`"quoteId":"quote-1"`)) {
		t.Fatalf("unexpected PAYMENT-REQUIRED document: %s", decoded)
	}
}

func TestEvaluateAccessPrepaidNegativeBalanceBlocks(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	getLimits := func(string) (int, int) { return 10, 2 }
	billing := fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true}

	decision := EvaluateAccess(context.Background(), AccessRequest{
		TenantID:      "tenant-1",
		ClientIP:      "10.0.0.1",
		Path:          "/graphql",
		OperationName: "streamsConnection",
	}, rl, getLimits, billing, nil, nil, nil, nil)

	if decision.Allowed {
		t.Fatal("expected prepaid negative balance to be blocked")
	}
	if decision.Status != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", decision.Status)
	}
	if decision.Body["code"] != "INSUFFICIENT_BALANCE" {
		t.Fatalf("expected insufficient balance code, got %#v", decision.Body["code"])
	}
}

func TestEvaluateAccessRechecksCanonicalBalanceAfterX402(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	request := AccessRequest{
		TenantID:      "tenant-1",
		ClientIP:      "10.0.0.1",
		Path:          "/graphql",
		OperationName: "createClip",
		X402Processed: true,
	}

	t.Run("insufficient topup remains blocked", func(t *testing.T) {
		decision := EvaluateAccess(context.Background(), request, rl, func(string) (int, int) { return 10, 2 },
			fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: false}, nil,
			fakeX402Settler{status: &purserpb.GetTenantBillingStatusResponse{
				BillingModel:      "prepaid",
				IsBalanceNegative: true,
				BalanceCents:      -1,
			}}, nil, nil)
		if decision.Allowed || decision.Status != http.StatusPaymentRequired {
			t.Fatalf("decision = %+v, want 402 after insufficient topup", decision)
		}
	})

	t.Run("confirmed sufficient topup overrides stale negative cache", func(t *testing.T) {
		decision := EvaluateAccess(context.Background(), request, rl, func(string) (int, int) { return 10, 2 },
			fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true}, nil,
			fakeX402Settler{status: &purserpb.GetTenantBillingStatusResponse{
				BillingModel: "prepaid",
				BalanceCents: 500,
			}}, nil, nil)
		if !decision.Allowed {
			t.Fatalf("decision = %+v, want allowed after canonical positive balance", decision)
		}
	})

	t.Run("status lookup failure is retryable and fail closed", func(t *testing.T) {
		decision := EvaluateAccess(context.Background(), request, rl, func(string) (int, int) { return 10, 2 }, nil, nil,
			fakeX402Settler{statusErr: context.DeadlineExceeded}, nil, nil)
		if decision.Allowed || decision.Status != http.StatusServiceUnavailable {
			t.Fatalf("decision = %+v, want 503", decision)
		}
	})
}

func TestEvaluateAccessPrepaidAllowlistBypassesBalance(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	getLimits := func(string) (int, int) { return 100, 100 }
	billing := fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true}

	for _, operation := range []string{
		"billingStatus", "createDeveloperToken", "developerTokensConnection", "CreateAPIToken",
		"mcp:tools/call:list_linked_wallets", "mcp:tools/call:link_wallet", "mcp:tools/call:unlink_wallet",
		"mcp:resources/read:billing://documents", "mcp:resources/read:billing://documents/credit_note/doc-1",
	} {
		t.Run(operation, func(t *testing.T) {
			decision := EvaluateAccess(context.Background(), AccessRequest{
				TenantID:      "tenant-1",
				ClientIP:      "10.0.0.1",
				Path:          "/graphql",
				OperationName: operation,
			}, rl, getLimits, billing, nil, nil, nil, nil)

			if !decision.Allowed {
				t.Fatalf("expected allowlisted prepaid request to be allowed, got status %d", decision.Status)
			}
		})
	}
}

func TestEvaluateAccessPrepaidOperationPolicy(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()
	billing := fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true}

	t.Run("authenticated query is non-rated", func(t *testing.T) {
		decision := EvaluateAccess(context.Background(), AccessRequest{
			TenantID: "tenant-1", ClientIP: "10.0.0.1", Path: "/graphql",
			OperationName: "Dashboard", OperationNames: []string{"streamsConnection", "billingStatus"}, OperationType: "query",
		}, rl, func(string) (int, int) { return 10, 2 }, billing, nil, nil, nil, nil)
		if !decision.Allowed {
			t.Fatalf("non-rated query denied: %+v", decision)
		}
	})

	t.Run("client operation name cannot disguise rated mutation", func(t *testing.T) {
		decision := EvaluateAccess(context.Background(), AccessRequest{
			TenantID: "tenant-1", ClientIP: "10.0.0.1", Path: "/graphql",
			OperationName: "billingStatus", OperationNames: []string{"createClip"}, OperationType: "mutation",
		}, rl, func(string) (int, int) { return 10, 2 }, billing, nil, nil, nil, nil)
		if decision.Allowed || decision.Status != http.StatusPaymentRequired {
			t.Fatalf("rated mutation should be denied: %+v", decision)
		}
	})

	t.Run("mixed mutation fails closed", func(t *testing.T) {
		decision := EvaluateAccess(context.Background(), AccessRequest{
			TenantID: "tenant-1", ClientIP: "10.0.0.1", Path: "/graphql",
			OperationNames: []string{"updateBillingDetails", "startDVR"}, OperationType: "mutation",
		}, rl, func(string) (int, int) { return 10, 2 }, billing, nil, nil, nil, nil)
		if decision.Allowed {
			t.Fatalf("mixed rated mutation should be denied: %+v", decision)
		}
	})
}

func TestEvaluateAccessSuspensionPolicyIsNarrowerThanZeroBalance(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()
	billing := fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true, isSuspended: true}
	evaluate := func(operationType string, names ...string) AccessDecision {
		return EvaluateAccess(context.Background(), AccessRequest{
			TenantID: "tenant-1", ClientIP: "10.0.0.1", Path: "/graphql",
			OperationNames: names, OperationType: operationType,
		}, rl, func(string) (int, int) { return 10, 2 }, billing, nil, nil, nil, nil)
	}

	if decision := evaluate("query", "billingStatus", "prepaidBalance"); !decision.Allowed {
		t.Fatalf("billing recovery reads must remain available: %+v", decision)
	}
	if decision := evaluate("mutation", "createCryptoTopup"); !decision.Allowed {
		t.Fatalf("top-up recovery must remain available: %+v", decision)
	}
	if decision := evaluate("query", "streamsConnection"); decision.Allowed || decision.Status != http.StatusPaymentRequired {
		t.Fatalf("suspension must block unrelated reads: %+v", decision)
	}
	if decision := evaluate("mutation", "createStream"); decision.Allowed || decision.Status != http.StatusPaymentRequired {
		t.Fatalf("suspension must block otherwise non-rated configuration: %+v", decision)
	}
}

func TestEvaluateAccessPublicTenantRequiresPayment(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	decision := EvaluateAccess(context.Background(), AccessRequest{
		TenantID:      "",
		ClientIP:      "203.0.113.10",
		Path:          "/graphql",
		OperationName: "streamsConnection",
	}, rl, nil, nil, nil, nil, nil, nil)

	if decision.Allowed {
		t.Fatal("expected public request without allowlist to be blocked")
	}
	if decision.Status != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", decision.Status)
	}
}

func TestPublicOperationRateLimitMiddlewareNeverReturnsPaymentChallenge(t *testing.T) {
	t.Setenv("PUBLIC_RATE_LIMIT_PER_MINUTE", "1")
	t.Setenv("PUBLIC_RATE_LIMIT_BURST", "1")
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	router := gin.New()
	router.POST("/auth/wallet-challenge", PublicOperationRateLimitMiddleware(rl, nil, "walletChallenge"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/wallet-challenge", nil)
	firstReq.RemoteAddr = "203.0.113.20:1234"
	router.ServeHTTP(first, firstReq)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first request status = %d", first.Code)
	}

	burst := httptest.NewRecorder()
	burstReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/wallet-challenge", nil)
	burstReq.RemoteAddr = "203.0.113.20:1235"
	router.ServeHTTP(burst, burstReq)
	if burst.Code != http.StatusNoContent {
		t.Fatalf("burst request status = %d", burst.Code)
	}

	limited := httptest.NewRecorder()
	limitedReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/wallet-challenge", nil)
	limitedReq.RemoteAddr = "203.0.113.20:1236"
	router.ServeHTTP(limited, limitedReq)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited request status = %d, want 429", limited.Code)
	}
	if limited.Header().Get(x402.PaymentRequiredHeader) != "" {
		t.Fatal("authentication throttling must never emit a payment challenge")
	}
}

func TestEvaluateAccessRateLimitAddsDocumentation(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	t.Setenv("DOCS_PUBLIC_URL", "https://docs.example.com")
	getLimits := func(string) (int, int) { return 1, 1 }

	req := AccessRequest{
		TenantID:      "tenant-1",
		ClientIP:      "10.0.0.2",
		Path:          "/graphql",
		OperationName: "streamsConnection",
	}

	EvaluateAccess(context.Background(), req, rl, getLimits, nil, nil, nil, nil, nil)
	EvaluateAccess(context.Background(), req, rl, getLimits, nil, nil, nil, nil, nil)
	decision := EvaluateAccess(context.Background(), req, rl, getLimits, nil, nil, nil, nil, nil)

	if decision.Allowed {
		t.Fatal("expected rate limit to deny request")
	}
	if decision.Body["documentation"] != "https://docs.example.com/api/rate-limits" {
		t.Fatalf("expected documentation URL, got %#v", decision.Body["documentation"])
	}
}

// GraphQL over WebSocket cannot use the HTTP rate-limit middleware (it
// authenticates later, in the connection InitFunc), so it throttles at the
// operation level instead. It must bucket callers exactly as the HTTP path
// does, or the two transports would maintain separate allowances for the same
// caller.
func TestRateLimitBucketKeyMatchesHTTPConvention(t *testing.T) {
	cases := []struct {
		name     string
		tenantID string
		clientIP string
		want     string
	}{
		{"authenticated caller buckets on tenant", "tenant-abc", "203.0.113.9", "tenant-abc"},
		{"anonymous caller buckets per IP", "", "203.0.113.9", "public:203.0.113.9"},
		{"unknown IP still lands in a public bucket", "", "", "public:unknown"},
	}
	for _, tc := range cases {
		if got := RateLimitBucketKey(tc.tenantID, tc.clientIP); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}

	// The anonymous keys must be the ones isPublicTenant recognises, otherwise
	// they would silently escape the public throttle.
	if !isPublicTenant(RateLimitBucketKey("", "203.0.113.9")) {
		t.Error("anonymous bucket not classified as public")
	}
	if isPublicTenant(RateLimitBucketKey("tenant-abc", "203.0.113.9")) {
		t.Error("tenant bucket misclassified as public")
	}
}

func TestPaidHTTPMutationResultIsReplayedWithoutExecutingAgain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var mu sync.Mutex
	var fingerprint string
	var storedResult []byte
	var storedStatus int32
	var executions int
	store := fakeX402Settler{
		claimFn: func(req *purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			if fingerprint == "" {
				fingerprint = req.GetRequestFingerprint()
				return &purserpb.ClaimX402MutationResultResponse{State: "claimed"}, nil
			}
			if fingerprint != req.GetRequestFingerprint() {
				return nil, status.Error(codes.AlreadyExists, "key collision")
			}
			return &purserpb.ClaimX402MutationResultResponse{
				State: "completed", Result: append([]byte(nil), storedResult...),
				StatusCode: storedStatus, ContentType: "application/json",
			}, nil
		},
		completeFn: func(req *purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			storedResult = append([]byte(nil), req.GetResult()...)
			storedStatus = req.GetStatusCode()
			return &purserpb.CompleteX402MutationResultResponse{Completed: true}, nil
		},
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		if handlePaidHTTPMutationIdempotency(c, store, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "createStream", nil) {
			return
		}
	})
	router.POST("/graphql", func(c *gin.Context) {
		executions++
		c.JSON(http.StatusCreated, gin.H{"id": "stream-1"})
	})

	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "mutation-123")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	first := request(`{"query":"mutation { createStream { id } }"}`)
	second := request(`{"query":"mutation { createStream { id } }"}`)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || first.Body.String() != second.Body.String() {
		t.Fatalf("responses differ: first=%d %q second=%d %q", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	if executions != 1 {
		t.Fatalf("mutation executed %d times, want exactly once", executions)
	}
	collision := request(`{"query":"mutation { createStream(input:{name:\"different\"}) { id } }"}`)
	if collision.Code != http.StatusConflict || executions != 1 {
		t.Fatalf("collision status=%d executions=%d, want 409 and one execution", collision.Code, executions)
	}
}
