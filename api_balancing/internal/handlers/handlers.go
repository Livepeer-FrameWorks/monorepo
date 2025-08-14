package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
)

var (
	db            *sql.DB
	logger        logging.Logger
	lb            *balancer.LoadBalancer
	decklogClient *decklog.Client
)

// StreamKeyRegex matches stream keys in format xxxx-xxxx-xxxx-xxxx
var StreamKeyRegex = regexp.MustCompile(`^(?:\w{4}-){3}\w{4}$`)

// NodeHostRegex matches the first part of hostname before first dot
var NodeHostRegex = regexp.MustCompile(`^.+?\.`)

// Init initializes the handlers with dependencies
func Init(database *sql.DB, log logging.Logger, loadBalancer *balancer.LoadBalancer) {
	db = database
	logger = log
	lb = loadBalancer

	decklogURL := os.Getenv("DECKLOG_URL")
	if decklogURL == "" {
		decklogURL = "decklog:18006"
	}

	// Remove protocol prefix if present
	address := strings.TrimPrefix(decklogURL, "http://")
	address = strings.TrimPrefix(address, "https://")

	config := decklog.ClientConfig{
		Target:        address,
		AllowInsecure: true, // Using insecure for internal service communication
		Timeout:       10 * time.Second,
	}

	client, err := decklog.NewClient(config, logger)
	if err != nil {
		logger.WithError(err).Error("Failed to initialize Decklog gRPC client")
		return
	}
	decklogClient = client
}

// MistServerCompatibilityHandler handles ALL MistServer requests
// This implements the exact same HTTP API as the C++ MistUtilLoad
func MistServerCompatibilityHandler(c middleware.Context) {
	// Handle HTTP/2 protocol initialization
	if c.Request.Method == "PRI" && c.Request.RequestURI == "*" {
		c.String(http.StatusOK, "")
		return
	}

	// Set CORS headers
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "*")

	path := c.Request.URL.Path
	query := c.Request.URL.Query()

	// Handle favicon
	if path == "/favicon.ico" {
		c.String(http.StatusNotFound, "No favicon")
		return
	}

	// Handle root path with query parameters (admin functions)
	if path == "/" {
		handleRootQueries(c, query)
		return
	}

	// Handle stream balancing: /<stream>
	streamName := strings.TrimPrefix(path, "/")

	// Validate stream name format
	if streamName == "" || !StreamKeyRegex.MatchString(streamName) {
		c.String(http.StatusBadRequest, "Invalid stream name")
		return
	}

	handleStreamBalancing(c, streamName)
}

// HandleNodeUpdate receives node updates from Helmsman
func HandleNodeUpdate(c middleware.Context) {
	var update struct {
		NodeID    string                 `json:"node_id"`
		BaseURL   string                 `json:"base_url"`
		IsHealthy bool                   `json:"is_healthy"`
		Latitude  *float64               `json:"latitude"`
		Longitude *float64               `json:"longitude"`
		Location  string                 `json:"location"`
		EventType string                 `json:"event_type"`
		Timestamp int64                  `json:"timestamp"`
		Metrics   map[string]interface{} `json:"metrics"`
	}

	if err := c.ShouldBindJSON(&update); err != nil {
		logger.WithError(err).Error("Failed to parse node update")
		c.JSON(http.StatusBadRequest, middleware.H{"error": "invalid update format"})
		return
	}

	// Convert metrics to format expected by UpdateNodeMetrics
	metrics := map[string]interface{}{
		"cpu":         update.Metrics["cpu"],
		"ram_max":     update.Metrics["ram_max"],
		"ram_current": update.Metrics["ram_current"],
		"up_speed":    update.Metrics["up_speed"],
		"down_speed":  update.Metrics["down_speed"],
		"bwlimit":     update.Metrics["bwlimit"],
		"streams":     update.Metrics["streams"],
		"loc": map[string]interface{}{
			"lat":  update.Latitude,
			"lon":  update.Longitude,
			"name": update.Location,
		},
	}

	// Add or update node
	nodes := lb.GetNodes()
	if _, exists := nodes[update.BaseURL]; !exists {
		if err := lb.AddNode(update.BaseURL, 4242); err != nil {
			logger.WithError(err).Error("Failed to add node")
			c.JSON(http.StatusInternalServerError, middleware.H{"error": "failed to add node"})
			return
		}
	}

	// Update node metrics
	if err := lb.UpdateNodeMetrics(update.BaseURL, metrics); err != nil {
		logger.WithError(err).Error("Failed to update node metrics")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "failed to update metrics"})
		return
	}

	c.JSON(http.StatusOK, middleware.H{"status": "updated"})
}

// HandleStreamHealth receives immediate stream health updates from Helmsman
func HandleStreamHealth(c middleware.Context) {
	var update struct {
		NodeID       string                 `json:"node_id"`
		StreamName   string                 `json:"stream_name"`
		InternalName string                 `json:"internal_name"`
		IsHealthy    bool                   `json:"is_healthy"`
		Timestamp    int64                  `json:"timestamp"`
		Details      map[string]interface{} `json:"details"`
	}

	if err := c.ShouldBindJSON(&update); err != nil {
		logger.WithError(err).Error("Failed to parse stream health update")
		c.JSON(http.StatusBadRequest, middleware.H{"error": "invalid update format"})
		return
	}

	// Get node info
	nodes := lb.GetAllNodes()
	var nodeHost string
	for _, node := range nodes {
		if strings.Contains(node.Host, update.NodeID) {
			nodeHost = node.Host
			break
		}
	}

	if nodeHost == "" {
		logger.WithField("node_id", update.NodeID).Error("Node not found for stream health update")
		c.JSON(http.StatusNotFound, middleware.H{"error": "node not found"})
		return
	}

	// Update stream health in the balancer
	if err := lb.UpdateStreamHealth(nodeHost, update.StreamName, update.IsHealthy, update.Details); err != nil {
		logger.WithError(err).Error("Failed to update stream health")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "failed to update stream health"})
		return
	}

	// Post event to Decklog
	go postBalancingEvent(c, update.InternalName, nodeHost, 0, 0, 0, "health_update", fmt.Sprintf("Stream health: %v", update.IsHealthy))

	c.JSON(http.StatusOK, middleware.H{"status": "updated"})
}

// HandleNodeShutdown receives graceful shutdown notifications from Helmsman
func HandleNodeShutdown(c middleware.Context) {
	var update struct {
		NodeID    string                 `json:"node_id"`
		Type      string                 `json:"type"`
		Timestamp int64                  `json:"timestamp"`
		Reason    string                 `json:"reason"`
		Details   map[string]interface{} `json:"details"`
	}

	if err := c.ShouldBindJSON(&update); err != nil {
		logger.WithError(err).Error("Failed to parse node shutdown update")
		c.JSON(http.StatusBadRequest, middleware.H{"error": "invalid update format"})
		return
	}

	// Get node info
	nodes := lb.GetAllNodes()
	var nodeHost string
	for _, node := range nodes {
		if strings.Contains(node.Host, update.NodeID) {
			nodeHost = node.Host
			break
		}
	}

	if nodeHost == "" {
		logger.WithField("node_id", update.NodeID).Error("Node not found for shutdown update")
		c.JSON(http.StatusNotFound, middleware.H{"error": "node not found"})
		return
	}

	// Mark node as inactive and clear its streams
	if err := lb.HandleNodeShutdown(nodeHost, update.Reason, update.Details); err != nil {
		logger.WithError(err).Error("Failed to handle node shutdown")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "failed to handle shutdown"})
		return
	}

	// Post event to Decklog
	go postBalancingEvent(c, "node_shutdown", nodeHost, 0, 0, 0, "shutdown", update.Reason)

	c.JSON(http.StatusOK, middleware.H{"status": "handled"})
}

/*
Future Enhancements for Node Health Monitoring:

1. System Resource Alerts
   - CPU/RAM/Network spikes
   - Disk space warnings
   - Process health (MistServer, system services)
   Example endpoint: POST /node/alert
   {
     "node_id": "edge-1",
     "type": "system_alert",
     "metrics": {
       "cpu_usage": 95.5,
       "ram_usage": 85.2,
       "disk_free": 10.5
     }
   }

2. Network Health
   - Latency/jitter monitoring
   - Packet loss detection
   - Bandwidth saturation
   Example endpoint: POST /node/network
   {
     "node_id": "edge-1",
     "type": "network_health",
     "metrics": {
       "latency_ms": 150,
       "packet_loss": 2.5,
       "jitter_ms": 45
     }
   }

3. Stream Quality Metrics
   - Encoder performance
   - Buffer health details
   - Frame drop rates
   Example endpoint: POST /stream/quality
   {
     "node_id": "edge-1",
     "stream_name": "live_123",
     "metrics": {
       "encoder_load": 75.5,
       "dropped_frames": 42,
       "buffer_health": 95.5
     }
   }

4. Maintenance Mode
   - Planned maintenance windows
   - Gradual stream migration
   - Zero-downtime updates
   Example endpoint: POST /node/maintenance
   {
     "node_id": "edge-1",
     "type": "maintenance",
     "window": {
       "start_time": "2024-03-15T02:00:00Z",
       "duration_minutes": 120,
       "drain_timeout": 300
     }
   }

Implementation Strategy:
1. Each enhancement should follow the push-based pattern
2. Helmsman detects and pushes updates to Foghorn
3. Foghorn adjusts routing based on received health data
4. Events are forwarded to Decklog for analytics
5. Critical alerts trigger immediate routing changes
*/

// handleRootQueries handles the admin API endpoints (EXACT C++ implementation)
func handleRootQueries(c middleware.Context, query url.Values) {
	c.Header("Content-Type", "text/plain")

	// Get/set weights: /?weights=<json> (EXACT C++ implementation)
	if weights := query.Get("weights"); weights != "" {
		handleWeights(c, weights)
		return
	}

	// List servers: /?lstserver=1 (EXACT C++ implementation)
	if query.Get("lstserver") != "" {
		handleListServers(c)
		return
	}

	// Remove server: /?delserver=<url> (EXACT C++ implementation)
	if delServer := query.Get("delserver"); delServer != "" {
		handleDeleteServer(c, delServer)
		return
	}

	// Add server: /?addserver=<url> (EXACT C++ implementation)
	if addServer := query.Get("addserver"); addServer != "" {
		handleAddServer(c, addServer)
		return
	}

	// Get source: /?source=<stream> (EXACT C++ implementation)
	if source := query.Get("source"); source != "" {
		handleGetSource(c, source, query)
		return
	}

	// Find ingest point: /?ingest=<cpu> (EXACT C++ implementation)
	if ingest := query.Get("ingest"); ingest != "" {
		handleFindIngest(c, ingest, query)
		return
	}

	// Stream stats: /?streamstats=<stream> (EXACT C++ implementation)
	if streamStats := query.Get("streamstats"); streamStats != "" {
		handleStreamStats(c, streamStats)
		return
	}

	// Viewer count: /?viewers=<stream> (EXACT C++ implementation)
	if viewers := query.Get("viewers"); viewers != "" {
		handleViewerCount(c, viewers)
		return
	}

	// Host status: /?host=<hostname> or no params for all hosts (EXACT C++ implementation)
	handleHostStatus(c, query.Get("host"))
}

// handleWeights implements /?weights=<json> (EXACT C++ implementation)
func handleWeights(c middleware.Context, weightsJSON string) {
	if weightsJSON != "" {
		// Set new weights
		var newWeights map[string]interface{}
		if err := json.Unmarshal([]byte(weightsJSON), &newWeights); err == nil {
			weights := lb.GetWeights()

			if cpu, ok := newWeights["cpu"].(float64); ok {
				weights["cpu"] = uint64(cpu)
			}
			if ram, ok := newWeights["ram"].(float64); ok {
				weights["ram"] = uint64(ram)
			}
			if bw, ok := newWeights["bw"].(float64); ok {
				weights["bw"] = uint64(bw)
			}
			if geo, ok := newWeights["geo"].(float64); ok {
				weights["geo"] = uint64(geo)
			}
			if bonus, ok := newWeights["bonus"].(float64); ok {
				weights["bonus"] = uint64(bonus)
			}

			lb.SetWeights(weights["cpu"], weights["ram"], weights["bw"], weights["geo"], weights["bonus"])
		}
	}

	// Return current weights (like C++)
	weights := lb.GetWeights()
	result := map[string]uint64{
		"cpu":   weights["cpu"],
		"ram":   weights["ram"],
		"bw":    weights["bw"],
		"geo":   weights["geo"],
		"bonus": weights["bonus"],
	}

	jsonBytes, _ := json.Marshal(result)
	c.String(http.StatusOK, string(jsonBytes))
}

// handleListServers implements /?lstserver=1 (EXACT C++ implementation)
func handleListServers(c middleware.Context) {
	nodes := lb.GetAllNodes()
	result := make(map[string]string)

	for _, node := range nodes {
		if node.IsActive {
			result[node.Host] = "Monitored (online)"
		} else {
			result[node.Host] = "Offline"
		}
	}

	jsonBytes, _ := json.MarshalIndent(result, "", "  ")
	c.String(http.StatusOK, string(jsonBytes))
}

// handleDeleteServer implements /?delserver=<url> (EXACT C++ implementation)
func handleDeleteServer(c middleware.Context, serverURL string) {
	err := lb.RemoveNode(serverURL)
	if err != nil {
		c.String(http.StatusOK, "Server not monitored - could not delete from monitored server list!")
	} else {
		c.String(http.StatusOK, "Offline")
	}
}

// handleAddServer implements /?addserver=<url> (EXACT C++ implementation)
func handleAddServer(c middleware.Context, serverURL string) {
	if len(serverURL) >= 1024 {
		c.String(http.StatusOK, "Host length too long for monitoring")
		return
	}

	// Check if already exists
	nodes := lb.GetAllNodes()
	for _, node := range nodes {
		if node.Host == serverURL {
			result := map[string]string{"message": "Server already monitored - add request ignored"}
			jsonBytes, _ := json.MarshalIndent(result, "", "  ")
			c.String(http.StatusOK, string(jsonBytes))
			return
		}
	}

	// Add new node
	err := lb.AddNode(serverURL, 4242)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to add server")
		return
	}

	result := map[string]string{serverURL: "Starting monitoring"}
	jsonBytes, _ := json.MarshalIndent(result, "", "  ")
	c.String(http.StatusOK, string(jsonBytes))
}

// handleGetSource implements /?source=<stream> (EXACT C++ implementation)
func handleGetSource(c middleware.Context, streamName string, query url.Values) {
	lat := getLatLon(c, query, "lat", "X-Latitude")
	lon := getLatLon(c, query, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, query)

	// Get client IP for same-host detection (like C++)
	clientIP := c.ClientIP()

	bestNode, score, err := lb.GetBestNodeWithScore(c.Request.Context(), streamName, lat, lon, tagAdjust, clientIP)
	if err != nil {
		// Post failed event
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "failed", err.Error())

		fallback := query.Get("fallback")
		if fallback == "" {
			fallback = "dtsc://localhost:4200"
		}
		c.String(http.StatusOK, fallback)
		return
	}

	dtscURL := fmt.Sprintf("dtsc://%s:4200", bestNode)

	// Check if this is a redirect or direct response
	if query.Get("redirect") == "1" {
		// Post redirect event
		go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "redirect", dtscURL)
		c.Redirect(http.StatusFound, dtscURL)
	} else {
		// Post success event
		go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "success", "")
		c.String(http.StatusOK, dtscURL)
	}
}

// handleFindIngest implements /?ingest=<cpu> (EXACT C++ implementation)
func handleFindIngest(c middleware.Context, cpuUsage string, query url.Values) {
	lat := getLatLon(c, query, "lat", "X-Latitude")
	lon := getLatLon(c, query, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, query)

	// Convert CPU usage to uint32 (like C++ atof * 10), optional parameter
	var minCpu uint32 = 0
	if cpuUsage != "" {
		if cpuUse, err := strconv.ParseFloat(cpuUsage, 64); err == nil {
			minCpu = uint32(cpuUse * 10) // C++ multiplies by 10
		}
	}

	// Find best node for ingest (empty stream name means no same-host filtering)
	bestNode, score, err := lb.GetBestNodeWithScore(c.Request.Context(), "", lat, lon, tagAdjust, "")
	if err != nil {
		// Post failed ingest event
		go postBalancingEvent(c, "ingest", "", 0, lat, lon, "failed", err.Error())
		c.String(http.StatusOK, "FULL") // C++ fallback for no ingest point
		return
	}

	// If minCpu specified, verify the selected node can handle the additional load
	// This implements the C++ logic: if (minCpu && cpu + minCpu >= 1000){return 0;}
	if minCpu > 0 {
		nodes := lb.GetAllNodes()
		for _, node := range nodes {
			if node.Host == bestNode {
				if node.CPU+uint64(minCpu) >= 1000 {
					// Node would be overloaded, return fallback (like C++ FAIL_MSG("No ingest point found!"))
					go postBalancingEvent(c, "ingest", "", 0, lat, lon, "failed", "CPU overload")
					c.String(http.StatusOK, "FULL") // C++ fallback for CPU overload
					return
				}
				break
			}
		}
	}

	// Post successful ingest event
	go postBalancingEvent(c, "ingest", bestNode, score, lat, lon, "success", "")
	c.String(http.StatusOK, bestNode)
}

// handleStreamStats implements /?streamstats=<stream> (EXACT C++ implementation)
func handleStreamStats(c middleware.Context, streamName string) {
	nodes := lb.GetAllNodes()
	result := make(map[string][]interface{})

	for _, node := range nodes {
		if !node.IsActive {
			continue
		}
		if stream, exists := node.Streams[streamName]; exists {
			result[streamName] = []interface{}{
				stream.Total,     // viewers
				stream.Bandwidth, // bandwidth
				stream.BytesUp,   // bytes up
				stream.BytesDown, // bytes down
			}
			break
		}
	}

	jsonBytes, _ := json.MarshalIndent(result, "", "  ")
	c.String(http.StatusOK, string(jsonBytes))
}

// handleViewerCount implements /?viewers=<stream> (EXACT C++ implementation)
func handleViewerCount(c middleware.Context, streamName string) {
	nodes := lb.GetAllNodes()
	totalViewers := uint64(0)

	for _, node := range nodes {
		if !node.IsActive {
			continue
		}
		if stream, exists := node.Streams[streamName]; exists {
			totalViewers += stream.Total
		}
	}

	c.String(http.StatusOK, strconv.FormatUint(totalViewers, 10))
}

// handleHostStatus implements /?host=<hostname> or no params (EXACT C++ implementation)
func handleHostStatus(c middleware.Context, hostname string) {
	nodes := lb.GetAllNodes()
	result := make(map[string]interface{})

	for _, node := range nodes {
		if hostname == "" || node.Host == hostname {
			status := map[string]interface{}{
				"cpu":     node.CPU / 10,                         // Convert 0-1000 to 0-100 like C++
				"ram":     (node.RAMCurrent * 100) / node.RAMMax, // Percentage like C++
				"up":      node.UpSpeed,
				"up_add":  node.AddBandwidth,
				"down":    node.DownSpeed,
				"streams": len(node.Streams),
				"viewers": getTotalViewers(node),
				"bwlimit": node.AvailBandwidth,
			}

			if node.GeoLatitude != 0 || node.GeoLongitude != 0 {
				status["geo"] = map[string]float64{
					"lat": node.GeoLatitude,
					"lon": node.GeoLongitude,
				}
			}

			if len(node.Tags) > 0 {
				status["tags"] = node.Tags
			}

			// Add scoring info like C++
			if node.RAMMax > 0 && node.AvailBandwidth > 0 {
				weights := lb.GetWeights()
				status["score"] = map[string]uint64{
					"cpu": weights["cpu"] - (node.CPU*weights["cpu"])/1000,
					"ram": weights["ram"] - ((node.RAMCurrent * weights["ram"]) / node.RAMMax),
					"bw":  weights["bw"] - (((node.UpSpeed + node.AddBandwidth) * weights["bw"]) / node.AvailBandwidth),
				}
			}

			result[node.Host] = status
		}
	}

	jsonBytes, _ := json.MarshalIndent(result, "", "  ")
	c.String(http.StatusOK, string(jsonBytes))
}

// handleStreamBalancing implements /<stream> (EXACT C++ implementation)
func handleStreamBalancing(c middleware.Context, streamName string) {
	query := c.Request.URL.Query()
	lat := getLatLon(c, query, "lat", "X-Latitude")
	lon := getLatLon(c, query, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, query)

	logger.WithField("stream", streamName).Info("Balancing stream")

	bestNode, err := lb.GetBestNode(c.Request.Context(), streamName, lat, lon, tagAdjust)
	if err != nil {
		logger.WithError(err).Error("All servers seem to be out of bandwidth!")
		c.String(http.StatusOK, "localhost") // fallback like C++

		// Post failure event to Firehose
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "failed", err.Error())
		return
	}

	// Check if redirect is requested (like C++)
	proto := query.Get("proto")
	vars := c.Request.URL.RawQuery
	if proto != "" && bestNode != "" {
		redirectURL := fmt.Sprintf("%s://%s/%s", proto, bestNode, streamName)
		if vars != "" {
			redirectURL += "?" + vars
		}
		c.Header("Location", redirectURL)
		c.String(http.StatusTemporaryRedirect, redirectURL)

		// Post redirect event to Firehose
		go postBalancingEvent(c, streamName, bestNode, 0, lat, lon, "redirect", redirectURL)
		return
	}

	c.String(http.StatusOK, bestNode)

	// Post successful balancing event to Firehose
	go postBalancingEvent(c, streamName, bestNode, 0, lat, lon, "success", "")
}

// postBalancingEvent posts load balancing decisions to Decklog via gRPC
func postBalancingEvent(c middleware.Context, streamName, selectedNode string, score uint64, lat, lon float64, status, details string) {

	// Extract client IP (check X-Forwarded-For first, then X-Real-IP, then RemoteAddr)
	clientIP := c.GetHeader("X-Forwarded-For")
	if clientIP == "" {
		clientIP = c.GetHeader("X-Real-IP")
	}
	if clientIP == "" {
		clientIP = c.ClientIP()
	}

	// Extract country/region from headers if available (set by Cloudflare/nginx)
	country := c.GetHeader("CF-IPCountry")
	if country == "" {
		country = c.GetHeader("X-Country-Code")
	}

	// Extract real IP from CloudFlare (overrides X-Forwarded-For if present)
	cfConnectingIP := c.GetHeader("CF-Connecting-IP")
	if cfConnectingIP != "" {
		clientIP = cfConnectingIP
	}

	// Determine tenant ID from headers (service-to-service calls must set X-Tenant-ID)
	tenantID := c.GetHeader("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "00000000-0000-0000-0000-000000000001"
	}

	// Create Event with LoadBalancingData
	event := decklog.NewLoadBalancingEvent(tenantID, streamName, selectedNode, clientIP, country, status, details, lat, lon, score)

	if decklogClient == nil {
		logger.Error("Decklog gRPC client not initialized")
		return
	}

	// Send event via gRPC
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := decklogClient.SendEvent(ctx, event)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":       err,
			"stream_name": streamName,
			"node":        selectedNode,
		}).Error("Failed to send balancing event to Decklog")
		return
	}

	logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"node":        selectedNode,
		"status":      status,
	}).Debug("Successfully sent balancing event to Decklog")
}

// Helper functions

func getLatLon(c middleware.Context, query url.Values, queryKey, headerKey string) float64 {
	// First check CloudFlare geographic headers (most accurate)
	if queryKey == "lat" {
		if val := c.GetHeader("CF-IPLatitude"); val != "" {
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				return f
			}
		}
	}
	if queryKey == "lon" {
		if val := c.GetHeader("CF-IPLongitude"); val != "" {
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				return f
			}
		}
	}

	// Then check standard headers (nginx with GeoIP, etc.)
	if val := c.GetHeader(headerKey); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}

	// Finally check query parameter
	if val := query.Get(queryKey); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return 0
}

func getTagAdjustments(c middleware.Context, query url.Values) map[string]int {
	// Check header first (like C++)
	if tagAdjust := c.GetHeader("X-Tag-Adjust"); tagAdjust != "" {
		var adjustments map[string]int
		if err := json.Unmarshal([]byte(tagAdjust), &adjustments); err == nil {
			return adjustments
		}
	}

	// Check query parameter
	if tagAdjust := query.Get("tag_adjust"); tagAdjust != "" {
		var adjustments map[string]int
		if err := json.Unmarshal([]byte(tagAdjust), &adjustments); err == nil {
			return adjustments
		}
	}

	return make(map[string]int)
}

func getTotalViewers(node *balancer.Node) uint64 {
	total := uint64(0)
	for _, stream := range node.Streams {
		total += stream.Total
	}
	return total
}
