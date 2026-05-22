package metrics

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds all Prometheus metrics for the analytics query service.
// Per-RPC counts + duration live on GRPCRequests / GRPCDuration via
// GRPCMetricsInterceptor; ClickHouse-level query telemetry is intentionally
// not tracked here until there is a single QueryContext chokepoint to
// instrument.
type Metrics struct {
	CursorCollisions *prometheus.CounterVec
	GRPCRequests     *prometheus.CounterVec
	GRPCDuration     *prometheus.HistogramVec
}
