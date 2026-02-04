package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	"frameworks/pkg/models"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ClipInfo represents local clip metadata for VOD serving
type ClipInfo struct {
	FilePath     string
	StreamName   string
	Format       string
	SizeBytes    uint64
	CreatedAt    time.Time
	S3URL        string
	SegmentCount int  // Number of segments (for DVR recordings)
	HasDtsh      bool // True if .dtsh index file exists locally
	AccessCount  int
	LastAccessed time.Time
	ArtifactType pb.ArtifactEvent_ArtifactType
}

// PrometheusMonitor handles monitoring of MistServer Prometheus endpoints
type PrometheusMonitor struct {
	mutex         sync.RWMutex
	updateChannel chan models.NodeUpdate
	stopChannel   chan bool
	mistPassword  string // Configurable password for /koekjes endpoint
	// MistServer API authentication
	mistUsername    string // Username for MistServer API
	mistAPIPassword string // Password for MistServer API
	// Single node info (since each sidecar monitors one node)
	nodeID        string
	baseURL       string // Internal MistServer URL (for API calls)
	edgePublicURL string // Public edge URL (for client-facing BaseUrl)
	latitude      *float64
	longitude     *float64
	location      string
	lastSeen      time.Time
	isHealthy     bool
	lastJSONData  map[string]interface{} // Store last fetched JSON data
	// Artifact index for fast VOD lookups
	artifactIndex    map[string]*ClipInfo // clipHash -> ClipInfo
	lastArtifactScan time.Time

	// Shared Mist API client
	mistClient *mist.Client
	mistMu     sync.Mutex

	// Bandwidth rate calculation state
	lastBwUp     uint64
	lastBwDown   uint64
	lastPollTime time.Time
}

var prometheusMonitor *PrometheusMonitor
var monitorLogger logging.Logger
var fileStabilityThreshold = 10 * time.Second

var (
	streamViewers = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_viewers",
			Help: "Number of viewers per stream",
		},
		[]string{"stream"},
	)

	streamBandwidthDown = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_bandwidth_down_bps",
			Help: "Download bandwidth in bytes per second",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamBandwidthUp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_bandwidth_up_bps",
			Help: "Upload bandwidth in bytes per second",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamConnectionTime = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_connection_time_seconds",
			Help: "Connection duration in seconds",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamPacketsTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_packets_total",
			Help: "Total packets processed",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamPacketsLost = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_packets_lost",
			Help: "Total packets lost",
		},
		[]string{"stream", "protocol", "host"},
	)

	streamPacketsRetransmitted = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stream_packets_retransmitted",
			Help: "Total packets retransmitted",
		},
		[]string{"stream", "protocol", "host"},
	)
)

// InitPrometheusMonitor initializes the Prometheus monitoring system with logger
func InitPrometheusMonitor(logger logging.Logger) {
	monitorLogger = logger

	mistAPIPassword := os.Getenv("MIST_API_PASSWORD")
	mistUsername := os.Getenv("MIST_API_USERNAME")
	mistPassword := os.Getenv("MIST_PASSWORD")

	if mistAPIPassword == "" {
		mistAPIPassword = "test"
	}
	if mistUsername == "" {
		mistUsername = "test"
	}
	if mistPassword == "" {
		mistPassword = "koekjes"
	}

	prometheusMonitor = &PrometheusMonitor{
		mistPassword:    mistPassword,
		mistUsername:    mistUsername,
		mistAPIPassword: mistAPIPassword,
		updateChannel:   make(chan models.NodeUpdate, 10),
		stopChannel:     make(chan bool, 1),
		isHealthy:       true,
		lastJSONData:    make(map[string]interface{}),
		artifactIndex:   make(map[string]*ClipInfo),
	}

	monitorLogger.WithFields(logging.Fields{
		"mist_api_user": mistUsername,
	}).Info("Prometheus monitor initialized")

	// Start monitoring goroutines
	go prometheusMonitor.monitorNodes()
	go prometheusMonitor.processUpdates()
}

// AddNode adds a MistServer node to monitor
// baseURL is internal MistServer URL, edgePublicURL is client-facing URL
func (pm *PrometheusMonitor) AddNode(nodeID, baseURL, edgePublicURL string) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	pm.nodeID = nodeID
	pm.baseURL = baseURL
	pm.edgePublicURL = edgePublicURL
	pm.lastSeen = time.Now()
	pm.isHealthy = false
	pm.latitude = nil
	pm.longitude = nil
	pm.location = ""
	pm.lastJSONData = make(map[string]interface{}) // Clear previous data

	// Initialize or update Mist client for this node
	if pm.mistClient == nil {
		pm.mistClient = mist.NewClient(monitorLogger)
	}
	pm.mistClient.BaseURL = baseURL // Internal URL for API calls

	monitorLogger.WithFields(logging.Fields{
		"node_id":         nodeID,
		"base_url":        baseURL,
		"edge_public_url": edgePublicURL,
	}).Info("Added MistServer node for monitoring")
}

// RemoveNode removes a MistServer node from monitoring
func (pm *PrometheusMonitor) RemoveNode(nodeID string) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	pm.nodeID = ""
	pm.baseURL = ""
	pm.lastSeen = time.Time{}
	pm.isHealthy = false
	pm.latitude = nil
	pm.longitude = nil
	pm.location = ""
	pm.lastJSONData = make(map[string]interface{}) // Clear previous data

	monitorLogger.WithFields(logging.Fields{
		"node_id": nodeID,
	}).Info("Removed MistServer node from monitoring")
}

// TriggerImmediatePoll triggers immediate JSON and stream polling using the stored node
func (pm *PrometheusMonitor) TriggerImmediatePoll() {
	pm.mutex.RLock()
	nodeID := pm.nodeID
	baseURL := pm.baseURL
	pm.mutex.RUnlock()
	if nodeID == "" || baseURL == "" {
		return
	}
	go pm.emitNodeLifecycle(nodeID, baseURL)
	go pm.emitStreamLifecycle(nodeID, baseURL)
}

// TriggerImmediatePoll triggers immediate polling if the monitor is initialized
func TriggerImmediatePoll() {
	if prometheusMonitor != nil {
		prometheusMonitor.TriggerImmediatePoll()
	}
}

// GetNodes returns the single monitored node (for compatibility)
func (pm *PrometheusMonitor) GetNodes() map[string]*models.NodeInfo {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	nodes := make(map[string]*models.NodeInfo)
	if pm.nodeID != "" {
		nodes[pm.nodeID] = &models.NodeInfo{
			NodeID:    pm.nodeID,
			BaseURL:   pm.baseURL,
			LastSeen:  pm.lastSeen,
			IsHealthy: pm.isHealthy,
			GeoData: geoip.GeoData{
				Latitude:  getFloat64PointerValue(pm.latitude),
				Longitude: getFloat64PointerValue(pm.longitude),
			},
			Location: pm.location,
		}
	}
	return nodes
}

// monitorNodes continuously monitors all registered nodes
func (pm *PrometheusMonitor) monitorNodes() {
	Ticker := time.NewTicker(10 * time.Second) // Monitor every 10 seconds
	defer Ticker.Stop()

	// Separate ticker for artifact scanning (less frequent to reduce disk I/O)
	artifactTicker := time.NewTicker(60 * time.Second)
	defer artifactTicker.Stop()

	for {
		select {
		case <-Ticker.C:
			pm.mutex.RLock()
			if pm.nodeID != "" && pm.baseURL != "" {
				nodeID := pm.nodeID
				baseURL := pm.baseURL
				pm.mutex.RUnlock()

				go pm.emitNodeLifecycle(nodeID, baseURL)
				go pm.emitStreamLifecycle(nodeID, baseURL)
				go pm.emitClientLifecycle(nodeID, baseURL) //nolint:errcheck // goroutine; errors logged internally
			} else {
				pm.mutex.RUnlock()
			}

		case <-artifactTicker.C:
			// Periodic artifact rescan to detect late-appearing .dtsh files
			if storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH"); storagePath != "" {
				go scanLocalArtifacts(storagePath)
			}

		case <-pm.stopChannel:
			monitorLogger.Info("Stopping Prometheus monitor")
			return
		}
	}
}

// emitNodeLifecycle fetches metrics from a single node (JSON only)
func (pm *PrometheusMonitor) emitNodeLifecycle(nodeID, baseURL string) {
	// Fetch JSON data using Mist client (/{secret}.json)
	pm.mistMu.Lock()
	jsonData, jsonErr := pm.mistClient.FetchJSON("")
	pm.mistMu.Unlock()

	// Send update through channel
	update := models.NodeUpdate{
		NodeID:   nodeID,
		BaseURL:  baseURL,
		JSONData: jsonData,
		Error:    jsonErr,
	}

	select {
	case pm.updateChannel <- update:
	default:
		control.TriggersDropped.WithLabelValues("node_lifecycle", "channel_full").Inc()
		monitorLogger.WithFields(logging.Fields{
			"node_id": nodeID,
		}).Warn("Update channel full, dropping update for node")
	}
}

// emitStreamLifecycle fetches data from MistServer's TCP API directly
func (pm *PrometheusMonitor) emitStreamLifecycle(nodeID, baseURL string) {
	monitorLogger.WithFields(logging.Fields{
		"api_url": baseURL + "/api2",
		"node_id": nodeID,
	}).Info("Fetching active streams from Mist API")

	pm.mistMu.Lock()
	apiResponse, err := pm.mistClient.GetActiveStreams()
	pm.mistMu.Unlock()
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"api_url": baseURL + "/api2",
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to fetch active streams")
		return
	}

	// Extract active streams data
	if activeStreams, ok := apiResponse["active_streams"].(map[string]interface{}); ok {
		monitorLogger.WithFields(logging.Fields{
			"api_url": baseURL + "/api2",
			"node_id": nodeID,
			"count":   len(activeStreams),
		}).Info("Found active streams via Mist API")
		for streamName, streamData := range activeStreams {
			if streamInfo, ok := streamData.(map[string]interface{}); ok {
				pm.processActiveStreamData(nodeID, streamName, streamInfo)
			}
		}
	} else {
		monitorLogger.WithFields(logging.Fields{
			"api_url": baseURL + "/api2",
			"node_id": nodeID,
		}).Warn("No active_streams found")
	}
}

// processActiveStreamData processes individual stream data from MistServer API
func (pm *PrometheusMonitor) processActiveStreamData(nodeID, streamName string, streamData map[string]interface{}) {
	// Extract internal name from wildcard stream
	var internalName string
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		internalName = streamName[plusIndex+1:]
	} else {
		internalName = streamName
	}

	// Get current viewers
	viewers := 0
	if v, ok := streamData["viewers"].(float64); ok {
		viewers = int(v)
	}

	// Get client count (total connections)
	clients := 0
	if c, ok := streamData["clients"].(float64); ok {
		clients = int(c)
	}

	// Get track count
	trackCount := 0
	if t, ok := streamData["tracks"].(float64); ok {
		trackCount = int(t)
	}

	// Get input count
	inputs := 0
	if i, ok := streamData["inputs"].(float64); ok {
		inputs = int(i)
	}

	// Get output count
	outputs := 0
	if o, ok := streamData["outputs"].(float64); ok {
		outputs = int(o)
	}

	// Get bandwidth data
	var upbytes, downbytes int64
	if ub, ok := streamData["upbytes"].(float64); ok {
		upbytes = int64(ub)
	}
	if db, ok := streamData["downbytes"].(float64); ok {
		downbytes = int64(db)
	}

	// Get timing data (for potential future use in health calculations)
	var firstMs, lastMs int64
	if fm, ok := streamData["firstms"].(float64); ok {
		firstMs = int64(fm)
	}
	if lm, ok := streamData["lastms"].(float64); ok {
		lastMs = int64(lm)
	}
	_ = firstMs // Available for future timing calculations
	_ = lastMs  // Available for future timing calculations

	// Parse health data for detailed track information
	var healthData map[string]interface{}
	var trackDetails []map[string]interface{}

	if health, ok := streamData["health"].(map[string]interface{}); ok {
		healthData = health
		healthJSON, _ := json.MarshalIndent(health, "", "  ")
		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
			"body":        string(healthJSON),
		}).Debug("Raw health data for stream")

		// Parse individual tracks from health data
		for trackName, trackInfo := range health {
			if trackMap, ok := trackInfo.(map[string]interface{}); ok {
				// Skip non-track fields (buffer, jitter, maxkeepaway)
				if trackName == "buffer" || trackName == "jitter" || trackName == "maxkeepaway" {
					continue
				}

				// Check if this looks like a track (has codec field)
				if codec, hasCodec := trackMap["codec"].(string); hasCodec {
					trackDetail := map[string]interface{}{
						"track_name": trackName,
						"codec":      codec,
					}

					// Extract bitrate (this is the real-time accurate bitrate!)
					if kbits, ok := trackMap["kbits"].(float64); ok {
						trackDetail["bitrate_kbps"] = int(kbits)
						trackDetail["bitrate_bps"] = int64(kbits * 1000)
					}

					// Extract buffer info
					if buffer, ok := trackMap["buffer"].(float64); ok {
						trackDetail["buffer"] = int(buffer)
					}

					// Extract jitter
					if jitter, ok := trackMap["jitter"].(float64); ok {
						trackDetail["jitter"] = int(jitter)
					}

					// Determine track type and extract type-specific fields
					if strings.Contains(trackName, "video_") || codec == "H264" || codec == "H265" || codec == "AV1" {
						trackDetail["type"] = "video"

						// Extract video-specific fields
						if width, ok := trackMap["width"].(float64); ok {
							trackDetail["width"] = int(width)
						}
						if height, ok := trackMap["height"].(float64); ok {
							trackDetail["height"] = int(height)
						}
						if fpks, ok := trackMap["fpks"].(float64); ok {
							trackDetail["fps"] = fpks / 1000 // fpks is frames per kilosecond
						}
						if bframes, ok := trackMap["bframes"].(bool); ok {
							trackDetail["has_bframes"] = bframes
						}

						// Create resolution string
						if width, hasWidth := trackDetail["width"].(int); hasWidth {
							if height, hasHeight := trackDetail["height"].(int); hasHeight {
								trackDetail["resolution"] = fmt.Sprintf("%dx%d", width, height)
							}
						}

					} else if strings.Contains(trackName, "audio_") || codec == "AAC" || codec == "opus" || codec == "MP3" {
						trackDetail["type"] = "audio"

						// Extract audio-specific fields
						if channels, ok := trackMap["channels"].(float64); ok {
							trackDetail["channels"] = int(channels)
						}
						if rate, ok := trackMap["rate"].(float64); ok {
							trackDetail["sample_rate"] = int(rate)
						}

					} else if strings.Contains(trackName, "meta_") || codec == "JSON" {
						trackDetail["type"] = "meta"
					} else {
						trackDetail["type"] = "unknown"
					}

					// Extract timing/frame info from keys if available
					if keys, ok := trackMap["keys"].(map[string]interface{}); ok {
						if frameMax, ok := keys["frames_max"].(float64); ok {
							trackDetail["frames_max"] = int(frameMax)
						}
						if frameMin, ok := keys["frames_min"].(float64); ok {
							trackDetail["frames_min"] = int(frameMin)
						}
					}

					trackDetails = append(trackDetails, trackDetail)
					monitorLogger.WithFields(logging.Fields{
						"node_id":     nodeID,
						"stream_name": streamName,
						"track_name":  trackName,
						"type":        trackDetail["type"],
						"codec":       codec,
						"bitrate":     trackDetail["bitrate_kbps"],
					}).Debug("Parsed track")
				}
			}
		}

		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
			"count":       len(trackDetails),
		}).Debug("Extracted tracks from health data")
	} else {
		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
		}).Warn("No health data found for stream")
	}

	// Get node geographic information for logging context
	pm.mutex.RLock()
	latitude := pm.latitude
	longitude := pm.longitude
	location := pm.location
	pm.mutex.RUnlock()

	// Log stream location context (geographic data not included in StreamLifecycle payload)
	geoContext := "unknown"
	if location != "" {
		geoContext = location
	} else if latitude != nil && longitude != nil {
		geoContext = fmt.Sprintf("%.2f,%.2f", *latitude, *longitude)
	}

	monitorLogger.WithFields(logging.Fields{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"viewers":       viewers,
		"clients":       clients,
		"tracks":        trackCount,
		"inputs":        inputs,
		"outputs":       outputs,
		"upbytes":       upbytes,
		"downbytes":     downbytes,
		"health_tracks": len(trackDetails),
		"location":      geoContext,
	}).Info("Processing active stream")

	// Analytics data forwarded via MistTrigger below

	// Convert API response to MistTrigger using converter
	mistTrigger := convertStreamAPIToMistTrigger(nodeID, streamName, internalName, streamData, healthData, trackDetails, trackCount, monitorLogger)

	// Send
	if _, err := control.SendMistTrigger(mistTrigger, monitorLogger); err != nil {
		monitorLogger.WithFields(logging.Fields{
			"error":         err,
			"internal_name": internalName,
		}).Error("Failed to send stream lifecycle update to Foghorn")
	}
}

// processUpdates processes node updates from the update channel
func (pm *PrometheusMonitor) processUpdates() {
	for update := range pm.updateChannel {
		pm.mutex.Lock()

		// Check if this is our monitored node
		if pm.nodeID != update.NodeID {
			pm.mutex.Unlock()
			continue
		}

		// Update node information
		pm.lastSeen = time.Now()

		if update.Error != nil {
			pm.isHealthy = false
			monitorLogger.WithFields(logging.Fields{
				"node_id": update.NodeID,
				"error":   update.Error,
			}).Error("Error monitoring node")
		} else {
			pm.isHealthy = true
			pm.lastJSONData = update.JSONData // Store the fetched JSON data

			// Extract geographic coordinates from JSON data (only update if changed)
			if jsonData := update.JSONData; jsonData != nil {
				monitorLogger.WithFields(logging.Fields{
					"node_id":       update.NodeID,
					"has_json_data": true,
					"json_keys":     getMapKeys(jsonData),
				}).Debug("Processing JSON data from koekjes endpoint")

				if locData, ok := jsonData["loc"].(map[string]interface{}); ok {
					monitorLogger.WithFields(logging.Fields{
						"node_id":  update.NodeID,
						"loc_data": locData,
					}).Info("Found location data in koekjes JSON")

					oldLat := pm.latitude
					oldLon := pm.longitude
					oldLoc := pm.location

					if lat, ok := locData["lat"].(float64); ok {
						pm.latitude = &lat
					}
					if lon, ok := locData["lon"].(float64); ok {
						pm.longitude = &lon
					}
					if name, ok := locData["name"].(string); ok && name != "" {
						pm.location = name
					}

					monitorLogger.WithFields(logging.Fields{
						"node_id":      update.NodeID,
						"old_lat":      oldLat,
						"new_lat":      pm.latitude,
						"old_lon":      oldLon,
						"new_lon":      pm.longitude,
						"old_location": oldLoc,
						"new_location": pm.location,
					}).Info("Updated PrometheusMonitor location data")
				} else {
					// If no location data from MistServer, log it with details
					monitorLogger.WithFields(logging.Fields{
						"node_id":     update.NodeID,
						"json_keys":   getMapKeys(jsonData),
						"has_loc_key": jsonData["loc"] != nil,
					}).Warn("No location data from MistServer for node")
				}
			} else {
				monitorLogger.WithFields(logging.Fields{
					"node_id": update.NodeID,
				}).Error("No JSON data received from koekjes endpoint")
			}
		}

		pm.mutex.Unlock()

		// Forward metrics to API and analytics
		go pm.forwardNodeMetrics(update.NodeID)
	}
}

// forwardNodeMetrics forwards node metrics to API and analytics services - TYPED VERSION
func (pm *PrometheusMonitor) forwardNodeMetrics(nodeID string) {
	// Capabilities from environment (fallback defaults: all true in dev)
	capIngest := os.Getenv("HELMSMAN_CAP_INGEST")
	capEdge := os.Getenv("HELMSMAN_CAP_EDGE")
	capStorage := os.Getenv("HELMSMAN_CAP_STORAGE")
	capProcessing := os.Getenv("HELMSMAN_CAP_PROCESSING")
	roles := rolesFromCapabilityFlags(capIngest, capEdge, capStorage, capProcessing)

	// Convert API response to MistTrigger using converter
	mistTrigger := pm.convertNodeAPIToMistTrigger(nodeID, pm.getLastJSONData(), monitorLogger)

	// Enrich with Helmsman-specific capabilities, storage, limits
	enrichNodeLifecycleTrigger(mistTrigger, capIngest, capEdge, capStorage, capProcessing, roles)

	// Send
	if _, err := control.SendMistTrigger(mistTrigger, monitorLogger); err != nil {
		monitorLogger.WithError(err).Error("Failed to send node lifecycle update via gRPC")
		return
	}
	monitorLogger.WithFields(logging.Fields{
		"node_id":  nodeID,
		"bw_limit": mistTrigger.GetNodeLifecycleUpdate().GetBwLimit(),
		"ram_max":  mistTrigger.GetNodeLifecycleUpdate().GetRamMax(),
	}).Info("Sent node lifecycle update to Foghorn")
}

// Stop stops the Prometheus monitor
func (pm *PrometheusMonitor) Stop() {
	close(pm.stopChannel)
	close(pm.updateChannel)
}

// HTTP handlers for Prometheus monitoring endpoints

// GetPrometheusNodes returns information about all monitored nodes
func GetPrometheusNodes(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Prometheus monitor not initialized"})
		return
	}

	prometheusMonitor.mutex.RLock()
	nodeInfo := map[string]interface{}{
		"node_id":   prometheusMonitor.nodeID,
		"base_url":  prometheusMonitor.baseURL,
		"latitude":  prometheusMonitor.latitude,
		"longitude": prometheusMonitor.longitude,
		"location":  prometheusMonitor.location,
		"last_seen": prometheusMonitor.lastSeen,
		"healthy":   prometheusMonitor.isHealthy,
	}
	prometheusMonitor.mutex.RUnlock()

	c.JSON(http.StatusOK, gin.H{"nodes": []interface{}{nodeInfo}})
}

// AddPrometheusNode adds a new node to monitor
func AddPrometheusNode(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Prometheus monitor not initialized"})
		return
	}

	var request struct {
		NodeID        string   `json:"node_id" binding:"required"`
		BaseURL       string   `json:"base_url" binding:"required"`
		EdgePublicURL string   `json:"edge_public_url"` // Optional, falls back to base_url
		Latitude      *float64 `json:"latitude"`
		Longitude     *float64 `json:"longitude"`
		Location      string   `json:"location"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	edgeURL := request.EdgePublicURL
	if edgeURL == "" {
		edgeURL = request.BaseURL // Fallback to base_url if not provided
	}
	prometheusMonitor.AddNode(request.NodeID, request.BaseURL, edgeURL)

	c.JSON(http.StatusOK, gin.H{"message": "Node added successfully"})
}

// AddPrometheusNodeDirect adds a node directly to the monitor (not via HTTP)
// baseURL is the internal MistServer URL, edgePublicURL is the client-facing URL
func AddPrometheusNodeDirect(nodeID, baseURL, edgePublicURL string) {
	if prometheusMonitor != nil {
		prometheusMonitor.AddNode(nodeID, baseURL, edgePublicURL)
	}
}

// RemovePrometheusNode removes a node from monitoring
func RemovePrometheusNode(c *gin.Context) {
	if prometheusMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Prometheus monitor not initialized"})
		return
	}

	nodeID := c.Param("node_id")
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node_id parameter required"})
		return
	}

	prometheusMonitor.RemoveNode(nodeID)
	c.JSON(http.StatusOK, gin.H{"message": "Node removed successfully"})
}

// getFloat64PointerValue safely dereferences a *float64, returning 0 if nil (for embedded structs)
func getFloat64PointerValue(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// getFloat64 safely converts interface{} to float64
func getFloat64(v interface{}) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// getInt64 safely converts interface{} to int64
func getInt64(v interface{}) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	if i, ok := v.(int64); ok {
		return i
	}
	return 0
}

// getString safely converts interface{} to string
func getString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// getMapKeys returns the keys of a map[string]interface{} for debugging
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (pm *PrometheusMonitor) emitClientLifecycle(nodeID, mistURL string) error {
	// Query MistServer clients API for detailed metrics using shared client
	pm.mistMu.Lock()
	result, err := pm.mistClient.GetClients()
	pm.mistMu.Unlock()
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"error": err,
			"url":   mistURL + "/api2",
		}).Error("Failed to query MistServer clients API")
		return err
	}

	// Process client metrics
	if clients, ok := result["clients"].(map[string]interface{}); ok {
		if data, ok := clients["data"].([]interface{}); ok {
			fields, ok := clients["fields"].([]interface{})
			if !ok {
				monitorLogger.Error("Failed to parse client fields as []interface{}")
				return err
			}

			// Map field names to indices
			fieldMap := make(map[string]int)
			for i, field := range fields {
				fieldStr, ok := field.(string)
				if !ok {
					monitorLogger.WithField("field", field).Error("Failed to parse field name as string")
					continue
				}
				fieldMap[fieldStr] = i
			}

			// Process each client connection
			for _, clientData := range data {
				client, ok := clientData.([]interface{})
				if !ok {
					monitorLogger.WithField("clientData", clientData).Error("Failed to parse client data as []interface{}")
					continue
				}

				// Safely extract required fields with bounds checking
				streamIdx, hasStream := fieldMap["stream"]
				protocolIdx, hasProtocol := fieldMap["protocol"]
				hostIdx, hasHost := fieldMap["host"]

				if !hasStream || !hasProtocol || !hasHost {
					monitorLogger.Error("Missing required fields in client data")
					continue
				}

				if streamIdx >= len(client) || protocolIdx >= len(client) || hostIdx >= len(client) {
					monitorLogger.Error("Client data array too short for required indices")
					continue
				}

				streamName, ok := client[streamIdx].(string)
				if !ok {
					monitorLogger.WithField("streamData", client[streamIdx]).Error("Failed to parse stream name as string")
					continue
				}

				protocol, ok := client[protocolIdx].(string)
				if !ok {
					monitorLogger.WithField("protocolData", client[protocolIdx]).Error("Failed to parse protocol as string")
					continue
				}

				host, ok := client[hostIdx].(string)
				if !ok {
					monitorLogger.WithField("hostData", client[hostIdx]).Error("Failed to parse host as string")
					continue
				}

				// Update Prometheus metrics
				streamViewers.WithLabelValues(streamName).Inc()

				// Bandwidth metrics
				if idx, ok := fieldMap["downbps"]; ok {
					if downBps, ok := client[idx].(float64); ok {
						streamBandwidthDown.WithLabelValues(streamName, protocol, host).Set(downBps)
					}
				}
				if idx, ok := fieldMap["upbps"]; ok {
					if upBps, ok := client[idx].(float64); ok {
						streamBandwidthUp.WithLabelValues(streamName, protocol, host).Set(upBps)
					}
				}

				// Connection time
				if idx, ok := fieldMap["conntime"]; ok {
					if connTime, ok := client[idx].(float64); ok {
						streamConnectionTime.WithLabelValues(streamName, protocol, host).Set(connTime)
					}
				}

				// Packet statistics (support both old and new field names)
				if idx, ok := fieldMap["pktcount"]; ok {
					if pktCount, ok := client[idx].(float64); ok {
						streamPacketsTotal.WithLabelValues(streamName, protocol, host).Set(pktCount)
					}
				} else if idx, ok := fieldMap["packet_count"]; ok {
					if pktCount, ok := client[idx].(float64); ok {
						streamPacketsTotal.WithLabelValues(streamName, protocol, host).Set(pktCount)
					}
				}
				if idx, ok := fieldMap["pktlost"]; ok {
					if pktLost, ok := client[idx].(float64); ok {
						streamPacketsLost.WithLabelValues(streamName, protocol, host).Set(pktLost)
					}
				} else if idx, ok := fieldMap["packet_lost"]; ok {
					if pktLost, ok := client[idx].(float64); ok {
						streamPacketsLost.WithLabelValues(streamName, protocol, host).Set(pktLost)
					}
				}
				if idx, ok := fieldMap["pktretransmit"]; ok {
					if pktRetransmit, ok := client[idx].(float64); ok {
						streamPacketsRetransmitted.WithLabelValues(streamName, protocol, host).Set(pktRetransmit)
					}
				} else if idx, ok := fieldMap["packet_retransmit"]; ok {
					if pktRetransmit, ok := client[idx].(float64); ok {
						streamPacketsRetransmitted.WithLabelValues(streamName, protocol, host).Set(pktRetransmit)
					}
				}

				// Extract internal name from stream name
				internalName := streamName
				if idx := strings.Index(streamName, "+"); idx != -1 && idx+1 < len(streamName) {
					internalName = streamName[idx+1:]
				}

				// Extract client data directly for protobuf
				sessionID := getString(client[fieldMap["sessid"]])
				connectionTime := getFloat64(client[fieldMap["conntime"]])
				position := getFloat64(client[fieldMap["position"]])

				bandwidthIn := func() float64 {
					if idx, ok := fieldMap["upbps"]; ok {
						if v, ok := client[idx].(float64); ok {
							return v
						}
					}
					return 0
				}()

				bandwidthOut := func() float64 {
					if idx, ok := fieldMap["downbps"]; ok {
						if v, ok := client[idx].(float64); ok {
							return v
						}
					}
					return 0
				}()

				bytesDown := func() int64 {
					if idx, ok := fieldMap["down"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["bytes_down"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				bytesUp := func() int64 {
					if idx, ok := fieldMap["up"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["bytes_up"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				packetsSent := func() int64 {
					if idx, ok := fieldMap["pktcount"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["packet_count"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				packetsLost := func() int64 {
					if idx, ok := fieldMap["pktlost"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["packet_lost"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				packetsRetransmitted := func() int64 {
					if idx, ok := fieldMap["pktretransmit"]; ok {
						return getInt64(client[idx])
					}
					if idx, ok := fieldMap["packet_retransmit"]; ok {
						return getInt64(client[idx])
					}
					return 0
				}()

				// Convert API response to MistTrigger using converter
				mistTrigger := convertClientAPIToMistTrigger(nodeID, streamName, internalName, protocol, host, sessionID, connectionTime, position, bandwidthIn, bandwidthOut, bytesDown, bytesUp, packetsSent, packetsLost, packetsRetransmitted, monitorLogger)

				// Send
				if _, err := control.SendMistTrigger(mistTrigger, monitorLogger); err != nil {
					monitorLogger.WithFields(logging.Fields{
						"error":  err,
						"stream": streamName,
						"type":   "client-lifecycle",
					}).Error("Failed to send client lifecycle update to Foghorn")
				}
			}
		}
	}

	return nil
}

// getLastJSONData safely gets the last JSON data from koekjes endpoint
func (pm *PrometheusMonitor) getLastJSONData() map[string]interface{} {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	if jsonData := pm.lastJSONData; jsonData != nil {
		return jsonData
	}
	return nil
}

// scanLocalArtifacts scans the local storage for clip and DVR artifacts and updates the artifact index
func scanLocalArtifacts(basePath string) (uint64, int) {
	if basePath == "" {
		return 0, 0
	}

	var totalSize uint64
	artifactCount := 0
	newArtifactIndex := make(map[string]*ClipInfo)

	// Scan clips directory
	clipsDir := fmt.Sprintf("%s/clips", basePath)
	clipSize, clipCount := scanClipsDirectory(clipsDir, newArtifactIndex)
	totalSize += clipSize
	artifactCount += clipCount

	// Scan DVR directory
	dvrDir := fmt.Sprintf("%s/dvr", basePath)
	dvrSize, dvrCount := scanDVRDirectory(dvrDir, newArtifactIndex)
	totalSize += dvrSize
	artifactCount += dvrCount

	// Scan VOD directory
	vodDir := fmt.Sprintf("%s/vod", basePath)
	vodSize, vodCount := scanVODDirectory(vodDir, newArtifactIndex)
	totalSize += vodSize
	artifactCount += vodCount

	// Update the PrometheusMonitor artifact index atomically
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.Lock()
		prometheusMonitor.artifactIndex = newArtifactIndex
		prometheusMonitor.lastArtifactScan = time.Now()
		prometheusMonitor.mutex.Unlock()

		monitorLogger.WithFields(logging.Fields{
			"total_artifacts": len(newArtifactIndex),
			"total_size":      totalSize,
		}).Debug("Updated artifact index from filesystem scan")
	}

	return totalSize, artifactCount
}

// scanVODDirectory scans the VOD directory for user-uploaded assets
func scanVODDirectory(vodDir string, artifactIndex map[string]*ClipInfo) (uint64, int) {
	if _, err := os.Stat(vodDir); os.IsNotExist(err) {
		return 0, 0
	}

	var totalSize uint64
	artifactCount := 0

	entries, err := os.ReadDir(vodDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read VOD directory")
		return 0, 0
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := filepath.Ext(name)
		if ext == "" {
			continue
		}
		hash := strings.TrimSuffix(name, ext)
		if len(hash) < 18 || !isHex(hash) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) < fileStabilityThreshold {
			continue
		}

		filePath := fmt.Sprintf("%s/%s", vodDir, name)
		vodInfo := &ClipInfo{
			FilePath:     filePath,
			StreamName:   "", // VOD assets are not tied to a live stream name
			Format:       strings.TrimPrefix(ext, "."),
			SizeBytes:    uint64(info.Size()),
			CreatedAt:    info.ModTime(),
			SegmentCount: 0,
			HasDtsh:      false,
			ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_VOD,
		}
		artifactIndex[hash] = vodInfo
		totalSize += uint64(info.Size())
		artifactCount++
	}

	return totalSize, artifactCount
}

func isHex(value string) bool {
	if value == "" {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// scanClipsDirectory scans the clips directory for clip artifacts
func scanClipsDirectory(clipsDir string, artifactIndex map[string]*ClipInfo) (uint64, int) {
	// Check if clips directory exists
	if _, err := os.Stat(clipsDir); os.IsNotExist(err) {
		return 0, 0
	}

	var totalSize uint64
	artifactCount := 0

	// Walk the clips directory structure
	entries, err := os.ReadDir(clipsDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read clips directory")
		return 0, 0
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			// Check if this is a direct VOD link (clips/abc123def456.mp4)
			ext := filepath.Ext(entry.Name())
			if IsVideoFile(ext) {
				clipHash := strings.TrimSuffix(entry.Name(), ext)
				if len(clipHash) >= 18 { // Artifact hash: timestamp(14) + hex(4+)
					format := strings.TrimPrefix(ext, ".")
					filePath := fmt.Sprintf("%s/%s", clipsDir, entry.Name())

					// Get file info
					if fileInfo, err := os.Stat(filePath); err == nil {
						if time.Since(fileInfo.ModTime()) < fileStabilityThreshold {
							continue
						}
						// Try to determine stream name from symlink target
						streamName := "unknown"
						if target, err := os.Readlink(filePath); err == nil {
							absTarget := target
							if !filepath.IsAbs(target) {
								absTarget = filepath.Join(filepath.Dir(filePath), target)
							}
							if resolved, err := filepath.EvalSymlinks(absTarget); err == nil {
								if strings.Contains(resolved, string(filepath.Separator)+"vod"+string(filepath.Separator)) {
									continue
								}
							}
							parts := strings.Split(target, "/")
							for i, part := range parts {
								if part == "clips" && i+1 < len(parts) {
									streamName = parts[i+1]
									break
								}
							}
						}

						// Check if .dtsh index file exists
						hasDtsh := false
						if _, err := os.Stat(filePath + ".dtsh"); err == nil {
							hasDtsh = true
						}

						clipInfo := &ClipInfo{
							FilePath:     filePath,
							StreamName:   streamName,
							Format:       format,
							SizeBytes:    uint64(fileInfo.Size()),
							CreatedAt:    fileInfo.ModTime(),
							HasDtsh:      hasDtsh,
							AccessCount:  0,
							LastAccessed: fileInfo.ModTime(),
							ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
						}

						artifactIndex[clipHash] = clipInfo
						totalSize += uint64(fileInfo.Size())
						artifactCount++
					}
				}
			}
			continue
		}

		// This is a stream directory - scan for organized clips
		streamName := entry.Name()
		streamDir := fmt.Sprintf("%s/%s", clipsDir, streamName)

		streamEntries, err := os.ReadDir(streamDir)
		if err != nil {
			continue
		}

		for _, clipFile := range streamEntries {
			if clipFile.IsDir() {
				continue
			}

			// Check if this looks like a clip file
			ext := filepath.Ext(clipFile.Name())
			if !IsVideoFile(ext) {
				continue
			}
			clipHash := strings.TrimSuffix(clipFile.Name(), ext)
			if len(clipHash) < 18 { // Artifact hash: timestamp(14) + hex(4+)
				continue
			}
			format := strings.TrimPrefix(ext, ".")
			filePath := fmt.Sprintf("%s/%s", streamDir, clipFile.Name())

			// Get file info
			if fileInfo, err := os.Stat(filePath); err == nil {
				if time.Since(fileInfo.ModTime()) < fileStabilityThreshold {
					continue
				}
				// Check if .dtsh index file exists
				hasDtsh := false
				if _, err := os.Stat(filePath + ".dtsh"); err == nil {
					hasDtsh = true
				}

				clipInfo := &ClipInfo{
					FilePath:     filePath,
					StreamName:   streamName,
					Format:       format,
					SizeBytes:    uint64(fileInfo.Size()),
					CreatedAt:    fileInfo.ModTime(),
					HasDtsh:      hasDtsh,
					AccessCount:  0,
					LastAccessed: fileInfo.ModTime(),
					ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
				}

				artifactIndex[clipHash] = clipInfo
				totalSize += uint64(fileInfo.Size())
				artifactCount++
			}
		}
	}

	return totalSize, artifactCount
}

// calculateDVRSegmentSize parses an HLS manifest and sums up segment file sizes
func calculateDVRSegmentSize(manifestPath, baseDir string) (uint64, int) {
	var totalSize uint64
	var segmentCount int

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return 0, 0
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip empty lines and tags
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// This is a segment reference (relative path like "segments/1234_0.ts")
		segPath := filepath.Join(baseDir, line)
		if info, err := os.Stat(segPath); err == nil && !info.IsDir() {
			if time.Since(info.ModTime()) >= fileStabilityThreshold {
				totalSize += uint64(info.Size())
				segmentCount++
			}
		}
	}

	return totalSize, segmentCount
}

// scanDVRDirectory scans the DVR directory for DVR manifest files
func scanDVRDirectory(dvrDir string, artifactIndex map[string]*ClipInfo) (uint64, int) {
	// Check if DVR directory exists
	if _, err := os.Stat(dvrDir); os.IsNotExist(err) {
		return 0, 0
	}

	var totalSize uint64
	artifactCount := 0

	// Walk the DVR directory structure: /dvr/{stream_id}/{dvr_hash}/{dvr_hash}.m3u8
	entries, err := os.ReadDir(dvrDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read DVR directory")
		return 0, 0
	}

	activeDVRs := control.GetActiveDVRHashes()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue // Skip files in the DVR root directory
		}

		// This is a stream directory (stream_id) - scan for DVR hash subdirectories
		streamID := entry.Name()
		streamDVRDir := filepath.Join(dvrDir, streamID)

		streamEntries, err := os.ReadDir(streamDVRDir)
		if err != nil {
			continue
		}

		for _, dvrHashDir := range streamEntries {
			if !dvrHashDir.IsDir() {
				continue // Skip non-directories
			}

			dvrHash := dvrHashDir.Name()
			if len(dvrHash) < 18 {
				continue // Not a valid DVR hash
			}
			if activeDVRs[dvrHash] {
				continue
			}

			// DVR directory: /dvr/{stream_id}/{dvr_hash}/
			dvrPath := filepath.Join(streamDVRDir, dvrHash)
			manifestPath := filepath.Join(dvrPath, dvrHash+".m3u8")

			// Check if manifest exists
			fileInfo, err := os.Stat(manifestPath)
			if err != nil {
				continue // No manifest in this directory
			}

			// Calculate total size including segments referenced by manifest
			manifestSize := uint64(fileInfo.Size())
			segmentSize, segmentCount := calculateDVRSegmentSize(manifestPath, dvrPath)
			dvrTotalSize := manifestSize + segmentSize

			// Check if any .dtsh index files exist in the DVR directory
			hasDtsh := false
			if dirEntries, err := os.ReadDir(dvrPath); err == nil {
				for _, de := range dirEntries {
					if !de.IsDir() && strings.HasSuffix(de.Name(), ".dtsh") {
						hasDtsh = true
						break
					}
				}
			}

			// Add DVR manifest to artifact index using same ClipInfo structure
			dvrInfo := &ClipInfo{
				FilePath:     manifestPath,
				StreamName:   streamID,
				Format:       "m3u8",
				SizeBytes:    dvrTotalSize,
				CreatedAt:    fileInfo.ModTime(),
				SegmentCount: segmentCount,
				HasDtsh:      hasDtsh,
				AccessCount:  0,
				LastAccessed: fileInfo.ModTime(),
				ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_DVR,
			}

			artifactIndex[dvrHash] = dvrInfo
			totalSize += dvrTotalSize
			artifactCount++
		}
	}

	return totalSize, artifactCount
}

// GetStoredArtifacts returns artifacts from the global prometheusMonitor's artifactIndex
func GetStoredArtifacts() []*pb.StoredArtifact {
	if prometheusMonitor == nil {
		return nil
	}

	prometheusMonitor.mutex.RLock()
	defer prometheusMonitor.mutex.RUnlock()

	var artifacts []*pb.StoredArtifact
	for clipHash, clipInfo := range prometheusMonitor.artifactIndex {
		artifact := &pb.StoredArtifact{
			ClipHash:   clipHash,
			StreamName: clipInfo.StreamName,
			FilePath:   clipInfo.FilePath,
			SizeBytes:  clipInfo.SizeBytes,
			CreatedAt:  clipInfo.CreatedAt.Unix(),
			Format:     clipInfo.Format,
			HasDtsh:    clipInfo.HasDtsh,
			ArtifactType: func() pb.ArtifactEvent_ArtifactType {
				if clipInfo.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED {
					return clipInfo.ArtifactType
				}
				return pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED
			}(),
			AccessCount: func() uint64 {
				if clipInfo.AccessCount < 0 {
					return 0
				}
				return uint64(clipInfo.AccessCount)
			}(),
			LastAccessed: func() int64 {
				if clipInfo.LastAccessed.IsZero() {
					return 0
				}
				return clipInfo.LastAccessed.Unix()
			}(),
		}

		// Add S3 URL if available
		if clipInfo.S3URL != "" {
			artifact.S3Url = clipInfo.S3URL
		}

		artifacts = append(artifacts, artifact)
	}

	return artifacts
}

// touchArtifactAccess updates access metadata for a known artifact hash.
func touchArtifactAccess(hash string) {
	if hash == "" || prometheusMonitor == nil {
		return
	}

	prometheusMonitor.mutex.Lock()
	if clipInfo, ok := prometheusMonitor.artifactIndex[hash]; ok {
		clipInfo.AccessCount++
		clipInfo.LastAccessed = time.Now()
	}
	prometheusMonitor.mutex.Unlock()
}

// convertNodeAPIToMistTrigger converts MistServer JSON API response to MistTrigger
func (pm *PrometheusMonitor) convertNodeAPIToMistTrigger(nodeID string, jsonData map[string]interface{}, logger logging.Logger) *pb.MistTrigger {
	// Use public edge URL for client-facing BaseUrl (for playback URLs)
	// Fall back to internal URL if public not configured
	baseURL := pm.edgePublicURL
	if baseURL == "" {
		baseURL = pm.baseURL
	}

	nodeUpdate := &pb.NodeLifecycleUpdate{
		NodeId:    nodeID,
		BaseUrl:   baseURL, // Client-facing URL for playback
		EventType: "node_lifecycle_update",
		Timestamp: time.Now().Unix(),
	}

	if jsonData != nil {
		// Extract CPU usage (Mist provides integer percentage 0-100 or more)
		if cpu, ok := jsonData["cpu"].(float64); ok {
			nodeUpdate.CpuTenths = uint32(cpu * 10) // Convert % to tenths (e.g. 14% -> 140)
		}

		// Extract RAM info (Mist provides bytes)
		if memTotal, ok := jsonData["mem_total"].(float64); ok {
			nodeUpdate.RamMax = uint64(memTotal) // Bytes
		} else if ram, ok := jsonData["ram"].(map[string]interface{}); ok {
			// Fallback to old 'ram' object if 'mem_total' missing
			if max, ok := ram["max"].(float64); ok {
				nodeUpdate.RamMax = uint64(max)
			}
		}

		if memUsed, ok := jsonData["mem_used"].(float64); ok {
			nodeUpdate.RamCurrent = uint64(memUsed) // Bytes
		} else if ram, ok := jsonData["ram"].(map[string]interface{}); ok {
			// Fallback
			if current, ok := ram["current"].(float64); ok {
				nodeUpdate.RamCurrent = uint64(current)
			}
		}

		// Extract Shared Memory info
		if shmTotal, ok := jsonData["shm_total"].(float64); ok {
			nodeUpdate.ShmTotalBytes = uint64(shmTotal)
		}
		if shmUsed, ok := jsonData["shm_used"].(float64); ok {
			nodeUpdate.ShmUsedBytes = uint64(shmUsed)
		}

		// Extract bandwidth data from bw array: [up_total, down_total]
		if bw, ok := jsonData["bw"].([]interface{}); ok && len(bw) >= 2 {
			var currentUp, currentDown uint64
			if up, ok := bw[0].(float64); ok {
				currentUp = uint64(up)
			}
			if down, ok := bw[1].(float64); ok {
				currentDown = uint64(down)
			}

			// Store cumulative totals
			nodeUpdate.BandwidthOutTotal = currentUp
			nodeUpdate.BandwidthInTotal = currentDown

			// Compute rates (bytes/sec) from delta
			elapsed := time.Since(pm.lastPollTime).Seconds()
			if pm.lastBwUp > 0 && elapsed > 1.0 && currentUp >= pm.lastBwUp {
				// Normal case: compute rate from delta
				nodeUpdate.UpSpeed = uint64(float64(currentUp-pm.lastBwUp) / elapsed)
				nodeUpdate.DownSpeed = uint64(float64(currentDown-pm.lastBwDown) / elapsed)
			}
			// Else: first poll or counter reset - leave rates at 0

			// Store for next poll
			pm.lastBwUp = currentUp
			pm.lastBwDown = currentDown
			pm.lastPollTime = time.Now()
		} else if bandwidth, ok := jsonData["bandwidth"].(map[string]interface{}); ok {
			// Fallback to old 'bandwidth' object (legacy)
			if up, ok := bandwidth["up"].(float64); ok {
				nodeUpdate.UpSpeed = uint64(up)
			}
			if down, ok := bandwidth["down"].(float64); ok {
				nodeUpdate.DownSpeed = uint64(down)
			}
		}

		// Extract current connections from curr array
		// curr = [viewers, inputs, outgoing, unspecified, cached]
		if curr, ok := jsonData["curr"].([]interface{}); ok {
			if len(curr) > 0 {
				if viewers, ok := curr[0].(float64); ok {
					nodeUpdate.ConnectionsCurrent = uint32(viewers)
				}
			}
			if len(curr) > 1 {
				if inputs, ok := curr[1].(float64); ok {
					nodeUpdate.ConnectionsInputs = uint32(inputs)
				}
			}
			if len(curr) > 2 {
				if outgoing, ok := curr[2].(float64); ok {
					nodeUpdate.ConnectionsOutgoing = uint32(outgoing)
				}
			}
			if len(curr) > 4 {
				if cached, ok := curr[4].(float64); ok {
					nodeUpdate.ConnectionsCached = uint32(cached)
				}
			}
		}

		// Extract MistServer trigger health statistics (for monitoring/debugging)
		if triggers, ok := jsonData["triggers"].(map[string]interface{}); ok {
			if triggersJSON, err := json.Marshal(triggers); err == nil {
				nodeUpdate.TriggersJson = string(triggersJSON)
			}
		}

		if limit, ok := jsonData["bwlimit"].(float64); ok && limit > 0 {
			nodeUpdate.BwLimit = uint64(limit)
		} else {
			// Default to 1Gbps when MistServer doesn't report bwlimit (same as C++ default)
			nodeUpdate.BwLimit = 128 * 1024 * 1024 // 128 MB/s = ~1 Gbps
		}

		// Extract location data
		if locData, ok := jsonData["loc"].(map[string]interface{}); ok {
			if lat, ok := locData["lat"].(float64); ok {
				nodeUpdate.Latitude = lat
			}
			if lon, ok := locData["lon"].(float64); ok {
				nodeUpdate.Longitude = lon
			}
			if name, ok := locData["name"].(string); ok && name != "" {
				nodeUpdate.Location = name
			}
		}

		// Extract outputs configuration
		if outputs, ok := jsonData["outputs"]; ok {
			if outputsJSON, err := json.Marshal(outputs); err == nil {
				nodeUpdate.OutputsJson = string(outputsJSON)
			}
		}
	}

	// Get Disk Usage from OS
	// Default to /var/lib/mistserver if env not set
	storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH")
	if storagePath == "" {
		storagePath = "/var/lib/mistserver"
	}

	info, err := os.Stat(storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.WithField("path", storagePath).Warn("Disk metrics path does not exist; set HELMSMAN_STORAGE_LOCAL_PATH to a valid mount point")
		} else {
			logger.WithError(err).WithField("path", storagePath).Warn("Failed to stat disk metrics path")
		}
	} else if !info.IsDir() {
		logger.WithField("path", storagePath).Warn("Disk metrics path is not a directory")
	} else if total, used, err := getDiskUsage(storagePath); err == nil {
		nodeUpdate.DiskTotalBytes = total
		nodeUpdate.DiskUsedBytes = used
	} else {
		logger.WithError(err).WithField("path", storagePath).Warn("Failed to get disk usage")
	}

	// Determine node health based on resource utilization thresholds
	// Matches MistUtilHealth logic: CPU > 90%, RAM > 90%, SHM > 90% = degraded
	cpuPercent := float64(nodeUpdate.CpuTenths) / 10.0
	memPercent := float64(0)
	if nodeUpdate.RamMax > 0 {
		memPercent = float64(nodeUpdate.RamCurrent) / float64(nodeUpdate.RamMax) * 100
	}
	shmPercent := float64(0)
	if nodeUpdate.ShmTotalBytes > 0 {
		shmPercent = float64(nodeUpdate.ShmUsedBytes) / float64(nodeUpdate.ShmTotalBytes) * 100
	}

	// Node is healthy if: we got MistServer data AND CPU <= 90% AND RAM <= 90% AND SHM <= 90%
	hasMistData := jsonData != nil
	isHealthy := hasMistData && cpuPercent <= 90 && memPercent <= 90 && shmPercent <= 90
	nodeUpdate.IsHealthy = isHealthy

	logger.WithFields(logging.Fields{
		"node_id":       nodeID,
		"has_mist_data": hasMistData,
		"cpu_percent":   cpuPercent,
		"mem_percent":   memPercent,
		"shm_percent":   shmPercent,
		"is_healthy":    isHealthy,
	}).Info("Node health determination")

	// Populate full Streams map from MistServer data
	// This is CRITICAL for load balancing - balancer checks stream.Inputs > 0
	if jsonData != nil {
		if streams, ok := jsonData["streams"].(map[string]interface{}); ok {
			nodeUpdate.Streams = make(map[string]*pb.StreamData)
			for streamName, streamData := range streams {
				if streamInfo, ok := streamData.(map[string]interface{}); ok {
					sd := &pb.StreamData{}

					// Extract from curr array: [viewers, inputs, outgoing, unspecified, cached]
					if curr, ok := streamInfo["curr"].([]interface{}); ok {
						if len(curr) > 0 {
							if viewers, ok := curr[0].(float64); ok {
								sd.Total = uint64(viewers)
							}
						}
						if len(curr) > 1 {
							if inputs, ok := curr[1].(float64); ok {
								sd.Inputs = uint32(inputs)
							}
						}
					}

					// Extract from bw array: [bandwidth_in, bandwidth_out]
					if bw, ok := streamInfo["bw"].([]interface{}); ok && len(bw) >= 2 {
						if bandwidthIn, ok := bw[0].(float64); ok {
							sd.BytesUp = uint64(bandwidthIn)
						}
						if bandwidthOut, ok := bw[1].(float64); ok {
							sd.BytesDown = uint64(bandwidthOut)
						}
						// Calculate bandwidth per viewer (bytes/sec per viewer)
						if sd.Total > 0 && sd.BytesDown > 0 {
							sd.Bandwidth = uint32(sd.BytesDown / sd.Total)
						}
					}

					// Extract replicated status
					if rep, ok := streamInfo["rep"].(bool); ok {
						sd.Replicated = rep
					}

					// Extract packet counts from pkts array
					if pkts, ok := streamInfo["pkts"].([]interface{}); ok {
						sd.PacketCounts = make([]int64, len(pkts))
						for i, pkt := range pkts {
							if v, ok := pkt.(float64); ok {
								sd.PacketCounts[i] = int64(v)
							}
						}
					}

					// Extract total connections from tot array
					if tot, ok := streamInfo["tot"].([]interface{}); ok {
						sd.TotalConnections = make([]int64, len(tot))
						for i, t := range tot {
							if v, ok := t.(float64); ok {
								sd.TotalConnections[i] = int64(v)
							}
						}
					}

					// Use normalized internal name as key (e.g., "live+demo_stream" -> "demo_stream")
					internalName := mist.ExtractInternalName(streamName)
					nodeUpdate.Streams[internalName] = sd
				}
			}
			logger.WithFields(logging.Fields{
				"node_id":      nodeID,
				"stream_count": len(nodeUpdate.Streams),
			}).Debug("Populated streams map for NodeLifecycleUpdate")
		}
	}

	return &pb.MistTrigger{
		TriggerType: "NODE_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		RequestId:   "", // Non-blocking
		TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
			NodeLifecycleUpdate: nodeUpdate,
		},
	}
}

// getDiskUsage returns total and used bytes for the file system containing path
func getDiskUsage(path string) (total, used uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}

	// Available blocks * size per block = available space in bytes
	// Total blocks * size per block = total space in bytes
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	used = total - free
	return total, used, nil
}

// enrichNodeLifecycleTrigger enriches node lifecycle trigger with Helmsman-specific data
func enrichNodeLifecycleTrigger(mistTrigger *pb.MistTrigger, capIngest, capEdge, capStorage, capProcessing string, roles []string) {
	if nodeUpdate := mistTrigger.GetNodeLifecycleUpdate(); nodeUpdate != nil {
		// Report the authoritative mode from ConfigSeed (set by Foghorn), not the env var
		nodeUpdate.OperationalMode = sidecarcfg.GetOperationalMode()

		// Add capabilities
		nodeUpdate.Capabilities = &pb.NodeCapabilities{
			Ingest:     capIngest == "" || capIngest == "1" || strings.ToLower(capIngest) == "true",
			Edge:       capEdge == "" || capEdge == "1" || strings.ToLower(capEdge) == "true",
			Storage:    capStorage == "" || capStorage == "1" || strings.ToLower(capStorage) == "true",
			Processing: capProcessing == "" || capProcessing == "1" || strings.ToLower(capProcessing) == "true",
			Roles:      roles,
		}

		// Add storage info
		nodeUpdate.Storage = &pb.StorageInfo{
			LocalPath: os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH"),
			S3Bucket:  os.Getenv("HELMSMAN_STORAGE_S3_BUCKET"),
			S3Prefix:  os.Getenv("HELMSMAN_STORAGE_S3_PREFIX"),
		}

		// Add limits from environment
		limits := &pb.NodeLimits{}
		hasLimits := false
		if maxT, err := strconv.Atoi(os.Getenv("HELMSMAN_MAX_TRANSCODES")); err == nil && maxT > 0 {
			limits.MaxTranscodes = int32(maxT)
			hasLimits = true
		}
		if capBytes, err := strconv.ParseUint(os.Getenv("HELMSMAN_STORAGE_CAPACITY_BYTES"), 10, 64); err == nil && capBytes > 0 {
			limits.StorageCapacityBytes = capBytes
			hasLimits = true
		}
		if hasLimits {
			nodeUpdate.Limits = limits
		}

		// Add artifacts from artifactIndex
		nodeUpdate.Artifacts = GetStoredArtifacts()

		// Attach tenant_id from last ConfigSeed (provided by Foghorn)
		if t := sidecarcfg.GetTenantID(); t != "" {
			nodeUpdate.TenantId = &t
		}
	}
}

// parseOperationalMode removed (unused); see control.parseRequestedMode for mode parsing.

func rolesFromCapabilityFlags(capIngest, capEdge, capStorage, capProcessing string) []string {
	var roles []string
	if interpretCapabilityFlag(capIngest, true) {
		roles = append(roles, "ingest")
	}
	if interpretCapabilityFlag(capEdge, true) {
		roles = append(roles, "edge")
	}
	if interpretCapabilityFlag(capStorage, true) {
		roles = append(roles, "storage")
	}
	if interpretCapabilityFlag(capProcessing, true) {
		roles = append(roles, "processing")
	}
	return roles
}

func interpretCapabilityFlag(value string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

// convertStreamAPIToMistTrigger converts stream API response data to a MistTrigger protobuf
func convertStreamAPIToMistTrigger(nodeID, streamName, internalName string, streamData, healthData map[string]interface{}, trackDetails []map[string]interface{}, trackCount int, logger logging.Logger) *pb.MistTrigger {
	streamLifecycleUpdate := &pb.StreamLifecycleUpdate{
		NodeId:       nodeID,
		InternalName: internalName,
		Status:       "live",
	}

	// Extract basic metrics from stream data
	// Note: 0 is a valid value for all these metrics (e.g., stream just started, no viewers yet)
	if viewers, ok := streamData["viewers"].(float64); ok {
		totalViewers := uint32(viewers)
		streamLifecycleUpdate.TotalViewers = &totalViewers
	}
	if inputs, ok := streamData["inputs"].(float64); ok {
		totalInputs := uint32(inputs)
		streamLifecycleUpdate.TotalInputs = &totalInputs
	}
	if upbytes, ok := streamData["upbytes"].(float64); ok {
		uploadedBytes := uint64(upbytes)
		streamLifecycleUpdate.UploadedBytes = &uploadedBytes
	}
	if downbytes, ok := streamData["downbytes"].(float64); ok {
		downloadedBytes := uint64(downbytes)
		streamLifecycleUpdate.DownloadedBytes = &downloadedBytes
	}
	// Extract replicated status (pull vs push stream)
	if replicated, ok := streamData["replicated"].(bool); ok {
		streamLifecycleUpdate.Replicated = &replicated
	}

	// Add health data as stream details
	if len(healthData) > 0 {
		if healthDataBytes, err := json.Marshal(healthData); err == nil {
			streamDetails := string(healthDataBytes)
			streamLifecycleUpdate.StreamDetails = &streamDetails
		}
	}

	// Extract packet statistics from streamData (MistServer active_streams API fields)
	// Note: These are stream-level totals, NOT in the health blob
	// 0 is valid (e.g., HLS streams don't track packets at stream level)
	if packsent, ok := streamData["packsent"].(float64); ok {
		ps := uint64(packsent)
		streamLifecycleUpdate.PacketsSent = &ps
	}
	if packloss, ok := streamData["packloss"].(float64); ok {
		pl := uint64(packloss)
		streamLifecycleUpdate.PacketsLost = &pl
	}
	if packretrans, ok := streamData["packretrans"].(float64); ok {
		pr := uint64(packretrans)
		streamLifecycleUpdate.PacketsRetransmitted = &pr
	}

	// Extract viewseconds if available (cumulative viewer time)
	// 0 is valid (stream just started)
	if viewseconds, ok := streamData["viewseconds"].(float64); ok {
		vs := uint64(viewseconds)
		streamLifecycleUpdate.ViewerSeconds = &vs
	}

	// Extract top-level health blob metrics (stream-wide summary)
	// 0 is valid (perfect conditions with no buffer latency or jitter)
	if buffer, ok := healthData["buffer"].(float64); ok {
		buf := uint32(buffer)
		streamLifecycleUpdate.BufferMs = &buf
	}
	if jitter, ok := healthData["jitter"].(float64); ok {
		jit := uint32(jitter)
		streamLifecycleUpdate.JitterMs = &jit
	}
	if maxkeepaway, ok := healthData["maxkeepaway"].(float64); ok {
		mka := uint32(maxkeepaway)
		streamLifecycleUpdate.MaxKeepawayMs = &mka
	}

	// Extract quality metrics from track details
	var qualityTier string
	var primaryWidth, primaryHeight int32
	var primaryFPS float64
	var primaryBitrate int32
	var primaryCodec string
	var primaryVideoBufferMs, primaryVideoJitterMs uint32
	var foundVideo, foundAudio bool

	if len(trackDetails) > 0 {
		// Serialize full track details to JSON for storage
		if trackJSON, err := json.Marshal(trackDetails); err == nil {
			trackDetailsStr := string(trackJSON)
			streamLifecycleUpdate.TrackDetailsJson = &trackDetailsStr
		}

		for _, track := range trackDetails {
			trackType, _ := track["type"].(string)

			// Extract primary video track info
			if trackType == "video" && !foundVideo {
				foundVideo = true
				if width, ok := track["width"].(int); ok {
					primaryWidth = int32(width)
				}
				if height, ok := track["height"].(int); ok {
					primaryHeight = int32(height)
				}
				if fps, ok := track["fps"].(float64); ok {
					primaryFPS = fps
				}
				if bitrate, ok := track["bitrate_kbps"].(int); ok {
					primaryBitrate = int32(bitrate)
				}
				if codec, ok := track["codec"].(string); ok {
					primaryCodec = codec
				}
				// Per-track buffer/jitter for primary video
				// 0 is valid (perfect conditions with no buffer delay or jitter)
				if buffer, ok := track["buffer"].(int); ok {
					primaryVideoBufferMs = uint32(buffer)
					streamLifecycleUpdate.VideoBufferMs = &primaryVideoBufferMs
				}
				if jitter, ok := track["jitter"].(int); ok {
					primaryVideoJitterMs = uint32(jitter)
					streamLifecycleUpdate.VideoJitterMs = &primaryVideoJitterMs
				}
				// Build rich quality tier label: "1080p60 H264 @ 6Mbps"
				if primaryHeight > 0 {
					// Resolution tier
					var resolution string
					if primaryHeight >= 2160 {
						resolution = "2160p"
					} else if primaryHeight >= 1440 {
						resolution = "1440p"
					} else if primaryHeight >= 1080 {
						resolution = "1080p"
					} else if primaryHeight >= 720 {
						resolution = "720p"
					} else if primaryHeight >= 480 {
						resolution = "480p"
					} else {
						resolution = "SD"
					}

					// Append FPS if available
					if primaryFPS > 0 {
						resolution = fmt.Sprintf("%s%d", resolution, int(primaryFPS+0.5))
					}

					qualityTier = resolution

					// Add codec if available
					if primaryCodec != "" {
						qualityTier += " " + primaryCodec
					}

					// Add bitrate if available
					if primaryBitrate > 0 {
						if primaryBitrate >= 1000 {
							qualityTier += fmt.Sprintf(" @ %.1fMbps", float64(primaryBitrate)/1000)
						} else {
							qualityTier += fmt.Sprintf(" @ %dkbps", primaryBitrate)
						}
					}
				}
			}

			// Extract primary audio track info
			if trackType == "audio" && !foundAudio {
				foundAudio = true
				if channels, ok := track["channels"].(int); ok && channels > 0 {
					ch := uint32(channels)
					streamLifecycleUpdate.AudioChannels = &ch
				}
				if sampleRate, ok := track["sample_rate"].(int); ok && sampleRate > 0 {
					sr := uint32(sampleRate)
					streamLifecycleUpdate.AudioSampleRate = &sr
				}
				if codec, ok := track["codec"].(string); ok && codec != "" {
					streamLifecycleUpdate.AudioCodec = &codec
				}
				if bitrate, ok := track["bitrate_kbps"].(int); ok && bitrate > 0 {
					br := uint32(bitrate)
					streamLifecycleUpdate.AudioBitrate = &br
				}
			}
		}
	}

	// Start with MistServer's native issues (primary source of truth)
	// e.g., "HLSnoaudio!", "VeryLowBuffer", etc.
	hasIssues := false
	var issuesDesc []string

	if mistIssues, ok := healthData["issues"].(string); ok && mistIssues != "" {
		hasIssues = true
		issuesDesc = append(issuesDesc, mistIssues)
	}

	// Calculate packet loss ratio from streamData (already extracted above)
	var packetLossRatio float64
	if packsent, ok := streamData["packsent"].(float64); ok && packsent > 0 {
		if packloss, ok := streamData["packloss"].(float64); ok {
			packetLossRatio = packloss / packsent
		}
	}

	// Append Helmsman's derived analysis (supplementary diagnostics)
	if packetLossRatio > 0.05 {
		hasIssues = true
		issuesDesc = append(issuesDesc, fmt.Sprintf("High packet loss: %.2f%%", packetLossRatio*100))
	} else if packetLossRatio > 0.01 {
		hasIssues = true
		issuesDesc = append(issuesDesc, fmt.Sprintf("Moderate packet loss: %.2f%%", packetLossRatio*100))
	}

	for _, track := range trackDetails {
		if jitter, ok := track["jitter"].(int); ok && jitter > 100 {
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("High jitter on track %v", track["track_name"]))
		}
		if buffer, ok := track["buffer"].(int); ok && buffer < 50 {
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("Low buffer on track %v", track["track_name"]))
		}
	}

	// Set issue indicators
	streamLifecycleUpdate.HasIssues = &hasIssues
	if len(issuesDesc) > 0 {
		issues := strings.Join(issuesDesc, "; ")
		streamLifecycleUpdate.IssuesDescription = &issues
	}
	if qualityTier != "" {
		streamLifecycleUpdate.QualityTier = &qualityTier
	}
	if primaryWidth > 0 {
		streamLifecycleUpdate.PrimaryWidth = &primaryWidth
	}
	if primaryHeight > 0 {
		streamLifecycleUpdate.PrimaryHeight = &primaryHeight
	}
	if primaryFPS > 0 {
		primaryFPSFloat32 := float32(primaryFPS)
		streamLifecycleUpdate.PrimaryFps = &primaryFPSFloat32
	}
	if primaryBitrate > 0 {
		streamLifecycleUpdate.PrimaryBitrate = &primaryBitrate
	}
	if primaryCodec != "" {
		streamLifecycleUpdate.PrimaryCodec = &primaryCodec
	}
	if trackCount > 0 {
		count := int32(trackCount)
		streamLifecycleUpdate.TrackCount = &count
	}

	return &pb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		RequestId:   "",
		TriggerPayload: &pb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: streamLifecycleUpdate,
		},
	}
}

// convertClientAPIToMistTrigger converts client API response data to a MistTrigger protobuf
func convertClientAPIToMistTrigger(nodeID, streamName, internalName, protocol, host, sessionID string, connectionTime, position float64, bandwidthIn, bandwidthOut float64, bytesDown, bytesUp, packetsSent, packetsLost, packetsRetransmitted int64, logger logging.Logger) *pb.MistTrigger {
	clientLifecycleUpdate := &pb.ClientLifecycleUpdate{
		NodeId:       nodeID,
		InternalName: internalName,
		Action:       "connect",
		Protocol:     protocol,
		Host:         host,
	}

	// Add optional fields if present
	if sessionID != "" {
		clientLifecycleUpdate.SessionId = &sessionID
	}
	// Note: 0 is valid for all these metrics (e.g., client just connected)
	connectionTimeFloat32 := float32(connectionTime)
	clientLifecycleUpdate.ConnectionTime = &connectionTimeFloat32
	positionFloat32 := float32(position)
	clientLifecycleUpdate.Position = &positionFloat32
	bandwidthInUint64 := uint64(bandwidthIn)
	clientLifecycleUpdate.BandwidthInBps = &bandwidthInUint64
	bandwidthOutUint64 := uint64(bandwidthOut)
	clientLifecycleUpdate.BandwidthOutBps = &bandwidthOutUint64
	bytesDownUint64 := uint64(bytesDown)
	clientLifecycleUpdate.BytesDownloaded = &bytesDownUint64
	bytesUpUint64 := uint64(bytesUp)
	clientLifecycleUpdate.BytesUploaded = &bytesUpUint64
	// Always set packet stats - 0 is a valid value (e.g., HLS doesn't track packets)
	// These fields are explicitly requested from MistServer, so we always have them
	packetsSentUint64 := uint64(packetsSent)
	clientLifecycleUpdate.PacketsSent = &packetsSentUint64
	packetsLostUint64 := uint64(packetsLost)
	clientLifecycleUpdate.PacketsLost = &packetsLostUint64
	packetsRetransmittedUint64 := uint64(packetsRetransmitted)
	clientLifecycleUpdate.PacketsRetransmitted = &packetsRetransmittedUint64

	return &pb.MistTrigger{
		TriggerType: "CLIENT_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		RequestId:   "",
		TriggerPayload: &pb.MistTrigger_ClientLifecycleUpdate{
			ClientLifecycleUpdate: clientLifecycleUpdate,
		},
	}
}
