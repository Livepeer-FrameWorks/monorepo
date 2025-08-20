package metrics

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds all Prometheus metrics for the analytics query service
type Metrics struct {
	AnalyticsQueries  *prometheus.CounterVec
	QueryDuration     *prometheus.HistogramVec
	ClickHouseQueries *prometheus.CounterVec
	PostgresQueries   *prometheus.CounterVec
	DBDuration        *prometheus.HistogramVec
	DBConnections     *prometheus.GaugeVec
}
