package signalman

import (
	"time"

	"frameworks/pkg/api/common"
	pb "frameworks/pkg/proto"
)

// EventData contains typed protobuf payloads for different event types
type EventData struct {
	ClientLifecycle *pb.ClientLifecycleUpdate  `json:"client_lifecycle,omitempty"`
	NodeLifecycle   *pb.NodeLifecycleUpdate    `json:"node_lifecycle,omitempty"`
	TrackList       *pb.StreamTrackListTrigger `json:"track_list,omitempty"`
	ClipLifecycle   *pb.ClipLifecycleData      `json:"clip_lifecycle,omitempty"`
	DVRLifecycle    *pb.DVRLifecycleData       `json:"dvr_lifecycle,omitempty"`
	LoadBalancing   *pb.LoadBalancingData      `json:"load_balancing,omitempty"`
}

// Message represents a real-time message sent to clients
type Message struct {
	Type      string                 `json:"type"`
	Channel   string                 `json:"channel"`
	Data      EventData              `json:"data"` // Typed protobuf payloads
	RawData   map[string]interface{} `json:"-"`    // Raw data for backwards compatibility
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
