package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/gin-gonic/gin"
	"reflect"
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

func TestEvaluateAccessPrepaidAllowlistBypassesBalance(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{})
	defer rl.Stop()

	getLimits := func(string) (int, int) { return 5, 1 }
	billing := fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true}

	for _, operation := range []string{"billingStatus", "createDeveloperToken", "developerTokensConnection", "CreateAPIToken"} {
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
