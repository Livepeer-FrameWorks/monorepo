package control

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPublisherRecoveryOldestPendingMetricSurvivesOutageAndClearsOnEmpty(t *testing.T) {
	previous := controlMetrics
	t.Cleanup(func() { controlMetrics = previous })
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_publisher_recovery_oldest_pending_timestamp_seconds"}, []string{"queue"})
	controlMetrics = &ControlMetrics{PublisherRecoveryOldestPending: gauge}
	oldest := time.Unix(1000, 0)
	observePublisherRecoveryQueue("settlement", oldest)
	if got := testutil.ToFloat64(gauge.WithLabelValues("settlement")); got != 1000 {
		t.Fatalf("oldest timestamp=%v, want 1000 so age continues increasing without successful polls", got)
	}
	observePublisherRecoveryQueue("settlement", time.Time{})
	if got := testutil.ToFloat64(gauge.WithLabelValues("settlement")); got != 0 {
		t.Fatalf("empty queue retained timestamp %v", got)
	}
	for _, queue := range []string{"settlement", "abandonment", "lease_release"} {
		observePublisherRecoveryQueue(queue, oldest)
	}
	ClearPublisherRecoveryBacklog()
	for _, queue := range []string{"settlement", "abandonment", "lease_release"} {
		if got := testutil.ToFloat64(gauge.WithLabelValues(queue)); got != 0 {
			t.Fatalf("follower advertises %s backlog timestamp %v", queue, got)
		}
	}
}
