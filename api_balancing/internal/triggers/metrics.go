package triggers

import "github.com/prometheus/client_golang/prometheus"

// ProcessorMetrics holds optional Prometheus metrics for trigger processing and fan-out.
type ProcessorMetrics struct {
	// DecklogTriggerSends counts attempts and results when forwarding MistTriggers to Decklog.
	// Labels: trigger_type, status
	DecklogTriggerSends *prometheus.CounterVec

	// ServiceResolutionRejected counts service-discovery resolutions that ended without
	// a usable target. Labels: reason ("service_unavailable"), service ("livepeer-gateway").
	ServiceResolutionRejected *prometheus.CounterVec

	// PlaybackDenyTotal counts every USER_NEW deny by structured reason. Used to
	// distinguish customer-side outages from attack traffic in alerting.
	// Labels: reason (bounded enum: jwt-expired, webhook-timeout, policy-fetch-failed, ...).
	PlaybackDenyTotal *prometheus.CounterVec

	// PlaybackWebhookErrors counts webhook-specific failures separately so an
	// on-call alert can route customer-webhook outages without firing on
	// JWT/policy denials. Bumped only when the deny reason starts with "webhook-".
	// Labels: class (suffix after "webhook-": timeout, blocked-ssrf, deny-403, ...).
	PlaybackWebhookErrors *prometheus.CounterVec
}
