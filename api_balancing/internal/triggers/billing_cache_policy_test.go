package triggers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/prometheus/client_golang/prometheus"
)

type countingTenantAdmission struct {
	calls    atomic.Int32
	response *quartermasterpb.ValidateTenantResponse
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (f *countingTenantAdmission) ValidateTenant(ctx context.Context, _, _ string) (*quartermasterpb.ValidateTenantResponse, error) {
	f.calls.Add(1)
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.response, nil
}

func TestBillingCacheGraceAndExplicitInvalidation(t *testing.T) {
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_billing_cache_events_total"}, []string{"outcome"})
	p.SetMetrics(&ProcessorMetrics{BillingCacheEvents: events})
	p.billingCache.Set("tenant-1", &BillingStatus{
		TenantID: "tenant-1", BillingModel: "postpaid", State: BillingStatusHealthy,
	}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	status := p.GetBillingStatus(context.Background(), "stream-1", "tenant-1")
	if status.State != BillingStatusStaleValid || !status.FromCache {
		t.Fatalf("status = %+v, want stale-valid cached decision", status)
	}
	if got := counterValue(t, events.WithLabelValues("stale_served")); got != 1 {
		t.Fatalf("stale events = %v, want 1", got)
	}

	p.InvalidateTenantCache("tenant-1")
	status = p.GetBillingStatus(context.Background(), "stream-1", "tenant-1")
	if status.State != BillingStatusUnavailable {
		t.Fatalf("status after invalidation = %+v, want unavailable", status)
	}
	if got := counterValue(t, events.WithLabelValues("invalidated")); got != 1 {
		t.Fatalf("invalidation events = %v, want 1", got)
	}
}

func TestBillingCacheSWRDefault(t *testing.T) {
	t.Setenv("BILLING_CACHE_SWR", "")
	if got := billingCacheSWR(); got != 5*time.Minute {
		t.Fatalf("billingCacheSWR = %s, want 5m", got)
	}
}

func TestBillingDeniedCacheTTLDefault(t *testing.T) {
	t.Setenv("BILLING_DENIED_CACHE_TTL", "")
	if got := billingDeniedTTL(); got != 30*time.Second {
		t.Fatalf("billingDeniedTTL = %s, want 30s", got)
	}
}

func TestBillingCacheCollapsesConcurrentColdFill(t *testing.T) {
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	fake := &countingTenantAdmission{
		response: &quartermasterpb.ValidateTenantResponse{Valid: true, IsActive: true, BillingModel: "postpaid"},
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	p.tenantAdmission = fake

	const callers = 100
	start := make(chan struct{})
	results := make(chan *BillingStatus, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			results <- p.GetBillingStatus(context.Background(), "stream-1", "tenant-1")
		}()
	}
	ready.Wait()
	close(start)
	<-fake.started
	time.Sleep(25 * time.Millisecond)
	close(fake.release)

	for range callers {
		status := <-results
		if status.State != BillingStatusHealthy {
			t.Fatalf("status = %+v, want healthy", status)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("authority calls = %d, want one collapsed cold fill", got)
	}
}

func TestBillingCacheKeepsDeniedDecisionUntilInvalidated(t *testing.T) {
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_billing_denied_cache_events_total"}, []string{"outcome"})
	p.SetMetrics(&ProcessorMetrics{BillingCacheEvents: events})
	fake := &countingTenantAdmission{
		response: &quartermasterpb.ValidateTenantResponse{Valid: false, IsActive: false, BillingModel: "prepaid"},
	}
	p.tenantAdmission = fake

	for range 20 {
		status := p.GetBillingStatus(context.Background(), "stream-1", "tenant-1")
		if status.State != BillingStatusDenied {
			t.Fatalf("status = %+v, want denied", status)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("authority calls for repeated denial = %d, want 1", got)
	}
	if got := counterValue(t, events.WithLabelValues("fresh_deny")); got != 1 {
		t.Fatalf("fresh deny events = %v, want 1", got)
	}
	if got := counterValue(t, events.WithLabelValues("cached_deny")); got != 19 {
		t.Fatalf("cached deny events = %v, want 19", got)
	}

	p.InvalidateTenantCache("tenant-1")
	if status := p.GetBillingStatus(context.Background(), "stream-1", "tenant-1"); status.State != BillingStatusDenied {
		t.Fatalf("status after invalidation = %+v, want denied", status)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("authority calls after invalidation = %d, want 2", got)
	}
	if got := counterValue(t, events.WithLabelValues("invalidated")); got != 1 {
		t.Fatalf("invalidation events = %v, want 1", got)
	}
}

func TestBillingCacheDoesNotTrustPartialIdentityContext(t *testing.T) {
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	fake := &countingTenantAdmission{
		response: &quartermasterpb.ValidateTenantResponse{Valid: true, IsActive: true, BillingModel: "postpaid"},
	}
	p.tenantAdmission = fake
	p.streamCache.Set("tenant-1:stream-1", streamContext{
		TenantID: "tenant-1",
		StreamID: "stream-id-1",
		Source:   "resolve_internal_name",
	}, time.Minute)

	status := p.GetBillingStatus(context.Background(), "stream-1", "tenant-1")
	if status.State != BillingStatusHealthy || status.BillingModel != "postpaid" {
		t.Fatalf("status = %+v, want owner-resolved postpaid decision", status)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("authority calls = %d, want partial identity context to fall through once", got)
	}
}

func TestBillingCacheDeniedDecisionRecoversWithoutInvalidation(t *testing.T) {
	t.Setenv("BILLING_DENIED_CACHE_TTL", "20ms")
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_billing_denied_expiry_events_total"}, []string{"outcome"})
	p.SetMetrics(&ProcessorMetrics{BillingCacheEvents: events})
	fake := &countingTenantAdmission{
		response: &quartermasterpb.ValidateTenantResponse{Valid: false, IsActive: false, BillingModel: "prepaid"},
	}
	p.tenantAdmission = fake

	if status := p.GetBillingStatus(context.Background(), "stream-1", "tenant-1"); status.State != BillingStatusDenied {
		t.Fatalf("initial status = %+v, want denied", status)
	}
	fake.response = &quartermasterpb.ValidateTenantResponse{Valid: true, IsActive: true, BillingModel: "postpaid"}
	time.Sleep(25 * time.Millisecond)

	const callers = 50
	start := make(chan struct{})
	results := make(chan *BillingStatus, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			results <- p.GetBillingStatus(context.Background(), "stream-1", "tenant-1")
		}()
	}
	ready.Wait()
	close(start)
	for range callers {
		if status := <-results; status.State != BillingStatusHealthy {
			t.Fatalf("recovered status = %+v, want healthy", status)
		}
	}
	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("authority calls = %d, want initial deny plus one collapsed recovery", got)
	}
	if got := counterValue(t, events.WithLabelValues("deny_expired")); got < 1 {
		t.Fatalf("deny expired events = %v, want at least 1", got)
	}
}

func TestBillingAuthoritySharedFillOutlivesShortCaller(t *testing.T) {
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	fake := &countingTenantAdmission{
		response: &quartermasterpb.ValidateTenantResponse{Valid: true, IsActive: true, BillingModel: "postpaid"},
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	p.tenantAdmission = fake

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	shortResult := make(chan *BillingStatus, 1)
	go func() {
		shortResult <- p.GetBillingStatus(ctx, "stream-1", "tenant-1")
	}()
	<-fake.started

	longResult := make(chan *BillingStatus, 1)
	go func() {
		longResult <- p.GetBillingStatus(context.Background(), "stream-1", "tenant-1")
	}()

	status := <-shortResult
	if status.State != BillingStatusUnavailable {
		t.Fatalf("status = %+v, want unavailable", status)
	}
	close(fake.release)
	if status := <-longResult; status.State != BillingStatusHealthy {
		t.Fatalf("long follower status = %+v, want healthy", status)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("authority calls = %d, want one shared fill", got)
	}
}
