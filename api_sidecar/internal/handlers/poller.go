package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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
	FilePath   string
	StreamName string
	Format     string
	SizeBytes  uint64
	CreatedAt  time.Time
	S3URL      string
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
	nodeID       string
	baseURL      string
	latitude     *float64
	longitude    *float64
	location     string
	lastSeen     time.Time
	isHealthy    bool
	lastJSONData map[string]interface{} // Store last fetched JSON data
	// Artifact index for fast VOD lookups
	artifactIndex    map[string]*ClipInfo // clipHash -> ClipInfo
	lastArtifactScan time.Time

	// Shared Mist API client
	mistClient *mist.Client
	mistMu     sync.Mutex
}

var prometheusMonitor *PrometheusMonitor
var monitorLogger logging.Logger

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
func (pm *PrometheusMonitor) AddNode(nodeID, baseURL string) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	pm.nodeID = nodeID
	pm.baseURL = baseURL
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
	pm.mistClient.BaseURL = baseURL

	monitorLogger.WithFields(logging.Fields{
		"node_id":                nodeID,
		"base_url":               baseURL,
		"prometheus_monitor_ptr": fmt.Sprintf("%p", pm),
	}).Info("Added MistServer node for monitoring")

	// Immediately verify the nodeID was set
	monitorLogger.WithFields(logging.Fields{
		"stored_node_id": pm.nodeID,
		"is_empty":       pm.nodeID == "",
	}).Info("PrometheusMonitor nodeID verification after AddNode")
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
				go pm.emitClientLifecycle(nodeID, baseURL)
			} else {
				pm.mutex.RUnlock()
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
		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
		}).Info("=== RAW HEALTH DATA FOR STREAM ===")
		healthJSON, _ := json.MarshalIndent(health, "", "  ")
		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
			"body":        string(healthJSON),
		}).Info("=== END RAW HEALTH DATA ===")

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
					}).Info("Parsed track")
				}
			}
		}

		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
			"count":       len(trackDetails),
		}).WithField("count", len(trackDetails)).Info("Extracted tracks from health data")
	} else {
		monitorLogger.WithFields(logging.Fields{
			"node_id":     nodeID,
			"stream_name": streamName,
		}).WithField("stream_name", streamName).Warn("No health data found for stream")
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
	mistTrigger := convertStreamAPIToMistTrigger(nodeID, streamName, internalName, streamData, healthData, trackDetails, monitorLogger)

	// Send
	if _, _, err := control.SendMistTrigger(mistTrigger, monitorLogger); err != nil {
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
	mistTrigger := convertNodeAPIToMistTrigger(nodeID, pm.getLastJSONData(), monitorLogger)

	// Enrich with Helmsman-specific capabilities, storage, limits
	enrichNodeLifecycleTrigger(mistTrigger, capIngest, capEdge, capStorage, capProcessing, roles)

	// Send
	if _, _, err := control.SendMistTrigger(mistTrigger, monitorLogger); err != nil {
		monitorLogger.WithError(err).Error("Failed to send node lifecycle update via gRPC")
		return
	}
	monitorLogger.WithField("node_id", nodeID).Debug("Successfully sent node lifecycle update to Foghorn via gRPC")
}

// extractStreamMetrics extracts per-stream metrics from MistServer JSON data
func (pm *PrometheusMonitor) extractStreamMetrics(jsonData map[string]interface{}) map[string]map[string]interface{} {
	streamMetrics := make(map[string]map[string]interface{})

	// Extract from JSON data (actual MistServer format)
	if jsonData != nil {
		if streams, ok := jsonData["streams"].(map[string]interface{}); ok {
			for streamName, streamData := range streams {
				if streamInfo, ok := streamData.(map[string]interface{}); ok {
					metrics := make(map[string]interface{})

					// Basic stream info
					metrics["stream_name"] = streamName
					metrics["source"] = "json"

					// Extract viewer count from curr[0] (current viewers)
					if curr, ok := streamInfo["curr"].([]interface{}); ok && len(curr) > 0 {
						if viewers, ok := curr[0].(float64); ok {
							metrics["viewers"] = int(viewers)
						} else {
							metrics["viewers"] = 0
						}
					} else {
						metrics["viewers"] = 0
					}

					// Extract bandwidth from bw[0] (in) and bw[1] (out)
					if bw, ok := streamInfo["bw"].([]interface{}); ok && len(bw) >= 2 {
						if bandwidthIn, ok := bw[0].(float64); ok {
							metrics["bandwidth_in"] = int64(bandwidthIn)
						}
						if bandwidthOut, ok := bw[1].(float64); ok {
							metrics["bandwidth_out"] = int64(bandwidthOut)
						}
					}

					// Extract packet counts from pkts array
					if pkts, ok := streamInfo["pkts"].([]interface{}); ok && len(pkts) >= 3 {
						metrics["packet_count"] = pkts
					}

					// Extract total counts from tot array
					if tot, ok := streamInfo["tot"].([]interface{}); ok && len(tot) >= 3 {
						metrics["total_connections"] = tot
					}

					// Check replication status (CRITICAL: matches C++ parsing)
					if rep, ok := streamInfo["rep"].(bool); ok {
						metrics["replicated"] = rep
					} else {
						metrics["replicated"] = false
					}

					// Extract input count from curr[1] (matches C++ parsing)
					if curr, ok := streamInfo["curr"].([]interface{}); ok && len(curr) > 1 {
						if inputs, ok := curr[1].(float64); ok {
							metrics["inputs"] = int(inputs)
						} else {
							metrics["inputs"] = 0
						}
					} else {
						metrics["inputs"] = 0
					}

					// Determine stream status based on available data
					// Only set to live if we have clear indicators of activity
					if viewers, ok := metrics["viewers"].(int); ok && viewers > 0 {
						metrics["status"] = "live"
					} else if tot, ok := streamInfo["tot"].([]interface{}); ok && len(tot) > 1 {
						// Check if there have been any connections (tot[1] > 0)
						if totalConnections, ok := tot[1].(float64); ok && totalConnections > 0 {
							metrics["status"] = "live"
						} else {
							metrics["status"] = "unknown"
						}
					} else {
						metrics["status"] = "unknown"
					}

					streamMetrics[streamName] = metrics
				}
			}
		}
	}

	return streamMetrics
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
		NodeID    string   `json:"node_id" binding:"required"`
		BaseURL   string   `json:"base_url" binding:"required"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
		Location  string   `json:"location"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	prometheusMonitor.AddNode(request.NodeID, request.BaseURL)

	c.JSON(http.StatusOK, gin.H{"message": "Node added successfully"})
}

// AddPrometheusNodeDirect adds a node directly to the monitor (not via HTTP)
func AddPrometheusNodeDirect(nodeID, baseURL string) {
	if prometheusMonitor != nil {
		prometheusMonitor.AddNode(nodeID, baseURL)
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

// getFloat64Value safely dereferences a *float64, returning 0 if nil
func getFloat64Value(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// getFloat64PointerValue safely dereferences a *float64, returning 0 if nil (for embedded structs)
func getFloat64PointerValue(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// contains checks if a string slice contains a specific value
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
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
				if _, _, err := control.SendMistTrigger(mistTrigger, monitorLogger); err != nil {
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

// Helper functions to get metrics from MistServer data
func (pm *PrometheusMonitor) getCPUUsage() float64 {
	// Get raw CPU usage from MistServer (tenths of percentage)
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if cpu, ok := jsonData["cpu"].(float64); ok {
			return cpu // Return raw value from MistServer (tenths of percentage)
		}
	}
	return 1000.0 // Default to 100% (1000 tenths) if unavailable
}

func (pm *PrometheusMonitor) getRAMMax() uint64 {
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if ram, ok := jsonData["ram"].(map[string]interface{}); ok {
			if max, ok := ram["max"].(float64); ok {
				return uint64(max)
			}
		}
	}
	return 8 * 1024 * 1024 * 1024 // Default to 8GB like C++
}

func (pm *PrometheusMonitor) getRAMCurrent() uint64 {
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if ram, ok := jsonData["ram"].(map[string]interface{}); ok {
			if current, ok := ram["current"].(float64); ok {
				return uint64(current)
			}
		}
	}
	return 4 * 1024 * 1024 * 1024 // Default to 4GB
}

func (pm *PrometheusMonitor) getUpSpeed() uint64 {
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if bw, ok := jsonData["bandwidth"].(map[string]interface{}); ok {
			if up, ok := bw["up"].(float64); ok {
				return uint64(up)
			}
		}
	}
	return 0
}

func (pm *PrometheusMonitor) getDownSpeed() uint64 {
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if bw, ok := jsonData["bandwidth"].(map[string]interface{}); ok {
			if down, ok := bw["down"].(float64); ok {
				return uint64(down)
			}
		}
	}
	return 0
}

func (pm *PrometheusMonitor) getBandwidthLimit() uint64 {
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if bw, ok := jsonData["bandwidth"].(map[string]interface{}); ok {
			if limit, ok := bw["limit"].(float64); ok {
				return uint64(limit)
			}
		}
	}
	return 128 * 1024 * 1024 // Default to 1Gbps like C++
}

func (pm *PrometheusMonitor) getStreamMetrics() map[string]interface{} {
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if streams, ok := jsonData["streams"].(map[string]interface{}); ok {
			streamMetrics := make(map[string]interface{})
			for streamName, streamData := range streams {
				if streamInfo, ok := streamData.(map[string]interface{}); ok {
					metrics := make(map[string]interface{})

					// Extract viewer count (matches C++ curr[0])
					if curr, ok := streamInfo["curr"].([]interface{}); ok && len(curr) > 0 {
						if viewers, ok := curr[0].(float64); ok {
							metrics["total"] = uint64(viewers)
						}
					}

					// Extract input count (matches C++ curr[1])
					if curr, ok := streamInfo["curr"].([]interface{}); ok && len(curr) > 1 {
						if inputs, ok := curr[1].(float64); ok {
							metrics["inputs"] = uint32(inputs)
						}
					}

					// Extract bandwidth (matches C++ bw[0] and bw[1])
					if bw, ok := streamInfo["bw"].([]interface{}); ok && len(bw) >= 2 {
						if bandwidthUp, ok := bw[0].(float64); ok {
							metrics["bytes_up"] = uint64(bandwidthUp)
						}
						if bandwidthDown, ok := bw[1].(float64); ok {
							metrics["bytes_down"] = uint64(bandwidthDown)
						}
					}

					// Check replication status (CRITICAL: matches C++ strm.rep parsing)
					if rep, ok := streamInfo["rep"].(bool); ok && rep {
						metrics["replicated"] = true
					}

					// Calculate approximate bandwidth per viewer (like C++)
					if total, okTotal := metrics["total"].(uint64); okTotal && total > 0 {
						if bytesUp, okUp := metrics["bytes_up"].(uint64); okUp {
							if bytesDown, okDown := metrics["bytes_down"].(uint64); okDown {
								metrics["bandwidth"] = uint32((bytesUp + bytesDown) / total)
							}
						}
					} else {
						metrics["bandwidth"] = uint32(131072) // Default 1mbps like C++
					}

					streamMetrics[streamName] = metrics
				}
			}
			return streamMetrics
		}
	}
	return make(map[string]interface{})
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

// getOutputsConfiguration extracts MistServer outputs configuration from JSON data
func (pm *PrometheusMonitor) getOutputsConfiguration() string {
	pm.mutex.RLock()
	jsonData := pm.lastJSONData
	pm.mutex.RUnlock()

	if jsonData == nil {
		return ""
	}

	// Extract outputs directly from top-level JSON
	if outputs, ok := jsonData["outputs"]; ok {
		if outputsJSON, err := json.Marshal(outputs); err == nil {
			return string(outputsJSON)
		}
	}

	return ""
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

	formats := []string{"mp4", "webm", "mkv"}

	for _, entry := range entries {
		if !entry.IsDir() {
			// Check if this is a direct VOD link (clips/abc123def456.mp4)
			if len(entry.Name()) > 4 {
				for _, format := range formats {
					if strings.HasSuffix(entry.Name(), "."+format) {
						clipHash := strings.TrimSuffix(entry.Name(), "."+format)
						if len(clipHash) == 32 { // Valid clip hash length
							filePath := fmt.Sprintf("%s/%s", clipsDir, entry.Name())

							// Get file info
							if fileInfo, err := os.Stat(filePath); err == nil {
								// Try to determine stream name from symlink target
								streamName := "unknown"
								if target, err := os.Readlink(filePath); err == nil {
									parts := strings.Split(target, "/")
									for i, part := range parts {
										if part == "clips" && i+1 < len(parts) {
											streamName = parts[i+1]
											break
										}
									}
								}

								clipInfo := &ClipInfo{
									FilePath:   filePath,
									StreamName: streamName,
									Format:     format,
									SizeBytes:  uint64(fileInfo.Size()),
									CreatedAt:  fileInfo.ModTime(),
								}

								artifactIndex[clipHash] = clipInfo
								totalSize += uint64(fileInfo.Size())
								artifactCount++
							}
						}
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
			for _, format := range formats {
				if strings.HasSuffix(clipFile.Name(), "."+format) {
					clipHash := strings.TrimSuffix(clipFile.Name(), "."+format)
					if len(clipHash) == 32 { // Valid clip hash length
						filePath := fmt.Sprintf("%s/%s", streamDir, clipFile.Name())

						// Get file info
						if fileInfo, err := os.Stat(filePath); err == nil {
							clipInfo := &ClipInfo{
								FilePath:   filePath,
								StreamName: streamName,
								Format:     format,
								SizeBytes:  uint64(fileInfo.Size()),
								CreatedAt:  fileInfo.ModTime(),
							}

							artifactIndex[clipHash] = clipInfo
							totalSize += uint64(fileInfo.Size())
							artifactCount++
						}
					}
					break
				}
			}
		}
	}

	return totalSize, artifactCount
}

// scanDVRDirectory scans the DVR directory for DVR manifest files
func scanDVRDirectory(dvrDir string, artifactIndex map[string]*ClipInfo) (uint64, int) {
	// Check if DVR directory exists
	if _, err := os.Stat(dvrDir); os.IsNotExist(err) {
		return 0, 0
	}

	var totalSize uint64
	artifactCount := 0

	// Walk the DVR directory structure: /dvr/{internal_name}/{dvr_hash}.m3u8
	entries, err := os.ReadDir(dvrDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read DVR directory")
		return 0, 0
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue // Skip files in the DVR root directory
		}

		// This is a stream directory - scan for DVR manifests
		internalName := entry.Name()
		streamDVRDir := fmt.Sprintf("%s/%s", dvrDir, internalName)

		streamEntries, err := os.ReadDir(streamDVRDir)
		if err != nil {
			continue
		}

		for _, dvrFile := range streamEntries {
			if dvrFile.IsDir() {
				continue // Skip subdirectories (like segments/)
			}

			// Check if this looks like a DVR manifest file (hash.m3u8)
			if strings.HasSuffix(dvrFile.Name(), ".m3u8") {
				dvrHash := strings.TrimSuffix(dvrFile.Name(), ".m3u8")
				if len(dvrHash) == 32 { // Valid DVR hash length (32-char hex)
					filePath := fmt.Sprintf("%s/%s", streamDVRDir, dvrFile.Name())

					// Get file info
					if fileInfo, err := os.Stat(filePath); err == nil {
						// Add DVR manifest to artifact index using same ClipInfo structure
						// (DVR manifests can be served as VOD just like clips)
						dvrInfo := &ClipInfo{
							FilePath:   filePath,
							StreamName: internalName,
							Format:     "m3u8", // HLS manifest format
							SizeBytes:  uint64(fileInfo.Size()),
							CreatedAt:  fileInfo.ModTime(),
						}

						artifactIndex[dvrHash] = dvrInfo
						totalSize += uint64(fileInfo.Size())
						artifactCount++
					}
				}
			}
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
		}

		// Add S3 URL if available
		if clipInfo.S3URL != "" {
			artifact.S3Url = clipInfo.S3URL
		}

		artifacts = append(artifacts, artifact)
	}

	return artifacts
}

// convertNodeAPIToMistTrigger converts MistServer JSON API response to MistTrigger
func convertNodeAPIToMistTrigger(nodeID string, jsonData map[string]interface{}, logger logging.Logger) *pb.MistTrigger {
	nodeUpdate := &pb.NodeLifecycleUpdate{
		NodeId:    nodeID,
		EventType: "node_lifecycle_update",
		Timestamp: time.Now().Unix(),
	}

	if jsonData != nil {
		// Extract CPU usage (tenths of percentage)
		if cpu, ok := jsonData["cpu"].(float64); ok {
			nodeUpdate.CpuTenths = uint32(cpu * 10)
		} else {
			nodeUpdate.CpuTenths = 1000 // Default to 100%
		}

		// Extract RAM info
		if ram, ok := jsonData["ram"].(map[string]interface{}); ok {
			if max, ok := ram["max"].(float64); ok {
				nodeUpdate.RamMax = uint64(max)
			} else {
				nodeUpdate.RamMax = 8 * 1024 * 1024 * 1024 // Default 8GB
			}
			if current, ok := ram["current"].(float64); ok {
				nodeUpdate.RamCurrent = uint64(current)
			} else {
				nodeUpdate.RamCurrent = 4 * 1024 * 1024 * 1024 // Default 4GB
			}
		}

		// Extract bandwidth info
		if bw, ok := jsonData["bandwidth"].(map[string]interface{}); ok {
			if up, ok := bw["up"].(float64); ok {
				nodeUpdate.UpSpeed = uint64(up)
			}
			if down, ok := bw["down"].(float64); ok {
				nodeUpdate.DownSpeed = uint64(down)
			}
			if limit, ok := bw["limit"].(float64); ok {
				nodeUpdate.BwLimit = uint64(limit)
			} else {
				nodeUpdate.BwLimit = 128 * 1024 * 1024 // Default 1Gbps
			}
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

// enrichNodeLifecycleTrigger enriches node lifecycle trigger with Helmsman-specific data
func enrichNodeLifecycleTrigger(mistTrigger *pb.MistTrigger, capIngest, capEdge, capStorage, capProcessing string, roles []string) {
	if nodeUpdate := mistTrigger.GetNodeLifecycleUpdate(); nodeUpdate != nil {
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
func convertStreamAPIToMistTrigger(nodeID, streamName, internalName string, streamData, healthData map[string]interface{}, trackDetails []map[string]interface{}, logger logging.Logger) *pb.MistTrigger {
	streamLifecycleUpdate := &pb.StreamLifecycleUpdate{
		NodeId:       nodeID,
		InternalName: internalName,
		Status:       "live",
	}

	// Extract basic metrics from stream data
	if viewers, ok := streamData["viewers"].(float64); ok && viewers > 0 {
		totalViewers := uint32(viewers)
		streamLifecycleUpdate.TotalViewers = &totalViewers
	}
	if inputs, ok := streamData["inputs"].(float64); ok && inputs > 0 {
		totalInputs := uint32(inputs)
		streamLifecycleUpdate.TotalInputs = &totalInputs
	}
	if upbytes, ok := streamData["upbytes"].(float64); ok && upbytes > 0 {
		uploadedBytes := uint64(upbytes)
		streamLifecycleUpdate.UploadedBytes = &uploadedBytes
	}
	if downbytes, ok := streamData["downbytes"].(float64); ok && downbytes > 0 {
		downloadedBytes := uint64(downbytes)
		streamLifecycleUpdate.DownloadedBytes = &downloadedBytes
	}

	// Add health data as stream details
	if len(healthData) > 0 {
		if healthDataBytes, err := json.Marshal(healthData); err == nil {
			streamDetails := string(healthDataBytes)
			streamLifecycleUpdate.StreamDetails = &streamDetails
		}
	}

	// Extract quality metrics from track details
	var qualityTier string
	var primaryWidth, primaryHeight int32
	var primaryFPS float64

	if len(trackDetails) > 0 {
		for _, track := range trackDetails {
			if trackType, ok := track["type"].(string); ok && trackType == "video" {
				if width, ok := track["width"].(int); ok {
					primaryWidth = int32(width)
				}
				if height, ok := track["height"].(int); ok {
					primaryHeight = int32(height)
				}
				if fps, ok := track["fps"].(float64); ok {
					primaryFPS = fps
				}
				// Determine quality tier based on resolution
				if primaryWidth > 0 && primaryHeight > 0 {
					if primaryHeight >= 2160 {
						qualityTier = "4K"
					} else if primaryHeight >= 1080 {
						qualityTier = "1080p"
					} else if primaryHeight >= 720 {
						qualityTier = "720p"
					} else if primaryHeight >= 480 {
						qualityTier = "480p"
					} else {
						qualityTier = "SD"
					}
				}
				break
			}
		}
	}

	// Calculate health score based on track quality
	healthScore := 100.0
	hasIssues := false
	var issuesDesc []string

	// Calculate packet loss if available
	var packetLossRatio float64
	if healthPacketsSent, ok := healthData["packets_sent"].(float64); ok && healthPacketsSent > 0 {
		if healthPacketsLost, ok := healthData["packets_lost"].(float64); ok {
			packetLossRatio = healthPacketsLost / healthPacketsSent
		}
	}

	// Factor in packet loss
	if packetLossRatio > 0.05 {
		healthScore -= 30
		hasIssues = true
		issuesDesc = append(issuesDesc, fmt.Sprintf("High packet loss: %.2f%%", packetLossRatio*100))
	} else if packetLossRatio > 0.01 {
		healthScore -= 10
		hasIssues = true
		issuesDesc = append(issuesDesc, fmt.Sprintf("Moderate packet loss: %.2f%%", packetLossRatio*100))
	}

	for _, track := range trackDetails {
		if jitter, ok := track["jitter"].(int); ok && jitter > 100 {
			healthScore -= 20
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("High jitter on track %v", track["track_name"]))
		}
		if buffer, ok := track["buffer"].(int); ok && buffer < 50 {
			healthScore -= 15
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("Low buffer on track %v", track["track_name"]))
		}
	}

	if healthScore < 0 {
		healthScore = 0
	}

	// Set quality and health fields
	if healthScore > 0 {
		healthScoreFloat32 := float32(healthScore)
		streamLifecycleUpdate.HealthScore = &healthScoreFloat32
	}
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
	if connectionTime > 0 {
		connectionTimeFloat32 := float32(connectionTime)
		clientLifecycleUpdate.ConnectionTime = &connectionTimeFloat32
	}
	if position > 0 {
		positionFloat32 := float32(position)
		clientLifecycleUpdate.Position = &positionFloat32
	}
	if bandwidthIn > 0 {
		bandwidthInUint64 := uint64(bandwidthIn)
		clientLifecycleUpdate.BandwidthInBps = &bandwidthInUint64
	}
	if bandwidthOut > 0 {
		bandwidthOutUint64 := uint64(bandwidthOut)
		clientLifecycleUpdate.BandwidthOutBps = &bandwidthOutUint64
	}
	if bytesDown > 0 {
		bytesDownUint64 := uint64(bytesDown)
		clientLifecycleUpdate.BytesDownloaded = &bytesDownUint64
	}
	if bytesUp > 0 {
		bytesUpUint64 := uint64(bytesUp)
		clientLifecycleUpdate.BytesUploaded = &bytesUpUint64
	}
	if packetsSent > 0 {
		packetsSentUint64 := uint64(packetsSent)
		clientLifecycleUpdate.PacketsSent = &packetsSentUint64
	}
	if packetsLost > 0 {
		packetsLostUint64 := uint64(packetsLost)
		clientLifecycleUpdate.PacketsLost = &packetsLostUint64
	}
	if packetsRetransmitted > 0 {
		packetsRetransmittedUint64 := uint64(packetsRetransmitted)
		clientLifecycleUpdate.PacketsRetransmitted = &packetsRetransmittedUint64
	}

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
