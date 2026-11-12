package sessionrevocations

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/prometheus/client_golang/prometheus"
)

func (f *Feed) Observe(metrics *monitoring.MetricsCollector) {
	for _, metric := range []struct {
		name, help string
		value      func() float64
	}{
		{"auth_session_revocation_feed_connected", "Whether the service-only revocation stream is connected.", func() float64 {
			if f.connected.Load() {
				return 1
			}
			return 0
		}},
		{"auth_session_revocation_snapshot_synchronized", "Whether the last snapshot included named-session revocations.", func() float64 {
			if f.synchronized.Load() {
				return 1
			}
			return 0
		}},
		{"auth_session_revocation_snapshot_timestamp_seconds", "Unix time of the last successful complete revocation snapshot.", func() float64 { return float64(f.lastSuccess.Load()) }},
		{"auth_session_revocation_entries", "Known user-wide and named-session revocation entries.", func() float64 { return float64(f.cache.Len()) }},
	} {
		metrics.RegisterCustomMetric(metric.name, prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: metric.name, Help: metric.help}, metric.value))
	}
	metrics.RegisterCustomMetric("auth_session_revocation_feed_failures_total", prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "auth_session_revocation_feed_failures_total", Help: "Background revocation stream and snapshot failures.",
	}, func() float64 { return float64(f.failures.Load()) }))
}
