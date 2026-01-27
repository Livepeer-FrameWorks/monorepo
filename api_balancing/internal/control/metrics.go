package control

import "github.com/prometheus/client_golang/prometheus"

// ControlMetrics holds Prometheus metrics for the HelmsmanControl stream ingress.
type ControlMetrics struct {
	// MistTriggers counts MistTrigger messages received/processed over the HelmsmanControl stream.
	// Labels: trigger_type, blocking ("true"|"false"), status
	MistTriggers *prometheus.CounterVec
}

var controlMetrics *ControlMetrics

// SetMetrics configures optional Prometheus metrics for the control server.
func SetMetrics(m *ControlMetrics) {
	controlMetrics = m
}

func incMistTrigger(triggerType string, blocking bool, status string) {
	if controlMetrics == nil || controlMetrics.MistTriggers == nil {
		return
	}
	b := "false"
	if blocking {
		b = "true"
	}
	controlMetrics.MistTriggers.WithLabelValues(triggerType, b, status).Inc()
}
