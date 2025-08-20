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
	KafkaMessages *prometheus.CounterVec
	KafkaDuration *prometheus.HistogramVec
	KafkaLag      *prometheus.GaugeVec
}
