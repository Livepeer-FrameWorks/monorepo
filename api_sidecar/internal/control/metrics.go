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

	// TriggersDropped tracks events that were dropped without being sent
	// Labels: trigger_type, reason: "stream_disconnected", "send_error", "channel_full"
	TriggersDropped = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "triggers_dropped_total",
			Help:      "Total MistTrigger events dropped due to errors",
		},
		[]string{"trigger_type", "reason"},
	)

	// BillingEventsSent tracks ProcessBillingEvent events specifically
	// Labels: process_type: "Livepeer", "AV"
	//         status: "success", "error", "stream_disconnected"
	BillingEventsSent = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "helmsman",
			Name:      "billing_events_sent_total",
			Help:      "Total ProcessBillingEvent events sent to Foghorn",
		},
		[]string{"process_type", "status"},
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
)
