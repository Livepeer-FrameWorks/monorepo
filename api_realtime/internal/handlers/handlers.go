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
	"frameworks/pkg/validation"

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
	metricsOutput += "# HELP signalman_websocket_connections Current WebSocket connections\n# TYPE signalman_websocket_connections gauge\n"
	metricsOutput += fmt.Sprintf("signalman_websocket_connections %d\n", hubStats.Connections)

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

// convertAnalyticsEventToTyped converts a kafka.AnalyticsEvent to a validation.KafkaEvent with typed data
func (h *SignalmanHandlers) convertAnalyticsEventToTyped(event kafka.AnalyticsEvent) (*validation.KafkaEvent, error) {
	// Convert kafka.AnalyticsEvent to validation.KafkaEvent structure
	typedEvent := validation.KafkaEvent{
		EventID:       event.EventID,
		EventType:     event.EventType,
		Timestamp:     event.Timestamp,
		Source:        event.Source,
		SchemaVersion: event.SchemaVersion,
		Data:          event.Data, // Already typed, no conversion needed
	}

	return &typedEvent, nil
}

// HandleEvent processes incoming events and broadcasts them via WebSocket
func (h *SignalmanHandlers) HandleEvent(event kafka.AnalyticsEvent) error {
	start := time.Now()

	// Track Kafka message processing
	if h.metrics != nil {
		h.metrics.KafkaMessages.WithLabelValues(event.EventType, "consume", "received").Inc()
	}

	// Convert to typed event with validation
	typedEvent, err := h.convertAnalyticsEventToTyped(event)
	if err != nil {
		h.logger.WithError(err).WithFields(logging.Fields{
			"event_type": event.EventType,
			"source":     event.Source,
		}).Error("Failed to convert event to typed structure")

		// Track validation failures
		if h.metrics != nil {
			h.metrics.KafkaMessages.WithLabelValues(event.EventType, "consume", "validation_failed").Inc()
		}
		return err
	}

	channel := mapEventTypeToChannel(event.EventType)
	tenantID := h.extractTenantID(typedEvent, event.TenantID)

	// Broadcast typed event
	if channel == "system" {
		if tenantID != "" {
			// Tenant-scoped system message (e.g., tenant's cluster/node events)
			h.hub.BroadcastTypedToTenant(tenantID, event.EventType, channel, typedEvent.Data)
		} else {
			// Global infrastructure message (e.g., platform-wide events)
			h.hub.BroadcastTypedInfrastructure(event.EventType, typedEvent.Data)
		}
	} else if tenantID != "" {
		h.hub.BroadcastTypedToTenant(tenantID, event.EventType, channel, typedEvent.Data)
	} else {
		// No tenant context; drop to avoid cross-tenant leakage
		h.logger.WithFields(logging.Fields{
			"event_type": event.EventType,
			"channel":    channel,
		}).Warn("Dropping event without tenant_id for non-system channel")

		// Track dropped messages
		if h.metrics != nil {
			h.metrics.KafkaMessages.WithLabelValues(event.EventType, "consume", "dropped").Inc()
		}
	}

	// Track Kafka processing duration and success
	if h.metrics != nil {
		h.metrics.KafkaDuration.WithLabelValues(event.EventType).Observe(time.Since(start).Seconds())
		h.metrics.KafkaMessages.WithLabelValues(event.EventType, "consume", "processed").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"event_type": event.EventType,
		"source":     event.Source,
		"channel":    channel,
		"tenant_id":  tenantID,
	}).Debug("Processed Kafka event for WebSocket broadcast")

	return nil
}

// extractTenantID extracts tenant ID from typed event data, with header fallback
func (h *SignalmanHandlers) extractTenantID(typedEvent *validation.KafkaEvent, headerTenantID string) string {
	// Prefer header-provided tenant ID
	if headerTenantID != "" {
		return headerTenantID
	}

	// Extract from typed event data based on event type
	switch validation.EventType(typedEvent.EventType) {
	case validation.EventStreamIngest:
		if typedEvent.Data.StreamIngest != nil {
			return typedEvent.Data.StreamIngest.TenantID
		}
	case validation.EventStreamView:
		if typedEvent.Data.StreamView != nil {
			return typedEvent.Data.StreamView.TenantID
		}
	case validation.EventStreamLifecycle:
		if typedEvent.Data.StreamLifecycle != nil {
			return typedEvent.Data.StreamLifecycle.TenantID
		}
	case validation.EventUserConnection:
		if typedEvent.Data.UserConnection != nil {
			return typedEvent.Data.UserConnection.TenantID
		}
	case validation.EventClientLifecycle:
		if typedEvent.Data.ClientLifecycle != nil {
			return typedEvent.Data.ClientLifecycle.TenantID
		}
	case validation.EventTrackList:
		if typedEvent.Data.TrackList != nil {
			return typedEvent.Data.TrackList.TenantID
		}
	case validation.EventRecordingLifecycle:
		if typedEvent.Data.Recording != nil {
			return typedEvent.Data.Recording.TenantID
		}
	case validation.EventPushLifecycle:
		if typedEvent.Data.PushLifecycle != nil {
			return typedEvent.Data.PushLifecycle.TenantID
		}
	case validation.EventBandwidthThreshold:
		if typedEvent.Data.BandwidthThreshold != nil {
			return typedEvent.Data.BandwidthThreshold.TenantID
		}
	case validation.EventLoadBalancing:
		if typedEvent.Data.LoadBalancing != nil {
			return typedEvent.Data.LoadBalancing.TenantID
		}
	}

	return ""
}
