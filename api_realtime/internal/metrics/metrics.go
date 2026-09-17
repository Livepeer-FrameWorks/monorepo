package metrics

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds all Prometheus metrics for the Signalman service
type Metrics struct {
	// WebSocket Hub metrics
	HubConnections     *prometheus.GaugeVec
	HubMessages        *prometheus.CounterVec
	EventsPublished    *prometheus.CounterVec
	MessageDeliveryLag *prometheus.HistogramVec

	// Kafka metrics
	KafkaMessages        *prometheus.CounterVec
	KafkaDuration        *prometheus.HistogramVec
	KafkaLag             *prometheus.GaugeVec
	KafkaDuplicateEvents *prometheus.CounterVec
}

// RecordDuplicateEvent counts a Kafka event dropped because its event ID was
// already broadcast. Labels: topic.
func (m *Metrics) RecordDuplicateEvent(topic string) {
	if m == nil || m.KafkaDuplicateEvents == nil {
		return
	}
	m.KafkaDuplicateEvents.WithLabelValues(topic).Inc()
}
