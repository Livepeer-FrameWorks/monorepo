package middleware

import (
	"context"
	"testing"
	"time"
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
