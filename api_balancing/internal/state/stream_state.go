package state

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// Metrics hooks (optional)
var (
	writeCounter             func(labels map[string]string)
	rehydrateDurationObserve func(seconds float64, labels map[string]string)
)

// Optional logger for state-level warnings (rehydrate failures, etc.)
var stateLogger logging.Logger

// DtshSyncHandler is called when a .dtsh file needs to be synced to S3 (set by control package)
var dtshSyncHandler func(nodeID, assetHash, assetType, filePath string)

// SetDtshSyncHandler registers the callback for triggering incremental .dtsh syncs
func SetDtshSyncHandler(handler func(nodeID, assetHash, assetType, filePath string)) {
	dtshSyncHandler = handler
}

// SetMetricsHooks allows the caller to inject metrics callbacks
func SetMetricsHooks(onWrite func(labels map[string]string), onRehydrateDuration func(seconds float64, labels map[string]string)) {
	writeCounter = onWrite
	rehydrateDurationObserve = onRehydrateDuration
}

// SetLogger registers an optional logger for state-level warnings.
func SetLogger(logger logging.Logger) {
	stateLogger = logger
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
	StreamName       string                 `json:"stream_name"`
	InternalName     string                 `json:"internal_name"`
	NodeID           string                 `json:"node_id"`
	TenantID         string                 `json:"tenant_id"`
	Status           string                 `json:"status"`       // "live", "offline", etc.
	BufferState      string                 `json:"buffer_state"` // "FULL", "EMPTY", "DRY", "RECOVER"
	Tracks           []StreamTrack          `json:"tracks"`
	Issues           string                 `json:"issues,omitempty"`
	HasIssues        bool                   `json:"has_issues"`
	StartedAt        *time.Time             `json:"started_at,omitempty"` // When stream first went live
	LastUpdate       time.Time              `json:"last_update"`
	RawDetails       map[string]interface{} `json:"raw_details,omitempty"` // Raw MistServer data
	Viewers          int                    `json:"viewers"`
	LastTrackList    string                 `json:"last_track_list,omitempty"`
	TotalConnections int                    `json:"total_connections,omitempty"`
	Inputs           int                    `json:"inputs,omitempty"`
	BytesUp          int64                  `json:"bytes_up,omitempty"`
	BytesDown        int64                  `json:"bytes_down,omitempty"`
}

// StreamInstanceState represents per-node state for a specific stream
type StreamInstanceState struct {
	NodeID           string                 `json:"node_id"`
	TenantID         string                 `json:"tenant_id"`
	Status           string                 `json:"status"`
	BufferState      string                 `json:"buffer_state"`
	LastTrackList    string                 `json:"last_track_list,omitempty"`
	Viewers          int                    `json:"viewers"`
	BytesUp          int64                  `json:"bytes_up,omitempty"`
	BytesDown        int64                  `json:"bytes_down,omitempty"`
	TotalConnections int                    `json:"total_connections,omitempty"`
	Inputs           int                    `json:"inputs,omitempty"`
	Replicated       bool                   `json:"replicated,omitempty"` // True if this is a replicated (pull) stream
	LastUpdate       time.Time              `json:"last_update"`
	RawDetails       map[string]interface{} `json:"raw_details,omitempty"`
}

// VirtualViewerState represents the lifecycle state of a virtual viewer
type VirtualViewerState string

const (
	VirtualViewerPending      VirtualViewerState = "PENDING"      // Redirected, awaiting USER_NEW
	VirtualViewerActive       VirtualViewerState = "ACTIVE"       // USER_NEW received, session live
	VirtualViewerAbandoned    VirtualViewerState = "ABANDONED"    // Redirect timed out, never connected
	VirtualViewerDisconnected VirtualViewerState = "DISCONNECTED" // USER_END received
)

// VirtualViewer tracks a viewer session from redirect through connection to disconnection.
// This enables accurate bandwidth prediction and session correlation.
type VirtualViewer struct {
	ID             string             `json:"id"`              // UUID for this virtual viewer
	NodeID         string             `json:"node_id"`         // Target node for redirect
	StreamName     string             `json:"stream_name"`     // Internal stream name
	ClientIP       string             `json:"client_ip"`       // Client IP for matching USER_NEW
	MistSessionID  string             `json:"mist_session_id"` // Mist session ID for disconnect correlation
	State          VirtualViewerState `json:"state"`           // Current lifecycle state
	RedirectTime   time.Time          `json:"redirect_time"`   // When redirect was issued
	ConnectTime    time.Time          `json:"connect_time"`    // When USER_NEW matched (zero if not yet)
	DisconnectTime time.Time          `json:"disconnect_time"` // When USER_END received (zero if not yet)
	EstBandwidth   uint64             `json:"est_bandwidth"`   // Estimated bytes/sec for this viewer
}

// NodeState captures per-node state
type NodeState struct {
	NodeID               string                 `json:"node_id"`
	BaseURL              string                 `json:"base_url"`
	IsHealthy            bool                   `json:"is_healthy"`
	IsStale              bool                   `json:"is_stale"` // Node hasn't reported recently
	OperationalMode      NodeOperationalMode    `json:"operational_mode,omitempty"`
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
	LastHeartbeat        time.Time              `json:"last_heartbeat"`

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

	// Virtual Viewer Tracking
	PendingRedirects    int       `json:"pending_redirects"` // Count of redirects awaiting USER_NEW
	EstBandwidthPerUser uint64    `json:"-"`                 // Cached per-viewer bandwidth estimate (bytes/sec)
	LastPollTime        time.Time `json:"-"`                 // When real metrics arrived from Helmsman
}

type NodeOperationalMode string

const (
	NodeModeNormal      NodeOperationalMode = "normal"
	NodeModeDraining    NodeOperationalMode = "draining"
	NodeModeMaintenance NodeOperationalMode = "maintenance"
)

// StreamStateManager manages in-memory cluster state
type StreamStateManager struct {
	streams         map[string]*StreamState                    // internal_name -> union summary
	streamInstances map[string]map[string]*StreamInstanceState // internal_name -> node_id -> instance
	nodes           map[string]*NodeState                      // node_id -> node state
	mu              sync.RWMutex

	// Virtual Viewer Tracking (Option B: per-session)
	virtualViewers map[string]*VirtualViewer // viewerID -> viewer (for full session tracking)
	viewersByNode  map[string][]string       // nodeID -> []viewerIDs (for fast per-node lookups)

	// Load balancer weights (exactly like C++ version)
	WeightCPU   uint64
	WeightRAM   uint64
	WeightBW    uint64
	WeightGeo   uint64
	WeightBonus uint64

	// Staleness detection
	stalenessChecker chan bool
	staleThreshold   time.Duration

	stalenessConfigMu       sync.RWMutex
	stalenessCheckInterval  time.Duration
	stalenessConfigRefreshC chan struct{}

	// Policies and repos (new)
	writePolicies map[EntityType]WritePolicy
	syncPolicies  map[EntityType]SyncPolicy
	cachePolicies map[string]CachePolicy
	repos         struct {
		Clips     ClipRepository
		DVR       DVRRepository
		Artifacts ArtifactRepository
	}
	nodeRepo NodeRepository // Hook into persistence/caching layer

	// Reconciler (new)
	reconcileStop   chan struct{}
	reconcileTicker *time.Ticker

	nodeLifecycleBatchMu            sync.Mutex
	nodeLifecycleBatch              []*pb.NodeLifecycleUpdate
	nodeLifecycleBatchTimer         *time.Timer
	nodeLifecycleBatchSize          int
	nodeLifecycleBatchFlushInterval time.Duration

	rehydrateMu      sync.Mutex
	lastRehydrateAt  time.Time
	lastRehydrateErr string
}

// NewStreamStateManager creates a new stream state manager
func NewStreamStateManager() *StreamStateManager {
	sm := &StreamStateManager{
		streams:          make(map[string]*StreamState),
		streamInstances:  make(map[string]map[string]*StreamInstanceState),
		nodes:            make(map[string]*NodeState),
		stalenessChecker: make(chan bool),
		staleThreshold:   90 * time.Second,

		// Virtual Viewer Tracking
		virtualViewers: make(map[string]*VirtualViewer),
		viewersByNode:  make(map[string][]string),

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

		nodeLifecycleBatchSize:          200,
		nodeLifecycleBatchFlushInterval: 2 * time.Second,

		stalenessCheckInterval:  30 * time.Second,
		stalenessConfigRefreshC: make(chan struct{}, 1),
	}

	// Start staleness detection background goroutine
	go sm.runStalenessChecker()

	return sm
}

func newNodeState(nodeID string) *NodeState {
	return &NodeState{
		NodeID:          nodeID,
		OperationalMode: NodeModeNormal,
	}
}

func normalizeNodeOperationalMode(mode NodeOperationalMode) (NodeOperationalMode, error) {
	switch mode {
	case "":
		return NodeModeNormal, nil
	case NodeModeNormal, NodeModeDraining, NodeModeMaintenance:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid operational mode %q", mode)
	}
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

// GetStreamsByTenant returns all active streams for a specific tenant
func (sm *StreamStateManager) GetStreamsByTenant(tenantID string) []*StreamState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var states []*StreamState
	for _, state := range sm.streams {
		if state.TenantID == tenantID && state.Status == "live" {
			stateCopy := *state
			stateCopy.Tracks = make([]StreamTrack, len(state.Tracks))
			copy(stateCopy.Tracks, state.Tracks)
			states = append(states, &stateCopy)
		}
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

func ResetDefaultManagerForTests() *StreamStateManager {
	if defaultManager != nil {
		defaultManager.Shutdown()
	}
	defaultManager = NewStreamStateManager()
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
// This pre-accounts for expected bandwidth usage before the next NodeLifecycleUpdate arrives with actual metrics.
func (sm *StreamStateManager) addViewerBandwidthPenalty(nodeID string, _ string, _ *StreamInstanceState) {
	node := sm.nodes[nodeID]
	if node == nil {
		return
	}

	var toAdd uint64 = 0

	// Use node's current UpSpeed (bytes/sec rate) to estimate per-viewer bandwidth
	// UpSpeed is the real-time upload rate from MistServer, not cumulative bytes
	if node.UpSpeed > 0 {
		// Count total viewers across all streams on this node
		totalViewers := uint64(0)
		for _, nodeInstances := range sm.streamInstances {
			if inst := nodeInstances[nodeID]; inst != nil && inst.TotalConnections > 0 {
				totalViewers += uint64(inst.TotalConnections)
			}
		}

		if totalViewers > 0 {
			// Estimate bandwidth per viewer = current upload rate / current viewers
			toAdd = uint64(node.UpSpeed) / totalViewers
		} else {
			// No viewers yet, use the full current upload rate as baseline
			// (stream is ingesting but nobody watching)
			toAdd = uint64(node.UpSpeed)
		}
	}

	// If we still don't have a good estimate, use default
	if toAdd == 0 {
		toAdd = 131072 // assume 1mbps (like C++)
	}

	// Ensure reasonable limits (like C++)
	if toAdd < 64*1024 {
		toAdd = 64 * 1024 // minimum 0.5 mbps (64 KB/s)
	}
	if toAdd > 1024*1024 {
		toAdd = 1024 * 1024 // maximum 8 mbps (1 MB/s)
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

func (sm *StreamStateManager) UpdateNodeStats(internalName, nodeID string, total, inputs int, up, down int64, replicated bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	union := sm.streams[internalName]
	if union == nil {
		union = &StreamState{InternalName: internalName, StreamName: internalName}
		sm.streams[internalName] = union
	}
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
	inst.Replicated = replicated
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

// TouchNode updates only health + last update time without overwriting identity fields.
func (sm *StreamStateManager) TouchNode(nodeID string, isHealthy bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
	}
	n.IsHealthy = isHealthy
	n.IsStale = false
	now := time.Now()
	n.LastUpdate = now
	n.LastHeartbeat = now
}

// SetNodeInfo updates per-node info
func (sm *StreamStateManager) SetNodeInfo(nodeID, baseURL string, isHealthy bool, lat, lon *float64, location string, outputsRaw string, outputs map[string]interface{}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	n := sm.nodes[nodeID]
	isNew := false
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
		isNew = true
	}
	n.BaseURL = baseURL
	n.IsHealthy = isHealthy
	if !isHealthy {
		n.IsStale = true
	}
	if isNew && n.LastHeartbeat.IsZero() {
		n.IsStale = true
	}
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
		n = newNodeState(nodeID)
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

// UpdateNodeDiskUsage updates the disk usage statistics for a node
func (sm *StreamStateManager) UpdateNodeDiskUsage(nodeID string, diskTotal, diskUsed uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
	}

	n.DiskTotalBytes = diskTotal
	n.DiskUsedBytes = diskUsed
	n.LastUpdate = time.Now()
}

// MarkNodeDisconnected immediately flags a node as unhealthy/stale after disconnect.
func (sm *StreamStateManager) MarkNodeDisconnected(nodeID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		return
	}
	n.IsHealthy = false
	n.IsStale = true
	// Keep the disconnected state sticky until a new heartbeat arrives.
	n.LastHeartbeat = time.Time{}
	n.LastUpdate = time.Now()
}

func (sm *StreamStateManager) SetNodeOperationalMode(ctx context.Context, nodeID string, mode NodeOperationalMode, setBy string) error {
	normalized, err := normalizeNodeOperationalMode(mode)
	if err != nil {
		return err
	}

	// Persist first so we don't change in-memory balancing behavior unless the DB write succeeds.
	if sm.nodeRepo != nil {
		if err := sm.nodeRepo.UpsertNodeMaintenance(ctx, nodeID, normalized, setBy); err != nil {
			return err
		}
	}

	sm.mu.Lock()
	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
	}
	n.OperationalMode = normalized
	n.LastUpdate = time.Now()
	sm.mu.Unlock()

	return nil
}

func (sm *StreamStateManager) GetNodeOperationalMode(nodeID string) NodeOperationalMode {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if node := sm.nodes[nodeID]; node != nil && node.OperationalMode != "" {
		return node.OperationalMode
	}
	return NodeModeNormal
}

func (sm *StreamStateManager) GetNodeActiveViewers(nodeID string) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	total := 0
	for _, nodes := range sm.streamInstances {
		if inst := nodes[nodeID]; inst != nil {
			total += inst.TotalConnections
		}
	}
	return total
}

// SetNodeGPUInfo updates GPU information for a node
func (sm *StreamStateManager) SetNodeGPUInfo(nodeID string, gpuVendor string, gpuCount int, gpuMemMB int, gpuCC string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
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
		n = newNodeState(nodeID)
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
				Replicated: inst.Replicated,
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

// GetAllStreamInstances returns all stream instances (internalName -> nodeID -> instance)
func (sm *StreamStateManager) GetAllStreamInstances() map[string]map[string]StreamInstanceState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	out := make(map[string]map[string]StreamInstanceState)
	for internalName, nodeInstances := range sm.streamInstances {
		nodeMap := make(map[string]StreamInstanceState)
		for nodeID, inst := range nodeInstances {
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
			nodeMap[nodeID] = c
		}
		if len(nodeMap) > 0 {
			out[internalName] = nodeMap
		}
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
		n = newNodeState(nodeID)
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

// ArtifactNodeInfo contains node information for artifact routing
type ArtifactNodeInfo struct {
	NodeID       string
	Host         string // Base URL
	Score        int64  // Load balancing score (lower is better)
	Artifact     *pb.StoredArtifact
	GeoLatitude  float64
	GeoLongitude float64
}

// FindNodesByArtifactHash searches for ALL nodes hosting the specified artifact (Clip/DVR).
// Returns a slice of nodes with their scores for load balancing.
func (sm *StreamStateManager) FindNodesByArtifactHash(hash string) []ArtifactNodeInfo {
	snapshot := sm.GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return nil
	}

	var nodes []ArtifactNodeInfo
	for _, node := range snapshot.Nodes {
		// Skip inactive nodes
		if !node.IsActive {
			continue
		}
		for _, artifact := range node.Artifacts {
			if artifact.GetClipHash() == hash {
				// Combine CPU and RAM scores for load balancing (lower is better)
				combinedScore := int64(node.CPUScore + node.RAMScore)
				nodes = append(nodes, ArtifactNodeInfo{
					NodeID:       node.NodeID,
					Host:         node.Host,
					Score:        combinedScore,
					Artifact:     artifact,
					GeoLatitude:  node.GeoLatitude,
					GeoLongitude: node.GeoLongitude,
				})
				break // Only count once per node
			}
		}
	}
	return nodes
}

// FindNodeByArtifactHash searches for a node hosting the specified artifact (Clip/DVR).
// Returns the best node's host/base URL and the artifact details if found.
// For multi-node support with load balancing, use FindNodesByArtifactHash instead.
func (sm *StreamStateManager) FindNodeByArtifactHash(hash string) (string, *pb.StoredArtifact) {
	nodes := sm.FindNodesByArtifactHash(hash)
	if len(nodes) == 0 {
		return "", nil
	}

	// If multiple nodes have the artifact, pick the one with the best (lowest) score
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.Score < best.Score {
			best = n
		}
	}

	return best.Host, best.Artifact
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

// SetNodeArtifacts updates the artifacts stored on a node (in-memory and persistent)
func (sm *StreamStateManager) SetNodeArtifacts(nodeID string, artifacts []*pb.StoredArtifact) {
	sm.mu.Lock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
	}

	// Deep copy artifacts to avoid shared slices
	n.Artifacts = make([]*pb.StoredArtifact, len(artifacts))
	copy(n.Artifacts, artifacts)
	n.LastUpdate = time.Now()

	// Get artifact repo reference while holding lock
	artifactRepo := sm.repos.Artifacts
	sm.mu.Unlock()

	// Persist to database (outside lock to avoid blocking)
	if artifactRepo != nil && len(artifacts) > 0 {
		records := make([]ArtifactRecord, 0, len(artifacts))
		for _, a := range artifacts {
			artifactType := artifactTypeToString(a.GetArtifactType())
			if artifactType == "" {
				artifactType = inferArtifactType(a.GetFilePath())
			}
			records = append(records, ArtifactRecord{
				ArtifactHash: a.GetClipHash(),
				ArtifactType: artifactType,
				StreamName:   a.GetStreamName(),
				FilePath:     a.GetFilePath(),
				SizeBytes:    int64(a.GetSizeBytes()),
				CreatedAt:    a.GetCreatedAt(),
				HasDtsh:      a.GetHasDtsh(),
				AccessCount:  int64(a.GetAccessCount()),
				LastAccessed: a.GetLastAccessed(),
			})
		}
		// Fire-and-forget persistence (errors logged in repository)
		go func() {
			_ = artifactRepo.UpsertArtifacts(context.Background(), nodeID, records)
		}()
	}

	// Check for .dtsh files that appeared after initial sync
	// If an artifact has HasDtsh=true but was synced without it, trigger incremental sync
	go sm.checkAndTriggerDtshSync(nodeID, artifacts)
}

// AddNodeArtifact adds or updates a single artifact in the in-memory node state.
func (sm *StreamStateManager) AddNodeArtifact(nodeID string, artifact *pb.StoredArtifact) {
	if artifact == nil {
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
	}

	for i, existing := range n.Artifacts {
		if existing.GetClipHash() == artifact.GetClipHash() {
			n.Artifacts[i] = artifact
			n.LastUpdate = time.Now()
			return
		}
	}

	n.Artifacts = append(n.Artifacts, artifact)
	n.LastUpdate = time.Now()
}

// setNodeArtifactsMemoryOnly updates artifacts in memory without persisting to DB.
// Used during rehydration to avoid corrupting warm-cache state.
func (sm *StreamStateManager) setNodeArtifactsMemoryOnly(nodeID string, artifacts []*pb.StoredArtifact) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	n := sm.nodes[nodeID]
	if n == nil {
		n = &NodeState{NodeID: nodeID}
		sm.nodes[nodeID] = n
	}

	n.Artifacts = make([]*pb.StoredArtifact, len(artifacts))
	copy(n.Artifacts, artifacts)
	// Don't update LastUpdate - this is stale data from DB, not a fresh node report
}

// inferArtifactType determines artifact type from file path
func inferArtifactType(filePath string) string {
	if strings.Contains(filePath, "/dvr/") {
		return "dvr"
	}
	if strings.Contains(filePath, "/vod/") {
		return "vod"
	}
	return "clip"
}

func artifactTypeToString(artifactType pb.ArtifactEvent_ArtifactType) string {
	switch artifactType {
	case pb.ArtifactEvent_ARTIFACT_TYPE_CLIP:
		return "clip"
	case pb.ArtifactEvent_ARTIFACT_TYPE_DVR:
		return "dvr"
	case pb.ArtifactEvent_ARTIFACT_TYPE_VOD:
		return "vod"
	default:
		return ""
	}
}

// checkAndTriggerDtshSync checks for artifacts that have .dtsh locally but weren't synced with it
// This catches the race condition where .dtsh is created after the initial sync
func (sm *StreamStateManager) checkAndTriggerDtshSync(nodeID string, artifacts []*pb.StoredArtifact) {
	sm.mu.RLock()
	clipsRepo := sm.repos.Clips
	dvrRepo := sm.repos.DVR
	sm.mu.RUnlock()

	if clipsRepo == nil && dvrRepo == nil {
		return
	}

	ctx := context.Background()

	for _, artifact := range artifacts {
		if !artifact.GetHasDtsh() {
			continue // No .dtsh locally, nothing to sync
		}

		artifactType := artifactTypeToString(artifact.GetArtifactType())
		if artifactType == "" {
			artifactType = inferArtifactType(artifact.GetFilePath())
		}
		hash := artifact.GetClipHash()

		var needsSync bool

		if artifactType == "clip" && clipsRepo != nil {
			// Check if clip is synced but dtsh wasn't included
			needsSync = clipsRepo.NeedsDtshSync(ctx, hash)
		} else if artifactType == "dvr" && dvrRepo != nil {
			// Check if DVR is synced but dtsh wasn't included
			needsSync = dvrRepo.NeedsDtshSync(ctx, hash)
		}

		if needsSync {
			// Trigger incremental .dtsh sync for this artifact
			go triggerIncrementalDtshSync(nodeID, hash, artifactType, artifact.GetFilePath())
		}
	}
}

// triggerIncrementalDtshSync requests Helmsman to upload just the .dtsh file
// This is called when .dtsh appeared after the main file was already synced
func triggerIncrementalDtshSync(nodeID, artifactHash, artifactType, filePath string) {
	if dtshSyncHandler != nil {
		dtshSyncHandler(nodeID, artifactHash, artifactType, filePath)
	}
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
	Host            string              `json:"host"`
	NodeID          string              `json:"node_id"`
	GeoLatitude     float64             `json:"geo_latitude"`
	GeoLongitude    float64             `json:"geo_longitude"`
	LocationName    string              `json:"location_name"`
	IsActive        bool                `json:"is_active"`
	LastUpdate      time.Time           `json:"last_update"`
	LastHeartbeat   time.Time           `json:"last_heartbeat"`
	OperationalMode NodeOperationalMode `json:"operational_mode"`

	// Performance-critical fields
	BinHost       [16]byte `json:"-"` // Binary IP for fast comparison
	Port          int      `json:"port"`
	DTSCPort      int      `json:"dtsc_port"`
	Tags          []string `json:"tags"`
	ConfigStreams []string `json:"config_streams"`
	AddBandwidth  uint64   `json:"add_bandwidth"`

	// Virtual Viewer Tracking
	PendingRedirects    int    `json:"pending_redirects"`      // Redirects awaiting USER_NEW confirmation
	EstBandwidthPerUser uint64 `json:"est_bandwidth_per_user"` // Estimated bytes/sec per viewer

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
	return sm.getBalancerSnapshotInternal(false, false)
}

// GetBalancerSnapshotAtomicWithOptions returns a snapshot with optional inclusion of stale nodes.
func (sm *StreamStateManager) GetBalancerSnapshotAtomicWithOptions(includeStale bool) *BalancerSnapshot {
	return sm.getBalancerSnapshotInternal(includeStale, false)
}

// GetAllNodesSnapshot returns ALL nodes regardless of health/stale status (for debugging dashboards)
func (sm *StreamStateManager) GetAllNodesSnapshot() *BalancerSnapshot {
	return sm.getBalancerSnapshotInternal(true, true)
}

// getBalancerSnapshotInternal is the internal implementation with full control over filtering
func (sm *StreamStateManager) getBalancerSnapshotInternal(includeStale, includeUnhealthy bool) *BalancerSnapshot {
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
				Replicated: inst.Replicated,
			}
		}
	}

	// Create enhanced snapshots for each node
	for nodeID, n := range sm.nodes {
		if n == nil {
			continue
		}

		// Skip unhealthy/stale nodes unless explicitly included (for debugging dashboards)
		if !includeUnhealthy && !n.IsHealthy {
			continue
		}
		if !includeStale && n.IsStale {
			continue
		}

		mode := n.OperationalMode
		if mode == "" {
			mode = NodeModeNormal
		}
		isActive := n.IsHealthy && mode != NodeModeMaintenance
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
			LocationName:    n.Location,
			IsActive:        isActive,
			LastUpdate:      n.LastUpdate,
			LastHeartbeat:   n.LastHeartbeat,
			OperationalMode: mode,

			// Performance-critical fields
			BinHost:       n.BinHost,
			Port:          n.Port,
			DTSCPort:      n.DTSCPort,
			Tags:          append([]string(nil), n.Tags...),
			ConfigStreams: append([]string(nil), n.ConfigStreams...),
			AddBandwidth:  n.AddBandwidth,

			// Virtual Viewer Tracking
			PendingRedirects:    n.PendingRedirects,
			EstBandwidthPerUser: n.EstBandwidthPerUser,

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
	for {
		ticker := time.NewTicker(sm.getStalenessCheckInterval())
		for {
			select {
			case <-ticker.C:
				sm.checkStaleNodes()
			case <-sm.stalenessConfigRefreshC:
				ticker.Stop()
				goto Restart
			case <-sm.stalenessChecker:
				ticker.Stop()
				return
			}
		}
	Restart:
	}
}

// checkStaleNodes marks nodes as stale but preserves their data
func (sm *StreamStateManager) checkStaleNodes() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	staleThreshold := sm.getStaleThreshold()

	for _, node := range sm.nodes {
		if node.LastHeartbeat.IsZero() {
			if !node.IsStale {
				node.IsStale = true
			}
			if node.IsHealthy {
				node.IsHealthy = false
			}
			continue
		}

		timeSinceHeartbeat := now.Sub(node.LastHeartbeat)
		if timeSinceHeartbeat > staleThreshold {
			if !node.IsStale {
				node.IsStale = true
			}
			if node.IsHealthy {
				node.IsHealthy = false
			}
		} else {
			if node.IsStale {
				node.IsStale = false
			}
		}
	}
}

// SetStalenessConfig overrides staleness thresholds for detection and checks.
func (sm *StreamStateManager) SetStalenessConfig(staleThreshold, checkInterval time.Duration) {
	sm.stalenessConfigMu.Lock()
	if staleThreshold > 0 {
		sm.staleThreshold = staleThreshold
	}
	if checkInterval > 0 {
		sm.stalenessCheckInterval = checkInterval
	}
	sm.stalenessConfigMu.Unlock()

	select {
	case sm.stalenessConfigRefreshC <- struct{}{}:
	default:
	}
}

func (sm *StreamStateManager) getStaleThreshold() time.Duration {
	sm.stalenessConfigMu.RLock()
	defer sm.stalenessConfigMu.RUnlock()
	return sm.staleThreshold
}

func (sm *StreamStateManager) getStalenessCheckInterval() time.Duration {
	sm.stalenessConfigMu.RLock()
	defer sm.stalenessConfigMu.RUnlock()
	return sm.stalenessCheckInterval
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
	sm.repos.Artifacts = cfg.ArtifactRepo
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
	var rehydrateErr error

	// Nodes
	if sm.nodeRepo != nil {
		recs, err := sm.nodeRepo.ListAllNodes(ctx)
		if err != nil {
			rehydrateErr = err
			if stateLogger != nil {
				stateLogger.WithError(err).Warn("Failed to rehydrate nodes from repository")
			}
		} else {
			for _, r := range recs {
				// Basic node info
				sm.SetNodeInfo(r.NodeID, r.BaseURL, true, nil, nil, "", r.OutputsJSON, nil)
			}
		}

		maintenance, err := sm.nodeRepo.ListNodeMaintenance(ctx)
		if err != nil {
			if rehydrateErr == nil {
				rehydrateErr = err
			}
			if stateLogger != nil {
				stateLogger.WithError(err).Warn("Failed to rehydrate node maintenance modes")
			}
		} else {
			sm.mu.Lock()
			for _, rec := range maintenance {
				mode, modeErr := normalizeNodeOperationalMode(rec.Mode)
				if modeErr != nil {
					if stateLogger != nil {
						stateLogger.WithFields(logging.Fields{
							"node_id": rec.NodeID,
							"mode":    rec.Mode,
						}).Warn("Skipping invalid operational mode during rehydrate")
					}
					continue
				}
				node := sm.nodes[rec.NodeID]
				if node == nil {
					node = newNodeState(rec.NodeID)
					sm.nodes[rec.NodeID] = node
				}
				node.OperationalMode = mode
			}
			sm.mu.Unlock()
		}
	}
	// DVR
	if sm.repos.DVR != nil {
		recs, err := sm.repos.DVR.ListAllDVR(ctx)
		if err != nil {
			if rehydrateErr == nil {
				rehydrateErr = err
			}
			if stateLogger != nil {
				stateLogger.WithError(err).Warn("Failed to rehydrate DVR records")
			}
		} else {
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
		if err != nil {
			if rehydrateErr == nil {
				rehydrateErr = err
			}
			if stateLogger != nil {
				stateLogger.WithError(err).Warn("Failed to rehydrate clip records")
			}
		} else {
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
	// Artifacts (warm cache for FindNodesByArtifactHash) - memory only, no DB writes
	if sm.repos.Artifacts != nil {
		nodeArtifacts, err := sm.repos.Artifacts.ListAllNodeArtifacts(ctx)
		if err != nil {
			if rehydrateErr == nil {
				rehydrateErr = err
			}
			if stateLogger != nil {
				stateLogger.WithError(err).Warn("Failed to rehydrate artifact records")
			}
		} else {
			for nodeID, records := range nodeArtifacts {
				artifacts := make([]*pb.StoredArtifact, 0, len(records))
				for _, r := range records {
					artifacts = append(artifacts, &pb.StoredArtifact{
						ClipHash:     r.ArtifactHash,
						StreamName:   r.StreamName,
						FilePath:     r.FilePath,
						SizeBytes:    uint64(r.SizeBytes),
						CreatedAt:    r.CreatedAt,
						AccessCount:  uint64(r.AccessCount),
						LastAccessed: r.LastAccessed,
					})
				}
				sm.setNodeArtifactsMemoryOnly(nodeID, artifacts)
			}
		}
	}
	if rehydrateDurationObserve != nil {
		rehydrateDurationObserve(time.Since(start).Seconds(), map[string]string{"entity": "all"})
	}

	sm.rehydrateMu.Lock()
	sm.lastRehydrateAt = time.Now()
	if rehydrateErr != nil {
		sm.lastRehydrateErr = rehydrateErr.Error()
	} else {
		sm.lastRehydrateErr = ""
	}
	sm.rehydrateMu.Unlock()

	return rehydrateErr
}

// RehydrateStatus returns the last rehydrate attempt timestamp and error message (if any).
func (sm *StreamStateManager) RehydrateStatus() (time.Time, string) {
	sm.rehydrateMu.Lock()
	defer sm.rehydrateMu.Unlock()
	return sm.lastRehydrateAt, sm.lastRehydrateErr
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
		CPU:        float64(update.GetCpuTenths()) / 10.0,
		RAMMax:     float64(update.GetRamMax()),
		RAMCurrent: float64(update.GetRamCurrent()),
		UpSpeed:    float64(update.GetUpSpeed()),
		DownSpeed:  float64(update.GetDownSpeed()),
		BWLimit: func() float64 {
			if limit := float64(update.GetBwLimit()); limit > 0 {
				return limit
			}
			return 128 * 1024 * 1024 // Default 1Gbps when not specified
		}(),
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
		sm.queueNodeLifecycleWrite(update)
	}
	return nil
}

func (sm *StreamStateManager) queueNodeLifecycleWrite(update *pb.NodeLifecycleUpdate) {
	if sm.nodeRepo == nil || update == nil {
		return
	}
	sm.nodeLifecycleBatchMu.Lock()
	sm.nodeLifecycleBatch = append(sm.nodeLifecycleBatch, update)

	if len(sm.nodeLifecycleBatch) >= sm.nodeLifecycleBatchSize {
		batch := sm.nodeLifecycleBatch
		sm.nodeLifecycleBatch = nil
		if sm.nodeLifecycleBatchTimer != nil {
			sm.nodeLifecycleBatchTimer.Stop()
			sm.nodeLifecycleBatchTimer = nil
		}
		sm.nodeLifecycleBatchMu.Unlock()
		sm.flushNodeLifecycleBatch(batch)
		return
	}

	if sm.nodeLifecycleBatchTimer == nil {
		sm.nodeLifecycleBatchTimer = time.AfterFunc(sm.nodeLifecycleBatchFlushInterval, sm.flushNodeLifecycleBatchFromTimer)
	}
	sm.nodeLifecycleBatchMu.Unlock()
}

func (sm *StreamStateManager) flushNodeLifecycleBatchFromTimer() {
	sm.flushNodeLifecycleBatch(nil)
}

func (sm *StreamStateManager) flushNodeLifecycleBatch(batch []*pb.NodeLifecycleUpdate) {
	if sm.nodeRepo == nil {
		return
	}
	if batch == nil {
		sm.nodeLifecycleBatchMu.Lock()
		batch = sm.nodeLifecycleBatch
		sm.nodeLifecycleBatch = nil
		sm.nodeLifecycleBatchTimer = nil
		sm.nodeLifecycleBatchMu.Unlock()
	}
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sm.nodeRepo.UpsertNodeLifecycles(ctx, batch); err != nil && stateLogger != nil {
		stateLogger.WithError(err).Warn("Failed to batch upsert node lifecycle updates")
	}
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
	ArtifactRepo  ArtifactRepository
}

// Records used by repositories

type ClipRecord struct {
	ClipHash        string
	TenantID        string
	InternalName    string
	NodeID          string
	Status          string
	StoragePath     string
	SizeBytes       int64
	StorageLocation string
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

type NodeMaintenanceRecord struct {
	NodeID string
	Mode   NodeOperationalMode
	SetAt  time.Time
	SetBy  string
}

// Repository interfaces

type ClipRepository interface {
	ListActiveClips(ctx context.Context) ([]ClipRecord, error)
	ResolveInternalNameByRequestID(ctx context.Context, requestID string) (string, error)
	UpdateClipProgressByRequestID(ctx context.Context, requestID string, percent uint32) error
	UpdateClipDoneByRequestID(ctx context.Context, requestID string, status string, storagePath string, sizeBytes int64, errorMsg string) error
	// NeedsDtshSync returns true if the clip is synced to S3 but .dtsh wasn't included
	NeedsDtshSync(ctx context.Context, clipHash string) bool
}

type DVRRepository interface {
	ListAllDVR(ctx context.Context) ([]DVRRecord, error)
	ResolveInternalNameByHash(ctx context.Context, dvrHash string) (string, error)
	UpdateDVRProgressByHash(ctx context.Context, dvrHash string, status string, sizeBytes int64) error
	UpdateDVRCompletionByHash(ctx context.Context, dvrHash string, finalStatus string, durationSeconds int64, sizeBytes int64, manifestPath string, errorMsg string) error
	// NeedsDtshSync returns true if the DVR is synced to S3 but .dtsh files weren't included
	NeedsDtshSync(ctx context.Context, dvrHash string) bool
}

type NodeRepository interface {
	ListAllNodes(ctx context.Context) ([]NodeRecord, error)
	ListNodeMaintenance(ctx context.Context) ([]NodeMaintenanceRecord, error)
	UpsertNodeOutputs(ctx context.Context, nodeID string, baseURL string, outputsJSON string) error
	UpsertNodeLifecycles(ctx context.Context, updates []*pb.NodeLifecycleUpdate) error
	UpsertNodeMaintenance(ctx context.Context, nodeID string, mode NodeOperationalMode, setBy string) error
}

// ArtifactRepository handles persistence of artifact registry (clips/DVR on storage nodes)
type ArtifactRepository interface {
	// UpsertArtifacts inserts or updates artifacts for a node, and marks stale artifacts as orphaned
	UpsertArtifacts(ctx context.Context, nodeID string, artifacts []ArtifactRecord) error

	// Sync tracking methods for dual-storage architecture
	// GetArtifactSyncInfo retrieves sync status for an artifact (across all nodes)
	GetArtifactSyncInfo(ctx context.Context, artifactHash string) (*ArtifactSyncInfo, error)
	// SetSyncStatus updates sync status and S3 URL for an artifact
	SetSyncStatus(ctx context.Context, artifactHash, status, s3URL string) error
	// AddCachedNode adds a node to the cached_nodes array for an artifact
	AddCachedNode(ctx context.Context, artifactHash, nodeID string) error
	// AddCachedNodeWithPath updates cache tracking with file path and size info.
	AddCachedNodeWithPath(ctx context.Context, artifactHash, nodeID, filePath string, sizeBytes int64) error
	// IsSynced returns true if the artifact is synced to S3
	IsSynced(ctx context.Context, artifactHash string) (bool, error)
	// GetCachedAt retrieves the cached_at timestamp (Unix ms) for calculating warm duration
	GetCachedAt(ctx context.Context, artifactHash string) (int64, error)
	// ListAllNodeArtifacts returns all non-orphaned artifacts grouped by node ID (for rehydration)
	ListAllNodeArtifacts(ctx context.Context) (map[string][]ArtifactRecord, error)
}

// ArtifactRecord represents an artifact (clip or DVR) stored on a node
type ArtifactRecord struct {
	ArtifactHash string
	ArtifactType string // "clip" or "dvr"
	StreamName   string
	FilePath     string
	SizeBytes    int64
	CreatedAt    int64 // Unix timestamp
	SegmentCount int   // DVR only
	SegmentBytes int64 // DVR only
	HasDtsh      bool  // True if .dtsh index file exists locally
	AccessCount  int64
	LastAccessed int64 // Unix timestamp
}

// ArtifactSyncInfo represents sync tracking state for an artifact
type ArtifactSyncInfo struct {
	ArtifactHash    string
	ArtifactType    string
	SyncStatus      string   // pending, in_progress, synced, failed
	S3URL           string   // S3 location when synced
	CachedNodes     []string // Node IDs with local copies
	LastSyncAttempt int64    // Unix timestamp
	SyncError       string
	CachedAt        int64 // When asset was last cached locally (Unix timestamp ms)
}

// =============================================================================
// Virtual Viewer Lifecycle Methods (Option B: Full Per-Session Tracking)
// =============================================================================

// CreateVirtualViewer creates a new PENDING virtual viewer when a redirect is issued.
// Returns the viewer ID for correlation.
func (sm *StreamStateManager) CreateVirtualViewer(nodeID, streamName, clientIP string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Ensure node exists
	node := sm.nodes[nodeID]
	if node == nil {
		node = newNodeState(nodeID)
		sm.nodes[nodeID] = node
	}

	// Calculate estimated bandwidth for this viewer
	estBW := sm.calculateEstBandwidthPerUserLocked(node)

	// Create the virtual viewer
	viewerID := uuid.New().String()
	viewer := &VirtualViewer{
		ID:           viewerID,
		NodeID:       nodeID,
		StreamName:   streamName,
		ClientIP:     clientIP,
		State:        VirtualViewerPending,
		RedirectTime: time.Now(),
		EstBandwidth: estBW,
	}

	// Store in maps
	sm.virtualViewers[viewerID] = viewer
	sm.viewersByNode[nodeID] = append(sm.viewersByNode[nodeID], viewerID)

	// Update node's pending count and add bandwidth
	node.PendingRedirects++
	node.AddBandwidth += estBW

	// Recompute scores since AddBandwidth changed
	sm.recomputeNodeScoresLocked(node)

	return viewerID
}

// ConfirmVirtualViewer transitions a PENDING viewer to ACTIVE when USER_NEW arrives.
// Matches by (nodeID, streamName, clientIP), oldest PENDING first.
// Returns true if a matching viewer was found and confirmed.
func (sm *StreamStateManager) ConfirmVirtualViewer(nodeID, streamName, clientIP string) bool {
	return sm.ConfirmVirtualViewerByID("", nodeID, streamName, clientIP, "")
}

// ConfirmVirtualViewerByID transitions a PENDING viewer to ACTIVE when USER_NEW arrives.
// If viewerID is provided, match the exact virtual viewer; otherwise fallback to IP matching.
func (sm *StreamStateManager) ConfirmVirtualViewerByID(viewerID, nodeID, streamName, clientIP, mistSessionID string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	node := sm.nodes[nodeID]
	if node == nil {
		return false
	}

	// Find oldest PENDING viewer matching criteria
	var matchedViewer *VirtualViewer
	var oldestTime time.Time

	if viewerID != "" {
		if viewer := sm.virtualViewers[viewerID]; viewer != nil &&
			viewer.State == VirtualViewerPending &&
			viewer.NodeID == nodeID &&
			viewer.StreamName == streamName {
			matchedViewer = viewer
		}
	}

	if matchedViewer == nil {
		for _, candidateID := range sm.viewersByNode[nodeID] {
			viewer := sm.virtualViewers[candidateID]
			if viewer == nil || viewer.State != VirtualViewerPending {
				continue
			}
			// Match by node, stream, and client IP
			if viewer.StreamName == streamName && viewer.ClientIP == clientIP {
				if matchedViewer == nil || viewer.RedirectTime.Before(oldestTime) {
					matchedViewer = viewer
					oldestTime = viewer.RedirectTime
				}
			}
		}
	}

	if matchedViewer == nil {
		// No matching PENDING viewer - this is a direct connection (not redirected by us)
		return false
	}

	// Transition to ACTIVE
	matchedViewer.State = VirtualViewerActive
	matchedViewer.ConnectTime = time.Now()
	if mistSessionID != "" {
		matchedViewer.MistSessionID = mistSessionID
	}

	// Decrement pending count and remove bandwidth penalty
	node.PendingRedirects--
	if node.PendingRedirects < 0 {
		node.PendingRedirects = 0
	}
	if node.AddBandwidth >= matchedViewer.EstBandwidth {
		node.AddBandwidth -= matchedViewer.EstBandwidth
	} else {
		node.AddBandwidth = 0
	}

	// Recompute scores
	sm.recomputeNodeScoresLocked(node)

	return true
}

// DisconnectVirtualViewer transitions an ACTIVE viewer to DISCONNECTED when USER_END arrives.
// Matches by (nodeID, streamName, clientIP), oldest ACTIVE first.
func (sm *StreamStateManager) DisconnectVirtualViewer(nodeID, streamName, clientIP string) {
	sm.DisconnectVirtualViewerBySessionID("", nodeID, streamName, clientIP)
}

// DisconnectVirtualViewerBySessionID transitions an ACTIVE viewer to DISCONNECTED when USER_END arrives.
// If mistSessionID is provided, match by Mist session ID; otherwise fallback to IP matching.
func (sm *StreamStateManager) DisconnectVirtualViewerBySessionID(mistSessionID, nodeID, streamName, clientIP string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Find oldest ACTIVE viewer matching criteria
	var matchedViewer *VirtualViewer
	var oldestTime time.Time

	if mistSessionID != "" {
		for _, candidateID := range sm.viewersByNode[nodeID] {
			viewer := sm.virtualViewers[candidateID]
			if viewer == nil || viewer.State != VirtualViewerActive {
				continue
			}
			if viewer.StreamName == streamName && viewer.MistSessionID == mistSessionID {
				if matchedViewer == nil || viewer.ConnectTime.Before(oldestTime) {
					matchedViewer = viewer
					oldestTime = viewer.ConnectTime
				}
			}
		}
	}

	if matchedViewer == nil {
		for _, candidateID := range sm.viewersByNode[nodeID] {
			viewer := sm.virtualViewers[candidateID]
			if viewer == nil || viewer.State != VirtualViewerActive {
				continue
			}
			if viewer.StreamName == streamName && viewer.ClientIP == clientIP {
				if matchedViewer == nil || viewer.ConnectTime.Before(oldestTime) {
					matchedViewer = viewer
					oldestTime = viewer.ConnectTime
				}
			}
		}
	}

	if matchedViewer != nil {
		matchedViewer.State = VirtualViewerDisconnected
		matchedViewer.DisconnectTime = time.Now()
	} else {
		var pendingViewer *VirtualViewer
		var pendingTime time.Time
		for _, viewerID := range sm.viewersByNode[nodeID] {
			viewer := sm.virtualViewers[viewerID]
			if viewer == nil || viewer.State != VirtualViewerPending {
				continue
			}
			if viewer.StreamName == streamName && viewer.ClientIP == clientIP {
				if pendingViewer == nil || viewer.RedirectTime.Before(pendingTime) {
					pendingViewer = viewer
					pendingTime = viewer.RedirectTime
				}
			}
		}
		if pendingViewer != nil {
			pendingViewer.State = VirtualViewerAbandoned
			pendingViewer.DisconnectTime = time.Now()
			if node := sm.nodes[nodeID]; node != nil {
				node.PendingRedirects--
				if node.PendingRedirects < 0 {
					node.PendingRedirects = 0
				}
				if node.AddBandwidth >= pendingViewer.EstBandwidth {
					node.AddBandwidth -= pendingViewer.EstBandwidth
				} else {
					node.AddBandwidth = 0
				}
				sm.recomputeNodeScoresLocked(node)
			}
		}
	}

	// Cleanup old DISCONNECTED viewers (keep for a short retention period)
	sm.cleanupOldViewersLocked(nodeID, 5*time.Minute)
}

// ReconcileVirtualViewers is called on NODE_LIFECYCLE_UPDATE to reconcile state with reality.
// It updates the bandwidth estimate, times out stale PENDING viewers, and recalculates AddBandwidth.
func (sm *StreamStateManager) ReconcileVirtualViewers(nodeID string, realTotalConnections int, realUpSpeed uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	node := sm.nodes[nodeID]
	if node == nil {
		return
	}

	// 1. Update real metrics (already done by UpdateNodeMetrics, but capture here too)
	node.UpSpeed = float64(realUpSpeed)
	node.LastPollTime = time.Now()

	// 2. Update bandwidth estimate for future redirects
	if realTotalConnections > 0 && realUpSpeed > 0 {
		node.EstBandwidthPerUser = sm.clampBandwidth(realUpSpeed / uint64(realTotalConnections))
	}

	// 3. Timeout stale PENDING viewers (>30s old)
	sm.timeoutStalePendingViewersLocked(nodeID, 30*time.Second)

	// 3.5. Cleanup old ABANDONED/DISCONNECTED viewers
	sm.cleanupOldViewersLocked(nodeID, 5*time.Minute)

	// 4. Recalculate AddBandwidth based on remaining pending viewers
	var totalPendingBW uint64
	pendingCount := 0
	for _, viewerID := range sm.viewersByNode[nodeID] {
		viewer := sm.virtualViewers[viewerID]
		if viewer != nil && viewer.State == VirtualViewerPending {
			totalPendingBW += viewer.EstBandwidth
			pendingCount++
		}
	}
	node.PendingRedirects = pendingCount
	node.AddBandwidth = totalPendingBW

	// 5. Recompute scores
	sm.recomputeNodeScoresLocked(node)
}

// timeoutStalePendingViewersLocked marks old PENDING viewers as ABANDONED (must hold lock)
func (sm *StreamStateManager) timeoutStalePendingViewersLocked(nodeID string, maxAge time.Duration) {
	now := time.Now()
	cutoff := now.Add(-maxAge)

	for _, viewerID := range sm.viewersByNode[nodeID] {
		viewer := sm.virtualViewers[viewerID]
		if viewer == nil || viewer.State != VirtualViewerPending {
			continue
		}
		if viewer.RedirectTime.Before(cutoff) {
			viewer.State = VirtualViewerAbandoned
			viewer.DisconnectTime = now
		}
	}
}

// cleanupOldViewersLocked removes DISCONNECTED and ABANDONED viewers older than retention (must hold lock)
func (sm *StreamStateManager) cleanupOldViewersLocked(nodeID string, retention time.Duration) {
	now := time.Now()
	cutoff := now.Add(-retention)

	var remainingViewers []string
	for _, viewerID := range sm.viewersByNode[nodeID] {
		viewer := sm.virtualViewers[viewerID]
		if viewer == nil {
			continue
		}
		// Keep if still PENDING or ACTIVE, or if recently completed
		if viewer.State == VirtualViewerPending || viewer.State == VirtualViewerActive {
			remainingViewers = append(remainingViewers, viewerID)
		} else if viewer.DisconnectTime.After(cutoff) {
			remainingViewers = append(remainingViewers, viewerID)
		} else {
			// Remove from main map
			delete(sm.virtualViewers, viewerID)
		}
	}
	sm.viewersByNode[nodeID] = remainingViewers
}

// calculateEstBandwidthPerUserLocked estimates per-viewer bandwidth (must hold lock)
func (sm *StreamStateManager) calculateEstBandwidthPerUserLocked(node *NodeState) uint64 {
	// Priority 1: Use cached estimate if recent
	if node.EstBandwidthPerUser > 0 {
		return node.EstBandwidthPerUser
	}

	// Priority 2: Calculate from this node's current metrics
	if node.UpSpeed > 0 {
		// Count total viewers on this node
		totalViewers := uint64(0)
		for _, nodeInstances := range sm.streamInstances {
			if inst := nodeInstances[node.NodeID]; inst != nil && inst.TotalConnections > 0 {
				totalViewers += uint64(inst.TotalConnections)
			}
		}
		if totalViewers > 0 {
			return sm.clampBandwidth(uint64(node.UpSpeed) / totalViewers)
		}
	}

	// Priority 3: Use cluster-wide average
	clusterUp, clusterViewers := sm.getClusterTotalsLocked()
	if clusterUp > 0 && clusterViewers > 0 {
		return sm.clampBandwidth(clusterUp / clusterViewers)
	}

	// Priority 4: Default assumption (1 Mbps)
	return 131072
}

// getClusterTotalsLocked returns total upload speed and viewer count across all nodes (must hold lock)
func (sm *StreamStateManager) getClusterTotalsLocked() (totalUp uint64, totalViewers uint64) {
	for _, node := range sm.nodes {
		if node == nil || !node.IsHealthy {
			continue
		}
		totalUp += uint64(node.UpSpeed)
	}
	for _, nodeInstances := range sm.streamInstances {
		for _, inst := range nodeInstances {
			if inst != nil && inst.TotalConnections > 0 {
				totalViewers += uint64(inst.TotalConnections)
			}
		}
	}
	return
}

// clampBandwidth ensures bandwidth estimate is within reasonable bounds
func (sm *StreamStateManager) clampBandwidth(bw uint64) uint64 {
	const minBW = 64 * 1024   // 0.5 Mbps (64 KB/s)
	const maxBW = 1024 * 1024 // 8 Mbps (1 MB/s)
	if bw < minBW {
		return minBW
	}
	if bw > maxBW {
		return maxBW
	}
	return bw
}

// GetVirtualViewerStats returns statistics about virtual viewers for observability
func (sm *StreamStateManager) GetVirtualViewerStats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stats := map[string]interface{}{
		"total_viewers": len(sm.virtualViewers),
	}

	// Count by state
	pending, active, abandoned, disconnected := 0, 0, 0, 0
	for _, viewer := range sm.virtualViewers {
		switch viewer.State {
		case VirtualViewerPending:
			pending++
		case VirtualViewerActive:
			active++
		case VirtualViewerAbandoned:
			abandoned++
		case VirtualViewerDisconnected:
			disconnected++
		}
	}
	stats["pending"] = pending
	stats["active"] = active
	stats["abandoned"] = abandoned
	stats["disconnected"] = disconnected

	// Per-node pending counts
	nodeStats := make(map[string]int)
	for nodeID, node := range sm.nodes {
		if node != nil {
			nodeStats[nodeID] = node.PendingRedirects
		}
	}
	stats["pending_by_node"] = nodeStats

	// Drift: virtual ACTIVE count vs Helmsman real count per node
	activeByNode := make(map[string]int)
	for _, viewer := range sm.virtualViewers {
		if viewer != nil && viewer.State == VirtualViewerActive {
			activeByNode[viewer.NodeID]++
		}
	}
	realByNode := make(map[string]int)
	for _, instances := range sm.streamInstances {
		for nodeID, inst := range instances {
			if inst != nil {
				realByNode[nodeID] += inst.TotalConnections
			}
		}
	}
	driftByNode := make(map[string]int)
	for nodeID := range sm.nodes {
		driftByNode[nodeID] = activeByNode[nodeID] - realByNode[nodeID]
	}
	stats["drift_by_node"] = driftByNode

	return stats
}

// GetViewerDrift returns the difference between event-based ACTIVE viewer count and Helmsman total connections per node.
// Positive = more virtual than real (potential ghosts), Negative = more real than virtual (missed USER_NEW).
func (sm *StreamStateManager) GetViewerDrift() map[string]int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	drift := make(map[string]int)

	// Count ACTIVE virtual viewers per node
	activeByNode := make(map[string]int)
	for _, viewerID := range sm.virtualViewers {
		if viewerID != nil && viewerID.State == VirtualViewerActive {
			activeByNode[viewerID.NodeID]++
		}
	}

	// Get real totals from Helmsman per node
	realByNode := make(map[string]int)
	for _, instances := range sm.streamInstances {
		for nodeID, inst := range instances {
			if inst != nil {
				realByNode[nodeID] += inst.TotalConnections
			}
		}
	}

	// Calculate drift for each node that has either virtual or real viewers
	allNodes := make(map[string]bool)
	for nodeID := range activeByNode {
		allNodes[nodeID] = true
	}
	for nodeID := range realByNode {
		allNodes[nodeID] = true
	}

	for nodeID := range allNodes {
		drift[nodeID] = activeByNode[nodeID] - realByNode[nodeID]
	}

	return drift
}

// GetVirtualViewersForNode returns all virtual viewers for a specific node
func (sm *StreamStateManager) GetVirtualViewersForNode(nodeID string) []*VirtualViewer {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var viewers []*VirtualViewer
	for _, viewerID := range sm.viewersByNode[nodeID] {
		if viewer := sm.virtualViewers[viewerID]; viewer != nil {
			// Return a copy
			viewerCopy := *viewer
			viewers = append(viewers, &viewerCopy)
		}
	}
	return viewers
}
