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

	// Synchronous reads past a short TTL, so each expiry asks Quartermaster.
	timing := defaultTenantCacheTiming
	timing.ttl = 20 * time.Millisecond
	timing.ttlJitter = 0
	timing.staleWindow = 0

	v := &scriptedTenantValidator{responses: []*quartermasterpb.ValidateTenantResponse{good, unavailable, good}}
	tc := newTenantCache(v, logging.NewLogger(), timing)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	// Past the TTL Quartermaster answers unavailable.
	time.Sleep(2 * timing.ttl)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("unavailable answer replaced the known billing state: %v", err)
	}
	cached, _ := tc.entries.Peek("t1")
	if freshFor := cached.(*TenantRateLimits).freshFor; freshFor > billingUnavailableRetryAfter {
		t.Fatalf("unavailable answer cached for %s, want at most %s", freshFor, billingUnavailableRetryAfter)
	}

	// With no known state the refusal stands, but only until the retry.
	timing.billingUnavailableRetry = 20 * time.Millisecond
	v2 := &scriptedTenantValidator{responses: []*quartermasterpb.ValidateTenantResponse{unavailable, good}}
	tc2 := newTenantCache(v2, logging.NewLogger(), timing)
	if _, err := tc2.GetBillingAccessStatus("t2"); err == nil {
		t.Fatal("unknown billing state must not admit rated work")
	}
	time.Sleep(2 * timing.billingUnavailableRetry)
	if _, err := tc2.GetBillingAccessStatus("t2"); err != nil {
		t.Fatalf("billing state not re-read after the retry delay: %v", err)
	}
}

// Staging: a tenant moved to payg and given credit kept getting the postpaid
// 402 for five minutes, the postpaid cache TTL. A refusal re-reads the tenant.
func TestRefusedRequestRereadsBillingStatus(t *testing.T) {
	unfunded := &quartermasterpb.ValidateTenantResponse{Valid: true, BillingModel: "postpaid", TierName: "supporter"}
	funded := &quartermasterpb.ValidateTenantResponse{Valid: true, BillingModel: "prepaid", TierName: "payg"}
	v := &scriptedTenantValidator{responses: []*quartermasterpb.ValidateTenantResponse{unfunded, funded}}
	tc := NewTenantCache(v, logging.NewLogger())
	status, err := tc.GetBillingAccessStatus("t1")
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	req := AccessRequest{TenantID: "t1", OperationName: "CreateClip", OperationNames: []string{"createClip"}, OperationType: "mutation"}
	if !billingStatusRefuses(status, req, false) {
		t.Fatal("a postpaid paid tier without collection must refuse rated work")
	}
	fresh, err := tc.RefreshBillingAccessStatus("t1")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fresh.BillingModel != "prepaid" || billingStatusRefuses(fresh, req, false) {
		t.Fatalf("refresh kept the stale refusal: %+v", fresh)
	}
	// A client retrying a refused request is answered from the cache until the
	// refresh interval passes.
	if _, err := tc.RefreshBillingAccessStatus("t1"); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if v.calls != 2 {
		t.Fatalf("Quartermaster called %d times, want 2 (refresh throttled)", v.calls)
	}
}
