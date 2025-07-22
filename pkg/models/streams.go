package models

import (
	"time"
)

// StreamAnalytics represents a stream's analytics data
type StreamAnalytics struct {
	ID                   string    `json:"id"`
	TenantID             string    `json:"tenant_id"`
	StreamID             string    `json:"stream_id"`
	InternalName         string    `json:"internal_name"`
	SessionStartTime     time.Time `json:"session_start_time"`
	SessionEndTime       time.Time `json:"session_end_time"`
	TotalSessionDuration int       `json:"total_session_duration"`
	CurrentViewers       int       `json:"current_viewers"`
	PeakViewers          int       `json:"peak_viewers"`
	TotalConnections     int       `json:"total_connections"`
	BandwidthIn          int64     `json:"bandwidth_in"`
	BandwidthOut         int64     `json:"bandwidth_out"`
	TotalBandwidthGB     float64   `json:"total_bandwidth_gb"`
	BitrateKbps          int       `json:"bitrate_kbps"`
	Resolution           string    `json:"resolution"`
	PacketsSent          int64     `json:"packets_sent"`
	PacketsLost          int64     `json:"packets_lost"`
	PacketsRetrans       int64     `json:"packets_retrans"`
	Upbytes              int64     `json:"upbytes"`
	Downbytes            int64     `json:"downbytes"`
	FirstMs              int       `json:"first_ms"`
	LastMs               int       `json:"last_ms"`
	TrackCount           int       `json:"track_count"`
	Inputs               string    `json:"inputs"`
	Outputs              string    `json:"outputs"`
	NodeID               string    `json:"node_id"`
	NodeName             string    `json:"node_name"`
	Latitude             float64   `json:"latitude"`
	Longitude            float64   `json:"longitude"`
	Country              string    `json:"country"`
	Region               string    `json:"region"`
	City                 string    `json:"city"`
	ISP                  string    `json:"isp"`
	Location             string    `json:"location"`
	Status               string    `json:"status"`
	LastUpdated          time.Time `json:"last_updated"`
	CreatedAt            time.Time `json:"created_at"`

	// Enriched metrics for API responses
	AvgViewers      float64 `json:"avg_viewers"`
	UniqueCountries int     `json:"unique_countries"`
	UniqueCities    int     `json:"unique_cities"`
	AvgBufferHealth float32 `json:"avg_buffer_health"`
	AvgBitrate      int     `json:"avg_bitrate"`
	PacketLossRate  float32 `json:"packet_loss_rate"`
}

// ViewerMetrics represents viewer metrics for analytics
type ViewerMetrics struct {
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
type ViewerMetrics5m struct {
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
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenant_id"`
	UserID             string     `json:"user_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	InternalName       string     `json:"internal_name"`
	StreamKey          string     `json:"stream_key"`
	PlaybackID         string     `json:"playback_id"` // Public playback identifier
	IsLive             bool       `json:"is_live"`
	IsRecording        bool       `json:"is_recording"`
	IsRecordingEnabled bool       `json:"is_recording_enabled"` // Alias for IsRecording
	IsPublic           bool       `json:"is_public"`
	Status             string     `json:"status"` // Stream status (offline, live, terminated)
	MaxViewers         int        `json:"max_viewers"`
	CurrentViewers     int        `json:"current_viewers"`
	TotalViews         int        `json:"total_views"`
	Duration           int        `json:"duration"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	StartTime          *time.Time `json:"start_time,omitempty"` // Alias for StartedAt
	EndedAt            *time.Time `json:"ended_at,omitempty"`
	EndTime            *time.Time `json:"end_time,omitempty"` // Alias for EndedAt
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// StreamKey represents a stream key for authentication
type StreamKey struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	UserID    string     `json:"user_id"`
	StreamID  string     `json:"stream_id"`
	KeyValue  string     `json:"key_value"`
	IsActive  bool       `json:"is_active"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
