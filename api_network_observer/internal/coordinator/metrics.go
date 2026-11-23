package coordinator

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Const metrics keep counter lifetimes tied to durable prober state rather than scrapes.
type observationMetrics struct {
	values                                                       []prometheus.Metric
	health, inboxPending, inboxOldest, inboxRejected, lastWork   *prometheus.Desc
	proberPending, dropped, quarantined, clockSkew, proberOldest *prometheus.Desc
}

func observationDesc(opts prometheus.Opts, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName(opts.Namespace, opts.Subsystem, opts.Name), opts.Help, labels, opts.ConstLabels)
}

func newObservationMetrics() *observationMetrics {
	return &observationMetrics{
		health:        observationDesc(prometheus.Opts{Name: "network_observer_health_read_success", Help: "Whether the current collection-health snapshot was read successfully."}),
		inboxPending:  observationDesc(prometheus.Opts{Name: "network_observer_inbox_pending_records", Help: "Durably received observations waiting for application."}, "observer"),
		inboxOldest:   observationDesc(prometheus.Opts{Name: "network_observer_inbox_oldest_pending_seconds", Help: "Age of the oldest observation waiting for application."}, "observer"),
		inboxRejected: observationDesc(prometheus.Opts{Name: "network_observer_inbox_rejected_records", Help: "Retained terminal rejection evidence."}, "observer", "code"),
		lastWork:      observationDesc(prometheus.Opts{Name: "network_observer_prober_last_work_timestamp_seconds", Help: "Most recent successful prober control poll."}, "observer", "region"),
		proberPending: observationDesc(prometheus.Opts{Name: "network_observer_prober_pending_records", Help: "Observations pending delivery on the prober."}, "observer", "region"),
		dropped:       observationDesc(prometheus.Opts{Name: "network_observer_prober_dropped_records_total", Help: "Durable count of observations dropped on the prober."}, "observer", "region"),
		quarantined:   observationDesc(prometheus.Opts{Name: "network_observer_prober_quarantined_records_total", Help: "Durable count of observations quarantined on the prober."}, "observer", "region"),
		clockSkew:     observationDesc(prometheus.Opts{Name: "network_observer_prober_clock_skew_seconds", Help: "Minimum clock offset proven by the control request interval."}, "observer", "region"),
		proberOldest:  observationDesc(prometheus.Opts{Name: "network_observer_prober_oldest_pending_seconds", Help: "Age of the oldest observation pending delivery on the prober."}, "observer", "region"),
	}
}

func (m *observationMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, metric := range m.values {
		ch <- metric.Desc()
	}
}

func (m *observationMetrics) Collect(ch chan<- prometheus.Metric) {
	for _, metric := range m.values {
		ch <- metric
	}
}

func (m *observationMetrics) add(desc *prometheus.Desc, kind prometheus.ValueType, value float64, labels ...string) {
	m.values = append(m.values, prometheus.MustNewConstMetric(desc, kind, value, labels...))
}

// Collection health is independent of intake readiness so a backlog can still drain.
func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	metrics := newObservationMetrics()
	health := float64(0)
	if a.collectMetrics(r.Context(), metrics) == nil {
		health = 1
	}
	metrics.add(metrics.health, prometheus.GaugeValue, health)
	registry := prometheus.NewRegistry()
	registry.MustRegister(metrics)
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (a *API) collectMetrics(ctx context.Context, metrics *observationMetrics) error {
	if readErr := a.pendingMetrics(ctx, metrics); readErr != nil {
		return readErr
	}
	if readErr := a.rejectionMetrics(ctx, metrics); readErr != nil {
		return readErr
	}
	observers, err := a.Store.Observers(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, o := range observers {
		if o.Kind != "prober" {
			continue
		}
		metrics.add(metrics.lastWork, prometheus.GaugeValue, float64(o.LastSeen.Unix()), o.ID, o.Region)
		if o.Delivery == nil {
			continue
		}
		d := o.Delivery
		metrics.add(metrics.proberPending, prometheus.GaugeValue, float64(d.PendingRecords), o.ID, o.Region)
		metrics.add(metrics.dropped, prometheus.CounterValue, float64(d.DroppedRecords), o.ID, o.Region)
		metrics.add(metrics.quarantined, prometheus.CounterValue, float64(d.QuarantinedRecords), o.ID, o.Region)
		metrics.add(metrics.clockSkew, prometheus.GaugeValue, d.ClockSkewSeconds, o.ID, o.Region)
		age := float64(0)
		if !d.OldestPending.IsZero() {
			age = max(0, now.Sub(d.OldestPending).Seconds())
		}
		metrics.add(metrics.proberOldest, prometheus.GaugeValue, age, o.ID, o.Region)
	}
	return nil
}

func (a *API) pendingMetrics(ctx context.Context, metrics *observationMetrics) error {
	rows, err := a.Store.DB.QueryContext(ctx, `SELECT observer_id,count(*),extract(epoch FROM now()-min(received_at)) FROM network_observer.observation_inbox WHERE tenant_id=$1 AND status='pending' GROUP BY observer_id`, a.Store.TenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var count int64
		var age float64
		if scanErr := rows.Scan(&id, &count, &age); scanErr != nil {
			return scanErr
		}
		metrics.add(metrics.inboxPending, prometheus.GaugeValue, float64(count), id)
		metrics.add(metrics.inboxOldest, prometheus.GaugeValue, max(0, age), id)
	}
	return rows.Err()
}

func (a *API) rejectionMetrics(ctx context.Context, metrics *observationMetrics) error {
	rows, err := a.Store.DB.QueryContext(ctx, `SELECT observer_id,error_code,count(*) FROM network_observer.observation_inbox WHERE tenant_id=$1 AND status='rejected' GROUP BY observer_id,error_code`, a.Store.TenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, code string
		var count int64
		if scanErr := rows.Scan(&id, &code, &count); scanErr != nil {
			return scanErr
		}
		metrics.add(metrics.inboxRejected, prometheus.GaugeValue, float64(count), id, code)
	}
	return rows.Err()
}
