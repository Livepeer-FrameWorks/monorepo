package monitoring

import (
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
}

// NewMetricsCollector creates a new metrics collector for a service
func NewMetricsCollector(serviceName, version, commit string) *MetricsCollector {
	// Sanitize service name for Prometheus (replace hyphens with underscores)
	sanitizedServiceName := strings.ReplaceAll(serviceName, "-", "_")

	mc := &MetricsCollector{
		serviceName:   sanitizedServiceName,
		customMetrics: make(map[string]prometheus.Collector),
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
	prometheus.MustRegister(mc.httpRequestsTotal)
	prometheus.MustRegister(mc.httpRequestDuration)
	prometheus.MustRegister(mc.activeConnections)
	prometheus.MustRegister(mc.serviceInfo)

	// Set service info
	mc.serviceInfo.WithLabelValues(version, commit).Set(1)

	return mc
}

// RegisterCustomMetric registers a custom Prometheus metric
func (mc *MetricsCollector) RegisterCustomMetric(name string, metric prometheus.Collector) {
	mc.customMetrics[name] = metric
	prometheus.MustRegister(metric)
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
	handler := promhttp.Handler()
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

// CreateDatabaseMetrics creates standard database metrics
func (mc *MetricsCollector) CreateDatabaseMetrics() (
	*prometheus.CounterVec, // db_queries_total
	*prometheus.HistogramVec, // db_query_duration_seconds
	*prometheus.GaugeVec, // db_connections_active
) {
	queries := mc.NewCounter("db_queries_total", "Total database queries", []string{"query_type", "status"})
	duration := mc.NewHistogram("db_query_duration_seconds", "Database query duration", []string{"query_type"}, nil)
	connections := mc.NewGauge("db_connections_active", "Active database connections", []string{"database"})

	return queries, duration, connections
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
