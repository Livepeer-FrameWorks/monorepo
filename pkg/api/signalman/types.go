package signalman

import (
	"time"

	"frameworks/pkg/api/common"
	"frameworks/pkg/validation"
)

// Message represents a real-time message sent to clients
type Message struct {
	Type      string               `json:"type"`
	Channel   string               `json:"channel"`
	Data      validation.EventData `json:"data"`
	Timestamp time.Time            `json:"timestamp"`
	TenantID  *string              `json:"tenant_id,omitempty"` // For tenant-scoped messages
}

// SubscriptionMessage represents a subscription request from client
type SubscriptionMessage struct {
	Action   string   `json:"action"`   // "subscribe" or "unsubscribe"
	Channels []string `json:"channels"` // ["streams", "analytics", "system"]
	UserID   *string  `json:"user_id,omitempty"`
	TenantID *string  `json:"tenant_id,omitempty"` // Required for tenant-scoped channels
}

// SubscriptionConfirmation represents a subscription confirmation response
type SubscriptionConfirmation struct {
	Type     string   `json:"type"`     // "subscription_confirmed" or "unsubscription_confirmed"
	Channels []string `json:"channels"` // Current subscribed channels
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status     string    `json:"status"`
	Service    string    `json:"service"`
	Version    string    `json:"version"`
	Timestamp  time.Time `json:"timestamp"`
	Uptime     string    `json:"uptime"`
	Kafka      string    `json:"kafka,omitempty"`
	KafkaError string    `json:"kafka_error,omitempty"`
	WebSocket  *HubStats `json:"websocket,omitempty"`
}

// HubStats represents WebSocket hub statistics
type HubStats struct {
	Connections          int            `json:"connections"`
	TotalClients         int            `json:"total_clients"`
	ChannelSubscriptions map[string]int `json:"channel_subscriptions"`
}

// EventData is now provided by validation.EventData for type safety

// Event types are now provided by the shared validation package
// Use validation.StreamLifecyclePayload, validation.TrackListPayload, etc.

// Channel constants for subscription
const (
	ChannelStreams   = "streams"
	ChannelAnalytics = "analytics"
	ChannelSystem    = "system"
	ChannelAll       = "all"
)

// Message type constants
const (
	// Subscription management
	TypeSubscriptionConfirmed   = "subscription_confirmed"
	TypeUnsubscriptionConfirmed = "unsubscription_confirmed"

	// Stream events
	TypeStreamStart  = "stream-start"
	TypeStreamEnd    = "stream-end"
	TypeStreamError  = "stream-error"
	TypeTrackList    = "track-list"
	TypeStreamBuffer = "stream-buffer"

	// System events
	TypeNodeUp        = "node-up"
	TypeNodeDown      = "node-down"
	TypeNodeDegraded  = "node-degraded"
	TypeLoadBalancing = "load-balancing"

	// Analytics events
	TypeViewerMetrics     = "viewer-metrics"
	TypeConnectionMetrics = "connection-metrics"
	TypeHealthMetrics     = "health-metrics"
)

// Subscription action constants
const (
	ActionSubscribe   = "subscribe"
	ActionUnsubscribe = "unsubscribe"
)

// ErrorResponse represents an enhanced error response for real-time operations
type ErrorResponse struct {
	common.ErrorResponse
	Message string `json:"message"` // Backwards compatibility with existing usage
}
