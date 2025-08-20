package handlers

import (
	"fmt"
	"net/http"
	"time"

	"frameworks/api_realtime/internal/metrics"
	"frameworks/api_realtime/internal/websocket"
	"frameworks/pkg/api/common"
	"frameworks/pkg/api/signalman"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

// SignalmanHandlers contains the HTTP handlers for the service
type SignalmanHandlers struct {
	hub       *websocket.Hub
	consumer  kafka.ConsumerInterface
	logger    logging.Logger
	startTime time.Time
	metrics   *metrics.Metrics
}

// NewSignalmanHandlers creates a new handlers instance
func NewSignalmanHandlers(hub *websocket.Hub, consumer kafka.ConsumerInterface, logger logging.Logger, m *metrics.Metrics) *SignalmanHandlers {
	return &SignalmanHandlers{
		hub:       hub,
		consumer:  consumer,
		logger:    logger,
		startTime: time.Now(),
		metrics:   m,
	}
}

// HandleWebSocketStreams serves WebSocket connections for stream updates
func (h *SignalmanHandlers) HandleWebSocketStreams(c *gin.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleWebSocketAnalytics serves WebSocket connections for analytics updates
func (h *SignalmanHandlers) HandleWebSocketAnalytics(c *gin.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleWebSocketSystem serves WebSocket connections for system updates
func (h *SignalmanHandlers) HandleWebSocketSystem(c *gin.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleWebSocketAll serves WebSocket connections for all event types
func (h *SignalmanHandlers) HandleWebSocketAll(c *gin.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleHealth provides health check endpoint
func (h *SignalmanHandlers) HandleHealth(c *gin.Context) {
	health := signalman.HealthResponse{
		Status:    "healthy",
		Service:   "signalman",
		Version:   "1.0.0",
		Timestamp: time.Now().UTC(),
		Uptime:    time.Since(h.startTime).String(),
	}

	// Check Kafka connectivity
	if err := h.consumer.HealthCheck(); err != nil {
		h.logger.WithError(err).Error("Kafka health check failed")
		health.Status = "unhealthy"
		health.KafkaError = err.Error()
		c.JSON(http.StatusServiceUnavailable, health)
		return
	}

	health.Kafka = "connected"

	// Add WebSocket hub stats
	hubStats := h.hub.GetStats()
	health.WebSocket = hubStats

	c.JSON(http.StatusOK, health)
}

// HandleMetrics provides operational metrics in Prometheus format
func (h *SignalmanHandlers) HandleMetrics(c *gin.Context) {
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// Basic service availability metric
	metricsOutput := "# HELP signalman_up Service availability\n# TYPE signalman_up gauge\nsignalman_up 1\n"

	// Add uptime metric
	uptime := time.Since(h.startTime).Seconds()
	metricsOutput += "# HELP signalman_uptime_seconds Service uptime in seconds\n# TYPE signalman_uptime_seconds counter\n"
	metricsOutput += fmt.Sprintf("signalman_uptime_seconds %.2f\n", uptime)

	// Add WebSocket hub metrics
	hubStats := h.hub.GetStats()
	if connections, ok := hubStats["connections"].(int); ok {
		metricsOutput += "# HELP signalman_websocket_connections Current WebSocket connections\n# TYPE signalman_websocket_connections gauge\n"
		metricsOutput += fmt.Sprintf("signalman_websocket_connections %d\n", connections)
	}

	c.String(http.StatusOK, metricsOutput)
}

// HandleNotFound provides a custom 404 handler
func (h *SignalmanHandlers) HandleNotFound(c *gin.Context) {
	errorResponse := signalman.ErrorResponse{
		ErrorResponse: common.ErrorResponse{
			Error:   "not_found",
			Service: "signalman",
		},
		Message: "Endpoint not found",
	}
	c.JSON(http.StatusNotFound, errorResponse)
}

func mapEventTypeToChannel(eventType string) string {
	switch eventType {
	case "stream-lifecycle", "track-list", "stream-buffer", "stream-end":
		return "streams"
	case "node-lifecycle", "load-balancing":
		return "system"
	default:
		return "analytics"
	}
}

// HandleEvent processes incoming events and broadcasts them via WebSocket
func (h *SignalmanHandlers) HandleEvent(event kafka.Event) error {
	start := time.Now()

	// Track Kafka message processing
	if h.metrics != nil {
		h.metrics.KafkaMessages.WithLabelValues(event.Type, "consume", "received").Inc()
	}

	channel := mapEventTypeToChannel(event.Type)

	// Prefer header-provided tenant, fallback to payload data
	tenantID := event.TenantID
	if tenantID == "" {
		if v, ok := event.Data["tenant_id"].(string); ok && v != "" {
			tenantID = v
		}
	}

	if channel == "system" {
		if tenantID != "" {
			// Tenant-scoped system message (e.g., tenant's cluster/node events)
			h.hub.BroadcastToTenant(tenantID, event.Type, channel, event.Data)
		} else {
			// Global infrastructure message (e.g., platform-wide events)
			h.hub.BroadcastInfrastructure(event.Type, event.Data)
		}
	} else if tenantID != "" {
		h.hub.BroadcastToTenant(tenantID, event.Type, channel, event.Data)
	} else {
		// No tenant context; drop to avoid cross-tenant leakage
		h.logger.WithFields(logging.Fields{
			"event_type": event.Type,
			"channel":    channel,
		}).Warn("Dropping event without tenant_id for non-system channel")

		// Track dropped messages
		if h.metrics != nil {
			h.metrics.KafkaMessages.WithLabelValues(event.Type, "consume", "dropped").Inc()
		}
	}

	// Track Kafka processing duration and success
	if h.metrics != nil {
		h.metrics.KafkaDuration.WithLabelValues(event.Type).Observe(time.Since(start).Seconds())
		h.metrics.KafkaMessages.WithLabelValues(event.Type, "consume", "processed").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"event_type": event.Type,
		"source":     event.Source,
		"channel":    channel,
		"tenant_id":  tenantID,
	}).Debug("Processed Kafka event for WebSocket broadcast")

	return nil
}
