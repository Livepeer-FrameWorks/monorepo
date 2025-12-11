package state

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	pb "frameworks/pkg/proto"
)

// Metrics hooks (optional)
var (
	writeCounter             func(labels map[string]string)
	rehydrateDurationObserve func(seconds float64, labels map[string]string)
)

// SetMetricsHooks allows the caller to inject metrics callbacks
func SetMetricsHooks(onWrite func(labels map[string]string), onRehydrateDuration func(seconds float64, labels map[string]string)) {
	writeCounter = onWrite
	rehydrateDurationObserve = onRehydrateDuration
}

// StreamTrack represents track information from MistServer
type StreamTrack struct {
	TrackID    string  `json:"track_id"`
	Codec      string  `json:"codec"`
	Type       string  `json:"type"` // "video", "audio"
	Bitrate    int     `json:"bitrate,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	Channels   int     `json:"channels,omitempty"`
	SampleRate int     `json:"sample_rate,omitempty"`
}

// StreamState represents the current state of a stream
type StreamState struct {
	StreamName         string                 `json:"stream_name"`
	InternalName       string                 `json:"internal_name"`
	NodeID             string                 `json:"node_id"`
	TenantID           string                 `json:"tenant_id"`
	Status             string                 `json:"status"`       // "live", "offline", etc.
	BufferState        string                 `json:"buffer_state"` // "FULL", "EMPTY", "DRY", "RECOVER"
	Tracks             []StreamTrack          `json:"tracks"`
	Issues             string                 `json:"issues,omitempty"`
	HasIssues          bool                   `json:"has_issues"`
	StartedAt          *time.Time             `json:"started_at,omitempty"` // When stream first went live
	LastUpdate         time.Time              `json:"last_update"`
	RawDetails         map[string]interface{} `json:"raw_details,omitempty"` // Raw MistServer data
	Viewers            int                    `json:"viewers"`
	LastTrackList      string                 `json:"last_track_list,omitempty"`
	CurrentBytesPerSec int64                  `json:"current_bytes_per_sec,omitempty"`
	TotalConnections   int                    `json:"total_connections,omitempty"`
	Inputs             int                    `json:"inputs,omitempty"`
	BytesUp            int64                  `json:"bytes_up,omitempty"`
	BytesDown          int64                  `json:"bytes_down,omitempty"`
}

// StreamInstanceState represents per-node state for a specific stream
type StreamInstanceState struct {
	NodeID             string                 `json:"node_id"`
	TenantID           string                 `json:"tenant_id"`
	Status             string                 `json:"status"`
	BufferState        string                 `json:"buffer_state"`
	LastTrackList      string                 `json:"last_track_list,omitempty"`
	CurrentBytesPerSec int64                  `json:"current_bytes_per_sec,omitempty"`
	Viewers            int                    `json:"viewers"`
	BytesUp            int64                  `json:"bytes_up,omitempty"`
	BytesDown          int64                  `json:"bytes_down,omitempty"`
	TotalConnections   int                    `json:"total_connections,omitempty"`
	Inputs             int                    `json:"inputs,omitempty"`
	LastUpdate         time.Time              `json:"last_update"`
	RawDetails         map[string]interface{} `json:"raw_details,omitempty"`
}

// NodeState captures per-node state
type NodeState struct {
	NodeID               string                 `json:"node_id"`
	BaseURL              string                 `json:"base_url"`
	IsHealthy            bool                   `json:"is_healthy"`
	IsStale              bool                   `json:"is_stale"`            // Node hasn't reported recently
	TenantID             string                 `json:"tenant_id,omitempty"` // Tenant owning this dedicated node
	Latitude             *float64               `json:"latitude,omitempty"`
	Longitude            *float64               `json:"longitude,omitempty"`
	Location             string                 `json:"location,omitempty"`
	Outputs              map[string]interface{} `json:"outputs,omitempty"`
	OutputsRaw           string                 `json:"outputs_raw,omitempty"`
	CPU                  float64                `json:"cpu,omitempty"`
	RAMMax               float64                `json:"ram_max,omitempty"`
	RAMCurrent           float64                `json:"ram_current,omitempty"`
	UpSpeed              float64                `json:"up_speed,omitempty"`
	DownSpeed            float64                `json:"down_speed,omitempty"`
	BWLimit              float64                `json:"bw_limit,omitempty"`
	CapIngest            bool                   `json:"cap_ingest,omitempty"`
	CapEdge              bool                   `json:"cap_edge,omitempty"`
	CapStorage           bool                   `json:"cap_storage,omitempty"`
	CapProcessing        bool                   `json:"cap_processing,omitempty"`
	Roles                []string               `json:"roles,omitempty"`
	StorageCapacityBytes uint64                 `json:"storage_capacity_bytes,omitempty"`
	StorageUsedBytes     uint64                 `json:"storage_used_bytes,omitempty"`
	MaxTranscodes        int                    `json:"max_transcodes,omitempty"`
	CurrentTranscodes    int                    `json:"current_transcodes,omitempty"`
	DiskTotalBytes       uint64                 `json:"disk_total_bytes,omitempty"`
	DiskUsedBytes        uint64                 `json:"disk_used_bytes,omitempty"`
	LastUpdate           time.Time              `json:"last_update"`

	// GPU information
	GPUVendor string `json:"gpu_vendor,omitempty"`
	GPUCount  int    `json:"gpu_count,omitempty"`
	GPUMemMB  int    `json:"gpu_mem_mb,omitempty"`
	GPUCC     string `json:"gpu_cc,omitempty"`

	// Storage paths
	StorageLocal  string `json:"storage_local,omitempty"`
	StorageBucket string `json:"storage_bucket,omitempty"`
	StoragePrefix string `json:"storage_prefix,omitempty"`

	// Performance-critical fields for load balancer
	BinHost       [16]byte `json:"-"`                        // Binary IP for fast comparison (don't serialize)
	Port          int      `json:"port,omitempty"`           // Main service port
	DTSCPort      int      `json:"dtsc_port,omitempty"`      // DTSC protocol port
	Tags          []string `json:"tags,omitempty"`           // Tags for scoring adjustments
	ConfigStreams []string `json:"config_streams,omitempty"` // Streams this node can serve
	AddBandwidth  uint64   `json:"add_bandwidth,omitempty"`  // Bandwidth penalty tracking

	// Artifacts stored on this node
	Artifacts []*pb.StoredArtifact `json:"artifacts,omitempty"`

	// Cached scoring helpers (computed on update)
	CPUScore      uint64    `json:"-"` // Pre-computed CPU score component
	RAMScore      uint64    `json:"-"` // Pre-computed RAM score component
	BWAvailable   uint64    `json:"-"` // Available bandwidth (BWLimit - UpSpeed - AddBandwidth)
	LastScoreTime time.Time `json:"-"` // When scores were last computed
}

// StreamStateManager manages in-memory cluster state
type StreamStateManager struct {
	streams         map[string]*StreamState                    // internal_name -> union summary
	streamInstances map[string]map[string]*StreamInstanceState // internal_name -> node_id -> instance
	nodes           map[string]*NodeState                      // node_id -> node state
	mu              sync.RWMutex

	// Load balancer weights (exactly like C++ version)
	WeightCPU   uint64
	WeightRAM   uint64
	WeightBW    uint64
	WeightGeo   uint64
	WeightBonus uint64

	// Staleness detection
	stalenessChecker chan bool

	// Policies and repos (new)
	writePolicies map[EntityType]WritePolicy
	syncPolicies  map[EntityType]SyncPolicy
	cachePolicies map[string]CachePolicy
	repos         struct {
		Clips ClipRepository
		DVR   DVRRepository
	}
	nodeRepo NodeRepository // Hook into persistence/caching layer

	// Reconciler (new)
	reconcileStop   chan struct{}
	reconcileTicker *time.Ticker
}

// NewStreamStateManager creates a new stream state manager
func NewStreamStateManager() *StreamStateManager {
	sm := &StreamStateManager{
		streams:          make(map[string]*StreamState),
		streamInstances:  make(map[string]map[string]*StreamInstanceState),
		nodes:            make(map[string]*NodeState),
		stalenessChecker: make(chan bool),

		// Load balancer weights (same as C++ defaults)
		WeightCPU:   500,  // Same as C++
		WeightRAM:   500,  // Same as C++
		WeightBW:    1000, // Same as C++
		WeightGeo:   1000, // Same as C++
		WeightBonus: 50,   // Same as C++ (not 200!)

		// New fields
		writePolicies: make(map[EntityType]WritePolicy),
		syncPolicies:  make(map[EntityType]SyncPolicy),
		cachePolicies: make(map[string]CachePolicy),
		reconcileStop: make(chan struct{}),
	}

	// Start staleness detection background goroutine
	go sm.runStalenessChecker()

	return sm
}

// UpdateStreamFromBuffer updates stream state from STREAM_BUFFER event
func (sm *StreamStateManager) UpdateStreamFromBuffer(streamName, internalName, nodeID, tenantID, bufferState string, streamDetailsJSON string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Get or create stream state
	state := sm.streams[internalName]
	now := time.Now()
	if state == nil {
		state = &StreamState{
			StreamName:   streamName,
			InternalName: internalName,
			NodeID:       nodeID,
			TenantID:     tenantID,
			StartedAt:    &now, // Track when stream first went live
		}
		sm.streams[internalName] = state
	}

	// ensure instance container
	if sm.streamInstances[internalName] == nil {
		sm.streamInstances[internalName] = make(map[string]*StreamInstanceState)
	}
	inst := sm.streamInstances[internalName][nodeID]
	if inst == nil {
		inst = &StreamInstanceState{NodeID: nodeID, TenantID: tenantID}
		sm.streamInstances[internalName][nodeID] = inst
	}

	// Update basic fields
	state.BufferState = bufferState
	state.Status = "live" // Set to live when buffer is available
	state.LastUpdate = time.Now()

	inst.BufferState = bufferState
	inst.Status = "live"
	inst.LastUpdate = time.Now()

	// Parse stream details if provided
	if streamDetailsJSON != "" {
		var details map[string]interface{}
		if err := json.Unmarshal([]byte(streamDetailsJSON), &details); err != nil {
			return err
		}

		state.RawDetails = details
		state.Tracks = extractTracksFromDetails(details)

		inst.RawDetails = details

		// Extract issues
		if issues, ok := details["issues"].(string); ok {
			state.Issues = issues
			state.HasIssues = true
		} else {
			state.HasIssues = false
		}
	}

	return nil
}

// GetStreamState returns the current state for a stream
func (sm *StreamStateManager) GetStreamState(internalName string) *StreamState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if state, exists := sm.streams[internalName]; exists {
		// Return a copy to avoid concurrent modification
		stateCopy := *state
		stateCopy.Tracks = make([]StreamTrack, len(state.Tracks))
		copy(stateCopy.Tracks, state.Tracks)
		return &stateCopy
	}
	return nil
}

// GetAllStreamStates returns all current stream states
func (sm *StreamStateManager) GetAllStreamStates() []*StreamState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	states := make([]*StreamState, 0, len(sm.streams))
	for _, state := range sm.streams {
		// Return copies
		stateCopy := *state
		stateCopy.Tracks = make([]StreamTrack, len(state.Tracks))
		copy(stateCopy.Tracks, state.Tracks)
		states = append(states, &stateCopy)
	}
	return states
}

// RemoveStream removes a stream from state tracking
func (sm *StreamStateManager) RemoveStream(internalName string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.streams, internalName)
}

// CleanupStaleStreams removes streams that haven't been updated recently
func (sm *StreamStateManager) CleanupStaleStreams(maxAge time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for internalName, state := range sm.streams {
		if state.LastUpdate.Before(cutoff) {
			delete(sm.streams, internalName)
		}
	}
}

// extractTracksFromDetails parses MistServer stream details to extract track info
func extractTracksFromDetails(details map[string]interface{}) []StreamTrack {
	var tracks []StreamTrack

	// Process each track to extract codec, quality, and metrics
	for trackID, trackData := range details {
		if trackID == "issues" {
			continue // Skip issues field
		}

		if track, ok := trackData.(map[string]interface{}); ok {
			streamTrack := StreamTrack{
				TrackID: trackID,
			}

			// Extract codec
			if codec, ok := track["codec"].(string); ok {
				streamTrack.Codec = codec

				// Determine track type from codec
				switch codec {
				case "H264", "HEVC", "VP8", "VP9", "AV1":
					streamTrack.Type = "video"
				case "AAC", "MP3", "Opus", "AC3":
					streamTrack.Type = "audio"
				default:
					// Try to infer from other fields
					if _, hasWidth := track["width"]; hasWidth {
						streamTrack.Type = "video"
					} else if _, hasChannels := track["channels"]; hasChannels {
						streamTrack.Type = "audio"
					}
				}
			}

			// Extract bitrate
			if kbits, ok := track["kbits"].(float64); ok {
				streamTrack.Bitrate = int(kbits)
			}

			// Extract FPS
			if fpks, ok := track["fpks"].(float64); ok {
				streamTrack.FPS = fpks / 1000.0 // Convert from fpks to fps
			}

			// Extract video dimensions
			if height, ok := track["height"].(float64); ok {
				streamTrack.Height = int(height)
			}
			if width, ok := track["width"].(float64); ok {
				streamTrack.Width = int(width)
			}

			// Extract audio properties
			if channels, ok := track["channels"].(float64); ok {
				streamTrack.Channels = int(channels)
			}
			if rate, ok := track["rate"].(float64); ok {
				streamTrack.SampleRate = int(rate)
			}

			tracks = append(tracks, streamTrack)
		}
	}

	return tracks
}

// Default manager and extra mutation helpers
var defaultManager *StreamStateManager

func DefaultManager() *StreamStateManager {
	if defaultManager == nil {
		defaultManager = NewStreamStateManager()
	}
	return defaultManager
}

func (sm *StreamStateManager) UpdateUserConnection(internalName, nodeID, tenantID string, delta int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	union := sm.streams[internalName]
	if union == nil {
		union = &StreamState{InternalName: internalName, StreamName: internalName}
		sm.streams[internalName] = union
	}
	union.Viewers += delta
	if union.Viewers < 0 {
		union.Viewers = 0
	}
	union.NodeID = nodeID
	union.TenantID = tenantID
	union.LastUpdate = time.Now()
	if sm.streamInstances[internalName] == nil {
		sm.streamInstances[internalName] = make(map[string]*StreamInstanceState)
	}
	inst := sm.streamInstances[internalName][nodeID]
	if inst == nil {
		inst = &StreamInstanceState{NodeID: nodeID, TenantID: tenantID}
		sm.streamInstances[internalName][nodeID] = inst
	}
	inst.Viewers += delta
	if inst.Viewers < 0 {
		inst.Viewers = 0
	}
	inst.LastUpdate = time.Now()

	// Apply bandwidth penalty when a viewer connects (delta > 0)
	if delta > 0 {
		sm.addViewerBandwidthPenalty(nodeID, internalName, inst)
	}
}

// addViewerBandwidthPenalty implements bandwidth penalty tracking when a viewer connects (must hold lock)
func (sm *StreamStateManager) addViewerBandwidthPenalty(nodeID string, _ string, streamInst *StreamInstanceState) {
	node := sm.nodes[nodeID]
	if node == nil {
		return
	}

	var toAdd uint64 = 0

	// Check if we can estimate bandwidth from this stream instance
	if streamInst != nil && streamInst.BytesUp > 0 && streamInst.TotalConnections > 0 {
		toAdd = uint64(streamInst.BytesUp) / uint64(streamInst.TotalConnections)
	} else {
		// Calculate estimated bandwidth like C++ balancer
		totalViewers := uint64(0)
		totalBytesUp := uint64(0)

		// Sum across all streams on this node
		for _, nodeInstances := range sm.streamInstances {
			if inst := nodeInstances[nodeID]; inst != nil {
				totalViewers += uint64(inst.TotalConnections)
				totalBytesUp += uint64(inst.BytesUp)
			}
		}

		if totalViewers > 0 && totalBytesUp > 0 {
			toAdd = totalBytesUp / totalViewers
		} else {
			toAdd = 131072 // assume 1mbps (like C++)
		}
	}

	// Ensure reasonable limits (like C++)
	if toAdd < 64*1024 {
		toAdd = 64 * 1024 // minimum 0.5 mbps
	}
	if toAdd > 1024*1024 {
		toAdd = 1024 * 1024 // maximum 8 mbps
	}

	node.AddBandwidth += toAdd

	// Recompute scores since bandwidth penalty changed
	sm.recomputeNodeScoresLocked(node)
}

func (sm *StreamStateManager) UpdateTrackList(internalName, nodeID, tenantID, trackListJSON string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	union := sm.streams[internalName]
	if union == nil {
		union = &StreamState{InternalName: internalName, StreamName: internalName}
		sm.streams[internalName] = union
	}
	union.NodeID = nodeID
	union.TenantID = tenantID
	union.LastTrackList = trackListJSON
	union.LastUpdate = time.Now()
	if sm.streamInstances[internalName] == nil {
		sm.streamInstances[internalName] = make(map[string]*StreamInstanceState)
	}
	inst := sm.streamInstances[internalName][nodeID]
	if inst == nil {
		inst = &StreamInstanceState{NodeID: nodeID, TenantID: tenantID}
		sm.streamInstances[internalName][nodeID] = inst
	}
	inst.LastTrackList = trackListJSON
	inst.LastUpdate = time.Now()
}

func (sm *StreamStateManager) UpdateBandwidth(internalName string, bps int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	union := sm.streams[internalName]
	if union == nil {
		union = &StreamState{InternalName: internalName, StreamName: internalName}
		sm.streams[internalName] = union
	}
	union.CurrentBytesPerSec = bps
	union.LastUpdate = time.Now()
}

func (sm *StreamStateManager) SetOffline(internalName, nodeID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	union := sm.streams[internalName]
	if union == nil {
		union = &StreamState{InternalName: internalName, StreamName: internalName}
		sm.streams[internalName] = union
	}
	union.Status = "offline"
	union.BufferState = "EMPTY"
	union.NodeID = nodeID
	union.LastUpdate = time.Now()
	if sm.streamInstances[internalName] == nil {
		sm.streamInstances[internalName] = make(map[string]*StreamInstanceState)
	}
	inst := sm.streamInstances[internalName][nodeID]
	if inst == nil {
		inst = &StreamInstanceState{NodeID: nodeID}
		sm.streamInstances[internalName][nodeID] = inst
	}
	inst.Status = "offline"
	inst.BufferState = "EMPTY"
	inst.LastUpdate = time.Now()
}

func (sm *StreamStateManager) UpdateNodeStats(internalName, nodeID string, total, inputs int, up, down int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	union := sm.streams[internalName]
	if union == nil {
		union = &StreamState{InternalName: internalName, StreamName: internalName}
		sm.streams[internalName] = union
	}
	union.NodeID = nodeID
	union.TotalConnections = total
	union.Inputs = inputs
	union.BytesUp = up
	union.BytesDown = down
	union.LastUpdate = time.Now()
	if sm.streamInstances[internalName] == nil {
		sm.streamInstances[internalName] = make(map[string]*StreamInstanceState)
	}
	inst := sm.streamInstances[internalName][nodeID]
	if inst == nil {
		inst = &StreamInstanceState{NodeID: nodeID}
		sm.streamInstances[internalName][nodeID] = inst
	}
	inst.TotalConnections = total
	inst.Inputs = inputs
	inst.BytesUp = up
	inst.BytesDown = down
	inst.LastUpdate = time.Now()
}

// UpdateStreamInstanceInfo merges arbitrary info into the per-node instance RawDetails
func (sm *StreamStateManager) UpdateStreamInstanceInfo(internalName, nodeID string, info map[string]interface{}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.streamInstances[internalName] == nil {
		sm.streamInstances[internalName] = make(map[string]*StreamInstanceState)
	}
	inst := sm.streamInstances[internalName][nodeID]
	if inst == nil {
		inst = &StreamInstanceState{NodeID: nodeID}
		sm.streamInstances[internalName][nodeID] = inst
	}
	if inst.RawDetails == nil {
		inst.RawDetails = make(map[string]interface{})
	}
	for k, v := range info {
		inst.RawDetails[k] = v
	}
	inst.LastUpdate = time.Now()
}

// SetNodeInfo updates per-node info
func (sm *StreamStateManager) SetNodeInfo(nodeID, baseURL string, isHealthy bool, lat, lon *float64, location string, outputsRaw string, outputs map[string]interface{}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	n := sm.nodes[nodeID]
	if n == nil {
		n = &NodeState{NodeID: nodeID}
		sm.nodes[nodeID] = n
	}
	n.BaseURL = baseURL
	n.IsHealthy = isHealthy
	n.IsStale = false // Reset staleness on update
	n.Latitude = lat
	n.Longitude = lon
	n.Location = location

	// Handle outputs parsing
	if outputs != nil {
		n.Outputs = outputs
	} else if outputsRaw != "" {
		// Try to parse raw JSON if map not provided (e.g. rehydration)
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(outputsRaw), &parsed); err == nil {
			n.Outputs = parsed
		}
	}

	if outputsRaw != "" {
		n.OutputsRaw = outputsRaw
	}
	n.LastUpdate = time.Now()
}

// UpdateNodeMetrics updates node metrics, capabilities, roles, and storage info
func (sm *StreamStateManager) UpdateNodeMetrics(nodeID string, metrics struct {
	CPU                  float64
	RAMMax               float64
	RAMCurrent           float64
	UpSpeed              float64
	DownSpeed            float64
	BWLimit              float64
	CapIngest            bool
	CapEdge              bool
	CapStorage           bool
	CapProcessing        bool
	Roles                []string
	StorageCapacityBytes uint64
	StorageUsedBytes     uint64
	MaxTranscodes        int
	CurrentTranscodes    int
}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	n := sm.nodes[nodeID]
	if n == nil {
		n = &NodeState{NodeID: nodeID}
		sm.nodes[nodeID] = n
	}
	n.CPU = metrics.CPU
	n.RAMMax = metrics.RAMMax
	n.RAMCurrent = metrics.RAMCurrent
	n.UpSpeed = metrics.UpSpeed
	n.DownSpeed = metrics.DownSpeed
	n.BWLimit = metrics.BWLimit
	n.CapIngest = metrics.CapIngest
	n.CapEdge = metrics.CapEdge
	n.CapStorage = metrics.CapStorage
	n.CapProcessing = metrics.CapProcessing
	n.Roles = append([]string(nil), metrics.Roles...)
	n.StorageCapacityBytes = metrics.StorageCapacityBytes
	n.StorageUsedBytes = metrics.StorageUsedBytes
	n.MaxTranscodes = metrics.MaxTranscodes
	n.CurrentTranscodes = metrics.CurrentTranscodes
	n.LastUpdate = time.Now()

	// Recompute cached scores
	sm.recomputeNodeScoresLocked(n)
}

// SetNodeGPUInfo updates GPU information for a node
func (sm *StreamStateManager) SetNodeGPUInfo(nodeID string, gpuVendor string, gpuCount int, gpuMemMB int, gpuCC string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = &NodeState{NodeID: nodeID}
		sm.nodes[nodeID] = n
	}

	n.GPUVendor = gpuVendor
	n.GPUCount = gpuCount
	n.GPUMemMB = gpuMemMB
	n.GPUCC = gpuCC
	n.LastUpdate = time.Now()
}

// SetNodeStoragePaths updates storage path information for a node
func (sm *StreamStateManager) SetNodeStoragePaths(nodeID string, storageLocal, storageBucket, storagePrefix string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = &NodeState{NodeID: nodeID}
		sm.nodes[nodeID] = n
	}

	n.StorageLocal = storageLocal
	n.StorageBucket = storageBucket
	n.StoragePrefix = storagePrefix
	n.LastUpdate = time.Now()
}

// BalancerStreamSummary provides per-stream metrics for balancer decisions
type BalancerStreamSummary struct {
	Total      uint64 `json:"total"`
	Inputs     uint32 `json:"inputs"`
	Bandwidth  uint32 `json:"bandwidth"`
	BytesUp    uint64 `json:"bytes_up"`
	BytesDown  uint64 `json:"bytes_down"`
	Replicated bool   `json:"replicated"`
}

// BalancerNodeSnapshot is a read-optimized view for the load balancer
type BalancerNodeSnapshot struct {
	Host                 string                           `json:"host"`
	NodeID               string                           `json:"node_id"`
	GeoLatitude          float64                          `json:"geo_latitude"`
	GeoLongitude         float64                          `json:"geo_longitude"`
	LocationName         string                           `json:"location_name"`
	CPU                  uint64                           `json:"cpu_tenths"`
	RAMMax               uint64                           `json:"ram_max"`
	RAMCurrent           uint64                           `json:"ram_current"`
	UpSpeed              uint64                           `json:"up_speed"`
	DownSpeed            uint64                           `json:"down_speed"`
	AvailBandwidth       uint64                           `json:"avail_bandwidth"`
	IsActive             bool                             `json:"is_active"`
	Streams              map[string]BalancerStreamSummary `json:"streams"`
	Roles                []string                         `json:"roles"`
	CapIngest            bool                             `json:"cap_ingest"`
	CapEdge              bool                             `json:"cap_edge"`
	CapStorage           bool                             `json:"cap_storage"`
	CapProcessing        bool                             `json:"cap_processing"`
	StorageCapacityBytes uint64                           `json:"storage_capacity_bytes"`
	StorageUsedBytes     uint64                           `json:"storage_used_bytes"`
}

// GetBalancerNodeSnapshots returns a read-optimized snapshot for the balancer
func (sm *StreamStateManager) GetBalancerNodeSnapshots() []BalancerNodeSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	snapshots := make([]BalancerNodeSnapshot, 0, len(sm.nodes))

	// Build a quick reverse index of nodeID -> streams on that node
	nodeStreams := make(map[string]map[string]BalancerStreamSummary)
	for internalName, instances := range sm.streamInstances {
		for nodeID, inst := range instances {
			if inst == nil {
				continue
			}
			m := nodeStreams[nodeID]
			if m == nil {
				m = make(map[string]BalancerStreamSummary)
				nodeStreams[nodeID] = m
			}
			m[internalName] = BalancerStreamSummary{
				Total:      uint64(inst.TotalConnections),
				Inputs:     uint32(inst.Inputs),
				Bandwidth:  0,
				BytesUp:    uint64(inst.BytesUp),
				BytesDown:  uint64(inst.BytesDown),
				Replicated: false,
			}
		}
	}

	for nodeID, n := range sm.nodes {
		if n == nil {
			continue
		}
		snap := BalancerNodeSnapshot{
			Host:   n.BaseURL,
			NodeID: nodeID,
			GeoLatitude: func() float64 {
				if n.Latitude != nil {
					return *n.Latitude
				}
				return 0
			}(),
			GeoLongitude: func() float64 {
				if n.Longitude != nil {
					return *n.Longitude
				}
				return 0
			}(),
			LocationName:         n.Location,
			CPU:                  uint64(n.CPU * 10.0),
			RAMMax:               uint64(n.RAMMax),
			RAMCurrent:           uint64(n.RAMCurrent),
			UpSpeed:              uint64(n.UpSpeed),
			DownSpeed:            uint64(n.DownSpeed),
			AvailBandwidth:       uint64(n.BWLimit),
			IsActive:             n.IsHealthy,
			Streams:              nodeStreams[nodeID],
			Roles:                append([]string(nil), n.Roles...),
			CapIngest:            n.CapIngest,
			CapEdge:              n.CapEdge,
			CapStorage:           n.CapStorage,
			CapProcessing:        n.CapProcessing,
			StorageCapacityBytes: n.StorageCapacityBytes,
			StorageUsedBytes:     n.StorageUsedBytes,
		}
		snapshots = append(snapshots, snap)
	}

	return snapshots
}

// GetStreamInstances returns copies of per-node instances for a stream
func (sm *StreamStateManager) GetStreamInstances(internalName string) map[string]StreamInstanceState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[string]StreamInstanceState)
	for nodeID, inst := range sm.streamInstances[internalName] {
		if inst == nil {
			continue
		}
		c := *inst
		if inst.RawDetails != nil {
			copied := make(map[string]interface{}, len(inst.RawDetails))
			for k, v := range inst.RawDetails {
				copied[k] = v
			}
			c.RawDetails = copied
		}
		out[nodeID] = c
	}
	return out
}

// GetNodeState returns a copy of the node state
func (sm *StreamStateManager) GetNodeState(nodeID string) *NodeState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	n := sm.nodes[nodeID]
	if n == nil {
		return nil
	}
	c := *n
	if n.Outputs != nil {
		copied := make(map[string]interface{}, len(n.Outputs))
		for k, v := range n.Outputs {
			copied[k] = v
		}
		c.Outputs = copied
	}
	return &c
}

// GetClusterSnapshot returns copies of streams and nodes
func (sm *StreamStateManager) GetClusterSnapshot() (streams []*StreamState, nodes []*NodeState) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, s := range sm.streams {
		c := *s
		c.Tracks = make([]StreamTrack, len(s.Tracks))
		copy(c.Tracks, s.Tracks)
		streams = append(streams, &c)
	}
	for _, n := range sm.nodes {
		c := *n
		if n.Outputs != nil {
			copied := make(map[string]interface{}, len(n.Outputs))
			for k, v := range n.Outputs {
				copied[k] = v
			}
			c.Outputs = copied
		}
		nodes = append(nodes, &c)
	}
	return
}

// SetNodeConnectionInfo updates connection-related information for a node.
// It primarily focuses on setting the binary host IP for same-host avoidance in the balancer.
// Node tags are updated only if explicitly provided, allowing for flexible tag management.
func (sm *StreamStateManager) SetNodeConnectionInfo(nodeID string, host string, tenantID string, tags []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = &NodeState{NodeID: nodeID}
		sm.nodes[nodeID] = n
	}

	if tenantID != "" {
		n.TenantID = tenantID
	}

	// Update node tags only if explicitly provided (non-nil slice)
	if tags != nil {
		n.Tags = append([]string(nil), tags...) // Copy to avoid shared slices
	}

	// Compute binary IP for fast comparison in load balancing decisions.
	if host != "" {
		n.BinHost = hostToBinary(host)
	}

	n.LastUpdate = time.Now() // Update timestamp since node info changed.

	// Recompute cached scores as node connection info (like BinHost) affects eligibility.
	sm.recomputeNodeScoresLocked(n)
}

// FindNodeByArtifactHash searches for a node hosting the specified artifact (Clip/DVR).
// Returns the node's host/base URL and the artifact details if found.
func (sm *StreamStateManager) FindNodeByArtifactHash(hash string) (string, *pb.StoredArtifact) {
	snapshot := sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return "", nil
	}

	for _, node := range snapshot.Nodes {
		// Skip inactive nodes
		if !node.IsActive {
			continue
		}
		for _, artifact := range node.Artifacts {
			if artifact.GetClipHash() == hash {
				return node.Host, artifact
			}
		}
	}
	return "", nil
}

// UpdateAddBandwidth updates the bandwidth penalty for a node
func (sm *StreamStateManager) UpdateAddBandwidth(nodeID string, addBandwidth uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		return
	}

	n.AddBandwidth = addBandwidth
	n.LastUpdate = time.Now()

	// Recompute cached scores since bandwidth affects them
	sm.recomputeNodeScoresLocked(n)
}

// SetNodeArtifacts updates the artifacts stored on a node
func (sm *StreamStateManager) SetNodeArtifacts(nodeID string, artifacts []*pb.StoredArtifact) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = &NodeState{NodeID: nodeID}
		sm.nodes[nodeID] = n
	}

	// Deep copy artifacts to avoid shared slices
	n.Artifacts = make([]*pb.StoredArtifact, len(artifacts))
	copy(n.Artifacts, artifacts)
	n.LastUpdate = time.Now()
}

// RemoveNodeArtifact removes a specific artifact from a node's list
func (sm *StreamStateManager) RemoveNodeArtifact(nodeID string, clipHash string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil || len(n.Artifacts) == 0 {
		return
	}

	// Filter out the deleted artifact
	newArtifacts := make([]*pb.StoredArtifact, 0, len(n.Artifacts))
	for _, a := range n.Artifacts {
		if a.ClipHash != clipHash {
			newArtifacts = append(newArtifacts, a)
		}
	}

	n.Artifacts = newArtifacts
	n.LastUpdate = time.Now()
}

// ApplyArtifactDeleted updates state and persists artifact deletion
func (sm *StreamStateManager) ApplyArtifactDeleted(ctx context.Context, clipHash string, nodeID string) error {
	// Remove from in-memory state
	sm.RemoveNodeArtifact(nodeID, clipHash)

	// Track metric
	if writeCounter != nil {
		writeCounter(map[string]string{"entity": "artifact", "op": "deleted"})
	}

	return nil
}

// DecayAddBandwidth applies bandwidth penalty decay to a node (like C++ LoadBalancer)
func (sm *StreamStateManager) DecayAddBandwidth(nodeID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		return
	}

	// Apply 0.75 decay factor (25% reduction each update)
	n.AddBandwidth = uint64(float64(n.AddBandwidth) * 0.75)
	n.LastUpdate = time.Now()

	// Recompute cached scores since bandwidth affects them
	sm.recomputeNodeScoresLocked(n)
}

// recomputeNodeScoresLocked recalculates cached score components for a node (must hold write lock)
func (sm *StreamStateManager) recomputeNodeScoresLocked(n *NodeState) {
	if n == nil {
		return
	}

	// Pre-compute scoring components to avoid repeated calculations during balancing
	// These use the actual weights from the state manager
	// Formulas match exactly: WeightCPU - (node.CPU*WeightCPU)/1000

	// CPU Score: WeightCPU - (node.CPU*WeightCPU)/1000
	// CPU is stored as 0-100 percentage, but balancer expects 0-1000 (tenths)
	if n.CPU <= 100 {
		cpuTenths := uint64(n.CPU * 10) // Convert to tenths of percentage
		n.CPUScore = sm.WeightCPU - (cpuTenths * sm.WeightCPU / 1000)
	} else {
		n.CPUScore = 0
	}

	// RAM Score: WeightRAM - ((node.RAMCurrent * WeightRAM) / node.RAMMax)
	if n.RAMMax > 0 {
		n.RAMScore = sm.WeightRAM - (uint64(n.RAMCurrent) * sm.WeightRAM / uint64(n.RAMMax))
	} else {
		n.RAMScore = 0
	}

	// Available Bandwidth: BWLimit - UpSpeed - AddBandwidth
	if n.BWLimit > 0 {
		used := uint64(n.UpSpeed) + n.AddBandwidth
		if used < uint64(n.BWLimit) {
			n.BWAvailable = uint64(n.BWLimit) - used
		} else {
			n.BWAvailable = 0
		}
	} else {
		n.BWAvailable = 0
	}

	n.LastScoreTime = time.Now()
}

// SetWeights updates the scoring weights and triggers score recomputation (like C++ /?weights= endpoint)
func (sm *StreamStateManager) SetWeights(cpu, ram, bandwidth, geo, streamBonus uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.WeightCPU = cpu
	sm.WeightRAM = ram
	sm.WeightBW = bandwidth
	sm.WeightGeo = geo
	sm.WeightBonus = streamBonus

	// Recompute all cached scores since weights changed
	for _, node := range sm.nodes {
		if node != nil {
			sm.recomputeNodeScoresLocked(node)
		}
	}
}

// GetWeights returns current weights
func (sm *StreamStateManager) GetWeights() map[string]uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return map[string]uint64{
		"cpu":   sm.WeightCPU,
		"ram":   sm.WeightRAM,
		"bw":    sm.WeightBW,
		"geo":   sm.WeightGeo,
		"bonus": sm.WeightBonus,
	}
}

// EnhancedBalancerNodeSnapshot includes pre-computed fields for fast scoring
type EnhancedBalancerNodeSnapshot struct {
	// Basic node info
	Host         string    `json:"host"`
	NodeID       string    `json:"node_id"`
	GeoLatitude  float64   `json:"geo_latitude"`
	GeoLongitude float64   `json:"geo_longitude"`
	LocationName string    `json:"location_name"`
	IsActive     bool      `json:"is_active"`
	LastUpdate   time.Time `json:"last_update"`

	// Performance-critical fields
	BinHost       [16]byte `json:"-"` // Binary IP for fast comparison
	Port          int      `json:"port"`
	DTSCPort      int      `json:"dtsc_port"`
	Tags          []string `json:"tags"`
	ConfigStreams []string `json:"config_streams"`
	AddBandwidth  uint64   `json:"add_bandwidth"`

	// Raw metrics
	CPU        float64 `json:"cpu"`
	RAMMax     float64 `json:"ram_max"`
	RAMCurrent float64 `json:"ram_current"`
	UpSpeed    float64 `json:"up_speed"`
	DownSpeed  float64 `json:"down_speed"`
	BWLimit    float64 `json:"bw_limit"`

	// Pre-computed scoring components
	CPUScore       uint64 `json:"-"`               // Pre-computed CPU score
	RAMScore       uint64 `json:"-"`               // Pre-computed RAM score
	BWAvailable    uint64 `json:"-"`               // Available bandwidth
	AvailBandwidth uint64 `json:"avail_bandwidth"` // Available bandwidth

	// Capabilities and storage
	Roles                []string `json:"roles"`
	CapIngest            bool     `json:"cap_ingest"`
	CapEdge              bool     `json:"cap_edge"`
	CapStorage           bool     `json:"cap_storage"`
	CapProcessing        bool     `json:"cap_processing"`
	StorageCapacityBytes uint64   `json:"storage_capacity_bytes"`
	StorageUsedBytes     uint64   `json:"storage_used_bytes"`

	// GPU information
	GPUVendor string `json:"gpu_vendor"`
	GPUCount  int    `json:"gpu_count"`
	GPUMemMB  int    `json:"gpu_mem_mb"`
	GPUCC     string `json:"gpu_cc"`

	// Storage paths
	StorageLocal  string `json:"storage_local"`
	StorageBucket string `json:"storage_bucket"`
	StoragePrefix string `json:"storage_prefix"`

	// Disk Usage (Real-time)
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	DiskUsedBytes  uint64 `json:"disk_used_bytes"`

	// Transcoding info
	MaxTranscodes     int `json:"max_transcodes"`
	CurrentTranscodes int `json:"current_transcodes"`

	// Artifacts stored on this node
	Artifacts []*pb.StoredArtifact `json:"artifacts"`

	// Stream summaries for this node
	Streams map[string]BalancerStreamSummary `json:"streams"`
}

// BalancerSnapshot provides an immutable, consistent view for load balancing
type BalancerSnapshot struct {
	Nodes     []EnhancedBalancerNodeSnapshot `json:"nodes"`
	Timestamp time.Time                      `json:"timestamp"`
}

// GetBalancerSnapshotAtomic returns an immutable snapshot with pre-computed values for consistent load balancing
func (sm *StreamStateManager) GetBalancerSnapshotAtomic() *BalancerSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	nodes := make([]EnhancedBalancerNodeSnapshot, 0, len(sm.nodes))

	// Build stream summaries by node
	nodeStreams := make(map[string]map[string]BalancerStreamSummary)
	for internalName, instances := range sm.streamInstances {
		for nodeID, inst := range instances {
			if inst == nil {
				continue
			}
			m := nodeStreams[nodeID]
			if m == nil {
				m = make(map[string]BalancerStreamSummary)
				nodeStreams[nodeID] = m
			}
			m[internalName] = BalancerStreamSummary{
				Total:      uint64(inst.TotalConnections),
				Inputs:     uint32(inst.Inputs),
				Bandwidth:  0, // TODO: Calculate from BytesUp/BytesDown if needed
				BytesUp:    uint64(inst.BytesUp),
				BytesDown:  uint64(inst.BytesDown),
				Replicated: false, // TODO: Detect replication if needed
			}
		}
	}

	// Create enhanced snapshots for each node
	for nodeID, n := range sm.nodes {
		if n == nil {
			continue
		}

		// Skip stale or unhealthy nodes to prevent routing blackholes
		if !n.IsHealthy || n.IsStale {
			continue
		}

		snapshot := EnhancedBalancerNodeSnapshot{
			Host:   n.BaseURL,
			NodeID: nodeID,
			GeoLatitude: func() float64 {
				if n.Latitude != nil {
					return *n.Latitude
				}
				return 0
			}(),
			GeoLongitude: func() float64 {
				if n.Longitude != nil {
					return *n.Longitude
				}
				return 0
			}(),
			LocationName: n.Location,
			IsActive:     n.IsHealthy,
			LastUpdate:   n.LastUpdate,

			// Performance-critical fields
			BinHost:       n.BinHost,
			Port:          n.Port,
			DTSCPort:      n.DTSCPort,
			Tags:          append([]string(nil), n.Tags...),
			ConfigStreams: append([]string(nil), n.ConfigStreams...),
			AddBandwidth:  n.AddBandwidth,

			// Raw metrics
			CPU:        n.CPU,
			RAMMax:     n.RAMMax,
			RAMCurrent: n.RAMCurrent,
			UpSpeed:    n.UpSpeed,
			DownSpeed:  n.DownSpeed,
			BWLimit:    n.BWLimit,

			// Pre-computed scoring components
			CPUScore:       n.CPUScore,
			RAMScore:       n.RAMScore,
			BWAvailable:    n.BWAvailable,
			AvailBandwidth: n.BWAvailable,

			// Capabilities
			Roles:                append([]string(nil), n.Roles...),
			CapIngest:            n.CapIngest,
			CapEdge:              n.CapEdge,
			CapStorage:           n.CapStorage,
			CapProcessing:        n.CapProcessing,
			StorageCapacityBytes: n.StorageCapacityBytes,
			StorageUsedBytes:     n.StorageUsedBytes,

			// GPU information
			GPUVendor: n.GPUVendor,
			GPUCount:  n.GPUCount,
			GPUMemMB:  n.GPUMemMB,
			GPUCC:     n.GPUCC,

			// Storage paths
			StorageLocal:  n.StorageLocal,
			StorageBucket: n.StorageBucket,
			StoragePrefix: n.StoragePrefix,

			// Disk Usage
			DiskTotalBytes: n.DiskTotalBytes,
			DiskUsedBytes:  n.DiskUsedBytes,

			// Transcoding info
			MaxTranscodes:     n.MaxTranscodes,
			CurrentTranscodes: n.CurrentTranscodes,

			// Artifacts stored on this node
			Artifacts: append([]*pb.StoredArtifact(nil), n.Artifacts...),

			// Stream summaries
			Streams: nodeStreams[nodeID],
		}

		// Ensure empty streams map instead of nil
		if snapshot.Streams == nil {
			snapshot.Streams = make(map[string]BalancerStreamSummary)
		}

		nodes = append(nodes, snapshot)
	}

	return &BalancerSnapshot{
		Nodes:     nodes,
		Timestamp: time.Now(),
	}
}

// hostToBinary converts a hostname/IP to binary format for fast comparison
func hostToBinary(host string) [16]byte {
	var binHost [16]byte

	// Try to parse as IP first
	ip := net.ParseIP(host)
	if ip != nil {
		// Convert to 16-byte representation (IPv6 format)
		if ipv4 := ip.To4(); ipv4 != nil {
			// IPv4: store in last 4 bytes with IPv4-mapped prefix
			copy(binHost[10:12], []byte{0xff, 0xff})
			copy(binHost[12:16], ipv4)
		} else if ipv6 := ip.To16(); ipv6 != nil {
			// IPv6: use full 16 bytes
			copy(binHost[:], ipv6)
		}
		return binHost
	}

	// If not an IP, resolve hostname to IP
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		// Return zero array if can't resolve
		return binHost
	}

	// Use first resolved address
	ip = net.ParseIP(addrs[0])
	if ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			copy(binHost[10:12], []byte{0xff, 0xff})
			copy(binHost[12:16], ipv4)
		} else if ipv6 := ip.To16(); ipv6 != nil {
			copy(binHost[:], ipv6)
		}
	}

	return binHost
}

// compareBinaryHosts checks if two binary host addresses are equal
func CompareBinaryHosts(host1, host2 [16]byte) bool {
	return host1 == host2
}

// runStalenessChecker runs a background goroutine to detect stale nodes
func (sm *StreamStateManager) runStalenessChecker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.checkStaleNodes()
		case <-sm.stalenessChecker:
			return
		}
	}
}

// checkStaleNodes marks nodes as stale but preserves their data
func (sm *StreamStateManager) checkStaleNodes() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	staleThreshold := 90 * time.Second

	for _, node := range sm.nodes {
		if node.LastUpdate.IsZero() {
			continue
		}

		timeSinceUpdate := now.Sub(node.LastUpdate)
		if timeSinceUpdate > staleThreshold {
			if !node.IsStale {
				node.IsStale = true
			}
		} else {
			if node.IsStale {
				node.IsStale = false
			}
		}
	}
}

// Shutdown gracefully shuts down the state manager
func (sm *StreamStateManager) Shutdown() {
	close(sm.stalenessChecker)
	if sm.reconcileTicker != nil {
		sm.reconcileTicker.Stop()
	}
	select {
	case <-sm.reconcileStop:
		// already closed
	default:
		close(sm.reconcileStop)
	}
}

// ConfigurePolicies applies policies and repos, then optionally rehydrates and starts reconciler
func (sm *StreamStateManager) ConfigurePolicies(cfg PoliciesConfig) {
	sm.mu.Lock()
	if cfg.WritePolicies != nil {
		sm.writePolicies = cfg.WritePolicies
	}
	if cfg.SyncPolicies != nil {
		sm.syncPolicies = cfg.SyncPolicies
	}
	if cfg.CachePolicies != nil {
		sm.cachePolicies = cfg.CachePolicies
	}
	sm.repos.Clips = cfg.ClipRepo
	sm.repos.DVR = cfg.DVRRepo
	sm.nodeRepo = cfg.NodeRepo
	sm.startReconcilerLocked()
	sm.mu.Unlock()

	if sm.shouldBootRehydrate() {
		_ = sm.Rehydrate(context.Background())
	}
}

func (sm *StreamStateManager) shouldBootRehydrate() bool {
	if p, ok := sm.syncPolicies[EntityClip]; ok && p.BootRehydrate {
		return true
	}
	if p, ok := sm.syncPolicies[EntityDVR]; ok && p.BootRehydrate {
		return true
	}
	return false
}

func (sm *StreamStateManager) startReconcilerLocked() {
	var interval time.Duration
	if p, ok := sm.syncPolicies[EntityClip]; ok {
		if p.ReconcileInterval > 0 && (interval == 0 || p.ReconcileInterval < interval) {
			interval = p.ReconcileInterval
		}
	}
	if p, ok := sm.syncPolicies[EntityDVR]; ok {
		if p.ReconcileInterval > 0 && (interval == 0 || p.ReconcileInterval < interval) {
			interval = p.ReconcileInterval
		}
	}
	if interval <= 0 {
		if sm.reconcileTicker != nil {
			sm.reconcileTicker.Stop()
			sm.reconcileTicker = nil
		}
		return
	}
	if sm.reconcileTicker != nil {
		sm.reconcileTicker.Reset(interval)
		return
	}
	sm.reconcileTicker = time.NewTicker(interval)
	go sm.runReconciler(sm.reconcileTicker)
}

func (sm *StreamStateManager) runReconciler(t *time.Ticker) {
	for {
		select {
		case <-t.C:
			_ = sm.Rehydrate(context.Background())
		case <-sm.reconcileStop:
			return
		}
	}
}

// Rehydrate refreshes in-memory state from durable repositories based on sync policies
func (sm *StreamStateManager) Rehydrate(ctx context.Context) error {
	start := time.Now()
	// Nodes
	if sm.nodeRepo != nil {
		recs, err := sm.nodeRepo.ListAllNodes(ctx)
		if err == nil {
			for _, r := range recs {
				// Basic node info
				sm.SetNodeInfo(r.NodeID, r.BaseURL, true, nil, nil, "", r.OutputsJSON, nil)
			}
		}
	}
	// DVR
	if sm.repos.DVR != nil {
		recs, err := sm.repos.DVR.ListAllDVR(ctx)
		if err == nil {
			for _, r := range recs {
				if r.InternalName == "" || r.StorageNodeID == "" {
					continue
				}
				sm.UpdateStreamInstanceInfo(r.InternalName, r.StorageNodeID, map[string]interface{}{
					"dvr_status":           r.Status,
					"dvr_hash":             r.Hash,
					"dvr_manifest_path":    r.ManifestPath,
					"dvr_duration_seconds": r.DurationSec,
					"dvr_size_bytes":       r.SizeBytes,
					"dvr_source_uri":       r.SourceURL,
				})
			}
		}
	}
	// Clips
	if sm.repos.Clips != nil {
		recs, err := sm.repos.Clips.ListActiveClips(ctx)
		if err == nil {
			for _, r := range recs {
				if r.InternalName == "" || r.NodeID == "" {
					continue
				}
				sm.UpdateStreamInstanceInfo(r.InternalName, r.NodeID, map[string]interface{}{
					"clip_status": r.Status,
					"clip_path":   r.StoragePath,
					"clip_size":   r.SizeBytes,
					"clip_hash":   r.ClipHash,
				})
			}
		}
	}
	if rehydrateDurationObserve != nil {
		rehydrateDurationObserve(time.Since(start).Seconds(), map[string]string{"entity": "all"})
	}
	return nil
}

// ApplyClipProgress updates state and persists clip progress by request_id
func (sm *StreamStateManager) ApplyClipProgress(ctx context.Context, requestID string, percent uint32, message string, nodeID string) error {
	if sm.repos.Clips != nil && sm.writePolicies[EntityClip].Enabled && sm.writePolicies[EntityClip].Mode == WriteThrough {
		_ = sm.repos.Clips.UpdateClipProgressByRequestID(ctx, requestID, percent)
		if writeCounter != nil {
			writeCounter(map[string]string{"entity": "clip", "op": "progress"})
		}
	}
	if sm.repos.Clips != nil {
		if internal, err := sm.repos.Clips.ResolveInternalNameByRequestID(ctx, requestID); err == nil && internal != "" {
			sm.UpdateStreamInstanceInfo(internal, nodeID, map[string]interface{}{
				"clip_status":   "processing",
				"clip_progress": percent,
				"clip_message":  message,
			})
		}
	}
	return nil
}

// ApplyClipDone updates state and persists clip completion by request_id
func (sm *StreamStateManager) ApplyClipDone(ctx context.Context, requestID string, status string, filePath string, sizeBytes uint64, errorMsg string, nodeID string) error {
	if sm.repos.Clips != nil && sm.writePolicies[EntityClip].Enabled && sm.writePolicies[EntityClip].Mode == WriteThrough {
		_ = sm.repos.Clips.UpdateClipDoneByRequestID(ctx, requestID, status, filePath, int64(sizeBytes), errorMsg)
		if writeCounter != nil {
			writeCounter(map[string]string{"entity": "clip", "op": "done"})
		}
	}
	if sm.repos.Clips != nil {
		if internal, err := sm.repos.Clips.ResolveInternalNameByRequestID(ctx, requestID); err == nil && internal != "" {
			sm.UpdateStreamInstanceInfo(internal, nodeID, map[string]interface{}{
				"clip_status": status,
				"clip_path":   filePath,
				"clip_size":   sizeBytes,
				"clip_error":  errorMsg,
			})
		}
	}
	return nil
}

// ApplyDVRProgress updates state and persists DVR progress by hash
func (sm *StreamStateManager) ApplyDVRProgress(ctx context.Context, dvrHash string, status string, sizeBytes uint64, segmentCount uint32, nodeID string) error {
	if sm.repos.DVR != nil && sm.writePolicies[EntityDVR].Enabled && sm.writePolicies[EntityDVR].Mode == WriteThrough {
		_ = sm.repos.DVR.UpdateDVRProgressByHash(ctx, dvrHash, status, int64(sizeBytes))
		if writeCounter != nil {
			writeCounter(map[string]string{"entity": "dvr", "op": "progress"})
		}
	}
	if sm.repos.DVR != nil {
		if internal, err := sm.repos.DVR.ResolveInternalNameByHash(ctx, dvrHash); err == nil && internal != "" {
			sm.UpdateStreamInstanceInfo(internal, nodeID, map[string]interface{}{
				"dvr_status":        status,
				"dvr_segment_count": segmentCount,
				"dvr_size_bytes":    sizeBytes,
			})
		}
	}
	return nil
}

// ApplyDVRStopped updates state and persists DVR completion by hash
func (sm *StreamStateManager) ApplyDVRStopped(ctx context.Context, dvrHash string, finalStatus string, durationSeconds int64, sizeBytes uint64, manifestPath string, errorMsg string, nodeID string) error {
	if sm.repos.DVR != nil && sm.writePolicies[EntityDVR].Enabled && sm.writePolicies[EntityDVR].Mode == WriteThrough {
		_ = sm.repos.DVR.UpdateDVRCompletionByHash(ctx, dvrHash, finalStatus, durationSeconds, int64(sizeBytes), manifestPath, errorMsg)
		if writeCounter != nil {
			writeCounter(map[string]string{"entity": "dvr", "op": "stopped"})
		}
	}
	if sm.repos.DVR != nil {
		if internal, err := sm.repos.DVR.ResolveInternalNameByHash(ctx, dvrHash); err == nil && internal != "" {
			sm.UpdateStreamInstanceInfo(internal, nodeID, map[string]interface{}{
				"dvr_status":           finalStatus,
				"dvr_manifest_path":    manifestPath,
				"dvr_duration_seconds": durationSeconds,
				"dvr_size_bytes":       sizeBytes,
				"dvr_error":            errorMsg,
			})
		}
	}
	return nil
}

// ApplyNodeLifecycle updates node info/metrics and persists outputs/base_url if configured
func (sm *StreamStateManager) ApplyNodeLifecycle(ctx context.Context, update *pb.NodeLifecycleUpdate) error {
	if update == nil {
		return nil
	}
	// Update in-memory node info and metrics
	var latPtr, lonPtr *float64
	if update.GetLatitude() != 0 {
		v := update.GetLatitude()
		latPtr = &v
	}
	if update.GetLongitude() != 0 {
		v := update.GetLongitude()
		lonPtr = &v
	}
	sm.SetNodeInfo(update.GetNodeId(), update.GetBaseUrl(), update.GetIsHealthy(), latPtr, lonPtr, update.GetLocation(), update.GetOutputsJson(), nil)
	sm.UpdateNodeMetrics(update.GetNodeId(), struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		MaxTranscodes        int
		CurrentTranscodes    int
	}{
		CPU:           float64(update.GetCpuTenths()) / 10.0,
		RAMMax:        float64(update.GetRamMax()),
		RAMCurrent:    float64(update.GetRamCurrent()),
		UpSpeed:       float64(update.GetUpSpeed()),
		DownSpeed:     float64(update.GetDownSpeed()),
		BWLimit:       float64(update.GetBwLimit()),
		CapIngest:     update.GetCapabilities() != nil && update.GetCapabilities().GetIngest(),
		CapEdge:       update.GetCapabilities() != nil && update.GetCapabilities().GetEdge(),
		CapStorage:    update.GetCapabilities() != nil && update.GetCapabilities().GetStorage(),
		CapProcessing: update.GetCapabilities() != nil && update.GetCapabilities().GetProcessing(),
		Roles: func() []string {
			if update.GetCapabilities() == nil {
				return nil
			}
			return update.GetCapabilities().GetRoles()
		}(),
		StorageCapacityBytes: func() uint64 {
			if update.GetLimits() == nil {
				return 0
			}
			return update.GetLimits().GetStorageCapacityBytes()
		}(),
		StorageUsedBytes: func() uint64 {
			if update.GetLimits() == nil {
				return 0
			}
			return update.GetLimits().GetStorageUsedBytes()
		}(),
		MaxTranscodes: func() int {
			if update.GetLimits() == nil {
				return 0
			}
			return int(update.GetLimits().GetMaxTranscodes())
		}(),
		CurrentTranscodes: 0,
	})

	// Update disk usage directly
	if n := sm.nodes[update.GetNodeId()]; n != nil {
		n.DiskTotalBytes = update.GetDiskTotalBytes()
		n.DiskUsedBytes = update.GetDiskUsedBytes()
	}

	// Write-through: persist outputs/base_url and lifecycle snapshot if policy allows
	if sm.nodeRepo != nil {
		if update.GetOutputsJson() != "" {
			_ = sm.nodeRepo.UpsertNodeOutputs(ctx, update.GetNodeId(), update.GetBaseUrl(), update.GetOutputsJson())
		}
		_ = sm.nodeRepo.UpsertNodeLifecycle(ctx, update)
	}
	return nil
}

// === Policy and repository types ===

type EntityType string

const (
	EntityClip EntityType = "clip"
	EntityDVR  EntityType = "dvr"
)

type WriteMode string

const (
	WriteThrough WriteMode = "write_through"
	WriteBack    WriteMode = "write_back"
)

type WritePolicy struct {
	Enabled bool
	Mode    WriteMode
}

type SyncPolicy struct {
	BootRehydrate     bool
	ReconcileInterval time.Duration
}

type CachePolicy struct {
	TTL                  time.Duration
	StaleWhileRevalidate time.Duration
	NegativeTTL          time.Duration
	MaxEntries           int
	Strategy             string
}

type PoliciesConfig struct {
	WritePolicies map[EntityType]WritePolicy
	SyncPolicies  map[EntityType]SyncPolicy
	CachePolicies map[string]CachePolicy
	ClipRepo      ClipRepository
	DVRRepo       DVRRepository
	NodeRepo      NodeRepository
}

// Records used by repositories

type ClipRecord struct {
	ClipHash     string
	TenantID     string
	InternalName string
	NodeID       string
	Status       string
	StoragePath  string
	SizeBytes    int64
}

type DVRRecord struct {
	Hash          string
	TenantID      string
	InternalName  string
	StorageNodeID string
	SourceURL     string
	Status        string
	DurationSec   int64
	SizeBytes     int64
	ManifestPath  string
}

type NodeRecord struct {
	NodeID      string
	BaseURL     string
	OutputsJSON string
}

// Repository interfaces

type ClipRepository interface {
	ListActiveClips(ctx context.Context) ([]ClipRecord, error)
	ResolveInternalNameByRequestID(ctx context.Context, requestID string) (string, error)
	UpdateClipProgressByRequestID(ctx context.Context, requestID string, percent uint32) error
	UpdateClipDoneByRequestID(ctx context.Context, requestID string, status string, storagePath string, sizeBytes int64, errorMsg string) error
}

type DVRRepository interface {
	ListAllDVR(ctx context.Context) ([]DVRRecord, error)
	ResolveInternalNameByHash(ctx context.Context, dvrHash string) (string, error)
	UpdateDVRProgressByHash(ctx context.Context, dvrHash string, status string, sizeBytes int64) error
	UpdateDVRCompletionByHash(ctx context.Context, dvrHash string, finalStatus string, durationSeconds int64, sizeBytes int64, manifestPath string, errorMsg string) error
}

type NodeRepository interface {
	ListAllNodes(ctx context.Context) ([]NodeRecord, error)
	UpsertNodeOutputs(ctx context.Context, nodeID string, baseURL string, outputsJSON string) error
	UpsertNodeLifecycle(ctx context.Context, update *pb.NodeLifecycleUpdate) error
}
