package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
)

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
