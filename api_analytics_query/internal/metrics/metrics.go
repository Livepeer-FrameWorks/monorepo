package metrics

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds all Prometheus metrics for the analytics query service.
// Periscope-query has no Postgres dependency; ClickHouse query counters
// are tracked separately from database/sql connection-pool stats.
type Metrics struct {
	AnalyticsQueries  *prometheus.CounterVec
	QueryDuration     *prometheus.HistogramVec
	ClickHouseQueries *prometheus.CounterVec
	CursorCollisions  *prometheus.CounterVec
}
