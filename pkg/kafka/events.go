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

// HandleEvent implements EventHandler by directly handling AnalyticsEvent
func (h *AnalyticsEventHandler) HandleEvent(event AnalyticsEvent) error {
	return h.handler(h.yugaDB, event)
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
	PublishTypedBatch(events []AnalyticsEvent) error // Typed method for analytics events
	PublishTypedEvent(event *AnalyticsEvent) error   // Single typed event method
	Close() error
	HealthCheck() error
	GetMetrics() (map[string]interface{}, error)
}
