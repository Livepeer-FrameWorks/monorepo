package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type scriptedTenantValidator struct {
	responses []*quartermasterpb.ValidateTenantResponse
	calls     int
}

func (v *scriptedTenantValidator) ValidateTenant(context.Context, string, string) (*quartermasterpb.ValidateTenantResponse, error) {
	resp := v.responses[min(v.calls, len(v.responses)-1)]
	v.calls++
	return resp, nil
}

// Staging: one 350 ms Purser timeout made Quartermaster answer "billing
// status unavailable", and that answer was cached for the full 5-minute TTL,
// refusing every rated mutation. It is a transient answer: the last known
// state is kept and the tenant is asked again within seconds.
func TestTenantCacheDoesNotHoldBillingUnavailableForTheTTL(t *testing.T) {
	good := &quartermasterpb.ValidateTenantResponse{Valid: true, BillingModel: "postpaid", CollectionReady: true}
	unavailable := &quartermasterpb.ValidateTenantResponse{Valid: true, BillingModel: "postpaid", BillingStatusUnavailable: true}

	v := &scriptedTenantValidator{responses: []*quartermasterpb.ValidateTenantResponse{good, unavailable, good}}
	tc := NewTenantCache(v, logging.NewLogger())
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	// Force a refresh; Quartermaster now answers unavailable.
	cached, _ := tc.cache.Load("t1")
	cached.(*TenantRateLimits).FetchedAt = time.Now().Add(-time.Hour)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("unavailable answer replaced the known billing state: %v", err)
	}
	cached, _ = tc.cache.Load("t1")
	if remaining := tc.ttlFor(cached.(*TenantRateLimits)) - time.Since(cached.(*TenantRateLimits).FetchedAt); remaining > billingUnavailableRetryAfter {
		t.Fatalf("unavailable answer cached for %s, want at most %s", remaining, billingUnavailableRetryAfter)
	}

	// With no known state the refusal stands, but only until the retry.
	v2 := &scriptedTenantValidator{responses: []*quartermasterpb.ValidateTenantResponse{unavailable, good}}
	tc2 := NewTenantCache(v2, logging.NewLogger())
	if _, err := tc2.GetBillingAccessStatus("t2"); err == nil {
		t.Fatal("unknown billing state must not admit rated work")
	}
	cached, _ = tc2.cache.Load("t2")
	cached.(*TenantRateLimits).FetchedAt = cached.(*TenantRateLimits).FetchedAt.Add(-billingUnavailableRetryAfter)
	if _, err := tc2.GetBillingAccessStatus("t2"); err != nil {
		t.Fatalf("billing state not re-read after the retry delay: %v", err)
	}
}
