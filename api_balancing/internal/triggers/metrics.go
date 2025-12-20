package triggers

import "github.com/prometheus/client_golang/prometheus"

// ProcessorMetrics holds optional Prometheus metrics for trigger processing and fan-out.
type ProcessorMetrics struct {
	// DecklogTriggerSends counts attempts and results when forwarding MistTriggers to Decklog.
	// Labels: trigger_type, status
	DecklogTriggerSends *prometheus.CounterVec
}

