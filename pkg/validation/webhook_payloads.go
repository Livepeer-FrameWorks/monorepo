package validation

import (
	"frameworks/pkg/api/periscope"
	"frameworks/pkg/geoip"
)

// WebhookPayloads define typed data structures for MistServer webhook events
// These extend existing types to avoid duplication while ensuring type safety

// UserConnectionPayload represents USER_NEW and USER_END webhook data
// Extends periscope.ConnectionEvent with webhook-specific fields
type UserConnectionPayload struct {
	periscope.ConnectionEvent

	// Webhook-specific fields not in periscope.ConnectionEvent
	Action            string `json:"action"`                       // "connect" or "disconnect"
	SessionIdentifier string `json:"session_identifier,omitempty"` // USER_END only
	ConnectionID      string `json:"connection_id,omitempty"`      // USER_NEW only
	RequestURL        string `json:"request_url,omitempty"`        // USER_NEW only
	SecondsConnected  int    `json:"seconds_connected,omitempty"`  // USER_END only
	UploadedBytes     int64  `json:"uploaded_bytes,omitempty"`     // USER_END only
	DownloadedBytes   int64  `json:"downloaded_bytes,omitempty"`   // USER_END only
	Tags              string `json:"tags,omitempty"`               // USER_END only
}

// StreamLifecyclePayload represents STREAM_BUFFER, STREAM_END, and stream lifecycle events
type StreamLifecyclePayload struct {
	StreamName   string `json:"stream_name"`
	InternalName string `json:"internal_name"`
	NodeID       string `json:"node_id"`
	TenantID     string `json:"tenant_id"`
	Status       string `json:"status"`       // "live", "offline"
	BufferState  string `json:"buffer_state"` // "FULL", "EMPTY", "DRY", "RECOVER"

	// Stream end metrics
	DownloadedBytes int64 `json:"downloaded_bytes,omitempty"`
	UploadedBytes   int64 `json:"uploaded_bytes,omitempty"`
	TotalViewers    int   `json:"total_viewers,omitempty"`
	TotalInputs     int   `json:"total_inputs,omitempty"`
	TotalOutputs    int   `json:"total_outputs,omitempty"`
	ViewerSeconds   int64 `json:"viewer_seconds,omitempty"`

	// Stream details (JSON)
	StreamDetails string `json:"stream_details,omitempty"`

	// Parsed health metrics
	HealthScore       float64 `json:"health_score,omitempty"`
	HasIssues         bool    `json:"has_issues,omitempty"`
	IssuesDesc        string  `json:"issues_description,omitempty"`
	TrackCount        int     `json:"track_count,omitempty"`
	QualityTier       string  `json:"quality_tier,omitempty"`
	PrimaryWidth      int     `json:"primary_width,omitempty"`
	PrimaryHeight     int     `json:"primary_height,omitempty"`
	PrimaryFPS        float64 `json:"primary_fps,omitempty"`
	PrimaryResolution string  `json:"primary_resolution,omitempty"` // Calculated as "WIDTHxHEIGHT"

	// Detailed frame timing metrics from STREAM_BUFFER JSON
	PrimaryCodec        string  `json:"primary_codec,omitempty"`
	PrimaryBitrate      int     `json:"primary_bitrate,omitempty"`       // kbits
	FrameJitterMS       float64 `json:"frame_jitter_ms,omitempty"`       // frame timing variability
	KeyFrameStabilityMS float64 `json:"keyframe_stability_ms,omitempty"` // keyframe interval stability
	FrameMSMax          float64 `json:"frame_ms_max,omitempty"`          // max frame duration
	FrameMSMin          float64 `json:"frame_ms_min,omitempty"`          // min frame duration
	FramesMax           int     `json:"frames_max,omitempty"`            // max frames in segment
	FramesMin           int     `json:"frames_min,omitempty"`            // min frames in segment
	KeyFrameIntervalMS  float64 `json:"keyframe_interval_ms,omitempty"`  // keyframe interval
}

// BandwidthThresholdPayload represents LIVE_BANDWIDTH webhook data
type BandwidthThresholdPayload struct {
	StreamName         string `json:"stream_name"`
	InternalName       string `json:"internal_name"`
	NodeID             string `json:"node_id"`
	TenantID           string `json:"tenant_id"`
	CurrentBytesPerSec int64  `json:"current_bytes_per_sec"`
	ThresholdExceeded  bool   `json:"threshold_exceeded"`
	ThresholdValue     int64  `json:"threshold_value,omitempty"`
}

// TrackListPayload represents LIVE_TRACK_LIST webhook data
type TrackListPayload struct {
	StreamName    string `json:"stream_name"`
	InternalName  string `json:"internal_name"`
	NodeID        string `json:"node_id"`
	TenantID      string `json:"tenant_id"`
	TrackListJSON string `json:"track_list"` // Raw JSON from MistServer

	// Parsed quality metrics
	TrackCount             int     `json:"track_count"`
	VideoTrackCount        int     `json:"video_track_count"`
	AudioTrackCount        int     `json:"audio_track_count"`
	QualityTier            string  `json:"quality_tier,omitempty"`
	PrimaryWidth           int     `json:"primary_width,omitempty"`
	PrimaryHeight          int     `json:"primary_height,omitempty"`
	PrimaryFPS             float64 `json:"primary_fps,omitempty"`
	PrimaryVideoBitrate    int     `json:"primary_video_bitrate,omitempty"`
	PrimaryVideoCodec      string  `json:"primary_video_codec,omitempty"`
	PrimaryAudioBitrate    int     `json:"primary_audio_bitrate,omitempty"`
	PrimaryAudioCodec      string  `json:"primary_audio_codec,omitempty"`
	PrimaryAudioChannels   int     `json:"primary_audio_channels,omitempty"`
	PrimaryAudioSampleRate int     `json:"primary_audio_sample_rate,omitempty"`
}

// RecordingPayload represents RECORDING_END webhook data
type RecordingPayload struct {
	StreamName      string `json:"stream_name"`
	InternalName    string `json:"internal_name"`
	NodeID          string `json:"node_id"`
	TenantID        string `json:"tenant_id"`
	FilePath        string `json:"file_path"`
	OutputProtocol  string `json:"output_protocol"`
	BytesWritten    int64  `json:"bytes_written"`
	SecondsWriting  int    `json:"seconds_writing"`
	TimeStarted     int64  `json:"time_started"`      // Unix timestamp
	TimeEnded       int64  `json:"time_ended"`        // Unix timestamp
	MediaDurationMs int64  `json:"media_duration_ms"` // Duration in milliseconds
	IsRecording     bool   `json:"is_recording"`      // Always false for RECORDING_END
}

// PushLifecyclePayload represents PUSH_END, PUSH_OUT_START webhook data
type PushLifecyclePayload struct {
	StreamName      string `json:"stream_name"`
	InternalName    string `json:"internal_name"`
	NodeID          string `json:"node_id"`
	TenantID        string `json:"tenant_id"`
	PushID          string `json:"push_id,omitempty"` // PUSH_END only
	PushTarget      string `json:"push_target"`
	TargetURIBefore string `json:"target_uri_before,omitempty"` // PUSH_END only
	TargetURIAfter  string `json:"target_uri_after,omitempty"`  // PUSH_END only
	Status          string `json:"status,omitempty"`            // JSON string for PUSH_END
	LogMessages     string `json:"log_messages,omitempty"`      // JSON array for PUSH_END
	Action          string `json:"action"`                      // "start" or "end"
}

// StreamIngestPayload represents stream ingest/push rewrite events
type StreamIngestPayload struct {
	StreamKey    string `json:"stream_key"`
	InternalName string `json:"internal_name"`
	UserID       string `json:"user_id,omitempty"`
	NodeID       string `json:"node_id"`
	TenantID     string `json:"tenant_id"`
	Hostname     string `json:"hostname"`
	PushURL      string `json:"push_url"`
	Protocol     string `json:"protocol"`

	// Geographic metadata from node location
	geoip.GeoData
	Location string `json:"location,omitempty"` // Keep separate location string for descriptive name
}

// StreamViewPayload represents DEFAULT_STREAM webhook data
type StreamViewPayload struct {
	TenantID     string `json:"tenant_id"`
	PlaybackID   string `json:"playback_id"`
	InternalName string `json:"internal_name"`
	NodeID       string `json:"node_id"`
	ViewerHost   string `json:"viewer_host"`
	OutputType   string `json:"output_type"`
	RequestURL   string `json:"request_url,omitempty"`

	// Geographic data for viewer
	geoip.GeoData
}

// NodeLifecyclePayload represents node health monitoring data from MistServer
type NodeLifecyclePayload struct {
	NodeID    string `json:"node_id"`
	BaseURL   string `json:"base_url"`
	IsHealthy bool   `json:"is_healthy"`

	// Geographic metadata from node location
	geoip.GeoData
	Location string `json:"location,omitempty"` // Keep separate location string for descriptive name

	// Raw MistServer metrics from koekjes.json endpoint
	CPUUsage       float64 `json:"cpu,omitempty"`             // Raw cpu field (tenths of percentage)
	RAMMax         uint64  `json:"ram_max,omitempty"`         // Raw ram.max field (MiB)
	RAMCurrent     uint64  `json:"ram_current,omitempty"`     // Raw ram.current field (MiB)
	BandwidthUp    uint64  `json:"bandwidth_up,omitempty"`    // Raw bandwidth.up field (bytes/sec)
	BandwidthDown  uint64  `json:"bandwidth_down,omitempty"`  // Raw bandwidth.down field (bytes/sec)
	BandwidthLimit uint64  `json:"bandwidth_limit,omitempty"` // Raw bandwidth.limit field (bytes/sec)
	ActiveStreams  int     `json:"active_streams,omitempty"`  // Count of streams map
}

// ConvertGeoDataToPayload converts geoip.GeoData to payload fields
func ConvertGeoDataToPayload(geo *geoip.GeoData) (string, string, float64, float64) {
	if geo == nil {
		return "", "", 0, 0
	}
	return geo.CountryCode, geo.City, geo.Latitude, geo.Longitude
}

// ClientLifecyclePayload represents per-client metrics from MistServer clients API polling
type ClientLifecyclePayload struct {
	StreamName           string  `json:"stream_name"`
	InternalName         string  `json:"internal_name"`
	NodeID               string  `json:"node_id"`
	TenantID             string  `json:"tenant_id"`
	Protocol             string  `json:"protocol"`
	Host                 string  `json:"host"`
	SessionID            string  `json:"session_id"`
	ConnectionTime       float64 `json:"connection_time"`    // seconds
	Position             float64 `json:"position,omitempty"` // playback position
	BandwidthIn          float64 `json:"bandwidth_in"`       // bytes per second
	BandwidthOut         float64 `json:"bandwidth_out"`      // bytes per second
	BytesDown            int64   `json:"bytes_downloaded"`
	BytesUp              int64   `json:"bytes_uploaded"`
	PacketsSent          int64   `json:"packets_sent,omitempty"`
	PacketsLost          int64   `json:"packets_lost,omitempty"`
	PacketsRetransmitted int64   `json:"packets_retransmitted,omitempty"`
}

// ConvertPeriscopeConnectionToPayload converts periscope.ConnectionEvent to UserConnectionPayload
func ConvertPeriscopeConnectionToPayload(conn *periscope.ConnectionEvent) *UserConnectionPayload {
	if conn == nil {
		return &UserConnectionPayload{}
	}

	return &UserConnectionPayload{
		ConnectionEvent: *conn,
		// Webhook-specific fields will be set by webhook handlers
	}
}
