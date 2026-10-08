package middleware

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// countingTenantValidator answers every ValidateTenant with the current
// response or error after a delay, counting calls.
type countingTenantValidator struct {
	mu    sync.Mutex
	resp  *quartermasterpb.ValidateTenantResponse
	err   error
	delay time.Duration
	calls atomic.Int64
}

func (v *countingTenantValidator) set(resp *quartermasterpb.ValidateTenantResponse, err error) {
	v.mu.Lock()
	v.resp, v.err = resp, err
	v.mu.Unlock()
}

func (v *countingTenantValidator) ValidateTenant(context.Context, string, string) (*quartermasterpb.ValidateTenantResponse, error) {
	v.calls.Add(1)
	if v.delay > 0 {
		time.Sleep(v.delay)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.resp, v.err
}

func activeTenantResponse() *quartermasterpb.ValidateTenantResponse {
	return &quartermasterpb.ValidateTenantResponse{
		Valid: true, BillingModel: "prepaid", TierName: "payg",
		RateLimitPerMinute: 600, RateLimitBurst: 60,
	}
}

// hitTenantCache runs the reads one gateway request makes (HTTP rate limit,
// HTTP billing check) from n concurrent requests at once.
func hitTenantCache(tc *TenantCache, tenantID string, n int) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tc.GetLimits(tenantID)
			_, _ = tc.GetBillingAccessStatus(tenantID)
		}()
	}
	close(start)
	wg.Wait()
}

// Production: every Bridge replica's cache entries for a tenant expired
// together and each concurrent request issued its own ValidateTenant (and so
// its own Purser admission read), a once-per-minute burst of timeouts.
func TestTenantCacheColdMissIssuesOneFetchForConcurrentRequests(t *testing.T) {
	v := &countingTenantValidator{delay: 20 * time.Millisecond}
	v.set(activeTenantResponse(), nil)
	tc := NewTenantCache(v, logging.NewLogger())

	hitTenantCache(tc, "t1", 50)

	if got := v.calls.Load(); got != 1 {
		t.Fatalf("ValidateTenant called %d times for 50 concurrent requests, want 1", got)
	}
}

// A failed lookup was not cached, so while Quartermaster was down every
// request for the tenant retried it.
func TestTenantCacheFetchErrorBacksOff(t *testing.T) {
	v := &countingTenantValidator{}
	v.set(nil, errors.New("quartermaster unavailable"))
	tc := NewTenantCache(v, logging.NewLogger())

	for range 20 {
		if _, err := tc.GetBillingAccessStatus("t1"); err == nil {
			t.Fatal("a tenant never read must not be admitted on a failed lookup")
		}
	}
	if got := v.calls.Load(); got != 1 {
		t.Fatalf("ValidateTenant called %d times within one backoff window, want 1", got)
	}
}

func shortTenantCacheTiming() tenantCacheTiming {
	timing := defaultTenantCacheTiming
	timing.ttl = 30 * time.Millisecond
	timing.staleWindow = time.Second
	timing.errorBackoff = 60 * time.Millisecond
	return timing
}

// maxFresh is the longest an answer can stay fresh under timing's jitter.
func maxFresh(timing tenantCacheTiming) time.Duration {
	return time.Duration(float64(timing.ttl) * (1 + timing.ttlJitter))
}

func waitForCalls(t *testing.T, v *countingTenantValidator, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for v.calls.Load() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Leave room for any extra call a broken cache would start.
	time.Sleep(20 * time.Millisecond)
	if got := v.calls.Load(); got != want {
		t.Fatalf("ValidateTenant called %d times, want %d", got, want)
	}
}

func TestTenantCacheExpiryIssuesOneFetchForConcurrentRequests(t *testing.T) {
	t.Run("stale window serves and refreshes once", func(t *testing.T) {
		timing := shortTenantCacheTiming()
		v := &countingTenantValidator{delay: 20 * time.Millisecond}
		v.set(activeTenantResponse(), nil)
		tc := newTenantCache(v, logging.NewLogger(), timing)
		tc.GetLimits("t1")
		time.Sleep(maxFresh(timing) + 5*time.Millisecond)

		hitTenantCache(tc, "t1", 50)
		waitForCalls(t, v, 2)
	})
	t.Run("hard expiry loads once", func(t *testing.T) {
		timing := shortTenantCacheTiming()
		timing.staleWindow = 5 * time.Millisecond
		v := &countingTenantValidator{delay: 20 * time.Millisecond}
		v.set(activeTenantResponse(), nil)
		tc := newTenantCache(v, logging.NewLogger(), timing)
		tc.GetLimits("t1")
		time.Sleep(maxFresh(timing) + timing.staleWindow + 5*time.Millisecond)

		hitTenantCache(tc, "t1", 50)
		waitForCalls(t, v, 2)
	})
}

// A Quartermaster blip is not a policy change: a tenant known to be active
// keeps its last answer and the lookup is retried once per backoff window.
func TestTenantCacheServesLastGoodAcrossFetchErrors(t *testing.T) {
	timing := shortTenantCacheTiming()
	timing.ttlJitter = 0
	timing.staleWindow = 0
	v := &countingTenantValidator{}
	v.set(activeTenantResponse(), nil)
	tc := newTenantCache(v, logging.NewLogger(), timing)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	v.set(nil, errors.New("quartermaster unavailable"))
	time.Sleep(timing.ttl + 5*time.Millisecond)

	windowEnd := time.Now().Add(timing.errorBackoff - 10*time.Millisecond)
	for time.Now().Before(windowEnd) {
		status, err := tc.GetBillingAccessStatus("t1")
		if err != nil || status.BillingModel != "prepaid" || status.IsSuspended {
			t.Fatalf("known-active tenant answered (%+v, %v) on a failed lookup", status, err)
		}
		if limit, _ := tc.GetLimits("t1"); limit != 600 {
			t.Fatalf("rate limit = %d on a failed lookup, want the last known 600", limit)
		}
		time.Sleep(time.Millisecond)
	}
	if got := v.calls.Load(); got != 2 {
		t.Fatalf("ValidateTenant called %d times within one backoff window, want 2", got)
	}

	time.Sleep(20 * time.Millisecond)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("last known answer dropped after the backoff: %v", err)
	}
	if got := v.calls.Load(); got != 3 {
		t.Fatalf("ValidateTenant called %d times after the backoff, want 3", got)
	}
}

func TestTenantCacheLastGoodExpiresUnderProlongedOutage(t *testing.T) {
	timing := shortTenantCacheTiming()
	timing.ttlJitter = 0
	timing.staleWindow = 0
	timing.lastGoodMaxAge = 40 * time.Millisecond
	v := &countingTenantValidator{}
	v.set(activeTenantResponse(), nil)
	tc := newTenantCache(v, logging.NewLogger(), timing)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	v.set(nil, errors.New("quartermaster unavailable"))
	time.Sleep(timing.lastGoodMaxAge + 10*time.Millisecond)
	if _, err := tc.GetBillingAccessStatus("t1"); err == nil {
		t.Fatal("an answer older than lastGoodMaxAge admitted rated work during an outage")
	}
}

// Serving stale while refreshing must not delay an authoritative suspension
// for an active tenant past the jittered TTL plus one lookup.
func TestTenantCacheObservesSuspensionWithinTTL(t *testing.T) {
	timing := shortTenantCacheTiming()
	timing.staleWindow = 10 * time.Second
	const lookup = 5 * time.Millisecond
	v := &countingTenantValidator{delay: lookup}
	v.set(activeTenantResponse(), nil)
	tc := newTenantCache(v, logging.NewLogger(), timing)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	suspended := activeTenantResponse()
	suspended.IsSuspended = true
	v.set(suspended, nil)
	flipped := time.Now()

	bound := maxFresh(timing) + lookup + 15*time.Millisecond
	for {
		status, err := tc.GetBillingAccessStatus("t1")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if status.IsSuspended {
			break
		}
		if elapsed := time.Since(flipped); elapsed > bound {
			t.Fatalf("suspension not observed %s after it took effect, want within %s", elapsed, bound)
		}
		time.Sleep(time.Millisecond)
	}
}

// "Not valid" is Quartermaster's answer, not a failed lookup: the last
// answer must not keep admitting the tenant afterwards.
func TestTenantCacheNotValidDropsLastGood(t *testing.T) {
	timing := shortTenantCacheTiming()
	timing.ttlJitter = 0
	timing.staleWindow = 0
	timing.errorBackoff = 10 * time.Millisecond
	v := &countingTenantValidator{}
	v.set(activeTenantResponse(), nil)
	tc := newTenantCache(v, logging.NewLogger(), timing)
	if _, err := tc.GetBillingAccessStatus("t1"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	v.set(&quartermasterpb.ValidateTenantResponse{Valid: false}, nil)
	time.Sleep(timing.ttl + 5*time.Millisecond)
	if _, err := tc.GetBillingAccessStatus("t1"); err == nil {
		t.Fatal("tenant reported not valid was still admitted")
	}
	v.set(nil, errors.New("quartermaster unavailable"))
	time.Sleep(timing.errorBackoff + 5*time.Millisecond)
	if _, err := tc.GetBillingAccessStatus("t1"); err == nil {
		t.Fatal("a failed lookup after a not-valid answer revived the earlier answer")
	}
}

// Concurrent refused requests must share one forced re-read.
func TestRefreshBillingAccessStatusIsSharedAcrossConcurrentRefusals(t *testing.T) {
	v := &countingTenantValidator{delay: 20 * time.Millisecond}
	v.set(activeTenantResponse(), nil)
	tc := NewTenantCache(v, logging.NewLogger())
	tc.GetLimits("t1")

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tc.RefreshBillingAccessStatus("t1"); err != nil {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := v.calls.Load(); got != 2 {
		t.Fatalf("ValidateTenant called %d times, want 2 (one load, one shared refresh)", got)
	}
}
