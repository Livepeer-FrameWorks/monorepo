package control

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPublisherRecoveryOldestPendingMetricSurvivesOutageAndClearsOnEmpty(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		previous := controlMetrics
		t.Cleanup(func() { ClearPublisherRecoveryBacklog(); controlMetrics = previous })
		gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_publisher_recovery_oldest_pending_seconds"}, []string{"queue"})
		controlMetrics = &ControlMetrics{PublisherRecoveryOldestPending: gauge}
		oldest := time.Now().Add(-5 * time.Second)
		observePublisherRecoveryQueue("settlement", oldest)
		if got := testutil.ToFloat64(gauge.WithLabelValues("settlement")); got != 5 {
			t.Fatalf("oldest pending age=%v, want 5 seconds from queue eligibility", got)
		}
		time.Sleep(3 * time.Second)
		RefreshPublisherRecoveryBacklog(time.Now())
		if got := testutil.ToFloat64(gauge.WithLabelValues("settlement")); got != 8 {
			t.Fatalf("outage froze pending age=%v, want 8 seconds without a new queue read", got)
		}
		observePublisherRecoveryQueue("settlement", time.Time{})
		if got := testutil.ToFloat64(gauge.WithLabelValues("settlement")); got != 0 {
			t.Fatalf("empty queue retained age %v", got)
		}
		observePublisherRecoveryQueue("settlement", time.Now().Add(time.Second))
		if got := testutil.ToFloat64(gauge.WithLabelValues("settlement")); got != 0 {
			t.Fatalf("future eligibility produced a negative age %v", got)
		}
		for _, queue := range []string{"settlement", "abandonment", "lease_release"} {
			observePublisherRecoveryQueue(queue, oldest)
		}
		ClearPublisherRecoveryBacklog()
		for _, queue := range []string{"settlement", "abandonment", "lease_release"} {
			if got := testutil.ToFloat64(gauge.WithLabelValues(queue)); got != 0 {
				t.Fatalf("follower advertises %s backlog age %v", queue, got)
			}
		}
	})
}
