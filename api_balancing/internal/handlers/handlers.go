package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	fapi "frameworks/pkg/api/foghorn"
	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/clips"
	"frameworks/pkg/dvr"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/validation"

	"github.com/google/uuid"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	db            *sql.DB
	logger        logging.Logger
	lb            *balancer.LoadBalancer
	decklogClient *decklog.Client
	metrics       *FoghornMetrics
	geoipReader   *geoip.Reader
)

func getClipContextByRequestID(requestID string) (string, string) {
	if db == nil || requestID == "" {
		return "", ""
	}
	var tenantID, internalName string
	_ = db.QueryRow(`SELECT tenant_id::text, stream_name FROM foghorn.clips WHERE request_id = $1`, requestID).Scan(&tenantID, &internalName)
	return tenantID, internalName
}

// FoghornMetrics holds all Prometheus metrics for Foghorn
type FoghornMetrics struct {
	RoutingDecisions        *prometheus.CounterVec
	NodeSelectionDuration   *prometheus.HistogramVec
	LoadDistribution        *prometheus.GaugeVec
	HealthScoreCalculations *prometheus.CounterVec
	DBQueries               *prometheus.CounterVec
	DBDuration              *prometheus.HistogramVec
	DBConnections           *prometheus.GaugeVec
}

// StreamKeyRegex matches stream keys in format xxxx-xxxx-xxxx-xxxx
var StreamKeyRegex = regexp.MustCompile(`^(?:\w{4}-){3}\w{4}$`)

// NodeService implements the NodeMetricsProcessor interface
type NodeService struct{}

// ProcessNodeMetrics implements control.NodeMetricsProcessor
func (ns *NodeService) ProcessNodeMetrics(nodeID, baseURL string, isHealthy bool, latitude, longitude *float64, location string, nodeMetrics *validation.FoghornNodeUpdate) error {
	// Process the metrics the same way as the HTTP handler
	return processNodeUpdateInternal(nodeID, baseURL, isHealthy, latitude, longitude, location, nodeMetrics)
}

// processNodeUpdateInternal contains the shared logic for processing node updates from both HTTP and gRPC
func processNodeUpdateInternal(nodeID, baseURL string, _ bool, latitude, longitude *float64, location string, nodeMetrics *validation.FoghornNodeUpdate) error {
	// Enrich location data from parameters if not in metrics
	if latitude != nil && longitude != nil {
		nodeMetrics.Location.Latitude = *latitude
		nodeMetrics.Location.Longitude = *longitude
	}
	if location != "" && nodeMetrics.Location.Name == "" {
		nodeMetrics.Location.Name = location
	}

	// Add or update node
	nodes := lb.GetNodes()
	if _, exists := nodes[baseURL]; !exists {
		if err := lb.AddNodeWithID(nodeID, baseURL, 4242); err != nil {
			logger.WithError(err).Error("Failed to add node")
			return fmt.Errorf("failed to add node: %w", err)
		}
	} else {
		// Node exists, but update NodeID mapping in case it changed
		if nodeID != "" {
			if err := lb.UpdateNodeIDMapping(nodeID, baseURL); err != nil {
				logger.WithError(err).Warn("Failed to update NodeID mapping")
			}
		}
	}

	// Update node metrics using typed data
	if err := lb.UpdateNodeMetrics(baseURL, nodeMetrics); err != nil {
		logger.WithError(err).Error("Failed to update node metrics")
		return fmt.Errorf("failed to update metrics: %w", err)
	}

	return nil
}

// NodeHostRegex matches the first part of hostname before first dot
var NodeHostRegex = regexp.MustCompile(`^.+?\.`)

// Init initializes the handlers with dependencies
func Init(database *sql.DB, log logging.Logger, loadBalancer *balancer.LoadBalancer, foghornMetrics *FoghornMetrics) {
	db = database
	logger = log
	lb = loadBalancer
	metrics = foghornMetrics

	// Share database connection with control package for clip operations
	control.SetDB(database)

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

	// Register clip progress/done handlers to emit analytics
	control.SetClipHandlers(
		func(p *pb.ClipProgress) {
			if decklogClient == nil {
				return
			}
			tenantID, internalName := getClipContextByRequestID(p.GetRequestId())
			evt := decklog.NewClipLifecycleEvent(tenantID, internalName, p.GetRequestId(), pb.ClipLifecycleData_STAGE_PROGRESS, func(d *pb.ClipLifecycleData) {
				percent := p.GetPercent()
				d.Percent = &percent
				msg := p.GetMessage()
				if msg != "" {
					d.Message = &msg
				}
			})
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = decklogClient.SendEvent(ctx, evt)
			}()
		},
		func(dn *pb.ClipDone) {
			if decklogClient == nil {
				return
			}
			tenantID, internalName := getClipContextByRequestID(dn.GetRequestId())
			stage := pb.ClipLifecycleData_STAGE_DONE
			if dn.GetStatus() != "success" {
				stage = pb.ClipLifecycleData_STAGE_FAILED
			}
			evt := decklog.NewClipLifecycleEvent(tenantID, internalName, dn.GetRequestId(), stage, func(d *pb.ClipLifecycleData) {
				if fp := dn.GetFilePath(); fp != "" {
					d.FilePath = &fp
				}
				if s3 := dn.GetS3Url(); s3 != "" {
					d.S3Url = &s3
				}
				sz := dn.GetSizeBytes()
				if sz > 0 {
					d.SizeBytes = &sz
				}
				if er := dn.GetError(); er != "" {
					d.Error = &er
				}
			})
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = decklogClient.SendEvent(ctx, evt)
			}()
		},
	)

	// Initialize GeoIP reader for fallback geo enrichment
	geoipPath := os.Getenv("GEOIP_MMDB_PATH")
	if geoipPath != "" {
		reader, err := geoip.NewReader(geoipPath)
		if err != nil {
			logger.WithFields(logging.Fields{
				"geoip_path": geoipPath,
				"error":      err,
			}).Warn("Failed to initialize GeoIP reader, fallback geo enrichment disabled")
		} else {
			geoipReader = reader
			logger.WithField("geoip_path", geoipPath).Info("GeoIP reader initialized successfully")
		}
	} else {
		logger.Debug("No GEOIP_MMDB_PATH provided, fallback geo enrichment disabled")
	}

	// Wire up the node service for gRPC
	control.SetNodeService(&NodeService{})

	// Set clip hash resolver
	control.SetClipHashResolver(func(clipHash string) (string, string, error) {
		var tenantID, name string
		// Try clips first
		err := db.QueryRow(`SELECT tenant_id, stream_name FROM foghorn.clips WHERE clip_hash = $1 AND status != 'deleted' LIMIT 1`, clipHash).Scan(&tenantID, &name)
		if err == sql.ErrNoRows {
			// Fallback to DVR by request_hash; use internal_name as returned name
			err = db.QueryRow(`SELECT tenant_id, internal_name FROM foghorn.dvr_requests WHERE request_hash = $1 LIMIT 1`, clipHash).Scan(&tenantID, &name)
			if err == sql.ErrNoRows {
				return "", "", nil
			}
		}
		if err != nil {
			return "", "", err
		}
		return tenantID, name, nil
	})
}

// MistServerCompatibilityHandler handles ALL MistServer requests
// This implements the exact same HTTP API as the C++ MistUtilLoad
func MistServerCompatibilityHandler(c *gin.Context) {
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

// HandleNodeUpdate receives node updates from Helmsman - TYPED VERSION
func HandleNodeUpdate(c *gin.Context) {
	start := time.Now()
	if metrics != nil {
		metrics.DBQueries.WithLabelValues("node_update", "received").Inc()
	}

	var update struct {
		NodeID    string                        `json:"node_id"`
		BaseURL   string                        `json:"base_url"`
		IsHealthy bool                          `json:"is_healthy"`
		Latitude  *float64                      `json:"latitude"`
		Longitude *float64                      `json:"longitude"`
		Location  string                        `json:"location"`
		EventType string                        `json:"event_type"`
		Timestamp int64                         `json:"timestamp"`
		Metrics   *validation.FoghornNodeUpdate `json:"metrics"`
	}

	if err := c.ShouldBindJSON(&update); err != nil {
		logger.WithError(err).Error("Failed to parse node update")
		if metrics != nil {
			metrics.DBQueries.WithLabelValues("node_update", "error").Inc()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid update format"})
		return
	}

	// Validate required fields
	if update.Metrics == nil {
		logger.Error("Node update missing metrics data")
		if metrics != nil {
			metrics.DBQueries.WithLabelValues("node_update", "error").Inc()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing metrics data"})
		return
	}

	// Enrich location data from request if not in metrics
	if update.Latitude != nil && update.Longitude != nil {
		update.Metrics.Location.Latitude = *update.Latitude
		update.Metrics.Location.Longitude = *update.Longitude
	}
	if update.Location != "" && update.Metrics.Location.Name == "" {
		update.Metrics.Location.Name = update.Location
	}

	// Add or update node
	nodes := lb.GetNodes()
	if _, exists := nodes[update.BaseURL]; !exists {
		if err := lb.AddNodeWithID(update.NodeID, update.BaseURL, 4242); err != nil {
			logger.WithError(err).Error("Failed to add node")
			if metrics != nil {
				metrics.DBQueries.WithLabelValues("node_update", "error").Inc()
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add node"})
			return
		}
	} else {
		// Node exists, but update NodeID mapping in case it changed
		if update.NodeID != "" {
			if err := lb.UpdateNodeIDMapping(update.NodeID, update.BaseURL); err != nil {
				logger.WithError(err).Warn("Failed to update NodeID mapping")
			}
		}
	}

	// Update node metrics using typed data
	if err := lb.UpdateNodeMetrics(update.BaseURL, update.Metrics); err != nil {
		logger.WithError(err).Error("Failed to update node metrics")
		if metrics != nil {
			metrics.DBQueries.WithLabelValues("node_update", "error").Inc()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update metrics"})
		return
	}

	if metrics != nil {
		metrics.DBQueries.WithLabelValues("node_update", "success").Inc()
		metrics.DBDuration.WithLabelValues("node_update").Observe(time.Since(start).Seconds())
	}

	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

// HandleStreamHealth receives immediate stream health updates from Helmsman - TYPED VERSION
func HandleStreamHealth(c *gin.Context) {
	start := time.Now()
	if metrics != nil {
		metrics.HealthScoreCalculations.WithLabelValues().Inc()
	}

	var update struct {
		NodeID       string                          `json:"node_id"`
		StreamName   string                          `json:"stream_name"`
		InternalName string                          `json:"internal_name"`
		IsHealthy    bool                            `json:"is_healthy"`
		Timestamp    int64                           `json:"timestamp"`
		Details      *validation.FoghornStreamHealth `json:"details,omitempty"`
	}

	if err := c.ShouldBindJSON(&update); err != nil {
		logger.WithError(err).Error("Failed to parse stream health update")
		if metrics != nil {
			metrics.DBQueries.WithLabelValues("stream_health", "error").Inc()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid update format"})
		return
	}

	// Get node info using NodeID lookup
	nodeHost, err := lb.GetNodeByID(update.NodeID)
	if err != nil {
		logger.WithField("node_id", update.NodeID).Error("Node not found for stream health update")
		if metrics != nil {
			metrics.DBQueries.WithLabelValues("stream_health", "error").Inc()
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	// Update stream health in the balancer using typed data
	if err := lb.UpdateStreamHealth(nodeHost, update.StreamName, update.IsHealthy, update.Details); err != nil {
		logger.WithError(err).Error("Failed to update stream health")
		if metrics != nil {
			metrics.DBQueries.WithLabelValues("stream_health", "error").Inc()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update stream health"})
		return
	}

	if metrics != nil {
		metrics.DBQueries.WithLabelValues("stream_health", "success").Inc()
		metrics.DBDuration.WithLabelValues("stream_health").Observe(time.Since(start).Seconds())
	}

	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

// HandleNodeShutdown receives graceful shutdown notifications from Helmsman - TYPED VERSION
func HandleNodeShutdown(c *gin.Context) {
	var update struct {
		NodeID    string                          `json:"node_id"`
		Type      string                          `json:"type"`
		Timestamp int64                           `json:"timestamp"`
		Reason    string                          `json:"reason"`
		Details   *validation.FoghornNodeShutdown `json:"details,omitempty"`
	}

	if err := c.ShouldBindJSON(&update); err != nil {
		logger.WithError(err).Error("Failed to parse node shutdown update")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid update format"})
		return
	}

	// Get node info using NodeID lookup
	nodeHost, err := lb.GetNodeByID(update.NodeID)
	if err != nil {
		logger.WithField("node_id", update.NodeID).Error("Node not found for shutdown update")
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	// Mark node as inactive and clear its streams using typed data
	if err := lb.HandleNodeShutdown(nodeHost, update.Reason, update.Details); err != nil {
		logger.WithError(err).Error("Failed to handle node shutdown")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to handle shutdown"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "handled"})
}

// HandleNodesOverview receives a request for an overview of all nodes with capabilities, limits, and artifacts.
func HandleNodesOverview(c *gin.Context) {
	capFilter := c.Query("cap")
	offsetStr := c.Query("offset")
	limitStr := c.Query("limit")
	var offset, limit int
	if v, err := strconv.Atoi(offsetStr); err == nil {
		offset = v
	}
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}

	nodes := lb.GetAllNodes()
	var out []map[string]interface{}
	for _, n := range nodes {
		if capFilter != "" {
			reqs := strings.Split(capFilter, ",")
			roleSet := map[string]bool{}
			for _, r := range n.Roles {
				roleSet[r] = true
			}
			ok := true
			for _, r := range reqs {
				r = strings.TrimSpace(r)
				switch r {
				case "ingest":
					ok = ok && (n.CapIngest || roleSet["ingest"])
				case "edge":
					ok = ok && (n.CapEdge || roleSet["edge"])
				case "storage":
					ok = ok && (n.CapStorage || roleSet["storage"])
				case "processing":
					ok = ok && (n.CapProcessing || roleSet["processing"])
				default:
					ok = ok && roleSet[r]
				}
			}
			if !ok {
				continue
			}
		}

		// Streams detail list
		streams := make([]map[string]interface{}, 0, len(n.Streams))
		for name, s := range n.Streams {
			streams = append(streams, map[string]interface{}{
				"name":       name,
				"total":      s.Total,
				"inputs":     s.Inputs,
				"bandwidth":  s.Bandwidth,
				"bytes_up":   s.BytesUp,
				"bytes_down": s.BytesDown,
				"replicated": s.Replicated,
			})
		}

		entry := map[string]interface{}{
			"host":  n.Host,
			"roles": n.Roles,
			// capabilities
			"cap_ingest":     n.CapIngest,
			"cap_edge":       n.CapEdge,
			"cap_storage":    n.CapStorage,
			"cap_processing": n.CapProcessing,
			// hardware summary
			"gpu_vendor": n.GPUVendor,
			"gpu_count":  n.GPUCount,
			"gpu_mem_mb": n.GPUMemMB,
			"gpu_cc":     n.GPUCC,
			// geo/location
			"geo_latitude":  n.GeoLatitude,
			"geo_longitude": n.GeoLongitude,
			"location_name": n.LocationName,
			// status and timing
			"is_active":   n.IsActive,
			"last_update": n.LastUpdate,
			// resource metrics
			"cpu_tenths":  n.CPU,
			"cpu_percent": n.CPU / 10,
			"ram_max":     n.RAMMax,
			"ram_current": n.RAMCurrent,
			"ram_percent": func() uint64 {
				if n.RAMMax > 0 {
					return (n.RAMCurrent * 100) / n.RAMMax
				}
				return 0
			}(),
			"up_speed":        n.UpSpeed,
			"down_speed":      n.DownSpeed,
			"avail_bandwidth": n.AvailBandwidth,
			"add_bandwidth":   n.AddBandwidth,
			"tags":            n.Tags,
			// storage
			"storage_local":  n.StorageLocal,
			"storage_bucket": n.StorageBucket,
			"storage_prefix": n.StoragePrefix,
			// limits and artifacts
			"max_transcodes":         n.MaxTranscodes,
			"current_transcodes":     n.CurrentTranscodes,
			"storage_capacity_bytes": n.StorageCapacityBytes,
			"storage_used_bytes":     n.StorageUsedBytes,
			"artifacts":              n.Artifacts,
			// streams
			"streams": streams,
		}

		out = append(out, entry)
	}
	if limit > 0 {
		start := offset
		if start < 0 {
			start = 0
		}
		if start > len(out) {
			start = len(out)
		}
		end := start + limit
		if end > len(out) {
			end = len(out)
		}
		out = out[start:end]
	}
	c.JSON(http.StatusOK, out)
}

// handleRootQueries handles the admin API endpoints (EXACT C++ implementation)
func handleRootQueries(c *gin.Context, query url.Values) {
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
func handleWeights(c *gin.Context, weightsJSON string) {
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
func handleListServers(c *gin.Context) {
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
func handleDeleteServer(c *gin.Context, serverURL string) {
	err := lb.RemoveNode(serverURL)
	if err != nil {
		c.String(http.StatusOK, "Server not monitored - could not delete from monitored server list!")
	} else {
		c.String(http.StatusOK, "Offline")
	}
}

// handleAddServer implements /?addserver=<url> (EXACT C++ implementation)
func handleAddServer(c *gin.Context, serverURL string) {
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
func handleGetSource(c *gin.Context, streamName string, query url.Values) {
	start := time.Now()
	lat := getLatLon(c, query, "lat", "X-Latitude")
	lon := getLatLon(c, query, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, query)

	// Get client IP for same-host detection (like C++)
	clientIP := c.ClientIP()

	// Optional capability filter
	requireCap := query.Get("cap") // ingest|edge|storage|processing or comma-separated

	var bestNode string
	var score uint64
	var nodeLat, nodeLon float64
	var nodeName string
	var err error
	ctx := c.Request.Context()
	if requireCap != "" {
		ctx = context.WithValue(ctx, "cap", requireCap)
	}
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, streamName, lat, lon, tagAdjust, clientIP)
	if err != nil {
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", "failed").Inc()
		}
		// Post failed event
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "failed", err.Error(), 0, 0, "")

		fallback := query.Get("fallback")
		if fallback == "" {
			fallback = "dtsc://localhost:4200"
		}
		c.String(http.StatusOK, fallback)
		return
	}

	// Extract hostname from URL for DTSC (C++ returns "dtsc://" + host)
	hostname := bestNode
	if u, err := url.Parse(bestNode); err == nil && u.Hostname() != "" {
		hostname = u.Hostname()
	}
	dtscURL := fmt.Sprintf("dtsc://%s:4200", hostname)

	// Check if this is a redirect or direct response
	if query.Get("redirect") == "1" {
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", bestNode).Inc()
			metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
		}
		// Post redirect event
		go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "redirect", dtscURL, nodeLat, nodeLon, nodeName)
		c.Redirect(http.StatusFound, dtscURL)
	} else {
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", bestNode).Inc()
			metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
		}
		// Post success event
		go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "success", "", nodeLat, nodeLon, nodeName)
		c.String(http.StatusOK, dtscURL)
	}
}

// handleFindIngest implements /?ingest=<cpu> (EXACT C++ implementation)
func handleFindIngest(c *gin.Context, cpuUsage string, query url.Values) {
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

	// Optional capability filter (default to ingest for this endpoint)
	requireCap := query.Get("cap")
	if requireCap == "" {
		requireCap = "ingest"
	}

	// Find best node for ingest (empty stream name means no same-host filtering)
	ctx := context.WithValue(c.Request.Context(), "cap", requireCap)
	bestNode, score, nodeLat, nodeLon, nodeName, err := lb.GetBestNodeWithScore(ctx, "", lat, lon, tagAdjust, "")
	if err != nil {
		// Post failed ingest event
		go postBalancingEvent(c, "ingest", "", 0, lat, lon, "failed", err.Error(), 0, 0, "")
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
					go postBalancingEvent(c, "ingest", "", 0, lat, lon, "failed", "CPU overload", 0, 0, "")
					c.String(http.StatusOK, "FULL") // C++ fallback for CPU overload
					return
				}
				break
			}
		}
	}

	// Post successful ingest event
	go postBalancingEvent(c, "ingest", bestNode, score, lat, lon, "success", "", nodeLat, nodeLon, nodeName)
	c.String(http.StatusOK, bestNode)
}

// handleStreamStats implements /?streamstats=<stream> (EXACT C++ implementation)
func handleStreamStats(c *gin.Context, streamName string) {
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
func handleViewerCount(c *gin.Context, streamName string) {
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
func handleHostStatus(c *gin.Context, hostname string) {
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
func handleStreamBalancing(c *gin.Context, streamName string) {
	start := time.Now()
	query := c.Request.URL.Query()
	lat := getLatLon(c, query, "lat", "X-Latitude")
	lon := getLatLon(c, query, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, query)

	logger.WithField("stream", streamName).Info("Balancing stream")

	// Optional capability filter
	requireCap := query.Get("cap")

	var bestNode string
	var score uint64
	var nodeLat, nodeLon float64
	var nodeName string
	var err error
	ctx := c.Request.Context()
	if requireCap != "" {
		ctx = context.WithValue(ctx, "cap", requireCap)
	}
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, streamName, lat, lon, tagAdjust, "")
	if err != nil {
		logger.WithError(err).Error("All servers seem to be out of bandwidth!")
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("load_balancer", "failed").Inc()
		}
		c.String(http.StatusOK, "localhost") // fallback like C++

		// Post failure event to Firehose
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "failed", err.Error(), 0, 0, "")
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

		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("load_balancer", bestNode).Inc()
			metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
		}

		// Post redirect event to Firehose
		go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "redirect", redirectURL, nodeLat, nodeLon, nodeName)
		return
	}

	c.String(http.StatusOK, bestNode)

	if metrics != nil {
		metrics.RoutingDecisions.WithLabelValues("load_balancer", bestNode).Inc()
		metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
	}

	// Post successful balancing event to Firehose
	go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "success", "", nodeLat, nodeLon, nodeName)
}

// Orchestrate clip creation: select ingest and storage, then call Helmsman on storage to pull clip from Mist
func HandleCreateClip(c *gin.Context) {
	var req struct {
		TenantID     string `json:"tenant_id"`
		InternalName string `json:"internal_name"`
		Format       string `json:"format"`
		Title        string `json:"title"`
		StartUnix    *int64 `json:"start_unix,omitempty"`
		StopUnix     *int64 `json:"stop_unix,omitempty"`
		StartMS      *int64 `json:"start_ms,omitempty"`
		StopMS       *int64 `json:"stop_ms,omitempty"`
		DurationSec  *int64 `json:"duration_sec,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if req.InternalName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal_name is required"})
		return
	}

	format := req.Format
	if format == "" {
		format = "mp4"
	}

	q := c.Request.URL.Query()
	lat := getLatLon(c, q, "lat", "X-Latitude")
	lon := getLatLon(c, q, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, q)

	// Select ingest node (cap=ingest)
	ictx := context.WithValue(c.Request.Context(), "cap", "ingest")
	ingestHost, _, _, _, _, err := lb.GetBestNodeWithScore(ictx, req.InternalName, lat, lon, tagAdjust, c.ClientIP())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no ingest node available", "details": err.Error()})
		return
	}
	// Select storage node (cap=storage)
	sctx := context.WithValue(c.Request.Context(), "cap", "storage")
	storageHost, _, _, _, _, err := lb.GetBestNodeWithScore(sctx, "", 0, 0, map[string]int{}, "")
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no storage node available", "details": err.Error()})
		return
	}

	// Generate request_id for correlation and emit ClipRequested
	reqID := uuid.New().String()
	if decklogClient != nil {
		evt := decklog.NewClipLifecycleEvent(c.GetString("tenant_id"), req.InternalName, reqID, pb.ClipLifecycleData_STAGE_REQUESTED, func(d *pb.ClipLifecycleData) {
			d.Title = func() *string {
				if req.Title == "" {
					return nil
				}
				v := req.Title
				return &v
			}()
			d.Format = &format
			d.StartUnix = req.StartUnix
			d.StopUnix = req.StopUnix
			d.StartMs = req.StartMS
			d.StopMs = req.StopMS
			d.DurationSec = req.DurationSec
		})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = decklogClient.SendEvent(ctx, evt)
		}()
	}

	// Send gRPC control message to storage Helmsman via registry using NodeID
	storageNodeID := lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage node not connected"})
		return
	}

	// Generate secure clip hash - no tenant information exposed to edge nodes
	var startMs, durationMs int64
	if req.StartMS != nil {
		startMs = *req.StartMS
	}
	if req.DurationSec != nil {
		durationMs = *req.DurationSec * 1000 // Convert to milliseconds
	}

	clipHash, err := clips.GenerateClipHash(req.InternalName, startMs, durationMs)
	if err != nil {
		logger.WithError(err).Error("Failed to generate clip hash")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate clip hash"})
		return
	}

	// Store clip metadata in database with tenant mapping (internal use only)
	clipID := uuid.New().String()
	_, err = db.Exec(`
		INSERT INTO foghorn.clips (id, tenant_id, stream_id, user_id, clip_hash, stream_name, title, 
						  start_time, duration, node_id, storage_path, status, request_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW())
	`, clipID, req.TenantID, uuid.Nil, uuid.Nil, clipHash, req.InternalName, req.Title,
		startMs, durationMs, storageNodeID, clips.BuildClipStoragePath(req.InternalName, clipHash, format), "requested", reqID)

	if err != nil {
		logger.WithError(err).Error("Failed to store clip metadata in database")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store clip metadata"})
		return
	}

	// Send gRPC message using secure protocol - no tenant info exposed
	clipReq := &pb.ClipPullRequest{
		ClipHash:      clipHash,         // Secure opaque identifier
		StreamName:    req.InternalName, // Stream name (safe to expose)
		Format:        format,
		OutputName:    clipHash, // Use hash as output name
		SourceBaseUrl: deriveMistHTTPBase(ingestHost),
		RequestId:     reqID, // For tracking
	}
	if req.StartUnix != nil {
		clipReq.StartUnix = req.StartUnix
	}
	if req.StopUnix != nil {
		clipReq.StopUnix = req.StopUnix
	}
	if req.StartMS != nil {
		clipReq.StartMs = req.StartMS
	}
	if req.StopMS != nil {
		clipReq.StopMs = req.StopMS
	}
	if req.DurationSec != nil {
		clipReq.DurationSec = req.DurationSec
	}

	if err := control.SendClipPull(storageNodeID, clipReq); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage node unavailable", "details": err.Error()})
		return
	}

	// Emit ClipQueued event to Decklog
	if decklogClient != nil {
		evt := decklog.NewClipLifecycleEvent(c.GetString("tenant_id"), req.InternalName, reqID, pb.ClipLifecycleData_STAGE_QUEUED, func(d *pb.ClipLifecycleData) {
			d.IngestNodeId = &ingestHost
			d.StorageNodeId = &storageNodeID
			d.RoutingDistanceKm = func() *float64 { v := 0.0; return &v }()
			d.Format = &format
			if req.DurationSec != nil {
				d.DurationSec = req.DurationSec
			}
		})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = decklogClient.SendEvent(ctx, evt)
		}()
	}

	c.JSON(http.StatusOK, fapi.CreateClipResponse{
		Status:      "queued",
		IngestHost:  ingestHost,
		StorageHost: storageHost,
		NodeID:      storageNodeID,
		RequestID:   reqID,
		ClipHash:    clipHash,
	})
}

func deriveMistHTTPBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		// assume host:port or hostname only
		host := strings.TrimPrefix(base, "http://")
		host = strings.TrimPrefix(host, "https://")
		parts := strings.Split(host, ":")
		hostname := parts[0]
		port := "8080"
		return "http://" + hostname + ":" + port
	}
	hostname := u.Hostname()
	port := u.Port()
	if port == "" || port == "4242" {
		port = "8080"
	}
	return u.Scheme + "://" + hostname + ":" + port
}

func deriveHelmsmanBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		host := strings.TrimPrefix(base, "http://")
		host = strings.TrimPrefix(host, "https://")
		parts := strings.Split(host, ":")
		hostname := parts[0]
		return "http://" + hostname + ":18007"
	}
	return u.Scheme + "://" + u.Hostname() + ":18007"
}

// postBalancingEvent posts load balancing decisions to Decklog via gRPC
func postBalancingEvent(c *gin.Context, streamName, selectedNode string, score uint64, lat, lon float64, status, details string, nodeLat, nodeLon float64, nodeName string) {

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

	// If no geo headers and geoip is available, use fallback geo enrichment
	if country == "" && geoipReader != nil && clientIP != "" {
		if geoData := geoipReader.Lookup(clientIP); geoData != nil {
			country = geoData.CountryCode
			logger.WithFields(logging.Fields{
				"client_ip":    clientIP,
				"country_code": geoData.CountryCode,
				"city":         geoData.City,
				"latitude":     geoData.Latitude,
				"longitude":    geoData.Longitude,
			}).Debug("Used GeoIP fallback for load balancing event")
		}
	}

	// Extract real IP from CloudFlare (overrides X-Forwarded-For if present)
	cfConnectingIP := c.GetHeader("CF-Connecting-IP")
	if cfConnectingIP != "" {
		clientIP = cfConnectingIP
	}

	// Determine tenant ID from headers (service-to-service calls must set X-Tenant-ID)
	tenantID := c.GetHeader("X-Tenant-ID")

	// Determine NodeID for selected node if known
	selectedNodeID := ""
	if lb != nil {
		if id := lb.GetNodeIDByHost(selectedNode); id != "" {
			selectedNodeID = id
		}
	}

	// Compute routing distance if we have both points
	routingDistanceKm := 0.0
	if lat != 0 && lon != 0 && nodeLat != 0 && nodeLon != 0 {
		const toRad = math.Pi / 180.0
		lat1 := lat * toRad
		lon1 := lon * toRad
		lat2 := nodeLat * toRad
		lon2 := nodeLon * toRad
		val := math.Sin(lat1)*math.Sin(lat2) + math.Cos(lat1)*math.Cos(lat2)*math.Cos(lon1-lon2)
		if val > 1 {
			val = 1
		}
		if val < -1 {
			val = -1
		}
		angle := math.Acos(val)
		routingDistanceKm = 6371.0 * angle
	}

	// Create Event with LoadBalancingData
	event := decklog.NewLoadBalancingEvent(tenantID, streamName, selectedNode, selectedNodeID, clientIP, country, status, details, lat, lon, score, nodeLat, nodeLon, nodeName, routingDistanceKm)

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

func getLatLon(c *gin.Context, query url.Values, queryKey, headerKey string) float64 {
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

func getTagAdjustments(c *gin.Context, query url.Values) map[string]int {
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

// === CLIP QUERY ENDPOINTS ===

// HandleGetClips lists clips for a specific tenant with pagination
func HandleGetClips(c *gin.Context) {
	tenantID := c.Query("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id query parameter is required"})
		return
	}

	status := c.Query("status")

	// Parse pagination parameters
	page := 1
	limit := 20
	if pageStr := c.Query("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	offset := (page - 1) * limit

	// Build query with optional status filter
	var countQuery, selectQuery string
	var countArgs, selectArgs []interface{}

	if status != "" {
		countQuery = "SELECT COUNT(*) FROM foghorn.clips WHERE tenant_id = $1 AND status = $2 AND status != 'deleted'"
		countArgs = []interface{}{tenantID, status}
		selectQuery = `
			SELECT id, clip_hash, stream_name, COALESCE(title, ''), start_time, duration,
			       COALESCE(node_id, ''), COALESCE(storage_path, ''), size_bytes,
			       status, access_count, created_at
			FROM foghorn.clips 
			WHERE tenant_id = $1 AND status = $2 AND status != 'deleted'
			ORDER BY created_at DESC 
			LIMIT $3 OFFSET $4
		`
		selectArgs = []interface{}{tenantID, status, limit, offset}
	} else {
		countQuery = "SELECT COUNT(*) FROM foghorn.clips WHERE tenant_id = $1 AND status != 'deleted'"
		countArgs = []interface{}{tenantID}
		selectQuery = `
			SELECT id, clip_hash, stream_name, COALESCE(title, ''), start_time, duration,
			       COALESCE(node_id, ''), COALESCE(storage_path, ''), size_bytes,
			       status, access_count, created_at
			FROM foghorn.clips 
			WHERE tenant_id = $1 AND status != 'deleted'
			ORDER BY created_at DESC 
			LIMIT $2 OFFSET $3
		`
		selectArgs = []interface{}{tenantID, limit, offset}
	}

	// Count total clips
	var total int
	if err := db.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
		logger.WithError(err).Error("Failed to count clips")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count clips"})
		return
	}

	// Fetch clips
	rows, err := db.Query(selectQuery, selectArgs...)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch clips")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch clips"})
		return
	}
	defer rows.Close()

	var clips []fapi.ClipInfo
	for rows.Next() {
		var clip fapi.ClipInfo
		var sizeBytes sql.NullInt64

		err := rows.Scan(
			&clip.ID, &clip.ClipHash, &clip.StreamName, &clip.Title,
			&clip.StartTime, &clip.Duration, &clip.NodeID, &clip.StoragePath,
			&sizeBytes, &clip.Status, &clip.AccessCount, &clip.CreatedAt,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan clip")
			continue
		}

		if sizeBytes.Valid {
			clip.SizeBytes = &sizeBytes.Int64
		}

		clips = append(clips, clip)
	}

	c.JSON(http.StatusOK, fapi.ClipsListResponse{
		Clips: clips,
		Total: total,
		Page:  page,
		Limit: limit,
	})
}

// HandleGetClip retrieves a specific clip by hash
func HandleGetClip(c *gin.Context) {
	clipHash := c.Param("clip_hash")
	tenantID := c.Query("tenant_id") // Required for authorization

	if clipHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clip_hash is required"})
		return
	}

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id query parameter is required"})
		return
	}

	query := `
		SELECT id, clip_hash, stream_name, COALESCE(title, ''), start_time, duration,
		       COALESCE(node_id, ''), COALESCE(storage_path, ''), size_bytes,
		       status, access_count, created_at
		FROM foghorn.clips 
		WHERE clip_hash = $1 AND tenant_id = $2 AND status != 'deleted'
	`

	var clip fapi.ClipInfo
	var sizeBytes sql.NullInt64

	err := db.QueryRow(query, clipHash, tenantID).Scan(
		&clip.ID, &clip.ClipHash, &clip.StreamName, &clip.Title,
		&clip.StartTime, &clip.Duration, &clip.NodeID, &clip.StoragePath,
		&sizeBytes, &clip.Status, &clip.AccessCount, &clip.CreatedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Clip not found"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to fetch clip")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch clip"})
		return
	}

	if sizeBytes.Valid {
		clip.SizeBytes = &sizeBytes.Int64
	}

	c.JSON(http.StatusOK, clip)
}

// HandleGetClipNode retrieves node information for clip viewing
func HandleGetClipNode(c *gin.Context) {
	clipHash := c.Param("clip_hash")
	tenantID := c.Query("tenant_id") // Required for authorization

	if clipHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clip_hash is required"})
		return
	}

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id query parameter is required"})
		return
	}

	// Get clip information and verify ownership
	var nodeID, status string
	query := "SELECT COALESCE(node_id, ''), status FROM foghorn.clips WHERE clip_hash = $1 AND tenant_id = $2 AND status != 'deleted'"
	err := db.QueryRow(query, clipHash, tenantID).Scan(&nodeID, &status)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Clip not found"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to fetch clip for node info")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch clip"})
		return
	}

	if status != "ready" {
		c.JSON(http.StatusConflict, gin.H{"error": "Clip is not ready for viewing", "status": status})
		return
	}

	if nodeID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No storage node available for this clip"})
		return
	}

	// Get node outputs from the database to generate URLs
	var outputsJSON []byte
	var baseURL string

	nodeQuery := "SELECT outputs, COALESCE(base_url, '') FROM foghorn.node_outputs WHERE node_id = $1"
	err = db.QueryRow(nodeQuery, nodeID).Scan(&outputsJSON, &baseURL)

	if err == sql.ErrNoRows {
		logger.WithFields(logging.Fields{
			"node_id":   nodeID,
			"clip_hash": clipHash,
		}).Warn("Node outputs not found - node may not have reported yet")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Node outputs not available"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to fetch node outputs")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch node outputs"})
		return
	}

	// Parse MistServer outputs
	var outputs map[string]interface{}
	if err := json.Unmarshal(outputsJSON, &outputs); err != nil {
		logger.WithError(err).Error("Failed to parse node outputs JSON")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid node outputs"})
		return
	}

	// Generate URLs for each protocol
	urls := make(map[string]string)
	vodStreamName := fmt.Sprintf("vod+%s", clipHash)

	if baseURL == "" {
		baseURL = "https://unknown-node"
		logger.WithField("node_id", nodeID).Warn("No base URL for node, using placeholder")
	}

	// Common protocols for VOD
	protocols := []string{"HLS", "DASH", "webrtc", "progressive"}
	for _, protocol := range protocols {
		if protocolData, exists := outputs[protocol]; exists {
			if protocolMap, ok := protocolData.(map[string]interface{}); ok {
				if urlPattern, exists := protocolMap["url"]; exists {
					if urlStr, ok := urlPattern.(string); ok {
						// Replace wildcard with VOD stream name
						finalURL := strings.ReplaceAll(urlStr, "$", vodStreamName)
						if !strings.HasPrefix(finalURL, "http") {
							finalURL = baseURL + finalURL
						}
						urls[strings.ToLower(protocol)] = finalURL
					}
				}
			}
		}
	}

	// Update access tracking
	go func() {
		updateQuery := "UPDATE foghorn.clips SET access_count = access_count + 1, last_accessed_at = NOW() WHERE clip_hash = $1 AND tenant_id = $2"
		if _, err := db.Exec(updateQuery, clipHash, tenantID); err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"clip_hash": clipHash,
				"tenant_id": tenantID,
			}).Error("Failed to update clip access tracking")
		}
	}()

	c.JSON(http.StatusOK, fapi.ClipNodeInfo{
		NodeID:   nodeID,
		BaseURL:  baseURL,
		Outputs:  outputs,
		ClipHash: clipHash,
		Status:   status,
	})
}

// HandleResolveClip resolves clip hash to tenant and stream info for analytics
// This is used by Helmsman for USER_NEW/USER_END webhook tenant resolution
func HandleResolveClip(c *gin.Context) {
	clipHash := c.Param("clip_hash")

	if clipHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clip_hash is required"})
		return
	}

	// Query for tenant_id and stream_name (no authorization needed for this endpoint)
	query := `
		SELECT tenant_id, stream_name
		FROM foghorn.clips 
		WHERE clip_hash = $1 AND status != 'deleted'
		LIMIT 1
	`

	var tenantID, streamName string
	err := db.QueryRow(query, clipHash).Scan(&tenantID, &streamName)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Clip not found"})
		return
	}

	if err != nil {
		logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to resolve clip")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve clip"})
		return
	}

	// Return minimal resolution info
	c.JSON(http.StatusOK, gin.H{
		"clip_hash":   clipHash,
		"tenant_id":   tenantID,
		"stream_name": streamName,
	})
}

// HandleDeleteClip soft-deletes a clip by setting status to 'deleted'
func HandleDeleteClip(c *gin.Context) {
	clipHash := c.Param("clip_hash")
	tenantID := c.Query("tenant_id") // Required for authorization

	if clipHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clip_hash is required"})
		return
	}

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id query parameter is required"})
		return
	}

	// Verify clip exists and is owned by the tenant
	var currentStatus string
	checkQuery := "SELECT status FROM foghorn.clips WHERE clip_hash = $1 AND tenant_id = $2"
	err := db.QueryRow(checkQuery, clipHash, tenantID).Scan(&currentStatus)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Clip not found"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to check clip existence")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check clip"})
		return
	}

	if currentStatus == "deleted" {
		c.JSON(http.StatusConflict, gin.H{"error": "Clip is already deleted"})
		return
	}

	// Soft delete the clip
	deleteQuery := "UPDATE foghorn.clips SET status = 'deleted', updated_at = NOW() WHERE clip_hash = $1 AND tenant_id = $2"
	result, err := db.Exec(deleteQuery, clipHash, tenantID)

	if err != nil {
		logger.WithError(err).Error("Failed to delete clip")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete clip"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Clip not found"})
		return
	}

	logger.WithFields(logging.Fields{
		"clip_hash": clipHash,
		"tenant_id": tenantID,
	}).Info("Clip soft-deleted successfully")

	c.JSON(http.StatusOK, gin.H{"message": "Clip deleted successfully"})
}

// === DVR MANAGEMENT ENDPOINTS ===

func getDVRContextByRequestID(requestID string) (string, string) {
	if db == nil || requestID == "" {
		return "", ""
	}
	var tenantID, internalName string
	_ = db.QueryRow(`SELECT tenant_id::text, internal_name FROM foghorn.dvr_requests WHERE request_hash = $1`, requestID).Scan(&tenantID, &internalName)
	return tenantID, internalName
}

// HandleStartDVRRecording orchestrates DVR recording start
func HandleStartDVRRecording(c *gin.Context) {
	var req struct {
		TenantID     string `json:"tenant_id"`
		InternalName string `json:"internal_name"`
		StreamID     string `json:"stream_id,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	if req.InternalName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal_name is required"})
		return
	}

	if req.TenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	q := c.Request.URL.Query()
	lat := getLatLon(c, q, "lat", "X-Latitude")
	lon := getLatLon(c, q, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, q)

	// Select ingest node (where stream is active)
	ictx := context.WithValue(c.Request.Context(), "cap", "ingest")
	ingestHost, _, _, _, _, err := lb.GetBestNodeWithScore(ictx, req.InternalName, lat, lon, tagAdjust, c.ClientIP())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no ingest node available", "details": err.Error()})
		return
	}

	// Select storage node (cap=storage)
	sctx := context.WithValue(c.Request.Context(), "cap", "storage")
	storageHost, _, _, _, _, err := lb.GetBestNodeWithScore(sctx, "", 0, 0, map[string]int{}, "")
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no storage node available", "details": err.Error()})
		return
	}

	storageNodeID := lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage node not connected"})
		return
	}

	// Generate DVR hash
	dvrHash, err := dvr.GenerateDVRHash()
	if err != nil {
		logger.WithError(err).Error("Failed to generate DVR hash")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate DVR hash"})
		return
	}

	// Store DVR request metadata with minimal state
	var streamID *uuid.UUID
	if req.StreamID != "" {
		if parsed, err := uuid.Parse(req.StreamID); err == nil {
			streamID = &parsed
		}
	}

	_, err = db.Exec(`
		INSERT INTO foghorn.dvr_requests (request_hash, tenant_id, stream_id, internal_name, 
		                                 storage_node_id, storage_node_url, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`, dvrHash, req.TenantID, streamID, req.InternalName, storageNodeID, storageHost, "requested")

	if err != nil {
		logger.WithError(err).Error("Failed to store DVR request in database")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store DVR request"})
		return
	}

	// Get recording configuration (default values)
	config := dvr.DVRConfig{
		Enabled:         true,
		RetentionDays:   30,
		Format:          "ts",
		SegmentDuration: 6,
	}

	// Send gRPC control message to storage Helmsman
	dvrReq := &pb.DVRStartRequest{
		DvrHash:       dvrHash,
		InternalName:  req.InternalName,
		SourceBaseUrl: deriveMistHTTPBase(ingestHost),
		RequestId:     dvrHash,
		Config: &pb.DVRConfig{
			Enabled:         config.Enabled,
			RetentionDays:   int32(config.RetentionDays),
			Format:          config.Format,
			SegmentDuration: int32(config.SegmentDuration),
		},
	}

	if err := control.SendDVRStart(storageNodeID, dvrReq); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage node unavailable", "details": err.Error()})
		return
	}

	// Emit DVR started event to analytics
	if decklogClient != nil {
		evt := decklog.NewDVRLifecycleEvent(req.TenantID, req.InternalName, dvrHash, pb.DVRLifecycleData_STAGE_REQUESTED, func(d *pb.DVRLifecycleData) {
			d.IngestNodeId = &ingestHost
			d.StorageNodeId = &storageNodeID
		})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = decklogClient.SendEvent(ctx, evt)
		}()
	}

	c.JSON(http.StatusOK, fapi.StartDVRResponse{
		Status:        "started",
		DVRHash:       dvrHash,
		IngestHost:    ingestHost,
		StorageHost:   storageHost,
		StorageNodeID: storageNodeID,
	})
}

// HandleStopDVRRecording orchestrates DVR recording stop
func HandleStopDVRRecording(c *gin.Context) {
	dvrHash := c.Param("dvr_hash")
	tenantID := c.Query("tenant_id")

	if dvrHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dvr_hash is required"})
		return
	}

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id query parameter is required"})
		return
	}

	// Get DVR request info and verify ownership
	var nodeID, status, internalName string
	query := "SELECT COALESCE(storage_node_id, ''), status, internal_name FROM foghorn.dvr_requests WHERE request_hash = $1 AND tenant_id = $2"
	err := db.QueryRow(query, dvrHash, tenantID).Scan(&nodeID, &status, &internalName)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "DVR recording not found"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to fetch DVR request")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch DVR request"})
		return
	}

	if status == "completed" || status == "failed" {
		c.JSON(http.StatusConflict, gin.H{"error": "DVR recording already finished", "status": status})
		return
	}

	if nodeID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No storage node available for this DVR"})
		return
	}

	// Send stop command to storage Helmsman
	stopReq := &pb.DVRStopRequest{
		DvrHash:   dvrHash,
		RequestId: dvrHash,
	}

	if err := control.SendDVRStop(nodeID, stopReq); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage node unavailable", "details": err.Error()})
		return
	}

	// Update status to stopping
	_, err = db.Exec("UPDATE foghorn.dvr_requests SET status = 'stopping', updated_at = NOW() WHERE request_hash = $1 AND tenant_id = $2", dvrHash, tenantID)
	if err != nil {
		logger.WithError(err).Error("Failed to update DVR status to stopping")
	}

	// Emit DVR stopping event to analytics
	if decklogClient != nil {
		evt := decklog.NewDVRLifecycleEvent(tenantID, internalName, dvrHash, pb.DVRLifecycleData_STAGE_STOPPING, nil)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = decklogClient.SendEvent(ctx, evt)
		}()
	}

	c.JSON(http.StatusOK, gin.H{"status": "stopping", "dvr_hash": dvrHash})
}

// HandleGetDVRStatus retrieves DVR recording status
func HandleGetDVRStatus(c *gin.Context) {
	dvrHash := c.Param("dvr_hash")
	tenantID := c.Query("tenant_id")

	if dvrHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dvr_hash is required"})
		return
	}

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id query parameter is required"})
		return
	}

	query := `
		SELECT request_hash, internal_name, storage_node_id, status, 
		       started_at, ended_at, duration_seconds, size_bytes, manifest_path, 
		       error_message, created_at, updated_at
		FROM foghorn.dvr_requests
		WHERE request_hash = $1 AND tenant_id = $2
	`

	var dvr fapi.DVRInfo
	var startedAt, endedAt sql.NullTime
	var durationSec sql.NullInt32
	var sizeBytes sql.NullInt64
	var manifestPath, errorMessage sql.NullString

	err := db.QueryRow(query, dvrHash, tenantID).Scan(
		&dvr.DVRHash, &dvr.InternalName, &dvr.StorageNodeID, &dvr.Status,
		&startedAt, &endedAt, &durationSec, &sizeBytes, &manifestPath,
		&errorMessage, &dvr.CreatedAt, &dvr.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "DVR recording not found"})
		return
	} else if err != nil {
		logger.WithError(err).Error("Failed to fetch DVR status")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch DVR status"})
		return
	}

	if startedAt.Valid {
		dvr.StartedAt = &startedAt.Time
	}
	if endedAt.Valid {
		dvr.EndedAt = &endedAt.Time
	}
	if durationSec.Valid {
		dvr.DurationSeconds = &durationSec.Int32
	}
	if sizeBytes.Valid {
		dvr.SizeBytes = &sizeBytes.Int64
	}
	if manifestPath.Valid {
		dvr.ManifestPath = manifestPath.String
	}
	if errorMessage.Valid {
		dvr.ErrorMessage = errorMessage.String
	}

	c.JSON(http.StatusOK, dvr)
}

// HandleUpdateDVRProgress handles progress updates from Helmsman
func HandleUpdateDVRProgress(c *gin.Context) {
	var update struct {
		DVRHash         string `json:"dvr_hash"`
		NodeID          string `json:"node_id"`
		Status          string `json:"status"`
		StartedAt       *int64 `json:"started_at,omitempty"`
		EndedAt         *int64 `json:"ended_at,omitempty"`
		DurationSeconds *int32 `json:"duration_seconds,omitempty"`
		SizeBytes       *int64 `json:"size_bytes,omitempty"`
		ManifestPath    string `json:"manifest_path,omitempty"`
		ErrorMessage    string `json:"error_message,omitempty"`
	}

	if err := c.ShouldBindJSON(&update); err != nil {
		logger.WithError(err).Error("Failed to parse DVR progress update")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid update format"})
		return
	}

	// Validate required fields
	if update.DVRHash == "" || update.NodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dvr_hash and node_id are required"})
		return
	}

	// Update DVR request status in database
	query := `
		UPDATE foghorn.dvr_requests 
		SET status = $2, started_at = $3, ended_at = $4, duration_seconds = $5, 
		    size_bytes = $6, manifest_path = $7, error_message = $8, updated_at = NOW()
		WHERE request_hash = $1 AND storage_node_id = $9
	`

	var startedAt, endedAt interface{}
	if update.StartedAt != nil {
		t := time.Unix(*update.StartedAt, 0)
		startedAt = t
	}
	if update.EndedAt != nil {
		t := time.Unix(*update.EndedAt, 0)
		endedAt = t
	}

	result, err := db.Exec(query, update.DVRHash, update.Status, startedAt, endedAt,
		update.DurationSeconds, update.SizeBytes, update.ManifestPath, update.ErrorMessage, update.NodeID)

	if err != nil {
		logger.WithError(err).Error("Failed to update DVR progress")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update DVR progress"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "DVR request not found or node mismatch"})
		return
	}

	// Get tenant and stream info for analytics
	tenantID, internalName := getDVRContextByRequestID(update.DVRHash)

	// Emit appropriate lifecycle event
	if decklogClient != nil && tenantID != "" {
		var stage pb.DVRLifecycleData_Stage
		switch update.Status {
		case "recording":
			stage = pb.DVRLifecycleData_STAGE_RECORDING
		case "completed":
			stage = pb.DVRLifecycleData_STAGE_COMPLETED
		case "failed":
			stage = pb.DVRLifecycleData_STAGE_FAILED
		default:
			stage = pb.DVRLifecycleData_STAGE_PROGRESS
		}

		evt := decklog.NewDVRLifecycleEvent(tenantID, internalName, update.DVRHash, stage, func(d *pb.DVRLifecycleData) {
			if update.DurationSeconds != nil {
				duration := int64(*update.DurationSeconds)
				d.DurationSec = &duration
			}
			if update.SizeBytes != nil {
				sizeBytes := uint64(*update.SizeBytes)
				d.SizeBytes = &sizeBytes
			}
			if update.ManifestPath != "" {
				d.ManifestPath = &update.ManifestPath
			}
			if update.ErrorMessage != "" {
				d.Error = &update.ErrorMessage
			}
		})

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = decklogClient.SendEvent(ctx, evt)
		}()
	}

	logger.WithFields(logging.Fields{
		"dvr_hash": update.DVRHash,
		"node_id":  update.NodeID,
		"status":   update.Status,
	}).Info("DVR progress updated")

	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

// ViewerEndpointRequest represents the request for viewer endpoint resolution
type ViewerEndpointRequest struct {
	ContentType    string                 `json:"content_type" binding:"required"`
	ContentID      string                 `json:"content_id" binding:"required"`
	ViewerIP       string                 `json:"viewer_ip,omitempty"` // Real viewer IP passed from Gateway
	ViewerLocation *ViewerLocationRequest `json:"viewer_location,omitempty"`
}

type ViewerLocationRequest struct {
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Country   string   `json:"country,omitempty"`
	City      string   `json:"city,omitempty"`
}

// ViewerEndpointResponse represents the resolved viewing endpoints
type ViewerEndpointResponse struct {
	Primary   ViewerEndpoint   `json:"primary"`
	Fallbacks []ViewerEndpoint `json:"fallbacks"`
	Metadata  ContentMetadata  `json:"metadata"`
}

type ViewerEndpoint struct {
	NodeID       string                    `json:"node_id"`
	BaseURL      string                    `json:"base_url"`
	Protocol     string                    `json:"protocol"`
	URL          string                    `json:"url"`
	GeoDistance  float64                   `json:"geo_distance"`
	LoadScore    float64                   `json:"load_score"`
	HealthScore  float64                   `json:"health_score"`
	Capabilities OutputCapability          `json:"capabilities"`
	Outputs      map[string]OutputEndpoint `json:"outputs,omitempty"`
}

type OutputCapability struct {
	SupportsSeek          bool     `json:"supports_seek"`
	SupportsQualitySwitch bool     `json:"supports_quality_switch"`
	MaxBitrate            int      `json:"max_bitrate,omitempty"`
	HasAudio              bool     `json:"has_audio"`
	HasVideo              bool     `json:"has_video"`
	Codecs                []string `json:"codecs,omitempty"`
}

type OutputEndpoint struct {
	Protocol     string           `json:"protocol"`
	URL          string           `json:"url"`
	Capabilities OutputCapability `json:"capabilities"`
}

// buildOutputCapabilities returns default capabilities for a given protocol and content type
func buildOutputCapabilities(protocol string, isLive bool) OutputCapability {
	caps := OutputCapability{
		SupportsSeek:          !isLive,
		SupportsQualitySwitch: true,
		HasAudio:              true,
		HasVideo:              true,
	}
	switch strings.ToUpper(protocol) {
	case "WHEP":
		caps.SupportsQualitySwitch = false
		caps.SupportsSeek = false
	case "MP4", "WEBM":
		caps.SupportsQualitySwitch = false
		caps.SupportsSeek = true
	}
	return caps
}

func ensureTrailingSlash(s string) string {
	if !strings.HasSuffix(s, "/") {
		return s + "/"
	}
	return s
}

// deriveWHEPFromHTML derives a WHEP URL by replacing the trailing .../stream.html with .../webrtc/stream
func deriveWHEPFromHTML(htmlURL string) string {
	u, err := url.Parse(htmlURL)
	if err != nil {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if !strings.HasSuffix(last, ".html") {
		return ""
	}
	stream := strings.TrimSuffix(last, ".html")
	base := parts[:len(parts)-1]
	base = append(base, "webrtc", stream)
	u.Path = "/" + strings.Join(base, "/")
	return u.String()
}

// resolveTemplateURL replaces placeholders in Mist outputs ($ for stream name, HOST for hostname)
func resolveTemplateURL(raw interface{}, baseURL, streamName string) string {
	var s string
	switch v := raw.(type) {
	case string:
		s = v
	case []interface{}:
		if len(v) > 0 {
			if ss, ok := v[0].(string); ok {
				s = ss
			}
		}
	default:
		return ""
	}
	if s == "" {
		return ""
	}
	s = strings.Replace(s, "$", streamName, -1)
	if strings.Contains(s, "HOST") {
		host := baseURL
		if strings.HasPrefix(host, "https://") {
			host = strings.TrimPrefix(host, "https://")
		}
		if strings.HasPrefix(host, "http://") {
			host = strings.TrimPrefix(host, "http://")
		}
		host = strings.TrimSuffix(host, "/")
		s = strings.Replace(s, "HOST", host, -1)
	}
	s = strings.Trim(s, "[]\"")
	return s
}

// buildOutputsMap constructs the per-protocol outputs for a node/stream
func buildOutputsMap(baseURL string, rawOutputs map[string]interface{}, streamName string, isLive bool) map[string]OutputEndpoint {
	outputs := make(map[string]OutputEndpoint)

	base := ensureTrailingSlash(baseURL)
	html := base + streamName + ".html"
	outputs["MIST_HTML"] = OutputEndpoint{Protocol: "MIST_HTML", URL: html, Capabilities: buildOutputCapabilities("MIST_HTML", isLive)}
	outputs["PLAYER_JS"] = OutputEndpoint{Protocol: "PLAYER_JS", URL: base + "player.js", Capabilities: buildOutputCapabilities("PLAYER_JS", isLive)}

	// Prefer explicit WHEP if present; otherwise derive from HTML
	if raw, ok := rawOutputs["WHEP"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WHEP"] = OutputEndpoint{Protocol: "WHEP", URL: u, Capabilities: buildOutputCapabilities("WHEP", isLive)}
		}
	}
	if _, ok := outputs["WHEP"]; !ok {
		if u := deriveWHEPFromHTML(html); u != "" {
			outputs["WHEP"] = OutputEndpoint{Protocol: "WHEP", URL: u, Capabilities: buildOutputCapabilities("WHEP", isLive)}
		}
	}

	if raw, ok := rawOutputs["HLS"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HLS"] = OutputEndpoint{Protocol: "HLS", URL: u, Capabilities: buildOutputCapabilities("HLS", isLive)}
		}
	}
	if raw, ok := rawOutputs["DASH"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["DASH"] = OutputEndpoint{Protocol: "DASH", URL: u, Capabilities: buildOutputCapabilities("DASH", isLive)}
		}
	}
	if raw, ok := rawOutputs["MP4"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["MP4"] = OutputEndpoint{Protocol: "MP4", URL: u, Capabilities: buildOutputCapabilities("MP4", isLive)}
		}
	}
	if raw, ok := rawOutputs["WEBM"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WEBM"] = OutputEndpoint{Protocol: "WEBM", URL: u, Capabilities: buildOutputCapabilities("WEBM", isLive)}
		}
	}
	if raw, ok := rawOutputs["HTTP"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HTTP"] = OutputEndpoint{Protocol: "HTTP", URL: u, Capabilities: buildOutputCapabilities("HTTP", isLive)}
		}
	}

	return outputs
}

type ContentMetadata struct {
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	Duration      *int   `json:"duration,omitempty"` // seconds, null for live
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at,omitempty"`
	IsLive        bool   `json:"is_live"`
	ViewCount     *int   `json:"view_count,omitempty"`
	RecordingSize *int64 `json:"recording_size,omitempty"` // for DVR, bytes
	ClipSource    string `json:"clip_source,omitempty"`    // for clips
}

// HandleResolveViewerEndpoint resolves optimal viewing endpoints for different content types
func HandleResolveViewerEndpoint(c *gin.Context) {
	var req ViewerEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request: %v", err)})
		return
	}

	// Validate required fields
	if req.ContentType == "" || req.ContentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content_type and content_id are required"})
		return
	}

	// Validate content type
	if req.ContentType != "live" && req.ContentType != "dvr" && req.ContentType != "clip" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content_type must be 'live', 'dvr', or 'clip'"})
		return
	}

	// GeoIP resolution using viewer IP (this is the key missing piece!)
	var lat, lon float64 = 0.0, 0.0
	var country, city string

	viewerIP := req.ViewerIP
	if viewerIP == "" {
		// Fallback to request IP if not provided
		viewerIP = c.ClientIP()
	}

	if viewerIP != "" && geoipReader != nil {
		if geoData := geoipReader.Lookup(viewerIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
			country = geoData.CountryCode
			city = geoData.City

			logger.WithFields(logging.Fields{
				"viewer_ip":    viewerIP,
				"country_code": country,
				"city":         city,
				"latitude":     lat,
				"longitude":    lon,
			}).Debug("Resolved viewer location via GeoIP")
		}
	}

	var response ViewerEndpointResponse
	var err error

	switch req.ContentType {
	case "live":
		response, err = resolveLiveViewerEndpoint(req, lat, lon)
	case "dvr":
		response, err = resolveDVRViewerEndpoint(req, lat, lon)
	case "clip":
		response, err = resolveClipViewerEndpoint(req, lat, lon)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid content type"})
		return
	}

	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"content_type": req.ContentType,
			"content_id":   req.ContentID,
		}).Error("Failed to resolve viewer endpoint")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Record metrics
	if metrics != nil {
		metrics.RoutingDecisions.WithLabelValues("viewer", req.ContentType, response.Primary.NodeID).Inc()
	}

	logger.WithFields(logging.Fields{
		"content_type": req.ContentType,
		"content_id":   req.ContentID,
		"node_id":      response.Primary.NodeID,
		"protocol":     response.Primary.Protocol,
	}).Info("Resolved viewer endpoint")

	c.JSON(http.StatusOK, response)
}

// resolveLiveViewerEndpoint uses load balancer to find optimal edge nodes with fallbacks
func resolveLiveViewerEndpoint(req ViewerEndpointRequest, lat, lon float64) (ViewerEndpointResponse, error) {
	// Use load balancer to get top 5 edge nodes for fallbacks
	ctx := context.WithValue(context.Background(), "cap", "edge")
	nodes, err := lb.GetTopNodesWithScores(ctx, req.ContentID, lat, lon, make(map[string]int), "", 5)
	if err != nil {
		return ViewerEndpointResponse{}, fmt.Errorf("no suitable edge nodes available: %v", err)
	}

	var endpoints []ViewerEndpoint
	streamName := "live+" + req.ContentID

	for _, node := range nodes {
		// Get outputs from control package
		nodeOutputs, exists := control.GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		var protocol, url string
		if webrtcURL, ok := nodeOutputs.Outputs["WebRTC"].(string); ok {
			protocol = "webrtc"
			url = strings.Replace(webrtcURL, "$", streamName, -1)
			url = strings.Replace(url, "HOST", strings.TrimPrefix(nodeOutputs.BaseURL, "https://"), -1)
		} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
			protocol = "hls"
			url = strings.Replace(hlsURL, "$", streamName, -1)
			url = strings.Trim(url, "[\"")
		}

		if url == "" {
			continue
		}

		// Calculate actual geo distance
		geoDistance := 0.0
		if lat != 0 && lon != 0 && node.GeoLatitude != 0 && node.GeoLongitude != 0 {
			const toRad = math.Pi / 180.0
			lat1 := lat * toRad
			lon1 := lon * toRad
			lat2 := node.GeoLatitude * toRad
			lon2 := node.GeoLongitude * toRad
			val := math.Sin(lat1)*math.Sin(lat2) + math.Cos(lat1)*math.Cos(lat2)*math.Cos(lon1-lon2)
			if val > 1 {
				val = 1
			}
			if val < -1 {
				val = -1
			}
			angle := math.Acos(val)
			geoDistance = 6371.0 * angle
		}

		endpoint := ViewerEndpoint{
			NodeID:       node.NodeID,
			BaseURL:      nodeOutputs.BaseURL,
			Protocol:     protocol,
			URL:          url,
			GeoDistance:  geoDistance,
			LoadScore:    float64(node.Score),
			HealthScore:  1.0,
			Capabilities: OutputCapability{},
			Outputs:      buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, streamName, true),
		}
		endpoints = append(endpoints, endpoint)
	}

	if len(endpoints) == 0 {
		return ViewerEndpointResponse{}, fmt.Errorf("no nodes with suitable outputs available")
	}

	return ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  ContentMetadata{Status: "live", IsLive: true},
	}, nil
}

// resolveDVRViewerEndpoint queries database for DVR storage node
func resolveDVRViewerEndpoint(req ViewerEndpointRequest, lat, lon float64) (ViewerEndpointResponse, error) {
	var nodeID, status string
	var duration *int
	var recordingSize *int64
	var createdAt time.Time

	err := db.QueryRow(`
		SELECT storage_node_id, status, duration_seconds, size_bytes, created_at
		FROM foghorn.dvr_requests 
		WHERE request_hash = $1 AND status = 'completed'
	`, req.ContentID).Scan(&nodeID, &status, &duration, &recordingSize, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return ViewerEndpointResponse{}, fmt.Errorf("DVR recording not found")
		}
		return ViewerEndpointResponse{}, fmt.Errorf("failed to query DVR: %v", err)
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)

	if !exists || nodeOutputs.Outputs == nil {
		return ViewerEndpointResponse{}, fmt.Errorf("storage node outputs not available")
	}

	streamName := "vod+" + req.ContentID
	var protocol, url string

	if httpURL, ok := nodeOutputs.Outputs["HTTP"].(string); ok {
		protocol = "http"
		url = strings.Replace(httpURL, "$", streamName, -1)
		url = strings.Trim(url, "[\"")
	} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		url = strings.Replace(hlsURL, "$", streamName, -1)
		url = strings.Trim(url, "[\"")
	}

	if url == "" {
		return ViewerEndpointResponse{}, fmt.Errorf("no suitable outputs for DVR playback")
	}

	endpoint := ViewerEndpoint{
		NodeID:       nodeID,
		BaseURL:      nodeOutputs.BaseURL,
		Protocol:     protocol,
		URL:          url,
		Capabilities: OutputCapability{},
		Outputs:      buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, streamName, false),
	}

	return ViewerEndpointResponse{
		Primary: endpoint,
		Metadata: ContentMetadata{
			Status:        status,
			IsLive:        false,
			Duration:      duration,
			RecordingSize: recordingSize,
			CreatedAt:     createdAt.Format(time.RFC3339),
		},
	}, nil
}

// resolveClipViewerEndpoint queries database for clip storage node
func resolveClipViewerEndpoint(req ViewerEndpointRequest, lat, lon float64) (ViewerEndpointResponse, error) {
	var nodeID, status, sourceStream string
	var duration *int
	var fileSize *int64
	var createdAt time.Time

	err := db.QueryRow(`
		SELECT node_id, status, stream_name, duration, size_bytes, created_at
		FROM foghorn.clips 
		WHERE clip_hash = $1 AND status = 'ready'
	`, req.ContentID).Scan(&nodeID, &status, &sourceStream, &duration, &fileSize, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return ViewerEndpointResponse{}, fmt.Errorf("clip not found")
		}
		return ViewerEndpointResponse{}, fmt.Errorf("failed to query clip: %v", err)
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)

	if !exists || nodeOutputs.Outputs == nil {
		return ViewerEndpointResponse{}, fmt.Errorf("storage node outputs not available")
	}

	streamName := "vod+" + req.ContentID
	var protocol, url string

	if httpURL, ok := nodeOutputs.Outputs["HTTP"].(string); ok {
		protocol = "http"
		url = strings.Replace(httpURL, "$", streamName, -1)
		url = strings.Trim(url, "[\"")
	} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		url = strings.Replace(hlsURL, "$", streamName, -1)
		url = strings.Trim(url, "[\"")
	}

	if url == "" {
		return ViewerEndpointResponse{}, fmt.Errorf("no suitable outputs for clip playback")
	}

	endpoint := ViewerEndpoint{
		NodeID:       nodeID,
		BaseURL:      nodeOutputs.BaseURL,
		Protocol:     protocol,
		URL:          url,
		Capabilities: OutputCapability{},
		Outputs:      buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, streamName, false),
	}

	return ViewerEndpointResponse{
		Primary: endpoint,
		Metadata: ContentMetadata{
			Status:     status,
			IsLive:     false,
			Duration:   duration,
			ClipSource: sourceStream,
			CreatedAt:  createdAt.Format(time.RFC3339),
		},
	}, nil
}

// HandleGetDVRRecordings lists DVR recordings for a tenant
func HandleGetDVRRecordings(c *gin.Context) {
	tenantID := c.Query("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id query parameter is required"})
		return
	}

	status := c.Query("status")
	internalName := c.Query("internal_name")

	// Parse pagination parameters
	page := 1
	limit := 20
	if pageStr := c.Query("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	offset := (page - 1) * limit

	// Build query with filters
	var countQuery, selectQuery string
	var countArgs, selectArgs []interface{}

	whereConditions := []string{"tenant_id = $1"}
	countArgs = append(countArgs, tenantID)
	selectArgs = append(selectArgs, tenantID)
	paramCount := 1

	if status != "" {
		paramCount++
		whereConditions = append(whereConditions, fmt.Sprintf("status = $%d", paramCount))
		countArgs = append(countArgs, status)
		selectArgs = append(selectArgs, status)
	}

	if internalName != "" {
		paramCount++
		whereConditions = append(whereConditions, fmt.Sprintf("internal_name = $%d", paramCount))
		countArgs = append(countArgs, internalName)
		selectArgs = append(selectArgs, internalName)
	}

	whereClause := strings.Join(whereConditions, " AND ")

	countQuery = fmt.Sprintf("SELECT COUNT(*) FROM foghorn.dvr_requests WHERE %s", whereClause)
	selectQuery = fmt.Sprintf(`
		SELECT request_hash, internal_name, storage_node_id, status, 
		       started_at, ended_at, duration_seconds, size_bytes, manifest_path, 
		       error_message, created_at, updated_at
		FROM foghorn.dvr_requests 
		WHERE %s
		ORDER BY created_at DESC 
		LIMIT $%d OFFSET $%d
	`, whereClause, paramCount+1, paramCount+2)

	selectArgs = append(selectArgs, limit, offset)

	// Count total recordings
	var total int
	if err := db.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
		logger.WithError(err).Error("Failed to count DVR recordings")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count DVR recordings"})
		return
	}

	// Fetch recordings
	rows, err := db.Query(selectQuery, selectArgs...)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch DVR recordings")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch DVR recordings"})
		return
	}
	defer rows.Close()

	var recordings []fapi.DVRInfo
	for rows.Next() {
		var dvr fapi.DVRInfo
		var startedAt, endedAt sql.NullTime
		var durationSec sql.NullInt32
		var sizeBytes sql.NullInt64
		var manifestPath, errorMessage sql.NullString

		err := rows.Scan(
			&dvr.DVRHash, &dvr.InternalName, &dvr.StorageNodeID, &dvr.Status,
			&startedAt, &endedAt, &durationSec, &sizeBytes, &manifestPath,
			&errorMessage, &dvr.CreatedAt, &dvr.UpdatedAt,
		)
		if err != nil {
			logger.WithError(err).Error("Failed to scan DVR recording")
			continue
		}

		if startedAt.Valid {
			dvr.StartedAt = &startedAt.Time
		}
		if endedAt.Valid {
			dvr.EndedAt = &endedAt.Time
		}
		if durationSec.Valid {
			dvr.DurationSeconds = &durationSec.Int32
		}
		if sizeBytes.Valid {
			dvr.SizeBytes = &sizeBytes.Int64
		}
		if manifestPath.Valid {
			dvr.ManifestPath = manifestPath.String
		}
		if errorMessage.Valid {
			dvr.ErrorMessage = errorMessage.String
		}

		recordings = append(recordings, dvr)
	}

	c.JSON(http.StatusOK, fapi.DVRListResponse{
		DVRRecordings: recordings,
		Total:         total,
		Page:          page,
		Limit:         limit,
	})
}
