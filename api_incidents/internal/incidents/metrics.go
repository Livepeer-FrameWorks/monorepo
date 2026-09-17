package incidents

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are Lookout's domain counters. A nil *Metrics records nothing.
type Metrics struct {
	Webhooks         *prometheus.CounterVec
	Transitions      *prometheus.CounterVec
	ScopeLookups     *prometheus.CounterVec
	Deliveries       *prometheus.CounterVec
	Realtime         *prometheus.CounterVec
	OutboxDeleted    *prometheus.CounterVec
	Ownership        *prometheus.CounterVec
	OperatorActivity *prometheus.CounterVec
}

// NewMetrics registers Lookout's domain counters on the service collector.
func NewMetrics(collector *monitoring.MetricsCollector) *Metrics {
	return &Metrics{
		Webhooks:         collector.NewCounter("alertmanager_webhooks_total", "Alertmanager webhook notifications by result", []string{"result"}),
		Transitions:      collector.NewCounter("incident_transitions_total", "Incident timeline transitions by scope and kind", []string{"scope", "transition"}),
		ScopeLookups:     collector.NewCounter("scope_lookups_total", "Cluster-to-incident-scope resolutions by result", []string{"result"}),
		Deliveries:       collector.NewCounter("deliveries_total", "Outbox delivery attempts and terminal failures by channel and result", []string{"channel", "result"}),
		Realtime:         collector.NewCounter("realtime_events_total", "Tenant incident realtime events sent to Decklog by result", []string{"result"}),
		OutboxDeleted:    collector.NewCounter("outbox_rows_deleted_total", "Settled delivery outbox rows deleted by retention, by settlement state", []string{"state"}),
		Ownership:        collector.NewCounter("ownership_reconciles_total", "Cluster ownership reconciliations of open incidents by source and result", []string{"source", "result"}),
		OperatorActivity: collector.NewCounter("operator_activity_events_total", "Selected operator activity events by type and result", []string{"event_type", "result"}),
	}
}

// ObserveOperatorActivity records selection and durable enqueue results for an
// allowlisted service event.
func (m *Metrics) ObserveOperatorActivity(eventType, result string) {
	if m != nil && m.OperatorActivity != nil {
		m.OperatorActivity.WithLabelValues(eventType, result).Inc()
	}
}

func (m *Metrics) ObserveWebhook(result string) {
	if m != nil && m.Webhooks != nil {
		m.Webhooks.WithLabelValues(result).Inc()
	}
}

func (m *Metrics) observeTransition(scope, transition string) {
	if m != nil && m.Transitions != nil {
		m.Transitions.WithLabelValues(scope, transition).Inc()
	}
}

func (m *Metrics) observeScopeLookup(result string) {
	if m != nil && m.ScopeLookups != nil {
		m.ScopeLookups.WithLabelValues(result).Inc()
	}
}

// ObserveDelivery records one outbox dispatch attempt or a terminal failure.
func (m *Metrics) ObserveDelivery(channel, result string) {
	if m != nil && m.Deliveries != nil {
		m.Deliveries.WithLabelValues(channel, result).Inc()
	}
}

// ObserveOutboxDeleted records rows removed by outbox retention; activity
// states are prefixed to distinguish them from incident delivery rows.
func (m *Metrics) ObserveOutboxDeleted(state string, rows int64) {
	if m != nil && m.OutboxDeleted != nil && rows > 0 {
		m.OutboxDeleted.WithLabelValues(state).Add(float64(rows))
	}
}

// ObserveOwnershipReconcile records one cluster ownership reconciliation;
// source is event or startup.
func (m *Metrics) ObserveOwnershipReconcile(source, result string) {
	if m != nil && m.Ownership != nil {
		m.Ownership.WithLabelValues(source, result).Inc()
	}
}

func (m *Metrics) observeRealtime(result string) {
	if m != nil && m.Realtime != nil {
		m.Realtime.WithLabelValues(result).Inc()
	}
}
