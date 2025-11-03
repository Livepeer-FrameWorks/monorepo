package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/triggers"
	fapi "frameworks/pkg/api/foghorn"
	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/clips"
	"frameworks/pkg/dvr"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	db                  *sql.DB
	logger              logging.Logger
	lb                  *balancer.LoadBalancer
	decklogClient       *decklog.BatchedClient
	commodoreClient     *commodore.Client
	quartermasterClient *qmclient.Client
	metrics             *FoghornMetrics
	geoipReader         *geoip.Reader
	geoipCache          *cache.Cache
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

	// Configure TLS based on environment variables
	allowInsecure := true // Default to insecure for backwards compatibility
	if os.Getenv("DECKLOG_USE_TLS") == "true" {
		allowInsecure = false
	}

	config := decklog.BatchedClientConfig{
		Target:        address,
		AllowInsecure: allowInsecure,
		Timeout:       10 * time.Second,
		Source:        "foghorn",
	}

	client, err := decklog.NewBatchedClient(config, logger)
	if err != nil {
		logger.WithError(err).Error("Failed to initialize Decklog gRPC client")
		return
	}
	decklogClient = client

	// Initialize Quartermaster client for enrollment and service bootstrap
	quartermasterURL := os.Getenv("QUARTERMASTER_URL")
	if quartermasterURL == "" {
		quartermasterURL = "http://localhost:18002"
	}
	serviceToken := os.Getenv("SERVICE_TOKEN")
	if serviceToken == "" {
		logger.Fatal("SERVICE_TOKEN environment variable is required")
	}
	quartermasterClient = qmclient.NewClient(qmclient.Config{
		BaseURL:      quartermasterURL,
		ServiceToken: serviceToken,
		Logger:       logger,
	})
	control.SetQuartermasterClient(quartermasterClient)

	// Self-register Foghorn instance in Quartermaster (best-effort)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = quartermasterClient.BootstrapService(ctx, &qmapi.BootstrapServiceRequest{
			Type:           "foghorn",
			Version:        os.Getenv("VERSION"),
			Protocol:       "http",
			HealthEndpoint: func() *string { s := "/health"; return &s }(),
			Port:           18008,
		})
	}()

	// Register clip progress/done handlers to emit analytics
	control.SetClipHandlers(
		func(p *pb.ClipProgress) {
			if decklogClient == nil {
				return
			}
			tenantID, internalName := getClipContextByRequestID(p.GetRequestId())
			clipData := &pb.ClipLifecycleData{
				Stage:     pb.ClipLifecycleData_STAGE_PROGRESS,
				ClipHash:  "", // Will be set from clip context
				RequestId: func() *string { s := p.GetRequestId(); return &s }(),
				StartedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
				// Enrichment fields added by Foghorn
				TenantId: func() *string {
					if tenantID != "" {
						return &tenantID
					} else {
						return nil
					}
				}(),
				InternalName: func() *string {
					if internalName != "" {
						return &internalName
					} else {
						return nil
					}
				}(),
			}
			if p.GetPercent() > 0 {
				percent := uint32(p.GetPercent())
				clipData.ProgressPercent = &percent
			}
			go func() {
				_ = decklogClient.SendClipLifecycle(clipData)
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
			clipData := &pb.ClipLifecycleData{
				Stage:       stage,
				ClipHash:    "", // Will be set from clip context
				RequestId:   func() *string { s := dn.GetRequestId(); return &s }(),
				CompletedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
				// Enrichment fields added by Foghorn
				TenantId: func() *string {
					if tenantID != "" {
						return &tenantID
					} else {
						return nil
					}
				}(),
				InternalName: func() *string {
					if internalName != "" {
						return &internalName
					} else {
						return nil
					}
				}(),
			}
			if fp := dn.GetFilePath(); fp != "" {
				clipData.FilePath = &fp
			}
			if s3 := dn.GetS3Url(); s3 != "" {
				clipData.S3Url = &s3
			}
			if sz := dn.GetSizeBytes(); sz > 0 {
				clipData.SizeBytes = &sz
			}
			if er := dn.GetError(); er != "" {
				clipData.Error = &er
			}
			go func() {
				_ = decklogClient.SendClipLifecycle(clipData)
			}()
		},
	)

	// Initialize GeoIP reader and cache for fallback geo enrichment
	geoipReader = geoip.GetSharedReader()
	if geoipReader != nil {
		// Configure GeoIP cache
		gttl := 300 * time.Second
		gswr := 120 * time.Second
		gneg := 60 * time.Second
		gmax := 50000
		if v := os.Getenv("GEOIP_CACHE_TTL"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				gttl = d
			}
		}
		if v := os.Getenv("GEOIP_CACHE_SWR"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				gswr = d
			}
		}
		if v := os.Getenv("GEOIP_CACHE_NEG_TTL"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				gneg = d
			}
		}
		if v := os.Getenv("GEOIP_CACHE_MAX"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				gmax = n
			}
		}
		geoipCache = cache.New(cache.Options{TTL: gttl, StaleWhileRevalidate: gswr, NegativeTTL: gneg, MaxEntries: gmax}, cache.MetricsHooks{})
		logger.Info("GeoIP reader initialized successfully with cache")
	} else {
		logger.Debug("GeoIP disabled (no GEOIP_MMDB_PATH or failed to load)")
	}

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

	// Initialize Commodore client for trigger processor
	commodoreURL := os.Getenv("COMMODORE_URL")
	if commodoreURL == "" {
		commodoreURL = "http://localhost:18001"
	}

	// Configure Commodore cache (env overrides)
	ttl := 60 * time.Second
	if v := os.Getenv("COMMODORE_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			ttl = d
		}
	}
	swr := 30 * time.Second
	if v := os.Getenv("COMMODORE_CACHE_SWR"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			swr = d
		}
	}
	neg := 10 * time.Second
	if v := os.Getenv("COMMODORE_CACHE_NEG_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			neg = d
		}
	}
	maxEntries := 10000
	if v := os.Getenv("COMMODORE_CACHE_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxEntries = n
		}
	}
	commodoreCache := cache.New(cache.Options{TTL: ttl, StaleWhileRevalidate: swr, NegativeTTL: neg, MaxEntries: maxEntries}, cache.MetricsHooks{})

	commodoreConfig := commodore.Config{
		BaseURL:      commodoreURL,
		ServiceToken: serviceToken,
		Timeout:      30 * time.Second,
		Logger:       logger,
		Cache:        commodoreCache,
	}
	commodoreClient = commodore.NewClient(commodoreConfig)

	// Initialize trigger processor with GeoIP reader - reuse the same Decklog client
	triggerProcessor := triggers.NewProcessor(logger, commodoreClient, decklogClient, lb, geoipReader)
	if geoipReader != nil && geoipCache != nil {
		triggerProcessor.SetGeoIPCache(geoipCache)
	}

	// Wire up the trigger processor to the control server
	control.SetMistTriggerProcessor(triggerProcessor)

	logger.Info("Initialized trigger processor with Commodore and Decklog clients")
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

	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	var out []map[string]interface{}
	if snapshot != nil {
		for _, n := range snapshot.Nodes {
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
				"cpu_tenths":  uint64(n.CPU * 10),
				"cpu_percent": uint64(n.CPU),
				"ram_max":     n.RAMMax,
				"ram_current": n.RAMCurrent,
				"ram_percent": func() uint64 {
					if n.RAMMax > 0 {
						return uint64((n.RAMCurrent * 100) / n.RAMMax)
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
				if uint64(node.CPU*10)+uint64(minCpu) >= 1000 {
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
					"cpu": weights["cpu"] - (uint64(node.CPU*10)*weights["cpu"])/1000,
					"ram": weights["ram"] - ((uint64(node.RAMCurrent) * weights["ram"]) / uint64(node.RAMMax)),
					"bw":  weights["bw"] - (((uint64(node.UpSpeed) + uint64(node.AddBandwidth)) * weights["bw"]) / node.AvailBandwidth),
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
	var req fapi.CreateClipRequest
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
		// TODO: Create proper ClipLifecycleData for STAGE_REQUESTED
		clipData := &pb.ClipLifecycleData{
			Stage:     pb.ClipLifecycleData_STAGE_REQUESTED,
			ClipHash:  "", // Will be set from clip context
			RequestId: func() *string { s := reqID; return &s }(),
			StartedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
			// Enrichment fields added by Foghorn
			TenantId: func() *string {
				if req.TenantID != "" {
					return &req.TenantID
				} else {
					return nil
				}
			}(),
			InternalName: func() *string {
				if req.InternalName != "" {
					return &req.InternalName
				} else {
					return nil
				}
			}(),
		}
		go func() {
			_ = decklogClient.SendClipLifecycle(clipData)
		}()
	}

	// Reflect clip requested into state manager
	state.DefaultManager().UpdateStreamInstanceInfo(req.InternalName, lb.GetNodeIDByHost(storageHost), map[string]interface{}{
		"clip_status":     "requested",
		"clip_request_id": reqID,
		"clip_format":     format,
	})

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
		// TODO: Create proper ClipLifecycleData for STAGE_QUEUED
		clipData := &pb.ClipLifecycleData{
			Stage:       pb.ClipLifecycleData_STAGE_QUEUED,
			ClipHash:    clipHash,
			RequestId:   func() *string { s := reqID; return &s }(),
			CompletedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
			// Enrichment fields added by Foghorn
			TenantId: func() *string {
				if req.TenantID != "" {
					return &req.TenantID
				} else {
					return nil
				}
			}(),
			InternalName: func() *string {
				if req.InternalName != "" {
					return &req.InternalName
				} else {
					return nil
				}
			}(),
		}
		go func() {
			_ = decklogClient.SendClipLifecycle(clipData)
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

	// Enrich with tenant info via Commodore
	var tenantID, internalName string
	if commodoreClient != nil && streamName != "" {
		ctx := context.Background()
		if resolveResp, err := commodoreClient.ResolveInternalName(ctx, streamName); err == nil {
			tenantID = resolveResp.TenantID
			internalName = resolveResp.InternalName
		} else {
			logger.WithError(err).WithField("stream_name", streamName).Debug("Failed to resolve tenant via Commodore")
		}
	}

	// Create LoadBalancingData event
	event := &pb.LoadBalancingData{
		SelectedNode:      selectedNode,
		SelectedNodeId:    func() *string { s := selectedNodeID; return &s }(),
		Latitude:          lat,
		Longitude:         lon,
		Status:            status,
		Details:           details,
		Score:             score,
		ClientIp:          clientIP,
		ClientCountry:     country,
		NodeLatitude:      nodeLat,
		NodeLongitude:     nodeLon,
		NodeName:          nodeName,
		RoutingDistanceKm: func() *float64 { d := routingDistanceKm; return &d }(),
		// Enrichment fields added by Foghorn
		TenantId: func() *string {
			if tenantID != "" {
				return &tenantID
			} else {
				return nil
			}
		}(),
		InternalName: func() *string {
			if internalName != "" {
				return &internalName
			} else {
				return nil
			}
		}(),
	}

	if decklogClient == nil {
		logger.Error("Decklog gRPC client not initialized")
		return
	}

	// Send event via gRPC
	err := decklogClient.SendLoadBalancing(event)
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

func getTotalViewers(node state.EnhancedBalancerNodeSnapshot) uint64 {
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
	var req fapi.StartDVRRequest

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

	_ = c.Request.URL.Query()

	// Resolve actual source node for this stream
	sourceNodeID, baseURL, ok := control.GetStreamSource(req.InternalName)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no source node available"})
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

	// Idempotency: check for existing active DVR for this stream
	var existingHash string
	_ = db.QueryRow(`SELECT request_hash FROM foghorn.dvr_requests WHERE internal_name=$1 AND status IN ('requested','starting','recording') ORDER BY created_at DESC LIMIT 1`, req.InternalName).Scan(&existingHash)
	if existingHash != "" {
		c.JSON(http.StatusOK, fapi.StartDVRResponse{Status: "already_started", DVRHash: existingHash, IngestHost: baseURL, StorageHost: storageHost, StorageNodeID: storageNodeID})
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

	// Build DTSC full URL using unified helper
	fullDTSC := control.BuildDTSCURI(sourceNodeID, req.InternalName, true, logger)
	if fullDTSC == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "DTSC output not available on source node"})
		return
	}

	// Send gRPC control message to storage Helmsman
	dvrReq := &pb.DVRStartRequest{
		DvrHash:       dvrHash,
		InternalName:  req.InternalName,
		SourceBaseUrl: fullDTSC,
		RequestId:     dvrHash,
		Config: &pb.DVRConfig{
			Enabled:         config.Enabled,
			RetentionDays:   int32(config.RetentionDays),
			Format:          config.Format,
			SegmentDuration: int32(config.SegmentDuration),
		},
	}

	if err := control.SendDVRStart(storageNodeID, dvrReq); err != nil {
		logger.WithFields(logging.Fields{"storage_node_id": storageNodeID, "error": err}).Error("Failed to send DVR start to storage node")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start dvr on storage node"})
		return
	}

	state.DefaultManager().UpdateStreamInstanceInfo(req.InternalName, storageNodeID, map[string]interface{}{"dvr_status": "requested", "dvr_hash": dvrReq.GetDvrHash()})

	if decklogClient != nil {
		// Enrich with tenant info via Commodore
		var tenantID, internalName string
		if commodoreClient != nil && req.InternalName != "" {
			ctx := context.Background()
			if resolveResp, err := commodoreClient.ResolveInternalName(ctx, req.InternalName); err == nil {
				tenantID = resolveResp.TenantID
				internalName = resolveResp.InternalName
			}
		}

		dvrData := &pb.DVRLifecycleData{
			Status:       pb.DVRLifecycleData_STATUS_STARTED,
			DvrHash:      dvrHash,
			StartedAt:    func() *int64 { t := time.Now().Unix(); return &t }(),
			TenantId:     &tenantID,
			InternalName: &internalName,
		}
		go func() { _ = decklogClient.SendDVRLifecycle(dvrData) }()
	}

	c.JSON(http.StatusOK, fapi.StartDVRResponse{Status: "started", DVRHash: dvrHash, IngestHost: baseURL, StorageHost: storageHost, StorageNodeID: storageNodeID})
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
		// Enrich with tenant info via Commodore
		var tenantID, internalName string
		if commodoreClient != nil && internalName != "" {
			ctx := context.Background()
			if resolveResp, err := commodoreClient.ResolveInternalName(ctx, internalName); err == nil {
				tenantID = resolveResp.TenantID
				internalName = resolveResp.InternalName
			}
		}

		dvrData := &pb.DVRLifecycleData{
			Status:       pb.DVRLifecycleData_STATUS_STOPPED,
			DvrHash:      dvrHash,
			EndedAt:      func() *int64 { t := time.Now().Unix(); return &t }(),
			TenantId:     &tenantID,
			InternalName: &internalName,
		}
		go func() {
			_ = decklogClient.SendDVRLifecycle(dvrData)
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

// buildOutputCapabilities returns default capabilities for a given protocol and content type
func buildOutputCapabilities(protocol string, isLive bool) fapi.OutputCapability {
	caps := fapi.OutputCapability{
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
func buildOutputsMap(baseURL string, rawOutputs map[string]interface{}, streamName string, isLive bool) map[string]fapi.OutputEndpoint {
	outputs := make(map[string]fapi.OutputEndpoint)

	base := ensureTrailingSlash(baseURL)
	html := base + streamName + ".html"
	outputs["MIST_HTML"] = fapi.OutputEndpoint{Protocol: "MIST_HTML", URL: html, Capabilities: buildOutputCapabilities("MIST_HTML", isLive)}
	outputs["PLAYER_JS"] = fapi.OutputEndpoint{Protocol: "PLAYER_JS", URL: base + "player.js", Capabilities: buildOutputCapabilities("PLAYER_JS", isLive)}

	// Prefer explicit WHEP if present; otherwise derive from HTML
	if raw, ok := rawOutputs["WHEP"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WHEP"] = fapi.OutputEndpoint{Protocol: "WHEP", URL: u, Capabilities: buildOutputCapabilities("WHEP", isLive)}
		}
	}
	if _, ok := outputs["WHEP"]; !ok {
		if u := deriveWHEPFromHTML(html); u != "" {
			outputs["WHEP"] = fapi.OutputEndpoint{Protocol: "WHEP", URL: u, Capabilities: buildOutputCapabilities("WHEP", isLive)}
		}
	}

	if raw, ok := rawOutputs["HLS"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HLS"] = fapi.OutputEndpoint{Protocol: "HLS", URL: u, Capabilities: buildOutputCapabilities("HLS", isLive)}
		}
	}
	if raw, ok := rawOutputs["DASH"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["DASH"] = fapi.OutputEndpoint{Protocol: "DASH", URL: u, Capabilities: buildOutputCapabilities("DASH", isLive)}
		}
	}
	if raw, ok := rawOutputs["MP4"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["MP4"] = fapi.OutputEndpoint{Protocol: "MP4", URL: u, Capabilities: buildOutputCapabilities("MP4", isLive)}
		}
	}
	if raw, ok := rawOutputs["WEBM"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WEBM"] = fapi.OutputEndpoint{Protocol: "WEBM", URL: u, Capabilities: buildOutputCapabilities("WEBM", isLive)}
		}
	}
	if raw, ok := rawOutputs["HTTP"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HTTP"] = fapi.OutputEndpoint{Protocol: "HTTP", URL: u, Capabilities: buildOutputCapabilities("HTTP", isLive)}
		}
	}

	return outputs
}

func playbackTracksFromState(tracks []state.StreamTrack) []fapi.PlaybackTrack {
	if len(tracks) == 0 {
		return nil
	}
	out := make([]fapi.PlaybackTrack, 0, len(tracks))
	for _, tr := range tracks {
		pt := fapi.PlaybackTrack{
			Type:        tr.Type,
			Codec:       tr.Codec,
			BitrateKbps: tr.Bitrate,
			Width:       tr.Width,
			Height:      tr.Height,
			Channels:    tr.Channels,
			SampleRate:  tr.SampleRate,
		}
		out = append(out, pt)
	}
	return out
}

func playbackInstancesFromState(instances map[string]state.StreamInstanceState) []fapi.PlaybackInstance {
	if len(instances) == 0 {
		return nil
	}
	keys := make([]string, 0, len(instances))
	for nodeID := range instances {
		keys = append(keys, nodeID)
	}
	sort.Strings(keys)
	out := make([]fapi.PlaybackInstance, 0, len(keys))
	for _, nodeID := range keys {
		inst := instances[nodeID]
		out = append(out, fapi.PlaybackInstance{
			NodeID:           nodeID,
			Viewers:          inst.Viewers,
			BufferState:      inst.BufferState,
			BytesUp:          inst.BytesUp,
			BytesDown:        inst.BytesDown,
			TotalConnections: inst.TotalConnections,
			Inputs:           inst.Inputs,
			LastUpdate:       inst.LastUpdate,
		})
	}
	return out
}

func protocolHintsFromEndpoints(endpoints []fapi.ViewerEndpoint) []string {
	set := make(map[string]struct{})
	for _, ep := range endpoints {
		if ep.Protocol != "" {
			set[strings.ToUpper(ep.Protocol)] = struct{}{}
		}
		for proto := range ep.Outputs {
			if proto == "" {
				continue
			}
			set[strings.ToUpper(proto)] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	hints := make([]string, 0, len(set))
	for proto := range set {
		hints = append(hints, proto)
	}
	sort.Strings(hints)
	return hints
}

func buildLivePlaybackMetadata(req fapi.ViewerEndpointRequest, endpoints []fapi.ViewerEndpoint) *fapi.PlaybackMetadata {
	sm := state.DefaultManager()
	streamState := sm.GetStreamState(req.ContentID)
	if streamState == nil {
		return nil
	}

	meta := &fapi.PlaybackMetadata{
		Status:        streamState.Status,
		IsLive:        strings.EqualFold(streamState.Status, "live"),
		Viewers:       streamState.Viewers,
		BufferState:   streamState.BufferState,
		TenantID:      streamState.TenantID,
		ContentID:     req.ContentID,
		ContentType:   req.ContentType,
		Tracks:        playbackTracksFromState(streamState.Tracks),
		Instances:     playbackInstancesFromState(sm.GetStreamInstances(req.ContentID)),
		ProtocolHints: protocolHintsFromEndpoints(endpoints),
	}

	if meta.Status == "" {
		meta.Status = req.ContentType
	}
	if !meta.IsLive && strings.EqualFold(req.ContentType, "live") {
		meta.IsLive = true
	}
	if streamState.HealthScore > 0 {
		score := streamState.HealthScore
		meta.HealthScore = &score
	}

	return meta
}

// HandleResolveViewerEndpoint resolves optimal viewing endpoints for different content types
func HandleResolveViewerEndpoint(c *gin.Context) {
	var req fapi.ViewerEndpointRequest
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

	var response fapi.ViewerEndpointResponse
	var err error

	switch req.ContentType {
	case "live":
		response, err = resolveLiveViewerEndpoint(req, lat, lon)
	case "dvr":
		response, err = resolveDVRViewerEndpoint(req)
	case "clip":
		response, err = resolveClipViewerEndpoint(req)
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

	// Enrich live metadata from unified state when available
	if req.ContentType == "live" {
		st := state.DefaultManager().GetStreamState(req.ContentID)
		if st != nil {
			response.Metadata.IsLive = st.Status == "live"
			response.Metadata.Status = st.Status
			response.Metadata.Viewers = st.Viewers
			response.Metadata.BufferState = st.BufferState
			if st.HealthScore > 0 {
				response.Metadata.HealthScore = &st.HealthScore
			}
		}
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

// HandleStreamMeta fetches Mist JSON meta for an internal_name without affecting viewer counts
func HandleStreamMeta(c *gin.Context) {
	var req fapi.StreamMetaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	internalName := strings.TrimSpace(req.InternalName)
	if internalName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal_name is required"})
		return
	}

	// Determine base URL without invoking the load balancer
	base := strings.TrimSpace(req.TargetBaseURL)
	if base == "" && req.TargetNodeID != "" {
		if no, ok := control.GetNodeOutputs(req.TargetNodeID); ok {
			base = no.BaseURL
		}
	}
	if base == "" {
		if nodeID, _, ok := control.GetStreamSource(internalName); ok {
			if no, ok2 := control.GetNodeOutputs(nodeID); ok2 {
				base = no.BaseURL
			}
		}
	}
	if base == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no source node available"})
		return
	}

	// Build json_<encoded>.js at the node's reported base path (no hardcoded "/view")
	encoded := url.PathEscape("live+" + internalName)
	jsonURL, err := url.JoinPath(strings.TrimRight(base, "/"), "json_"+encoded+".js")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build meta URL"})
		return
	}

	httpClient := &http.Client{Timeout: 4 * time.Second}
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, jsonURL, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build request"})
		return
	}
	httpReq.Header.Set("Accept", "application/json, text/javascript")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch meta"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.JSON(http.StatusBadGateway, gin.H{"error": "edge response error", "status": resp.StatusCode, "body": string(body)})
		return
	}

	var raw map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to parse meta"})
		return
	}

	// Extract summary
	summary := map[string]interface{}{
		"is_live":          false,
		"buffer_window_ms": 0,
		"jitter_ms":        0,
		"unix_offset_ms":   0,
		"type":             "",
		"tracks":           []map[string]interface{}{},
	}
	if meta, ok := raw["meta"].(map[string]interface{}); ok {
		if live, ok := meta["live"].(float64); ok {
			summary["is_live"] = live != 0
		}
		if bw, ok := meta["buffer_window"].(float64); ok {
			summary["buffer_window_ms"] = int64(bw)
		}
		if jit, ok := meta["jitter"].(float64); ok {
			summary["jitter_ms"] = int64(jit)
		}
		if uo, ok := raw["unixoffset"].(float64); ok {
			summary["unix_offset_ms"] = int64(uo)
		}
		if t, ok := raw["type"].(string); ok {
			summary["type"] = t
		}
		if w, ok := raw["width"].(float64); ok {
			v := int(w)
			summary["width"] = v
		}
		if h, ok := raw["height"].(float64); ok {
			v := int(h)
			summary["height"] = v
		}
		// tracks
		if tracks, ok := meta["tracks"].(map[string]interface{}); ok {
			list := []map[string]interface{}{}
			for id, tv := range tracks {
				if tm, ok := tv.(map[string]interface{}); ok {
					item := map[string]interface{}{
						"id":    id,
						"type":  stringOr(tm["type"]),
						"codec": stringOr(tm["codec"]),
					}
					if ch, ok := toInt(tm["channels"]); ok {
						item["channels"] = ch
					}
					if rate, ok := toInt(tm["rate"]); ok {
						item["rate"] = rate
					}
					if bw, ok := toInt(tm["bps"]); ok {
						item["bitrate_bps"] = bw
					}
					if w, ok := toInt(tm["width"]); ok {
						item["width"] = w
					}
					if h, ok := toInt(tm["height"]); ok {
						item["height"] = h
					}
					if now, ok := toInt64(tm["nowms"]); ok {
						item["now_ms"] = now
					}
					if last, ok := toInt64(tm["lastms"]); ok {
						item["last_ms"] = last
					}
					if first, ok := toInt64(tm["firstms"]); ok {
						item["first_ms"] = first
					}
					list = append(list, item)
				}
			}
			summary["tracks"] = list
		}
	}

	out := map[string]interface{}{"meta_summary": summary}
	if req.IncludeRaw {
		out["raw"] = raw
	}

	c.JSON(http.StatusOK, out)
}

func stringOr(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func toInt(v interface{}) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	default:
		return 0, false
	}
}
func toInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case int:
		return int64(x), true
	default:
		return 0, false
	}
}

// resolveLiveViewerEndpoint uses load balancer to find optimal edge nodes with fallbacks
func resolveLiveViewerEndpoint(req fapi.ViewerEndpointRequest, lat, lon float64) (fapi.ViewerEndpointResponse, error) {
	// Use load balancer to get top 5 edge nodes for fallbacks
	ctx := context.WithValue(context.Background(), "cap", "edge")
	nodes, err := lb.GetTopNodesWithScores(ctx, req.ContentID, lat, lon, make(map[string]int), "", 5)
	if err != nil {
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("no suitable edge nodes available: %v", err)
	}

	var endpoints []fapi.ViewerEndpoint
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

		endpoint := fapi.ViewerEndpoint{
			NodeID:      node.NodeID,
			BaseURL:     nodeOutputs.BaseURL,
			Protocol:    protocol,
			URL:         url,
			GeoDistance: geoDistance,
			LoadScore:   float64(node.Score),
			HealthScore: 1.0,
			Outputs:     buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, streamName, true),
		}
		endpoints = append(endpoints, endpoint)
	}

	if len(endpoints) == 0 {
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("no nodes with suitable outputs available")
	}

	return fapi.ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  buildLivePlaybackMetadata(req, endpoints),
	}, nil
}

// resolveDVRViewerEndpoint queries database for DVR storage node
func resolveDVRViewerEndpoint(req fapi.ViewerEndpointRequest) (fapi.ViewerEndpointResponse, error) {
	var (
		tenantID      string
		internalName  string
		nodeID        string
		status        string
		duration      sql.NullInt64
		recordingSize sql.NullInt64
		manifestPath  sql.NullString
		createdAt     sql.NullTime
	)

	err := db.QueryRow(`
		SELECT tenant_id, internal_name, storage_node_id, status, duration_seconds, size_bytes, manifest_path, created_at
		FROM foghorn.dvr_requests 
		WHERE request_hash = $1 AND status = 'completed'
	`, req.ContentID).Scan(&tenantID, &internalName, &nodeID, &status, &duration, &recordingSize, &manifestPath, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return fapi.ViewerEndpointResponse{}, fmt.Errorf("DVR recording not found")
		}
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("failed to query DVR: %v", err)
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)

	if !exists || nodeOutputs.Outputs == nil {
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("storage node outputs not available")
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
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("no suitable outputs for DVR playback")
	}

	endpoint := fapi.ViewerEndpoint{
		NodeID:   nodeID,
		BaseURL:  nodeOutputs.BaseURL,
		Protocol: protocol,
		URL:      url,
		Outputs:  buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, streamName, false),
	}

	meta := &fapi.PlaybackMetadata{
		Status:        status,
		IsLive:        false,
		TenantID:      tenantID,
		ContentID:     req.ContentID,
		ContentType:   req.ContentType,
		DvrStatus:     status,
		ProtocolHints: protocolHintsFromEndpoints([]fapi.ViewerEndpoint{endpoint}),
	}

	if duration.Valid {
		d := int(duration.Int64)
		meta.DurationSeconds = &d
	}
	if recordingSize.Valid {
		size := recordingSize.Int64
		meta.RecordingSizeBytes = &size
	}
	if manifestPath.Valid {
		meta.DvrSourceURI = manifestPath.String
	}
	if createdAt.Valid {
		t := createdAt.Time
		meta.CreatedAt = &t
	}
	if internalName != "" {
		meta.ClipSource = &internalName
	}

	return fapi.ViewerEndpointResponse{
		Primary:  endpoint,
		Metadata: meta,
	}, nil
}

// resolveClipViewerEndpoint queries database for clip storage node
func resolveClipViewerEndpoint(req fapi.ViewerEndpointRequest) (fapi.ViewerEndpointResponse, error) {
	var (
		tenantID     string
		nodeID       string
		status       string
		sourceStream string
		title        sql.NullString
		duration     sql.NullInt64
		fileSize     sql.NullInt64
		createdAt    sql.NullTime
		baseURL      sql.NullString
		storagePath  sql.NullString
	)

	err := db.QueryRow(`
		SELECT tenant_id, node_id, status, stream_name, title, duration, size_bytes, created_at, base_url, storage_path
		FROM foghorn.clips 
		WHERE clip_hash = $1 AND status = 'ready'
	`, req.ContentID).Scan(&tenantID, &nodeID, &status, &sourceStream, &title, &duration, &fileSize, &createdAt, &baseURL, &storagePath)

	if err != nil {
		if err == sql.ErrNoRows {
			return fapi.ViewerEndpointResponse{}, fmt.Errorf("clip not found")
		}
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("failed to query clip: %v", err)
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)

	if !exists || nodeOutputs.Outputs == nil {
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("storage node outputs not available")
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
		return fapi.ViewerEndpointResponse{}, fmt.Errorf("no suitable outputs for clip playback")
	}

	endpoint := fapi.ViewerEndpoint{
		NodeID:   nodeID,
		BaseURL:  nodeOutputs.BaseURL,
		Protocol: protocol,
		URL:      url,
		Outputs:  buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, streamName, false),
	}

	meta := &fapi.PlaybackMetadata{
		Status:        status,
		IsLive:        false,
		TenantID:      tenantID,
		ContentID:     req.ContentID,
		ContentType:   req.ContentType,
		ProtocolHints: protocolHintsFromEndpoints([]fapi.ViewerEndpoint{endpoint}),
	}
	if sourceStream != "" {
		clipSource := sourceStream
		meta.ClipSource = &clipSource
	}
	if title.Valid {
		meta.Title = &title.String
	}
	if duration.Valid {
		totalMs := duration.Int64
		secs := int(totalMs / 1000)
		if totalMs%1000 != 0 {
			secs++
		}
		if secs < 0 {
			secs = 0
		}
		meta.DurationSeconds = &secs
	}
	if fileSize.Valid {
		size := fileSize.Int64
		meta.RecordingSizeBytes = &size
	}
	if createdAt.Valid {
		t := createdAt.Time
		meta.CreatedAt = &t
	}
	if baseURL.Valid && storagePath.Valid {
		combined := strings.TrimRight(baseURL.String, "/") + "/" + strings.TrimLeft(storagePath.String, "/")
		meta.DvrSourceURI = combined
	} else if storagePath.Valid {
		meta.DvrSourceURI = storagePath.String
	}

	return fapi.ViewerEndpointResponse{
		Primary:  endpoint,
		Metadata: meta,
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
