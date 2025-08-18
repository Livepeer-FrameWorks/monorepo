package signalman

import (
	"time"

	"frameworks/pkg/api/common"
)

// Message represents a real-time message sent to clients
type Message struct {
	Type      string                 `json:"type"`
	Channel   string                 `json:"channel"`
	Data      map[string]interface{} `json:"data"`
	Timestamp time.Time              `json:"timestamp"`
	TenantID  *string                `json:"tenant_id,omitempty"` // For tenant-scoped messages
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
	Status     string                 `json:"status"`
	Service    string                 `json:"service"`
	Version    string                 `json:"version"`
	Timestamp  time.Time              `json:"timestamp"`
	Uptime     string                 `json:"uptime"`
	Kafka      string                 `json:"kafka,omitempty"`
	KafkaError string                 `json:"kafka_error,omitempty"`
	WebSocket  map[string]interface{} `json:"websocket,omitempty"`
}

// HubStats represents WebSocket hub statistics
type HubStats struct {
	TotalClients         int            `json:"total_clients"`
	ChannelSubscriptions map[string]int `json:"channel_subscriptions"`
}

// EventData represents the data payload for different event types
type EventData map[string]interface{}

// StreamLifecycleEvent represents stream lifecycle events
type StreamLifecycleEvent struct {
	Type         string    `json:"type"` // "stream-start", "stream-end", "stream-error"
	InternalName string    `json:"internal_name"`
	TenantID     string    `json:"tenant_id"`
	NodeID       string    `json:"node_id"`
	Status       string    `json:"status"`
	Timestamp    time.Time `json:"timestamp"`
	Details      string    `json:"details,omitempty"`
}

// RealtimeTrackListEvent represents track list update events for real-time streaming
type RealtimeTrackListEvent struct {
	Type         string    `json:"type"` // "track-list"
	InternalName string    `json:"internal_name"`
	TenantID     string    `json:"tenant_id"`
	NodeID       string    `json:"node_id"`
	TrackList    string    `json:"track_list"`
	TrackCount   int       `json:"track_count"`
	Timestamp    time.Time `json:"timestamp"`
}

// StreamBufferEvent represents stream buffer events
type StreamBufferEvent struct {
	Type         string    `json:"type"` // "stream-buffer"
	InternalName string    `json:"internal_name"`
	TenantID     string    `json:"tenant_id"`
	NodeID       string    `json:"node_id"`
	BufferHealth float32   `json:"buffer_health"`
	BufferSize   int       `json:"buffer_size"`
	BufferUsed   int       `json:"buffer_used"`
	Status       string    `json:"status"`
	Timestamp    time.Time `json:"timestamp"`
}

// NodeLifecycleEvent represents node lifecycle events
type NodeLifecycleEvent struct {
	Type      string    `json:"type"` // "node-up", "node-down", "node-degraded"
	NodeID    string    `json:"node_id"`
	Location  string    `json:"location"`
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Details   string    `json:"details,omitempty"`
}

// LoadBalancingEvent represents load balancing events
type LoadBalancingEvent struct {
	Type       string    `json:"type"` // "load-balancing"
	NodeID     string    `json:"node_id"`
	StreamName string    `json:"stream_name,omitempty"`
	Action     string    `json:"action"` // "route", "failover", "rebalance"
	Score      float64   `json:"score"`  // Node score
	Reason     string    `json:"reason"` // Reason for action
	Timestamp  time.Time `json:"timestamp"`
	TenantID   string    `json:"tenant_id,omitempty"`
}

// AnalyticsEvent represents analytics events
type AnalyticsEvent struct {
	Type         string                 `json:"type"`
	InternalName string                 `json:"internal_name,omitempty"`
	TenantID     string                 `json:"tenant_id"`
	NodeID       string                 `json:"node_id,omitempty"`
	Metrics      map[string]interface{} `json:"metrics"`
	Timestamp    time.Time              `json:"timestamp"`
}

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
