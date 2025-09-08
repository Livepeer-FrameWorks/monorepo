package foghorn

import "time"

// CreateClipRequest is the typed request payload for orchestrating a clip
type CreateClipRequest struct {
	TenantID     string `json:"tenant_id,omitempty"`
	InternalName string `json:"internal_name"`
	Format       string `json:"format,omitempty"`
	Title        string `json:"title,omitempty"`
	StartUnix    *int64 `json:"start_unix,omitempty"`
	StopUnix     *int64 `json:"stop_unix,omitempty"`
	StartMS      *int64 `json:"start_ms,omitempty"`
	StopMS       *int64 `json:"stop_ms,omitempty"`
	DurationSec  *int64 `json:"duration_sec,omitempty"`
}

// CreateClipResponse is the typed response payload returned by Foghorn
type CreateClipResponse struct {
	Status      string `json:"status"`
	IngestHost  string `json:"ingest_host,omitempty"`
	StorageHost string `json:"storage_host,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	ClipHash    string `json:"clip_hash,omitempty"`
}

// ClipInfo represents clip metadata for internal Foghorn endpoints
type ClipInfo struct {
	ID          string    `json:"id"`
	ClipHash    string    `json:"clip_hash"`
	StreamName  string    `json:"stream_name"`
	Title       string    `json:"title"`
	StartTime   int64     `json:"start_time"`
	Duration    int64     `json:"duration"`
	NodeID      string    `json:"node_id"`
	StoragePath string    `json:"storage_path"`
	SizeBytes   *int64    `json:"size_bytes"`
	Status      string    `json:"status"`
	AccessCount int       `json:"access_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// ClipsListResponse represents a paginated list of clips for internal use
type ClipsListResponse struct {
	Clips []ClipInfo `json:"clips"`
	Total int        `json:"total"`
	Page  int        `json:"page"`
	Limit int        `json:"limit"`
}

// ClipNodeInfo represents node information for clip viewing
type ClipNodeInfo struct {
	NodeID   string                 `json:"node_id"`
	BaseURL  string                 `json:"base_url"`
	Outputs  map[string]interface{} `json:"outputs"`
	ClipHash string                 `json:"clip_hash"`
	Status   string                 `json:"status"`
}

// ClipViewingURLs represents the viewing URLs generated for a clip
type ClipViewingURLs struct {
	URLs      map[string]string `json:"urls"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}

// DVR-related types

// StartDVRRequest is the typed request payload for starting DVR recording
type StartDVRRequest struct {
	TenantID     string `json:"tenant_id"`
	InternalName string `json:"internal_name"`
	StreamID     string `json:"stream_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
}

// StartDVRResponse is the response from starting DVR recording
type StartDVRResponse struct {
	Status        string `json:"status"`
	DVRHash       string `json:"dvr_hash"`
	IngestHost    string `json:"ingest_host,omitempty"`
	StorageHost   string `json:"storage_host,omitempty"`
	StorageNodeID string `json:"storage_node_id,omitempty"`
}

// DVRInfo represents DVR recording metadata
type DVRInfo struct {
	DVRHash         string     `json:"dvr_hash"`
	InternalName    string     `json:"internal_name"`
	StorageNodeID   string     `json:"storage_node_id"`
	Status          string     `json:"status"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	DurationSeconds *int32     `json:"duration_seconds,omitempty"`
	SizeBytes       *int64     `json:"size_bytes,omitempty"`
	ManifestPath    string     `json:"manifest_path,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// DVRListResponse represents a paginated list of DVR recordings
type DVRListResponse struct {
	DVRRecordings []DVRInfo `json:"dvr_recordings"`
	Total         int       `json:"total"`
	Page          int       `json:"page"`
	Limit         int       `json:"limit"`
}

// Viewer endpoint resolution types

// ViewerEndpointRequest is the request for viewer endpoint resolution
type ViewerEndpointRequest struct {
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id"`
	ViewerIP    string `json:"viewer_ip,omitempty"`
}

// ViewerEndpoint represents a single resolved endpoint from Foghorn
type ViewerEndpoint struct {
	NodeID      string                    `json:"node_id"`
	BaseURL     string                    `json:"base_url"`
	Protocol    string                    `json:"protocol"`
	URL         string                    `json:"url"`
	GeoDistance float64                   `json:"geo_distance"`
	LoadScore   float64                   `json:"load_score"`
	HealthScore float64                   `json:"health_score"`
	Outputs     map[string]OutputEndpoint `json:"outputs,omitempty"`
}

// OutputCapability describes capabilities for a given protocol output
type OutputCapability struct {
	SupportsSeek          bool     `json:"supports_seek"`
	SupportsQualitySwitch bool     `json:"supports_quality_switch"`
	MaxBitrate            int      `json:"max_bitrate,omitempty"`
	HasAudio              bool     `json:"has_audio"`
	HasVideo              bool     `json:"has_video"`
	Codecs                []string `json:"codecs,omitempty"`
}

// OutputEndpoint represents a concrete endpoint for a protocol with capabilities
type OutputEndpoint struct {
	Protocol     string           `json:"protocol"`
	URL          string           `json:"url"`
	Capabilities OutputCapability `json:"capabilities"`
}

// ViewerEndpointResponse is the response from viewer endpoint resolution
type ViewerEndpointResponse struct {
	Primary   ViewerEndpoint    `json:"primary"`
	Fallbacks []ViewerEndpoint  `json:"fallbacks"`
	Metadata  *PlaybackMetadata `json:"metadata,omitempty"`
}

// PlaybackMetadata is a richer, player-oriented metadata payload
type PlaybackMetadata struct {
	Status        string             `json:"status"`
	IsLive        bool               `json:"is_live"`
	Viewers       int                `json:"viewers"`
	BufferState   string             `json:"buffer_state,omitempty"`
	HealthScore   *float64           `json:"health_score,omitempty"`
	Tracks        []PlaybackTrack    `json:"tracks,omitempty"`
	ProtocolHints []string           `json:"protocol_hints,omitempty"`
	Instances     []PlaybackInstance `json:"instances,omitempty"`
	DvrStatus     string             `json:"dvr_status,omitempty"`
	DvrSourceURI  string             `json:"dvr_source_uri,omitempty"`
}

type PlaybackTrack struct {
	Type        string `json:"type"`
	Codec       string `json:"codec"`
	BitrateKbps int    `json:"bitrate_kbps,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	SampleRate  int    `json:"sample_rate,omitempty"`
}

type PlaybackInstance struct {
	NodeID           string    `json:"node_id"`
	Viewers          int       `json:"viewers"`
	BufferState      string    `json:"buffer_state,omitempty"`
	BytesUp          int64     `json:"bytes_up,omitempty"`
	BytesDown        int64     `json:"bytes_down,omitempty"`
	TotalConnections int       `json:"total_connections,omitempty"`
	Inputs           int       `json:"inputs,omitempty"`
	LastUpdate       time.Time `json:"last_update"`
}

// StreamMetaRequest requests Mist JSON meta for a given internal name
// ContentType should be one of: "live" | "dvr" | "clip". Defaults to "live".
type StreamMetaRequest struct {
	InternalName  string `json:"internal_name" binding:"required"`
	ContentType   string `json:"content_type,omitempty"`
	IncludeRaw    bool   `json:"include_raw,omitempty"`
	TargetNodeID  string `json:"target_node_id,omitempty"`
	TargetBaseURL string `json:"target_base_url,omitempty"`
}

// TrackSummary is a concise description of an available track
type TrackSummary struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Codec    string `json:"codec"`
	Channels *int   `json:"channels,omitempty"`
	Rate     *int   `json:"rate,omitempty"`
	Width    *int   `json:"width,omitempty"`
	Height   *int   `json:"height,omitempty"`
	Bitrate  *int   `json:"bitrate_bps,omitempty"`
	NowMs    *int64 `json:"now_ms,omitempty"`
	LastMs   *int64 `json:"last_ms,omitempty"`
	FirstMs  *int64 `json:"first_ms,omitempty"`
}

// MetaSummary contains the key values the player/UI needs
type MetaSummary struct {
	IsLive         bool           `json:"is_live"`
	BufferWindowMs int64          `json:"buffer_window_ms"`
	JitterMs       int64          `json:"jitter_ms"`
	UnixOffset     int64          `json:"unix_offset_ms"`
	NowMs          *int64         `json:"now_ms,omitempty"`
	LastMs         *int64         `json:"last_ms,omitempty"`
	Width          *int           `json:"width,omitempty"`
	Height         *int           `json:"height,omitempty"`
	Version        *int           `json:"version,omitempty"`
	Type           string         `json:"type,omitempty"`
	Tracks         []TrackSummary `json:"tracks,omitempty"`
}

// StreamMetaResponse returns the summarized meta and optional raw JSON
type StreamMetaResponse struct {
	MetaSummary MetaSummary `json:"meta_summary"`
	Raw         any         `json:"raw,omitempty"`
}
