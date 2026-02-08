package handlers

import "github.com/prometheus/client_golang/prometheus"

type FormMetrics struct {
	ContactRequests   *prometheus.CounterVec
	SubscribeRequests *prometheus.CounterVec
}

func (m *FormMetrics) IncContact(status string) {
	if m == nil || m.ContactRequests == nil {
		return
	}

	m.ContactRequests.WithLabelValues(status).Inc()
}

func (m *FormMetrics) IncSubscribe(status string) {
	if m == nil || m.SubscribeRequests == nil {
		return
	}

	m.SubscribeRequests.WithLabelValues(status).Inc()
}
