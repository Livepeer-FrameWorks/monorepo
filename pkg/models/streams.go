package models

import (
	"time"
)

// ViewerSession represents viewer metrics for analytics
type ViewerSession struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	StreamID       string     `json:"stream_id"`
	ViewerID       string     `json:"viewer_id"`
	SessionID      string     `json:"session_id"`
	ConnectedAt    time.Time  `json:"connected_at"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
	Duration       int        `json:"duration"`
	BandwidthOut   int64      `json:"bandwidth_out"`
	NodeID         string     `json:"node_id"`
	UserAgent      string     `json:"user_agent"`
	IPAddress      string     `json:"ip_address"`
	Country        string     `json:"country"`
	Region         string     `json:"region"`
	City           string     `json:"city"`
	ISP            string     `json:"isp"`
	Latitude       float64    `json:"latitude"`
	Longitude      float64    `json:"longitude"`
	CreatedAt      time.Time  `json:"created_at"`
}

// RoutingEvent represents a routing decision event
type RoutingEvent struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenant_id"`
	StreamID         string    `json:"stream_id"`
	ViewerID         string    `json:"viewer_id"`
	SelectedNode     string    `json:"selected_node"`
	RoutingReason    string    `json:"routing_reason"`
	LatencyMs        int       `json:"latency_ms"`
	LoadScore        float64   `json:"load_score"`
	GeographicScore  float64   `json:"geographic_score"`
	FinalScore       float64   `json:"final_score"`
	AlternativeNodes []string  `json:"alternative_nodes"`
	ClientIP         string    `json:"client_ip"`
	ClientCountry    string    `json:"client_country"`
	CreatedAt        time.Time `json:"created_at"`
}

// ViewerMetrics5m represents 5-minute aggregated viewer metrics
type ViewerSession5m struct {
	TenantID       string    `json:"tenant_id"`
	StreamID       string    `json:"stream_id"`
	TimeWindow     time.Time `json:"time_window"`
	ViewerCount    int       `json:"viewer_count"`
	PeakViewers    int       `json:"peak_viewers"`
	TotalBandwidth int64     `json:"total_bandwidth"`
	AvgLatency     float64   `json:"avg_latency"`
	NodeID         string    `json:"node_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// Stream represents a streaming session
type Stream struct {
	ID                 string     `json:"-"`    // UUID - internal only, not exposed via API
	TenantID           string     `json:"-"`    // Tenant ID - internal only, not exposed via API
	UserID             string     `json:"-"`    // User ID - internal only, not exposed via API
	Title              string     `json:"name"` // Map to GraphQL 'name' field
	Description        string     `json:"description"`
	InternalName       string     `json:"id"` // This becomes the public ID
	StreamKey          string     `json:"streamKey"`
	PlaybackID         string     `json:"playbackId"` // Public playback identifier
	IsLive             bool       `json:"is_live"`
	IsRecording        bool       `json:"record"`               // Map to GraphQL 'record' field
	IsRecordingEnabled bool       `json:"is_recording_enabled"` // Alias for IsRecording
	Status             string     `json:"status"`               // Stream status (offline, live, terminated)
	CurrentViewers     int        `json:"current_viewers"`
	TotalViews         int        `json:"total_views"`
	Duration           int        `json:"duration"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	StartTime          *time.Time `json:"start_time,omitempty"` // Alias for StartedAt
	EndedAt            *time.Time `json:"ended_at,omitempty"`
	EndTime            *time.Time `json:"end_time,omitempty"` // Alias for EndedAt
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// StreamKey represents a stream key for authentication
type StreamKey struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	UserID     string     `json:"user_id"`
	StreamID   string     `json:"streamId"`
	KeyValue   string     `json:"keyValue"`
	KeyName    *string    `json:"keyName,omitempty"`
	IsActive   bool       `json:"isActive"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// GraphQL union type marker methods for Stream
func (Stream) IsCreateStreamResult() {}
func (Stream) IsUpdateStreamResult() {}

// GraphQL union type marker methods for StreamKey
func (StreamKey) IsCreateStreamKeyResult() {}
