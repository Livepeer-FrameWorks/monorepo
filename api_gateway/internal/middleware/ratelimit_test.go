package middleware

import (
	"context"
	"net/http"
	"testing"
	"time"

	pb "frameworks/pkg/proto"
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
	requirements *pb.PaymentRequirements
	err          error
}

func (f fakeX402Provider) GetPaymentRequirements(ctx context.Context, tenantID, resource string) (*pb.PaymentRequirements, error) {
	return f.requirements, f.err
}

func TestBuild402ResponseIncludesRequirements(t *testing.T) {
	provider := fakeX402Provider{
		requirements: &pb.PaymentRequirements{
			X402Version: 1,
			Accepts: []*pb.PaymentRequirement{
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
		"maxTimeoutSeconds": int64(120),
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

	decision := EvaluateAccess(context.Background(), AccessRequest{
		TenantID:      "tenant-1",
		ClientIP:      "10.0.0.1",
		Path:          "/graphql",
		OperationName: "billingStatus",
	}, rl, getLimits, billing, nil, nil, nil, nil)

	if !decision.Allowed {
		t.Fatalf("expected allowlisted prepaid request to be allowed, got status %d", decision.Status)
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
