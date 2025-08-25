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

	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/mist"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"
	"frameworks/pkg/validation"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ClipInfo represents local clip metadata for VOD serving
type ClipInfo struct {
	FilePath   string
	StreamName string
	Format     string
	SizeBytes  uint64
	CreatedAt  int64
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

	// Get packet statistics for health calculation
	var packetsSent, packetsLost int64
	if ps, ok := streamData["packsent"].(float64); ok {
		packetsSent = int64(ps)
	}
	if pl, ok := streamData["packloss"].(float64); ok {
		packetsLost = int64(pl)
	}

	// Calculate packet loss ratio for health scoring
	packetLossRatio := 0.0
	if packetsSent > 0 {
		packetLossRatio = float64(packetsLost) / float64(packetsSent)
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

	// Forward essential stream state to Data API (for business logic)
	go forwardEventToCommodore("stream-status", map[string]interface{}{
		"node_id":       nodeID,
		"internal_name": internalName,
		"status":        "live",
		"event_type":    "stream_state_update",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_api",
	})

	// Create typed StreamLifecyclePayload from polling data
	streamPayload := &validation.StreamLifecyclePayload{
		StreamName:   streamName,
		InternalName: internalName,
		NodeID:       nodeID,
		TenantID:     getTenantForInternalName(internalName),
		Status:       "live",
		// Note: This is monitoring polling, not a webhook, so no BufferState

		// Bandwidth metrics (in bytes)
		UploadedBytes:   upbytes,
		DownloadedBytes: downbytes,

		// Stream metadata
		TotalViewers: viewers,
		TotalInputs:  inputs,
		TotalOutputs: outputs,
		TrackCount:   trackCount,
	}

	// Extract quality metrics from parsed track details
	if len(trackDetails) > 0 {
		// Convert health data to JSON for StreamDetails field
		if healthDataBytes, err := json.Marshal(healthData); err == nil {
			streamPayload.StreamDetails = string(healthDataBytes)
		}

		// Find primary video track and extract quality metrics
		for _, track := range trackDetails {
			if trackType, ok := track["type"].(string); ok && trackType == "video" {
				// Extract video resolution
				if width, ok := track["width"].(int); ok {
					streamPayload.PrimaryWidth = width
				}
				if height, ok := track["height"].(int); ok {
					streamPayload.PrimaryHeight = height
				}
				// Extract video frame rate
				if fps, ok := track["fps"].(float64); ok {
					streamPayload.PrimaryFPS = fps
				}
				// Determine quality tier based on resolution
				if streamPayload.PrimaryWidth > 0 && streamPayload.PrimaryHeight > 0 {
					if streamPayload.PrimaryHeight >= 2160 {
						streamPayload.QualityTier = "4K"
					} else if streamPayload.PrimaryHeight >= 1080 {
						streamPayload.QualityTier = "1080p"
					} else if streamPayload.PrimaryHeight >= 720 {
						streamPayload.QualityTier = "720p"
					} else if streamPayload.PrimaryHeight >= 480 {
						streamPayload.QualityTier = "480p"
					} else {
						streamPayload.QualityTier = "SD"
					}
				}
				// Use first video track as primary
				break
			}
		}

		// Calculate health score based on track quality and packet loss
		healthScore := 100.0
		hasIssues := false
		var issuesDesc []string

		// Factor in packet loss
		if packetLossRatio > 0.05 { // >5% packet loss is concerning
			healthScore -= 30
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("High packet loss: %.2f%%", packetLossRatio*100))
		} else if packetLossRatio > 0.01 { // >1% packet loss is noticeable
			healthScore -= 10
			hasIssues = true
			issuesDesc = append(issuesDesc, fmt.Sprintf("Moderate packet loss: %.2f%%", packetLossRatio*100))
		}

		for _, track := range trackDetails {
			// Check for high jitter (quality issue)
			if jitter, ok := track["jitter"].(int); ok && jitter > 100 {
				healthScore -= 20
				hasIssues = true
				issuesDesc = append(issuesDesc, fmt.Sprintf("High jitter on track %v", track["track_name"]))
			}
			// Check for low buffer (quality issue)
			if buffer, ok := track["buffer"].(int); ok && buffer < 50 {
				healthScore -= 15
				hasIssues = true
				issuesDesc = append(issuesDesc, fmt.Sprintf("Low buffer on track %v", track["track_name"]))
			}
		}

		// Ensure health score doesn't go below 0
		if healthScore < 0 {
			healthScore = 0
		}

		streamPayload.HealthScore = healthScore
		streamPayload.HasIssues = hasIssues
		if len(issuesDesc) > 0 {
			streamPayload.IssuesDesc = strings.Join(issuesDesc, "; ")
		}
		// Log final quality metrics
		monitorLogger.WithFields(logging.Fields{
			"node_id":      nodeID,
			"stream_name":  streamName,
			"quality_tier": streamPayload.QualityTier,
			"health_score": streamPayload.HealthScore,
			"has_issues":   streamPayload.HasIssues,
			"primary_res":  fmt.Sprintf("%dx%d", streamPayload.PrimaryWidth, streamPayload.PrimaryHeight),
			"primary_fps":  streamPayload.PrimaryFPS,
		}).Info("Stream quality metrics extracted")
	}

	// Create typed BaseEvent for monitoring data
	baseEvent := &validation.BaseEvent{
		EventID:         uuid.New().String(),
		EventType:       validation.EventStreamLifecycle,
		Timestamp:       time.Now().UTC(),
		Source:          "mistserver_api",
		InternalName:    &internalName,
		SchemaVersion:   "2.0",
		StreamLifecycle: streamPayload,
	}

	// Forward comprehensive analytics data to Decklog (for analytics)
	go ForwardTypedEventToDecklog(baseEvent)
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
	pm.mutex.RLock()
	baseURL := pm.baseURL
	latitude := pm.latitude
	longitude := pm.longitude
	location := pm.location
	isHealthy := pm.isHealthy
	pm.mutex.RUnlock()

	// Convert stream metrics to typed format
	streamsData := pm.getStreamMetrics()
	typedStreams := make(map[string]validation.FoghornStreamData)
	for streamName, streamMetrics := range streamsData {
		if metrics, ok := streamMetrics.(map[string]interface{}); ok {
			streamData := validation.FoghornStreamData{}

			if total, ok := metrics["total"].(uint64); ok {
				streamData.Total = total
			}
			if inputs, ok := metrics["inputs"].(uint32); ok {
				streamData.Inputs = inputs
			}
			if bytesUp, ok := metrics["bytes_up"].(uint64); ok {
				streamData.BytesUp = bytesUp
				// Add bandwidth calculation per viewer (like C++)
				if streamData.Total > 0 {
					streamData.Bandwidth = uint32((streamData.BytesUp + streamData.BytesDown) / streamData.Total)
				}
			}
			if bytesDown, ok := metrics["bytes_down"].(uint64); ok {
				streamData.BytesDown = bytesDown
				// Update bandwidth calculation per viewer (like C++)
				if streamData.Total > 0 {
					streamData.Bandwidth = uint32((streamData.BytesUp + streamData.BytesDown) / streamData.Total)
				}
			}

			typedStreams[streamName] = streamData
		}
	}

	// Create typed node metrics for Foghorn
	nodeMetrics := &validation.FoghornNodeUpdate{
		CPU:        pm.getCPUUsage(),                // Raw CPU (tenths of percentage)
		RAMMax:     float64(pm.getRAMMax()),         // Raw RAM max (MiB)
		RAMCurrent: float64(pm.getRAMCurrent()),     // Raw RAM current (MiB)
		UpSpeed:    float64(pm.getUpSpeed()),        // Raw upload speed (bytes/sec)
		DownSpeed:  float64(pm.getDownSpeed()),      // Raw download speed (bytes/sec)
		BWLimit:    float64(pm.getBandwidthLimit()), // Raw bandwidth limit (bytes/sec)
		Location: validation.FoghornLocationData{
			Latitude:  getFloat64Value(latitude),
			Longitude: getFloat64Value(longitude),
			Name:      location,
		},
		Streams: typedStreams,
	}

	// Capabilities from environment (fallback defaults: all true in dev)
	rolesCSV := os.Getenv("HELMSMAN_ROLES") // e.g. "ingest,edge,storage,processing"
	var roles []string
	for _, r := range strings.Split(rolesCSV, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			roles = append(roles, r)
		}
	}
	capIngest := os.Getenv("HELMSMAN_CAP_INGEST")
	capEdge := os.Getenv("HELMSMAN_CAP_EDGE")
	capStorage := os.Getenv("HELMSMAN_CAP_STORAGE")
	capProcessing := os.Getenv("HELMSMAN_CAP_PROCESSING")

	cap := validation.FoghornNodeCapabilities{
		Ingest:     capIngest == "" || capIngest == "1" || strings.ToLower(capIngest) == "true",
		Edge:       capEdge == "" || capEdge == "1" || strings.ToLower(capEdge) == "true",
		Storage:    capStorage == "" || capStorage == "1" || strings.ToLower(capStorage) == "true",
		Processing: capProcessing == "" || capProcessing == "1" || strings.ToLower(capProcessing) == "true",
		Roles:      roles,
	}
	nodeMetrics.Capabilities = cap

	// Storage info from env
	nodeMetrics.Storage = validation.FoghornStorageInfo{
		LocalPath: os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH"),
		S3Bucket:  os.Getenv("HELMSMAN_STORAGE_S3_BUCKET"),
		S3Prefix:  os.Getenv("HELMSMAN_STORAGE_S3_PREFIX"),
	}

	// Limits from env (optional)
	if maxT, err := strconv.Atoi(os.Getenv("HELMSMAN_MAX_TRANSCODES")); err == nil && maxT > 0 {
		if nodeMetrics.Limits == nil {
			nodeMetrics.Limits = &validation.FoghornNodeLimits{}
		}
		nodeMetrics.Limits.MaxTranscodes = maxT
	}
	if capBytes, err := strconv.ParseUint(os.Getenv("HELMSMAN_STORAGE_CAPACITY_BYTES"), 10, 64); err == nil && capBytes > 0 {
		if nodeMetrics.Limits == nil {
			nodeMetrics.Limits = &validation.FoghornNodeLimits{}
		}
		nodeMetrics.Limits.StorageCapacityBytes = capBytes
	}

	// Storage used & artifacts from local scan (best-effort)
	if nodeMetrics.Storage.LocalPath != "" {
		used, arts := scanLocalArtifacts(nodeMetrics.Storage.LocalPath)
		if nodeMetrics.Limits == nil {
			nodeMetrics.Limits = &validation.FoghornNodeLimits{}
		}
		nodeMetrics.Limits.StorageUsedBytes = used
		nodeMetrics.Artifacts = arts
	}

	// Extract MistServer outputs configuration from JSON data
	outputsJSON := pm.getOutputsConfiguration()

	// Include outputs in the gRPC message
	if outputsJSON != "" {
		monitorLogger.WithFields(logging.Fields{
			"node_id":      nodeID,
			"outputs_size": len(outputsJSON),
		}).Debug("Including MistServer outputs in node update")
	}

	// Forward to Foghorn using gRPC control stream
	if err := control.SendNodeMetrics(nodeMetrics, baseURL, outputsJSON); err != nil {
		monitorLogger.WithError(err).Error("Failed to send node metrics via gRPC")
		return
	}
	monitorLogger.WithField("node_id", nodeID).Debug("Successfully sent typed node update to Foghorn via gRPC")

	// Create typed NodeLifecyclePayload
	nodePayload := &validation.NodeLifecyclePayload{
		NodeID:    nodeID,
		BaseURL:   baseURL,
		IsHealthy: isHealthy,
		GeoData: geoip.GeoData{
			Latitude:  getFloat64Value(latitude),
			Longitude: getFloat64Value(longitude),
		},
		Location:       location,
		CPUUsage:       pm.getCPUUsage(),
		RAMMax:         pm.getRAMMax(),
		RAMCurrent:     pm.getRAMCurrent(),
		BandwidthUp:    pm.getUpSpeed(),
		BandwidthDown:  pm.getDownSpeed(),
		BandwidthLimit: pm.getBandwidthLimit(),
		ActiveStreams:  len(typedStreams),
	}

	// Create typed BaseEvent
	baseEvent := &validation.BaseEvent{
		EventID:       uuid.New().String(),
		EventType:     validation.EventNodeLifecycle,
		Timestamp:     time.Now().UTC(),
		Source:        "prometheus_polling",
		SchemaVersion: "2.0",
		NodeLifecycle: nodePayload,
	}

	// Forward typed event to Decklog for analytics
	go ForwardTypedEventToDecklog(baseEvent)
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

				// Create typed ClientLifecyclePayload from actual API data
				clientPayload := &validation.ClientLifecyclePayload{
					StreamName:     streamName,
					InternalName:   internalName,
					NodeID:         nodeID,
					TenantID:       getTenantForInternalName(internalName),
					Protocol:       protocol,
					Host:           host,
					SessionID:      getString(client[fieldMap["sessid"]]),
					ConnectionTime: getFloat64(client[fieldMap["conntime"]]),
					Position:       getFloat64(client[fieldMap["position"]]),
					BandwidthIn: func() float64 {
						if idx, ok := fieldMap["upbps"]; ok {
							if v, ok := client[idx].(float64); ok {
								return v
							}
						}
						return 0
					}(),
					BandwidthOut: func() float64 {
						if idx, ok := fieldMap["downbps"]; ok {
							if v, ok := client[idx].(float64); ok {
								return v
							}
						}
						return 0
					}(),
					BytesDown: func() int64 {
						if idx, ok := fieldMap["down"]; ok {
							return getInt64(client[idx])
						}
						if idx, ok := fieldMap["bytes_down"]; ok {
							return getInt64(client[idx])
						}
						return 0
					}(),
					BytesUp: func() int64 {
						if idx, ok := fieldMap["up"]; ok {
							return getInt64(client[idx])
						}
						if idx, ok := fieldMap["bytes_up"]; ok {
							return getInt64(client[idx])
						}
						return 0
					}(),
					PacketsSent: func() int64 {
						if idx, ok := fieldMap["pktcount"]; ok {
							return getInt64(client[idx])
						}
						if idx, ok := fieldMap["packet_count"]; ok {
							return getInt64(client[idx])
						}
						return 0
					}(),
					PacketsLost: func() int64 {
						if idx, ok := fieldMap["pktlost"]; ok {
							return getInt64(client[idx])
						}
						if idx, ok := fieldMap["packet_lost"]; ok {
							return getInt64(client[idx])
						}
						return 0
					}(),
					PacketsRetransmitted: func() int64 {
						if idx, ok := fieldMap["pktretransmit"]; ok {
							return getInt64(client[idx])
						}
						if idx, ok := fieldMap["packet_retransmit"]; ok {
							return getInt64(client[idx])
						}
						return 0
					}(),
				}

				// Create typed BaseEvent for client lifecycle metrics
				baseEvent := &validation.BaseEvent{
					EventID:         uuid.New().String(),
					EventType:       validation.EventClientLifecycle,
					Timestamp:       time.Now().UTC(),
					Source:          "mist_clients_api",
					InternalName:    &internalName,
					SchemaVersion:   "2.0",
					ClientLifecycle: clientPayload,
				}

				// Forward typed event to Decklog
				if err := ForwardTypedEventToDecklog(baseEvent); err != nil {
					monitorLogger.WithFields(logging.Fields{
						"error":  err,
						"stream": streamName,
						"type":   "client-lifecycle",
					}).Error("Failed to forward metrics to Decklog")
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
func scanLocalArtifacts(basePath string) (uint64, []validation.FoghornStoredArtifact) {
	if basePath == "" {
		return 0, nil
	}

	var totalSize uint64
	var artifacts []validation.FoghornStoredArtifact
	newArtifactIndex := make(map[string]*ClipInfo)

	// Scan clips directory
	clipsDir := fmt.Sprintf("%s/clips", basePath)
	clipSize, clipArtifacts := scanClipsDirectory(clipsDir, newArtifactIndex)
	totalSize += clipSize
	artifacts = append(artifacts, clipArtifacts...)

	// Scan DVR directory
	dvrDir := fmt.Sprintf("%s/dvr", basePath)
	dvrSize, dvrArtifacts := scanDVRDirectory(dvrDir, newArtifactIndex)
	totalSize += dvrSize
	artifacts = append(artifacts, dvrArtifacts...)

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

	return totalSize, artifacts
}

// scanClipsDirectory scans the clips directory for clip artifacts
func scanClipsDirectory(clipsDir string, artifactIndex map[string]*ClipInfo) (uint64, []validation.FoghornStoredArtifact) {
	// Check if clips directory exists
	if _, err := os.Stat(clipsDir); os.IsNotExist(err) {
		return 0, nil
	}

	var totalSize uint64
	var artifacts []validation.FoghornStoredArtifact

	// Walk the clips directory structure
	entries, err := os.ReadDir(clipsDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read clips directory")
		return 0, nil
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
									CreatedAt:  fileInfo.ModTime().Unix(),
								}

								artifactIndex[clipHash] = clipInfo
								totalSize += uint64(fileInfo.Size())

								// Create artifact for Foghorn reporting
								artifactID := streamName + "/" + clipHash
								artifacts = append(artifacts, validation.FoghornStoredArtifact{
									ID:        artifactID,
									Type:      "clip",
									Path:      filePath,
									SizeBytes: uint64(fileInfo.Size()),
									CreatedAt: fileInfo.ModTime().Unix(),
									Format:    format,
								})
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
								CreatedAt:  fileInfo.ModTime().Unix(),
							}

							artifactIndex[clipHash] = clipInfo
							totalSize += uint64(fileInfo.Size())

							// Create artifact for Foghorn reporting
							artifactID := streamName + "/" + clipHash
							artifacts = append(artifacts, validation.FoghornStoredArtifact{
								ID:        artifactID,
								Type:      "clip",
								Path:      filePath,
								SizeBytes: uint64(fileInfo.Size()),
								CreatedAt: fileInfo.ModTime().Unix(),
								Format:    format,
							})
						}
					}
					break
				}
			}
		}
	}

	return totalSize, artifacts
}

// scanDVRDirectory scans the DVR directory for DVR manifest files
func scanDVRDirectory(dvrDir string, artifactIndex map[string]*ClipInfo) (uint64, []validation.FoghornStoredArtifact) {
	// Check if DVR directory exists
	if _, err := os.Stat(dvrDir); os.IsNotExist(err) {
		return 0, nil
	}

	var totalSize uint64
	var artifacts []validation.FoghornStoredArtifact

	// Walk the DVR directory structure: /dvr/{internal_name}/{dvr_hash}.m3u8
	entries, err := os.ReadDir(dvrDir)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to read DVR directory")
		return 0, nil
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
							CreatedAt:  fileInfo.ModTime().Unix(),
						}

						artifactIndex[dvrHash] = dvrInfo
						totalSize += uint64(fileInfo.Size())

						// Create artifact for Foghorn reporting
						artifactID := internalName + "/" + dvrHash
						artifacts = append(artifacts, validation.FoghornStoredArtifact{
							ID:        artifactID,
							Type:      "dvr",
							Path:      filePath,
							SizeBytes: uint64(fileInfo.Size()),
							CreatedAt: fileInfo.ModTime().Unix(),
							Format:    "m3u8",
						})
					}
				}
			}
		}
	}

	return totalSize, artifacts
}
