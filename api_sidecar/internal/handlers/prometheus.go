package handlers

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/models"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

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

	monitorLogger.WithFields(logging.Fields{
		"node_id":  nodeID,
		"base_url": baseURL,
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
			Latitude:  pm.latitude,
			Longitude: pm.longitude,
			Location:  pm.location,
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
	// Only fetch JSON data from /koekjes.json (contains all the metrics we need)
	jsonData, jsonErr := pm.fetchJSON(baseURL + "/" + pm.mistPassword + ".json")

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

// fetchJSON fetches and parses JSON data from a URL
func (pm *PrometheusMonitor) fetchJSON(url string) (map[string]interface{}, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JSON from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return data, nil
}

// emitStreamLifecycle fetches data from MistServer's TCP API directly
func (pm *PrometheusMonitor) emitStreamLifecycle(nodeID, baseURL string) {
	// Use the provided baseURL for the API
	apiURL := baseURL

	monitorLogger.WithFields(logging.Fields{
		"api_url": apiURL,
		"node_id": nodeID,
	}).Info("Fetching active streams from TCP API")

	// Authenticate with MistServer
	client, err := pm.authenticateWithMistServer(apiURL)
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to authenticate with MistServer")
		return
	}

	// Request active streams with detailed fields including health data
	requestData := map[string]interface{}{
		"active_streams": map[string]interface{}{
			"longform": true,
			"fields": []string{
				"clients", "viewers", "inputs", "outputs", "tracks",
				"upbytes", "downbytes", "packsent", "packloss", "packretrans",
				"firstms", "lastms", "health", "pid", "tags", "status",
			},
		},
	}

	requestJSON, err := json.Marshal(requestData)
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to marshal active_streams request")
		return
	}

	// Make HTTP POST request to TCP API with authenticated client
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(requestJSON)))
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to create HTTP request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to fetch active streams")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"status":  resp.StatusCode,
		}).Error("MistServer TCP API returned non-200 status")
		if body, readErr := io.ReadAll(resp.Body); readErr == nil {
			monitorLogger.WithFields(logging.Fields{
				"api_url": apiURL,
				"node_id": nodeID,
				"body":    string(body)[:min(200, len(body))],
			}).Warn("TCP API error response")
		}
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to read TCP API response")
		return
	}

	monitorLogger.WithFields(logging.Fields{
		"api_url": apiURL,
		"node_id": nodeID,
		"body":    string(body)[:min(500, len(body))],
	}).Info("MistServer TCP API response")

	var apiResponse map[string]interface{}
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"error":   err,
		}).Error("Failed to parse TCP API response")
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"body":    string(body)[:min(200, len(body))],
		}).Warn("Response body")
		return
	}

	// Extract active streams data
	if activeStreams, ok := apiResponse["active_streams"].(map[string]interface{}); ok {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
			"count":   len(activeStreams),
		}).Info("Found active streams via TCP API")
		for streamName, streamData := range activeStreams {
			if streamInfo, ok := streamData.(map[string]interface{}); ok {
				pm.processActiveStreamData(nodeID, streamName, streamInfo)
			}
		}
	} else {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
			"node_id": nodeID,
		}).Warn("No active_streams found")
	}
}

// authenticateWithMistServer handles MistServer API authentication
func (pm *PrometheusMonitor) authenticateWithMistServer(apiURL string) (*http.Client, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// Step 1: Get the challenge
	challengeReq := map[string]interface{}{
		"authorize": map[string]interface{}{
			"username": pm.mistUsername,
			"password": "",
		},
	}

	challengeJSON, err := json.Marshal(challengeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal challenge request: %v", err)
	}

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(challengeJSON)))
	if err != nil {
		return nil, fmt.Errorf("failed to create challenge request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get challenge: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read challenge response: %v", err)
	}

	var challengeResp map[string]interface{}
	if err := json.Unmarshal(body, &challengeResp); err != nil {
		return nil, fmt.Errorf("failed to parse challenge response: %v", err)
	}

	// Extract challenge info
	authInfo, ok := challengeResp["authorize"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("no authorize info in response")
	}

	status, ok := authInfo["status"].(string)
	if !ok {
		return nil, fmt.Errorf("no status in authorize response")
	}

	// If already OK, no need to authenticate
	if status == "OK" {
		monitorLogger.WithFields(logging.Fields{
			"api_url": apiURL,
		}).Info("Already authenticated with MistServer")
		return client, nil
	}

	// If NOACC, we might need to create account (but we'll skip this for now)
	if status == "NOACC" {
		return nil, fmt.Errorf("no accounts exist on MistServer")
	}

	// If CHALL, proceed with authentication
	if status != "CHALL" {
		return nil, fmt.Errorf("unexpected auth status: %s", status)
	}

	challenge, ok := authInfo["challenge"].(string)
	if !ok {
		return nil, fmt.Errorf("no challenge in response")
	}

	monitorLogger.WithFields(logging.Fields{
		"api_url":   apiURL,
		"challenge": challenge,
	}).Info("Got MistServer challenge")

	// Step 2: Calculate password hash
	// MD5(MD5(password) + challenge)
	passwordHash := pm.calculatePasswordHash(pm.mistAPIPassword, challenge)

	// Step 3: Send authentication
	authReq := map[string]interface{}{
		"authorize": map[string]interface{}{
			"username": pm.mistUsername,
			"password": passwordHash,
		},
	}

	authJSON, err := json.Marshal(authReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal auth request: %v", err)
	}

	req, err = http.NewRequest("POST", apiURL, strings.NewReader(string(authJSON)))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err = client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %v", err)
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read auth response: %v", err)
	}

	var authResp map[string]interface{}
	if err := json.Unmarshal(body, &authResp); err != nil {
		return nil, fmt.Errorf("failed to parse auth response: %v", err)
	}

	// Check auth result
	authInfo, ok = authResp["authorize"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("no authorize info in auth response")
	}

	status, ok = authInfo["status"].(string)
	if !ok {
		return nil, fmt.Errorf("no status in auth response")
	}

	if status != "OK" {
		return nil, fmt.Errorf("authentication failed, status: %s", status)
	}

	monitorLogger.WithFields(logging.Fields{
		"api_url": apiURL,
	}).Info("Successfully authenticated with MistServer")
	return client, nil
}

// calculatePasswordHash calculates MD5(MD5(password) + challenge)
func (pm *PrometheusMonitor) calculatePasswordHash(password, challenge string) string {
	// First MD5: hash the password
	passwordMD5 := md5.Sum([]byte(password))
	passwordMD5Hex := hex.EncodeToString(passwordMD5[:])

	// Second MD5: hash(passwordMD5 + challenge)
	combined := passwordMD5Hex + challenge
	finalMD5 := md5.Sum([]byte(combined))

	return hex.EncodeToString(finalMD5[:])
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

	// Get packet statistics
	var packetsSent, packetsLost int64
	if ps, ok := streamData["packsent"].(float64); ok {
		packetsSent = int64(ps)
	}
	if pl, ok := streamData["packloss"].(float64); ok {
		packetsLost = int64(pl)
	}

	// Get timing data
	var firstMs, lastMs int64
	if fm, ok := streamData["firstms"].(float64); ok {
		firstMs = int64(fm)
	}
	if lm, ok := streamData["lastms"].(float64); ok {
		lastMs = int64(lm)
	}

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

	// Get node geographic information
	pm.mutex.RLock()
	latitude := pm.latitude
	longitude := pm.longitude
	location := pm.location
	pm.mutex.RUnlock()

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
	}).WithField("stream_name", streamName).Info("Active stream")

	// Forward essential stream state to Data API (for business logic)
	go forwardEventToCommodore("stream-status", map[string]interface{}{
		"node_id":       nodeID,
		"internal_name": internalName,
		"status":        "live",
		"event_type":    "stream_state_update",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_api",
	})

	// Forward comprehensive analytics data to Decklog (for analytics)
	go ForwardEventToDecklog("stream-lifecycle", map[string]interface{}{
		"node_id":       nodeID,
		"stream_name":   streamName,
		"internal_name": internalName,
		"bandwidth_in":  upbytes,
		"bandwidth_out": downbytes,
		"packets_sent":  packetsSent,
		"packets_lost":  packetsLost,
		"viewers":       viewers,
		"clients":       clients,
		"track_count":   trackCount,
		"inputs":        inputs,
		"outputs":       outputs,
		"first_ms":      firstMs,
		"last_ms":       lastMs,
		"status":        "live",
		"timestamp":     time.Now().Unix(),
		"source":        "mistserver_api",
		"latitude":      latitude,
		"longitude":     longitude,
		"location":      location,
		"health_data":   healthData,
		"track_details": trackDetails,
		"tenant_id":     getTenantForInternalName(internalName),
	})
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
				if locData, ok := jsonData["loc"].(map[string]interface{}); ok {
					if lat, ok := locData["lat"].(float64); ok {
						pm.latitude = &lat
					}
					if lon, ok := locData["lon"].(float64); ok {
						pm.longitude = &lon
					}
					if name, ok := locData["name"].(string); ok && name != "" {
						pm.location = name
					}
				} else {
					// If no location data from MistServer, log it
					monitorLogger.WithFields(logging.Fields{
						"node_id": update.NodeID,
					}).Warn("No location data from MistServer for node")
				}
			}
		}

		pm.mutex.Unlock()

		// Forward metrics to API and analytics
		go pm.forwardNodeMetrics(update.NodeID)
	}
}

// forwardNodeMetrics forwards node metrics to API and analytics services
func (pm *PrometheusMonitor) forwardNodeMetrics(nodeID string) {
	pm.mutex.RLock()
	baseURL := pm.baseURL
	latitude := pm.latitude
	longitude := pm.longitude
	location := pm.location
	isHealthy := pm.isHealthy
	pm.mutex.RUnlock()

	// Forward to Foghorn for load balancing
	foghorn := os.Getenv("FOGHORN_URL")
	if foghorn == "" {
		foghorn = "http://localhost:18008"
	}

	// Prepare metrics for Foghorn
	metrics := map[string]interface{}{
		"cpu":         pm.getCPUUsage(),
		"ram_max":     pm.getRAMMax(),
		"ram_current": pm.getRAMCurrent(),
		"up_speed":    pm.getUpSpeed(),
		"down_speed":  pm.getDownSpeed(),
		"bwlimit":     pm.getBandwidthLimit(),
		"streams":     pm.getStreamMetrics(),
	}

	update := map[string]interface{}{
		"node_id":    nodeID,
		"base_url":   baseURL,
		"is_healthy": isHealthy,
		"latitude":   latitude,
		"longitude":  longitude,
		"location":   location,
		"event_type": "node_metrics",
		"timestamp":  time.Now().Unix(),
		"metrics":    metrics,
	}

	jsonData, err := json.Marshal(update)
	if err != nil {
		monitorLogger.WithError(err).Error("Failed to marshal Foghorn update")
	} else {
		resp, err := http.Post(foghorn+"/node/update", "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			monitorLogger.WithError(err).Error("Failed to send update to Foghorn")
		} else {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				monitorLogger.WithField("status", resp.StatusCode).Error("Foghorn returned non-200 status")
			}
		}
	}

	// Forward to Decklog for analytics
	if err := ForwardEventToDecklog("node-lifecycle", map[string]interface{}{
		"node_id":      nodeID,
		"base_url":     baseURL,
		"is_healthy":   isHealthy,
		"latitude":     latitude,
		"longitude":    longitude,
		"location":     location,
		"event_type":   "node-lifecycle",
		"timestamp":    time.Now().Unix(),
		"cpu_usage":    metrics["cpu"],
		"ram_max":      metrics["ram_max"],
		"ram_current":  metrics["ram_current"],
		"up_speed":     metrics["up_speed"],
		"down_speed":   metrics["down_speed"],
		"bw_limit":     metrics["bwlimit"],
		"stream_count": len(metrics["streams"].(map[string]interface{})),
	}); err != nil {
		monitorLogger.WithError(err).Error("Failed to forward event to Decklog")
	}
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

func (pm *PrometheusMonitor) emitClientLifecycle(nodeID, mistURL string) error {
	// Query MistServer clients API for detailed metrics
	clientsPayload := map[string]interface{}{
		"clients": map[string]interface{}{
			"fields": []string{
				"host",
				"stream",
				"protocol",
				"conntime",
				"position",
				"down",
				"up",
				"downbps",
				"upbps",
				"sessid",
				"pktcount",
				"pktlost",
				"pktretransmit",
			},
			"time": -5, // Query 5 seconds ago for complete data
		},
	}

	jsonPayload, err := json.Marshal(clientsPayload)
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to marshal clients API payload")
		return err
	}

	resp, err := http.Post(mistURL+"/api", "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		monitorLogger.WithFields(logging.Fields{
			"error": err,
			"url":   mistURL + "/api",
		}).Error("Failed to query MistServer clients API")
		return err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		monitorLogger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to decode clients API response")
		return err
	}

	// Process client metrics
	if clients, ok := result["clients"].(map[string]interface{}); ok {
		if data, ok := clients["data"].([]interface{}); ok {
			fields := clients["fields"].([]interface{})

			// Map field names to indices
			fieldMap := make(map[string]int)
			for i, field := range fields {
				fieldMap[field.(string)] = i
			}

			// Process each client connection
			for _, clientData := range data {
				client := clientData.([]interface{})

				streamName := client[fieldMap["stream"]].(string)
				protocol := client[fieldMap["protocol"]].(string)
				host := client[fieldMap["host"]].(string)

				// Update Prometheus metrics
				streamViewers.WithLabelValues(streamName).Inc()

				// Bandwidth metrics
				if downBps, ok := client[fieldMap["downbps"]].(float64); ok {
					streamBandwidthDown.WithLabelValues(streamName, protocol, host).Set(downBps)
				}
				if upBps, ok := client[fieldMap["upbps"]].(float64); ok {
					streamBandwidthUp.WithLabelValues(streamName, protocol, host).Set(upBps)
				}

				// Connection time
				if connTime, ok := client[fieldMap["conntime"]].(float64); ok {
					streamConnectionTime.WithLabelValues(streamName, protocol, host).Set(connTime)
				}

				// Packet statistics for protocols that support it
				if pktCount, ok := client[fieldMap["pktcount"]].(float64); ok {
					streamPacketsTotal.WithLabelValues(streamName, protocol, host).Set(pktCount)
				}
				if pktLost, ok := client[fieldMap["pktlost"]].(float64); ok {
					streamPacketsLost.WithLabelValues(streamName, protocol, host).Set(pktLost)
				}
				if pktRetransmit, ok := client[fieldMap["pktretransmit"]].(float64); ok {
					streamPacketsRetransmitted.WithLabelValues(streamName, protocol, host).Set(pktRetransmit)
				}

				// Forward detailed metrics to Decklog
				internalName := streamName
				if idx := strings.Index(streamName, "+"); idx != -1 && idx+1 < len(streamName) {
					internalName = streamName[idx+1:]
				}

				event := map[string]interface{}{
					"event_type":            "client-lifecycle",
					"stream_name":           streamName,
					"internal_name":         internalName,
					"protocol":              protocol,
					"host":                  host,
					"node_id":               nodeID,
					"bandwidth_in":          client[fieldMap["upbps"]],
					"bandwidth_out":         client[fieldMap["downbps"]],
					"timestamp":             time.Now().Unix(),
					"source":                "mist_clients_api",
					"conn_time":             client[fieldMap["conntime"]],
					"position":              client[fieldMap["position"]],
					"bytes_down":            client[fieldMap["down"]],
					"bytes_up":              client[fieldMap["up"]],
					"packets_sent":          client[fieldMap["pktcount"]],
					"packets_lost":          client[fieldMap["pktlost"]],
					"packets_retransmitted": client[fieldMap["pktretransmit"]],
					"session_id":            client[fieldMap["sessid"]],
					"tenant_id":             getTenantForInternalName(internalName),
				}

				if err := ForwardEventToDecklog(event); err != nil {
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
	// Get CPU usage from MistServer metrics (0-1000 scale)
	if jsonData := pm.getLastJSONData(); jsonData != nil {
		if cpu, ok := jsonData["cpu"].(float64); ok {
			return cpu * 10 // Convert to 0-1000 scale
		}
	}
	return 1000 // Default to max load like C++
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
