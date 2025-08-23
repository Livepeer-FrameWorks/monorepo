package validation

// FoghornNodeUpdate represents typed node metrics update data sent from Helmsman to Foghorn
// This replaces the untyped map[string]interface{} in UpdateNodeMetrics
type FoghornNodeUpdate struct {
	// Raw node metrics from MistServer koekjes.json (matching Helmsman's NodeLifecyclePayload)
	CPU        float64 `json:"cpu"`         // Raw CPU (tenths of percentage, 0-1000)
	RAMMax     float64 `json:"ram_max"`     // Raw RAM max (MiB)
	RAMCurrent float64 `json:"ram_current"` // Raw RAM current (MiB)
	UpSpeed    float64 `json:"up_speed"`    // Raw upload speed (bytes/sec)
	DownSpeed  float64 `json:"down_speed"`  // Raw download speed (bytes/sec)
	BWLimit    float64 `json:"bwlimit"`     // Raw bandwidth limit (bytes/sec)

	// Geographic location from MistServer
	Location FoghornLocationData `json:"loc"`

	// Tags from MistServer configuration
	Tags []string `json:"tags,omitempty"`

	// Stream data from MistServer active streams
	Streams map[string]FoghornStreamData `json:"streams,omitempty"`

	// Config streams from MistServer
	ConfigStreams []string `json:"conf_streams,omitempty"`
}

// FoghornLocationData represents geographic data for nodes
type FoghornLocationData struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
	Name      string  `json:"name,omitempty"`
}

// FoghornStreamData represents stream metrics data from MistServer
type FoghornStreamData struct {
	Total      uint64 `json:"total"`      // Current viewer count
	Inputs     uint32 `json:"inputs"`     // Input connection count
	Bandwidth  uint32 `json:"bandwidth"`  // Calculated bandwidth per viewer
	BytesUp    uint64 `json:"bytes_up"`   // Total uploaded bytes
	BytesDown  uint64 `json:"bytes_down"` // Total downloaded bytes
	Replicated bool   `json:"replicated"` // Stream replication status
}

// FoghornStreamHealth represents stream health update data
// This replaces the untyped map[string]interface{} in stream health details
type FoghornStreamHealth struct {
	BufferState   string             `json:"buffer_state,omitempty"`   // "FULL", "EMPTY", "DRY", "RECOVER"
	BandwidthData string             `json:"bandwidth_data,omitempty"` // JSON bandwidth metrics
	TrackDetails  []FoghornTrackData `json:"track_details,omitempty"`  // Parsed track information
	HealthScore   float64            `json:"health_score,omitempty"`   // Calculated health score
	HasIssues     bool               `json:"has_issues,omitempty"`     // Whether stream has issues
	IssuesDesc    string             `json:"issues_desc,omitempty"`    // Description of issues
}

// FoghornTrackData represents individual track information
type FoghornTrackData struct {
	TrackName string `json:"track_name"`
	Type      string `json:"type"` // "video", "audio", "meta"
	Codec     string `json:"codec"`
	Bitrate   int    `json:"bitrate_kbps,omitempty"`

	// Video-specific fields
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	Resolution string  `json:"resolution,omitempty"`

	// Audio-specific fields
	Channels   int `json:"channels,omitempty"`
	SampleRate int `json:"sample_rate,omitempty"`

	// Quality metrics
	Buffer int `json:"buffer,omitempty"`
	Jitter int `json:"jitter,omitempty"`
}

// FoghornNodeShutdown represents node shutdown details
// This replaces the untyped map[string]interface{} in shutdown details
type FoghornNodeShutdown struct {
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	LastSeen     int64  `json:"last_seen,omitempty"`
	Graceful     bool   `json:"graceful"`
	UpTime       int64  `json:"uptime_seconds,omitempty"`
}
