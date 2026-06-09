package monitoring

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsCollector manages Prometheus metrics for a service
type MetricsCollector struct {
	serviceName string

	// Standard HTTP metrics
	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	activeConnections   prometheus.Gauge
	serviceInfo         *prometheus.GaugeVec

	// Custom metrics registry
	customMetrics map[string]prometheus.Collector

	// Backing Prometheus registry. Defaults to the process-global default so
	// production behaviour is unchanged; tests can inject an isolated registry.
	registerer prometheus.Registerer
	gatherer   prometheus.Gatherer
}

// NewMetricsCollector creates a new metrics collector for a service, registered
// on the global default Prometheus registry.
func NewMetricsCollector(serviceName, version, commit string) *MetricsCollector {
	return newMetricsCollector(serviceName, version, commit, prometheus.DefaultRegisterer, prometheus.DefaultGatherer)
}

// NewMetricsCollectorWithRegistry builds a collector backed by an isolated
// registry instead of the global default. Tests need this: the global registry
// rejects duplicate metric names, so a test that constructs a collector cannot
// be run twice (e.g. under -count) against the global registry.
func NewMetricsCollectorWithRegistry(serviceName, version, commit string, reg *prometheus.Registry) *MetricsCollector {
	return newMetricsCollector(serviceName, version, commit, reg, reg)
}

func newMetricsCollector(serviceName, version, commit string, registerer prometheus.Registerer, gatherer prometheus.Gatherer) *MetricsCollector {
	// Sanitize service name for Prometheus (replace hyphens with underscores)
	sanitizedServiceName := strings.ReplaceAll(serviceName, "-", "_")

	mc := &MetricsCollector{
		serviceName:   sanitizedServiceName,
		customMetrics: make(map[string]prometheus.Collector),
		registerer:    registerer,
		gatherer:      gatherer,
	}

	// Standard HTTP metrics
	mc.httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: mc.serviceName + "_http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "endpoint", "status"},
	)

	mc.httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    mc.serviceName + "_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)

	mc.activeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: mc.serviceName + "_active_connections",
			Help: "Number of active connections",
		},
	)

	mc.serviceInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: mc.serviceName + "_service_info",
			Help: "Service information",
		},
		[]string{"version", "commit"},
	)

	// Register standard metrics
	mc.registerer.MustRegister(mc.httpRequestsTotal)
	mc.registerer.MustRegister(mc.httpRequestDuration)
	mc.registerer.MustRegister(mc.activeConnections)
	mc.registerer.MustRegister(mc.serviceInfo)

	// Set service info
	mc.serviceInfo.WithLabelValues(version, commit).Set(1)

	return mc
}

// RegisterCustomMetric registers a custom Prometheus metric
func (mc *MetricsCollector) RegisterCustomMetric(name string, metric prometheus.Collector) {
	mc.customMetrics[name] = metric
	mc.registerer.MustRegister(metric)
}

// MetricsMiddleware returns middleware that collects HTTP metrics
func (mc *MetricsCollector) MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Increment active connections
		mc.activeConnections.Inc()
		defer mc.activeConnections.Dec()

		// Process request
		c.Next()

		// Record metrics
		duration := time.Since(start).Seconds()
		method := c.Request.Method
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = "unknown"
		}
		status := strconv.Itoa(c.Writer.Status())

		mc.httpRequestsTotal.WithLabelValues(method, endpoint, status).Inc()
		mc.httpRequestDuration.WithLabelValues(method, endpoint).Observe(duration)
	}
}

// Handler returns the Prometheus metrics HTTP handler
func (mc *MetricsCollector) Handler() gin.HandlerFunc {
	handler := promhttp.HandlerFor(mc.gatherer, promhttp.HandlerOpts{})
	return func(c *gin.Context) {
		handler.ServeHTTP(c.Writer, c.Request)
	}
}

// Service-specific metric helpers

// NewCounter creates a new counter metric for the service
func (mc *MetricsCollector) NewCounter(name, help string, labels []string) *prometheus.CounterVec {
	counter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: mc.serviceName + "_" + name,
			Help: help,
		},
		labels,
	)
	mc.RegisterCustomMetric(name, counter)
	return counter
}

// NewGauge creates a new gauge metric for the service
func (mc *MetricsCollector) NewGauge(name, help string, labels []string) *prometheus.GaugeVec {
	gauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: mc.serviceName + "_" + name,
			Help: help,
		},
		labels,
	)
	mc.RegisterCustomMetric(name, gauge)
	return gauge
}

// NewHistogram creates a new histogram metric for the service
func (mc *MetricsCollector) NewHistogram(name, help string, labels []string, buckets []float64) *prometheus.HistogramVec {
	if buckets == nil {
		buckets = prometheus.DefBuckets
	}

	histogram := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    mc.serviceName + "_" + name,
			Help:    help,
			Buckets: buckets,
		},
		labels,
	)
	mc.RegisterCustomMetric(name, histogram)
	return histogram
}

// NewSummary creates a new summary metric for the service
func (mc *MetricsCollector) NewSummary(name, help string, labels []string, objectives map[float64]float64) *prometheus.SummaryVec {
	if objectives == nil {
		objectives = map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001}
	}

	summary := prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       mc.serviceName + "_" + name,
			Help:       help,
			Objectives: objectives,
		},
		labels,
	)
	mc.RegisterCustomMetric(name, summary)
	return summary
}

// Common service metrics creators

// RegisterDBStats registers Prometheus collectors that expose Go
// database/sql connection-pool stats at scrape time. No background
// goroutine, no ticker; Prometheus calls db.Stats() on each scrape via
// GaugeFunc / CounterFunc, which avoids sampling drift and goroutine
// lifecycle management entirely.
//
// Series registered:
//   - <service>_db_open_connections          (GaugeFunc)
//   - <service>_db_in_use_connections        (GaugeFunc)
//   - <service>_db_idle_connections          (GaugeFunc)
//   - <service>_db_wait_count_total          (CounterFunc, monotonic)
//   - <service>_db_wait_duration_seconds_total (CounterFunc, monotonic)
func (mc *MetricsCollector) RegisterDBStats(db *sql.DB) {
	if db == nil {
		return
	}
	openConns := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: mc.serviceName + "_db_open_connections",
		Help: "Currently open database connections (in-use + idle)",
	}, func() float64 { return float64(db.Stats().OpenConnections) })
	inUse := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: mc.serviceName + "_db_in_use_connections",
		Help: "Database connections currently in use",
	}, func() float64 { return float64(db.Stats().InUse) })
	idle := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: mc.serviceName + "_db_idle_connections",
		Help: "Database connections currently idle",
	}, func() float64 { return float64(db.Stats().Idle) })
	waitCount := prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: mc.serviceName + "_db_wait_count_total",
		Help: "Total times a connection request had to wait for a free connection",
	}, func() float64 { return float64(db.Stats().WaitCount) })
	waitDuration := prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: mc.serviceName + "_db_wait_duration_seconds_total",
		Help: "Total time spent waiting for a database connection",
	}, func() float64 { return db.Stats().WaitDuration.Seconds() })

	mc.RegisterCustomMetric("db_open_connections", openConns)
	mc.RegisterCustomMetric("db_in_use_connections", inUse)
	mc.RegisterCustomMetric("db_idle_connections", idle)
	mc.RegisterCustomMetric("db_wait_count_total", waitCount)
	mc.RegisterCustomMetric("db_wait_duration_seconds_total", waitDuration)
}

// CreateKafkaMetrics creates standard Kafka metrics
func (mc *MetricsCollector) CreateKafkaMetrics() (
	*prometheus.CounterVec, // kafka_messages_total
	*prometheus.HistogramVec, // kafka_operation_duration_seconds
	*prometheus.GaugeVec, // kafka_consumer_lag
) {
	messages := mc.NewCounter("kafka_messages_total", "Total Kafka messages", []string{"topic", "operation", "status"})
	duration := mc.NewHistogram("kafka_operation_duration_seconds", "Kafka operation duration", []string{"operation"}, nil)
	lag := mc.NewGauge("kafka_consumer_lag", "Kafka consumer lag", []string{"topic", "partition"})

	return messages, duration, lag
}

// CreateBusinessMetrics creates common business metrics
func (mc *MetricsCollector) CreateBusinessMetrics() (
	*prometheus.GaugeVec, // active_items (streams, users, etc)
	*prometheus.CounterVec, // operations_total
	*prometheus.HistogramVec, // operation_duration_seconds
) {
	active := mc.NewGauge("active_items", "Currently active items", []string{"type"})
	operations := mc.NewCounter("operations_total", "Total operations", []string{"operation", "status"})
	duration := mc.NewHistogram("operation_duration_seconds", "Operation duration", []string{"operation"}, nil)

	return active, operations, duration
}
