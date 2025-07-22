package kafka

import (
	"context"
	"time"

	"frameworks/pkg/database"
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
	EventID       string                 `json:"event_id"`
	EventType     string                 `json:"event_type"`
	Timestamp     time.Time              `json:"timestamp"`
	Source        string                 `json:"source"`
	TenantID      string                 `json:"tenant_id,omitempty"`
	StreamID      *string                `json:"stream_id,omitempty"`
	UserID        *string                `json:"user_id,omitempty"`
	PlaybackID    *string                `json:"playback_id,omitempty"`
	InternalName  *string                `json:"internal_name,omitempty"`
	Region        string                 `json:"region"`
	NodeURL       *string                `json:"node_url,omitempty"`
	Data          map[string]interface{} `json:"data,omitempty"`
	SchemaVersion string                 `json:"schema_version"`
}

// EventHandler interface for handling Kafka events
type EventHandler interface {
	HandleEvent(event Event) error
}

// AnalyticsEventHandler implements EventHandler to handle analytics events
type AnalyticsEventHandler struct {
	handler func(database.PostgresConn, AnalyticsEvent) error
	logger  *logrus.Logger
	yugaDB  database.PostgresConn
}

// NewAnalyticsEventHandler creates a new handler for analytics events
func NewAnalyticsEventHandler(ydb database.PostgresConn, handler func(database.PostgresConn, AnalyticsEvent) error, logger *logrus.Logger) *AnalyticsEventHandler {
	return &AnalyticsEventHandler{
		handler: handler,
		logger:  logger,
		yugaDB:  ydb,
	}
}

// HandleEvent implements EventHandler by converting the event to an AnalyticsEvent
func (h *AnalyticsEventHandler) HandleEvent(event Event) error {
	// Convert Event to AnalyticsEvent
	analyticsEvent := AnalyticsEvent{
		EventID:       event.ID,
		EventType:     event.Type,
		Timestamp:     event.Timestamp,
		Source:        event.Source,
		TenantID:      event.TenantID,
		Data:          event.Data,
		SchemaVersion: "1.0",
	}

	// Extract optional fields from event.Data
	if streamID, ok := event.Data["stream_id"].(string); ok {
		analyticsEvent.StreamID = &streamID
	}
	if userID, ok := event.Data["user_id"].(string); ok {
		analyticsEvent.UserID = &userID
	}
	if playbackID, ok := event.Data["playback_id"].(string); ok {
		analyticsEvent.PlaybackID = &playbackID
	}
	if internalName, ok := event.Data["internal_name"].(string); ok {
		analyticsEvent.InternalName = &internalName
	}
	if region, ok := event.Data["region"].(string); ok {
		analyticsEvent.Region = region
	}
	if nodeURL, ok := event.Data["node_url"].(string); ok {
		analyticsEvent.NodeURL = &nodeURL
	}

	return h.handler(h.yugaDB, analyticsEvent)
}

// ConsumerInterface defines the interface for Kafka consumers
type ConsumerInterface interface {
	Subscribe(topics []string) error
	Start(ctx context.Context) error
	Close() error
	GetMetrics() (map[string]interface{}, error)
	HealthCheck() error
}

// ProducerInterface defines the interface for Kafka producers
type ProducerInterface interface {
	ProduceMessage(topic string, key []byte, value []byte, headers map[string]string) error
	PublishBatch(batch interface{}) error
	Close() error
	HealthCheck() error
	GetMetrics() (map[string]interface{}, error)
}
