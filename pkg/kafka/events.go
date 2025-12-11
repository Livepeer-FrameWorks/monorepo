package kafka

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sirupsen/logrus"
)

// Event represents a generic Kafka event
type Event struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Source    string                 `json:"source"`
	TenantID  string                 `json:"tenant_id,omitempty"`
	Channel   string                 `json:"channel,omitempty"`
	Data      map[string]interface{} `json:"data"`
	Timestamp time.Time              `json:"timestamp"`
}

// AnalyticsEvent represents a single analytics event
type AnalyticsEvent struct {
	EventID   string                 `json:"event_id"`
	EventType string                 `json:"event_type"`
	Timestamp time.Time              `json:"timestamp"`
	Source    string                 `json:"source"`
	TenantID  string                 `json:"tenant_id,omitempty"`
	Data      map[string]interface{} `json:"data"` // Transparent protobuf message as JSON
}

// EventHandler interface for handling Kafka events
type EventHandler interface {
	HandleEvent(event AnalyticsEvent) error
}

// AnalyticsEventHandler implements EventHandler to handle analytics events
type AnalyticsEventHandler struct {
	handler func(AnalyticsEvent) error
	logger  *logrus.Logger
}

// NewAnalyticsEventHandler creates a new handler for analytics events
func NewAnalyticsEventHandler(handler func(AnalyticsEvent) error, logger *logrus.Logger) *AnalyticsEventHandler {
	return &AnalyticsEventHandler{
		handler: handler,
		logger:  logger,
	}
}

// HandleEvent implements EventHandler by directly handling AnalyticsEvent
func (h *AnalyticsEventHandler) HandleEvent(event AnalyticsEvent) error {
	return h.handler(event)
}

// HandleMessage adapts the generic Message handler signature to the typed AnalyticsEvent handler.
// It unmarshals the message value into an AnalyticsEvent, maps headers, and calls HandleEvent.
func (h *AnalyticsEventHandler) HandleMessage(ctx context.Context, msg Message) error {
	var event AnalyticsEvent
	// We assume the value is JSON-encoded AnalyticsEvent
	// Note: In a high-throughput scenario, we might want to reuse a decoder.
	if err := json.Unmarshal(msg.Value, &event); err != nil {
		h.logger.WithError(err).Error("failed to unmarshal analytics event")
		return nil // Return nil to avoid retrying malformed messages indefinitely
	}

	// Map headers to event fields if they exist and aren't already set
	for k, v := range msg.Headers {
		switch k {
		case "source":
			if event.Source == "" {
				event.Source = v
			}
		case "tenant_id":
			if event.TenantID == "" {
				event.TenantID = v
			}
		}
	}

	// Delegate to the typed handler
	return h.HandleEvent(event)
}

// ConsumerInterface defines the interface for Kafka consumers
type ConsumerInterface interface {
	AddHandler(topic string, handler Handler)
	Start(ctx context.Context) error
	Close() error
	GetMetrics() (map[string]interface{}, error)
	HealthCheck() error
}

// ProducerInterface defines the interface for Kafka producers
type ProducerInterface interface {
	ProduceMessage(topic string, key []byte, value []byte, headers map[string]string) error
	PublishTypedBatch(events []AnalyticsEvent) error // Typed method for analytics events
	PublishTypedEvent(event *AnalyticsEvent) error   // Single typed event method
	Close() error
	HealthCheck() error
	GetMetrics() (map[string]interface{}, error)
}
