// Package metrics holds Bosun's domain metrics. No metric carries a tenant,
// endpoint, or event ID label.
package metrics

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are Bosun's domain metrics. A nil *Metrics records nothing.
type Metrics struct {
	DomainEvents      *prometheus.CounterVec
	DeliveriesCreated prometheus.Counter
	Attempts          *prometheus.CounterVec
	AttemptDuration   prometheus.Observer
	Terminal          *prometheus.CounterVec
	AutoDisabled      prometheus.Counter
	InternalErrors    *prometheus.CounterVec
	Pruned            *prometheus.CounterVec
	OldestDue         prometheus.Gauge
	Replays           *prometheus.CounterVec
	KafkaLag          *prometheus.GaugeVec
	GRPCRequests      *prometheus.CounterVec
	GRPCDuration      *prometheus.HistogramVec
}

// New registers Bosun's domain metrics on collector, the bosun service
// collector, so every name is prefixed bosun_.
func New(collector *monitoring.MetricsCollector) *Metrics {
	return &Metrics{
		DomainEvents:      collector.NewCounter("domain_events_total", "domain.events records by handling result", []string{"result"}),
		DeliveriesCreated: collector.NewCounter("deliveries_created_total", "Webhook deliveries created by event fan-out", nil).WithLabelValues(),
		Attempts:          collector.NewCounter("delivery_attempts_total", "Webhook delivery attempts by kind, outcome, and error class", []string{"kind", "outcome", "error_class"}),
		AttemptDuration:   collector.NewHistogram("delivery_attempt_duration_seconds", "Duration of webhook HTTP attempts", nil, []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}).WithLabelValues(),
		Terminal:          collector.NewCounter("deliveries_finished_total", "Webhook deliveries that reached a final status", []string{"status"}),
		AutoDisabled:      collector.NewCounter("endpoints_auto_disabled_total", "Webhook endpoints disabled after sustained failure", nil).WithLabelValues(),
		InternalErrors:    collector.NewCounter("internal_errors_total", "Bosun-side failures that left a claimed delivery unsent, by stage", []string{"stage"}),
		Pruned:            collector.NewCounter("pruned_rows_total", "Ledger rows deleted by retention, by table", []string{"table"}),
		OldestDue:         collector.NewGauge("oldest_due_delivery_seconds", "Seconds the oldest due pending webhook delivery has waited; 0 when none is due", nil).WithLabelValues(),
		Replays:           collector.NewCounter("replays_total", "Deliveries returned to pending by replay, by mode", []string{"mode"}),
		KafkaLag:          collector.NewGauge("kafka_consumer_lag", "Kafka consumer lag of the domain event group", []string{"topic", "partition"}),
		GRPCRequests:      collector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:      collector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}
}

// NewCollector returns the bosun service collector the domain metrics and
// the HTTP and gRPC metrics register on.
func NewCollector(version, commit string) *monitoring.MetricsCollector {
	return monitoring.NewMetricsCollector("bosun", version, commit)
}

// DomainEvent counts one domain.events record by result.
func (m *Metrics) DomainEvent(result string) {
	if m != nil && m.DomainEvents != nil {
		m.DomainEvents.WithLabelValues(result).Inc()
	}
}

// Created counts deliveries created by fan-out.
func (m *Metrics) Created(n int) {
	if m != nil && m.DeliveriesCreated != nil && n > 0 {
		m.DeliveriesCreated.Add(float64(n))
	}
}

// Attempt records one HTTP attempt.
func (m *Metrics) Attempt(kind string, success bool, errorClass string, seconds float64) {
	if m == nil {
		return
	}
	outcome := "failure"
	if success {
		outcome = "success"
	}
	if m.Attempts != nil {
		m.Attempts.WithLabelValues(kind, outcome, errorClass).Inc()
	}
	if m.AttemptDuration != nil {
		m.AttemptDuration.Observe(seconds)
	}
}

// Finished counts a delivery that reached a final status.
func (m *Metrics) Finished(status string) {
	if m != nil && m.Terminal != nil {
		m.Terminal.WithLabelValues(status).Inc()
	}
}

// Disabled counts an automatic endpoint disable.
func (m *Metrics) Disabled() {
	if m != nil && m.AutoDisabled != nil {
		m.AutoDisabled.Inc()
	}
}

// Internal counts a Bosun-side failure at stage.
func (m *Metrics) Internal(stage string) {
	if m != nil && m.InternalErrors != nil {
		m.InternalErrors.WithLabelValues(stage).Inc()
	}
}

// Prune counts deleted rows of table.
func (m *Metrics) Prune(table string, n int64) {
	if m != nil && m.Pruned != nil && n > 0 {
		m.Pruned.WithLabelValues(table).Add(float64(n))
	}
}

// SetOldestDue publishes the oldest due delivery age.
func (m *Metrics) SetOldestDue(seconds float64) {
	if m != nil && m.OldestDue != nil {
		m.OldestDue.Set(seconds)
	}
}

// Replay counts replayed deliveries.
func (m *Metrics) Replay(mode string, n int) {
	if m != nil && m.Replays != nil && n > 0 {
		m.Replays.WithLabelValues(mode).Add(float64(n))
	}
}
