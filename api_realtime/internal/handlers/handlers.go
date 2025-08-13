package handlers

import (
	"fmt"
	"net/http"
	"time"

	"frameworks/api_realtime/internal/websocket"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
)

// SignalmanHandlers contains the HTTP handlers for the service
type SignalmanHandlers struct {
	hub       *websocket.Hub
	consumer  kafka.ConsumerInterface
	logger    logging.Logger
	startTime time.Time
}

// NewSignalmanHandlers creates a new handlers instance
func NewSignalmanHandlers(hub *websocket.Hub, consumer kafka.ConsumerInterface, logger logging.Logger) *SignalmanHandlers {
	return &SignalmanHandlers{
		hub:       hub,
		consumer:  consumer,
		logger:    logger,
		startTime: time.Now(),
	}
}

// HandleWebSocketStreams serves WebSocket connections for stream updates
func (h *SignalmanHandlers) HandleWebSocketStreams(c middleware.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleWebSocketAnalytics serves WebSocket connections for analytics updates
func (h *SignalmanHandlers) HandleWebSocketAnalytics(c middleware.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleWebSocketSystem serves WebSocket connections for system updates
func (h *SignalmanHandlers) HandleWebSocketSystem(c middleware.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleWebSocketAll serves WebSocket connections for all event types
func (h *SignalmanHandlers) HandleWebSocketAll(c middleware.Context) {
	h.hub.ServeWS(c.Writer, c.Request)
}

// HandleHealth provides health check endpoint
func (h *SignalmanHandlers) HandleHealth(c middleware.Context) {
	health := middleware.H{
		"status":    "healthy",
		"service":   "signalman",
		"version":   "1.0.0",
		"timestamp": time.Now().UTC(),
		"uptime":    time.Since(h.startTime).String(),
	}

	// Check Kafka connectivity
	if err := h.consumer.HealthCheck(); err != nil {
		h.logger.WithError(err).Error("Kafka health check failed")
		health["status"] = "unhealthy"
		health["kafka_error"] = err.Error()
		c.JSON(http.StatusServiceUnavailable, health)
		return
	}

	health["kafka"] = "connected"

	// Add WebSocket hub stats
	hubStats := h.hub.GetStats()
	health["websocket"] = hubStats

	c.JSON(http.StatusOK, health)
}

// HandleMetrics provides operational metrics in Prometheus format
func (h *SignalmanHandlers) HandleMetrics(c middleware.Context) {
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
func (h *SignalmanHandlers) HandleNotFound(c middleware.Context) {
	c.JSON(http.StatusNotFound, middleware.H{
		"error":   "not_found",
		"message": "Endpoint not found",
		"service": "signalman",
	})
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
	channel := mapEventTypeToChannel(event.Type)

	// Prefer header-provided tenant, fallback to payload data
	tenantID := event.TenantID
	if tenantID == "" {
		if v, ok := event.Data["tenant_id"].(string); ok && v != "" {
			tenantID = v
		}
	}

	if channel == "system" {
		h.hub.BroadcastInfrastructure(event.Type, event.Data)
	} else if tenantID != "" {
		h.hub.BroadcastToTenant(tenantID, event.Type, channel, event.Data)
	} else {
		// No tenant context; drop to avoid cross-tenant leakage
		h.logger.WithFields(logging.Fields{
			"event_type": event.Type,
			"channel":    channel,
		}).Warn("Dropping event without tenant_id for non-system channel")
	}

	h.logger.WithFields(logging.Fields{
		"event_type": event.Type,
		"source":     event.Source,
		"channel":    channel,
		"tenant_id":  tenantID,
	}).Debug("Processed Kafka event for WebSocket broadcast")

	return nil
}
