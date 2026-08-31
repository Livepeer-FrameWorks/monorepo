package control

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Helmsman metrics for tracking event emission to Foghorn
var (
	// TriggersSent tracks all MistTrigger events sent to Foghorn
	// Labels: trigger_type (e.g., "PUSH_REWRITE", "USER_NEW", "process_billing")
	//         status: "sent", "send_error", "stream_disconnected", "exhausted"
	TriggersSent = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "triggers_sent_total",
			Help:      "Total MistTrigger events sent to Foghorn",
		},
		[]string{"trigger_type", "status"},
	)

	// BlockingTriggerRetries tracks retry attempts for blocking triggers
	// Labels: trigger_type, reason: "stream_disconnected", "send_error"
	BlockingTriggerRetries = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "blocking_trigger_retries_total",
			Help:      "Total retry attempts for blocking triggers",
		},
		[]string{"trigger_type", "reason"},
	)

	// TriggersDropped tracks asynchronous control observations that were dropped
	// before reaching Foghorn. Labels: trigger_type, reason.
	TriggersDropped = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "triggers_dropped_total",
			Help:      "Total asynchronous control observations dropped before delivery",
		},
		[]string{"trigger_type", "reason"},
	)

	// ControlStreamStatus tracks the current connection state to Foghorn
	// Value: 1 = connected, 0 = disconnected
	ControlStreamStatus = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "helmsman",
			Name:      "control_stream_connected",
			Help:      "Whether Helmsman is connected to Foghorn control stream (1=connected, 0=disconnected)",
		},
	)

	ControlOutboxPending = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "helmsman",
			Name:      "control_outbox_pending",
			Help:      "Number of durable media-control transitions pending or awaiting connection confirmation",
		},
	)

	ControlOutboxBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "helmsman",
			Name:      "control_outbox_bytes",
			Help:      "Bytes occupied by pending or unconfirmed durable media-control transitions",
		},
	)

	ControlOutboxQuarantined = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "helmsman",
			Name:      "control_outbox_quarantined",
			Help:      "Number of corrupt durable media-control rows quarantined for bounded operator inspection",
		},
	)

	ControlOutboxQuarantinedBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "helmsman",
			Name:      "control_outbox_quarantined_bytes",
			Help:      "Bytes occupied by quarantined durable media-control rows",
		},
	)

	ControlDeliveryOutcomes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "control_delivery_outcomes_total",
			Help:      "Control-message delivery outcomes by durability class",
		},
		[]string{"class", "outcome"},
	)

	ControlResponseDrops = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "control_response_drops_total",
			Help:      "Late or duplicate control responses discarded by non-blocking handoff",
		},
		[]string{"reason"},
	)

	ControlOutboxScanErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "control_outbox_scan_errors_total",
			Help:      "Durable control outbox read, decode, metric, and quarantine errors",
		},
		[]string{"phase"},
	)

	// TriggerWALPending is the canonical "is anything stuck?" signal for
	// the durable forwarder. Sampled by the forwarder loop on every tick;
	// also updated on every Append/Ack so quick incidents don't have to
	// wait for the next tick to be visible.
	TriggerWALPending = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "helmsman",
			Name:      "trigger_wal_pending",
			Help:      "Number of durable Mist triggers awaiting positive ack from Foghorn",
		},
	)

	// TriggerWALAppends counts WAL writes from Helmsman's HTTP handlers,
	// before any forward attempt. Labels: trigger_type, status:"appended"
	// (fresh source_event_id), "duplicate" (idempotent re-delivery from
	// Mist), "error" (fsync/write failure).
	TriggerWALAppends = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "trigger_wal_appends_total",
			Help:      "Durable trigger WAL append outcomes",
		},
		[]string{"trigger_type", "status"},
	)

	// TriggerAckOutcomes counts MistTriggerAck messages received from
	// Foghorn. Labels: trigger_type, outcome:"success" / "retryable" /
	// "non_retryable" / "timeout".
	TriggerAckOutcomes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "trigger_ack_outcomes_total",
			Help:      "Durable MistTriggerAck outcomes received from Foghorn",
		},
		[]string{"trigger_type", "outcome"},
	)
)
