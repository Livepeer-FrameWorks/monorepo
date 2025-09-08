package periscope

import (
	"time"

	"frameworks/pkg/api/common"
	"frameworks/pkg/models"
)

// StreamAnalyticsResponse represents the response from GetStreamAnalytics
type StreamAnalyticsResponse = []models.StreamAnalytics

// StreamDetailsResponse represents the response from GetStreamDetails
type StreamDetailsResponse = models.StreamAnalytics

// ViewerMetricsResponse represents the response from GetViewerMetrics
type ViewerMetricsResponse = []models.AnalyticsViewerSession

// RoutingEventsResponse represents the response from GetRoutingEvents
type RoutingEventsResponse = []models.AnalyticsRoutingEvent

// ViewerMetrics5mResponse represents the response from GetViewerMetrics5m
type ViewerMetrics5mResponse = []models.AnalyticsViewerSession5m

// StreamEventsResponse represents the response from GetStreamEvents
type StreamEventsResponse = []StreamEvent

// StreamEvent represents a single stream event
type StreamEvent struct {
	Timestamp    time.Time `json:"timestamp"`
	EventID      string    `json:"event_id"`
	EventType    string    `json:"event_type"`
	Status       string    `json:"status"`
	NodeID       string    `json:"node_id"`
	EventData    string    `json:"event_data"`
	InternalName string    `json:"internal_name"`
}

// TrackListEventsResponse represents the response from GetTrackListEvents
type TrackListEventsResponse = []AnalyticsTrackListEvent

// AnalyticsTrackListEvent represents a track list event for analytics
type AnalyticsTrackListEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	NodeID     string    `json:"node_id"`
	TrackList  string    `json:"track_list"`
	TrackCount int       `json:"track_count"`
	Stream     string    `json:"stream"`
}

// ViewerStatsResponse represents the response from GetViewerStats
type ViewerStatsResponse struct {
	CurrentViewers   int                   `json:"current_viewers"`
	PeakViewers      int                   `json:"peak_viewers"`
	TotalConnections int                   `json:"total_connections"`
	ViewerHistory    []ViewerHistoryEntry  `json:"viewer_history"`
	GeoStats         ViewerGeographicStats `json:"geo_stats"`
}

// ViewerHistoryEntry represents a single viewer history entry
type ViewerHistoryEntry struct {
	Timestamp         time.Time `json:"timestamp"`
	ViewerCount       int       `json:"viewer_count"`
	ConnectionType    string    `json:"connection_type"`
	BufferHealth      float32   `json:"buffer_health"`
	ConnectionQuality float32   `json:"connection_quality"`
	CountryCode       string    `json:"country_code"`
	City              string    `json:"city"`
}

// ViewerGeographicStats represents geographic viewer statistics
type ViewerGeographicStats struct {
	UniqueCountries  int                       `json:"unique_countries"`
	UniqueCities     int                       `json:"unique_cities"`
	CountryBreakdown map[string]int            `json:"country_breakdown"`
	CityBreakdown    map[string]map[string]int `json:"city_breakdown"`
}

// PlatformOverviewResponse represents the response from GetPlatformOverview
type PlatformOverviewResponse struct {
	TenantID       string    `json:"tenant_id"`
	TotalUsers     int       `json:"total_users"`
	ActiveUsers    int       `json:"active_users"`
	TotalStreams   int       `json:"total_streams"`
	ActiveStreams  int       `json:"active_streams"`
	TotalViewers   int       `json:"total_viewers"`
	AverageViewers float64   `json:"average_viewers"`
	PeakBandwidth  float64   `json:"peak_bandwidth"`
	GeneratedAt    time.Time `json:"generated_at"`
}

// RealtimeStreamsResponse represents the response from GetRealtimeStreams
type RealtimeStreamsResponse struct {
	Streams []RealtimeStream `json:"streams"`
	Count   int              `json:"count"`
}

// RealtimeStream represents a real-time stream with analytics
type RealtimeStream struct {
	InternalName      string  `json:"internal_name"`
	CurrentViewers    int     `json:"current_viewers"`
	BandwidthIn       int64   `json:"bandwidth_in"`
	BandwidthOut      int64   `json:"bandwidth_out"`
	Status            string  `json:"status"`
	NodeID            string  `json:"node_id"`
	Location          string  `json:"location"`
	ViewerTrend       float64 `json:"viewer_trend"`
	BufferHealth      float32 `json:"buffer_health"`
	ConnectionQuality float32 `json:"connection_quality"`
	UniqueCountries   int     `json:"unique_countries"`
}

// RealtimeViewersResponse represents the response from GetRealtimeViewers
type RealtimeViewersResponse struct {
	TotalViewers  int                    `json:"total_viewers"`
	StreamViewers []RealtimeStreamViewer `json:"stream_viewers"`
}

// RealtimeStreamViewer represents viewer data for a single stream
type RealtimeStreamViewer struct {
	InternalName    string  `json:"internal_name"`
	AvgViewers      float64 `json:"avg_viewers"`
	PeakViewers     float64 `json:"peak_viewers"`
	UniqueCountries int     `json:"unique_countries"`
	UniqueCities    int     `json:"unique_cities"`
}

// RealtimeEvent represents a single realtime event (union type)
type RealtimeEvent struct {
	// Common fields
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`

	// Stream event fields (nullable when not applicable)
	StreamEvent *StreamEvent `json:"stream_event,omitempty"`

	// Connection event fields (nullable when not applicable)
	ConnectionEvent *ConnectionEvent `json:"connection_event,omitempty"`

	// Buffer event fields (nullable when not applicable)
	BufferEvent *BufferEvent `json:"buffer_event,omitempty"`

	// Viewer metrics event (nullable when not applicable)
	ViewerMetrics *ViewerHistoryEntry `json:"viewer_metrics,omitempty"`
}

// RealtimeEventsResponse represents the response from GetRealtimeEvents
type RealtimeEventsResponse struct {
	Events []RealtimeEvent `json:"events"`
	Count  int             `json:"count"`
}

// ConnectionEventsResponse represents the response from GetConnectionEvents
type ConnectionEventsResponse = []ConnectionEvent

// ConnectionEvent represents a connection event from MistServer webhooks
type ConnectionEvent struct {
	EventID        string    `json:"event_id"`
	Timestamp      time.Time `json:"timestamp"`
	TenantID       string    `json:"tenant_id"`
	InternalName   string    `json:"internal_name"`
	SessionID      string    `json:"session_id"`
	ConnectionAddr string    `json:"connection_addr"`
	Connector      string    `json:"connector"`
	NodeID         string    `json:"node_id"`
	CountryCode    string    `json:"country_code"`
	City           string    `json:"city"`
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	EventType      string    `json:"event_type"` // "connect" or "disconnect"
}

// NodeMetricsResponse represents the response from GetNodeMetrics
type NodeMetricsResponse struct {
	Metrics   []NodeMetric `json:"metrics"`
	Count     int          `json:"count"`
	StartTime string       `json:"start_time"`
	EndTime   string       `json:"end_time"`
}

// NodeMetric represents a node metric entry
type NodeMetric struct {
	Timestamp          time.Time `json:"timestamp"`
	NodeID             string    `json:"node_id"`
	CPUUsage           float32   `json:"cpu_usage"`
	MemoryUsage        float32   `json:"memory_usage"`
	DiskUsage          float32   `json:"disk_usage"`
	RAMMax             uint64    `json:"ram_max"`
	RAMCurrent         uint64    `json:"ram_current"`
	BandwidthIn        int64     `json:"bandwidth_in"`
	BandwidthOut       int64     `json:"bandwidth_out"`
	UpSpeed            uint64    `json:"up_speed"`
	DownSpeed          uint64    `json:"down_speed"`
	ConnectionsCurrent int       `json:"connections_current"`
	StreamCount        int       `json:"stream_count"`
	HealthScore        float32   `json:"health_score"`
	IsHealthy          bool      `json:"is_healthy"`
	Latitude           float64   `json:"latitude"`
	Longitude          float64   `json:"longitude"`
	Tags               []string  `json:"tags"`
	Metadata           string    `json:"metadata"`
}

// NodeMetrics1hResponse represents the response from GetNodeMetrics1h
type NodeMetrics1hResponse = []NodeMetricHourly

// NodeMetricHourly represents an hourly aggregated node metric
type NodeMetricHourly struct {
	Timestamp         time.Time `json:"timestamp"`
	NodeID            string    `json:"node_id"`
	AvgCPU            float32   `json:"avg_cpu"`
	PeakCPU           float32   `json:"peak_cpu"`
	AvgMemory         float32   `json:"avg_memory"`
	PeakMemory        float32   `json:"peak_memory"`
	TotalBandwidthIn  int64     `json:"total_bandwidth_in"`
	TotalBandwidthOut int64     `json:"total_bandwidth_out"`
	AvgHealthScore    float32   `json:"avg_health_score"`
	WasHealthy        bool      `json:"was_healthy"`
}

// StreamHealthMetricsResponse represents the response from GetStreamHealthMetrics
type StreamHealthMetricsResponse = []StreamHealthMetric

// StreamHealthMetric represents a stream health metric entry
type StreamHealthMetric struct {
	Timestamp            time.Time `json:"timestamp"`
	TenantID             string    `json:"tenant_id"`
	InternalName         string    `json:"internal_name"`
	NodeID               string    `json:"node_id"`
	Bitrate              int       `json:"bitrate"`
	FPS                  float32   `json:"fps"`
	GOPSize              int       `json:"gop_size"`
	Width                int       `json:"width"`
	Height               int       `json:"height"`
	BufferSize           int       `json:"buffer_size"`
	BufferUsed           int       `json:"buffer_used"`
	BufferHealth         float32   `json:"buffer_health"`
	PacketsSent          int64     `json:"packets_sent"`
	PacketsLost          int64     `json:"packets_lost"`
	PacketsRetransmitted int64     `json:"packets_retransmitted"`
	BandwidthIn          int64     `json:"bandwidth_in"`
	BandwidthOut         int64     `json:"bandwidth_out"`
	Codec                string    `json:"codec"`
	Profile              string    `json:"profile"`
	TrackMetadata        string    `json:"track_metadata"`

	// Derived metrics computed in Periscope/MVs
	HealthScore          float32  `json:"health_score"`
	FrameJitterMs        *float64 `json:"frame_jitter_ms,omitempty"`
	KeyframeStabilityMs  *float64 `json:"keyframe_stability_ms,omitempty"`
	IssuesDescription    *string  `json:"issues_description,omitempty"`
	HasIssues            bool     `json:"has_issues"`
	PacketLossPercentage *float64 `json:"packet_loss_percentage,omitempty"`
	QualityTier          *string  `json:"quality_tier,omitempty"`
	BufferState          string   `json:"buffer_state"`
}

// BufferEventsResponse represents the response from GetStreamBufferEvents
type BufferEventsResponse = []BufferEvent

// BufferEvent represents a buffer event
type BufferEvent struct {
	Timestamp time.Time `json:"timestamp"`
	EventID   string    `json:"event_id"`
	Status    string    `json:"status"`
	NodeID    string    `json:"node_id"`
	EventData string    `json:"event_data"`
}

// EndEventsResponse represents the response from GetStreamEndEvents
type EndEventsResponse = []EndEvent

// EndEvent represents a stream end event
type EndEvent struct {
	Timestamp time.Time `json:"timestamp"`
	EventID   string    `json:"event_id"`
	Status    string    `json:"status"`
	NodeID    string    `json:"node_id"`
	EventData string    `json:"event_data"`
}

// PlatformEventsResponse represents the response from GetPlatformEvents
type PlatformEventsResponse struct {
	Events []PlatformEvent `json:"events"`
	Count  int             `json:"count"`
}

// PlatformEvent represents a platform-wide analytics event
type PlatformEvent struct {
	Timestamp         time.Time `json:"timestamp"`
	InternalName      string    `json:"internal_name"`
	NodeID            string    `json:"node_id"`
	ViewerCount       int       `json:"viewer_count"`
	ConnectionType    string    `json:"connection_type"`
	BufferHealth      float32   `json:"buffer_health"`
	ConnectionQuality float32   `json:"connection_quality"`
	CountryCode       string    `json:"country_code"`
	City              string    `json:"city"`
}

// ErrorResponse is a type alias to the common error response
type ErrorResponse = common.ErrorResponse

// ClipEventsResponse represents paged clip lifecycle events
type ClipEventsResponse struct {
	Events []ClipEvent `json:"events"`
}

// ClipEvent represents a single clip lifecycle analytics event
type ClipEvent struct {
	Timestamp    time.Time `json:"timestamp"`
	InternalName string    `json:"internal_name"`
	RequestID    string    `json:"request_id"`
	Stage        string    `json:"stage"`
	ContentType  *string   `json:"content_type,omitempty"`
	StartUnix    *int64    `json:"start_unix,omitempty"`     // StartedAt from protobuf
	StopUnix     *int64    `json:"stop_unix,omitempty"`      // CompletedAt from protobuf
	IngestNodeID *string   `json:"ingest_node_id,omitempty"` // NodeId from protobuf
	Percent      *uint32   `json:"percent,omitempty"`        // ProgressPercent from protobuf
	Message      *string   `json:"message,omitempty"`        // Error from protobuf
	FilePath     *string   `json:"file_path,omitempty"`      // FilePath from protobuf
	S3URL        *string   `json:"s3_url,omitempty"`         // S3Url from protobuf
	SizeBytes    *uint64   `json:"size_bytes,omitempty"`     // SizeBytes from protobuf
}
