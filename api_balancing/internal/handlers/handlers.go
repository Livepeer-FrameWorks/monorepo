package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/version"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	db                  *sql.DB
	logger              logging.Logger
	lb                  *balancer.LoadBalancer
	decklogClient       *decklog.BatchedClient
	commodoreClient     *commodore.GRPCClient
	quartermasterClient *qmclient.GRPCClient
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
	RoutingDecisions      *prometheus.CounterVec
	NodeSelectionDuration *prometheus.HistogramVec
	LoadDistribution      *prometheus.GaugeVec
	DBQueries             *prometheus.CounterVec
	DBDuration            *prometheus.HistogramVec
	DBConnections         *prometheus.GaugeVec
}

// StreamKeyRegex matches stream keys in format xxxx-xxxx-xxxx-xxxx
var StreamKeyRegex = regexp.MustCompile(`^(?:\w{4}-){3}\w{4}$`)

// NodeHostRegex matches the first part of hostname before first dot
var NodeHostRegex = regexp.MustCompile(`^.+?\.`)

// Init initializes the handlers with dependencies
func Init(
	database *sql.DB,
	log logging.Logger,
	loadBalancer *balancer.LoadBalancer,
	foghornMetrics *FoghornMetrics,
	dClient *decklog.BatchedClient,
	cClient *commodore.GRPCClient,
	qClient *qmclient.GRPCClient,
	geo *geoip.Reader,
	geoCache *cache.Cache,
) {
	db = database
	logger = log
	lb = loadBalancer
	metrics = foghornMetrics
	decklogClient = dClient
	commodoreClient = cClient
	quartermasterClient = qClient
	geoipReader = geo
	geoipCache = geoCache

	// Share database connection with control package for clip operations
	control.SetDB(database)

	// Share Commodore client with control package for unified resolution logic
	control.CommodoreClient = cClient

	// Share Quartermaster client with control package
	control.SetQuartermasterClient(qClient)

	// Self-register Foghorn instance in Quartermaster (best-effort)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		advertiseHost := config.GetEnv("FOGHORN_HOST", "foghorn")
		_, _ = quartermasterClient.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:           "foghorn",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: func() *string { s := "/health"; return &s }(),
			Port:           18008,
			AdvertiseHost:  &advertiseHost,
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
		func(del *pb.ArtifactDeleted) {
			// Update DB status
			clipHash := del.GetClipHash()
			nodeID := del.GetNodeId()

			// 1. Update clips table
			_, err := db.Exec(`UPDATE foghorn.clips SET status = 'deleted', updated_at = NOW() WHERE clip_hash = $1`, clipHash)
			if err != nil {
				logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to mark clip as deleted in DB")
			}

			// 2. Update artifact registry (orphaned/deleted)
			_, err = db.Exec(`DELETE FROM foghorn.artifact_registry WHERE clip_hash = $1 AND node_id = $2`, clipHash, nodeID)
			if err != nil {
				logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to remove artifact from registry")
			}

			// 3. Send Decklog event
			if decklogClient != nil {
				// Resolve tenant context if possible
				var tenantID, internalName string
				_ = db.QueryRow(`SELECT tenant_id::text, stream_name FROM foghorn.clips WHERE clip_hash = $1`, clipHash).Scan(&tenantID, &internalName)

				clipData := &pb.ClipLifecycleData{
					Stage:     pb.ClipLifecycleData_STAGE_DELETED,
					ClipHash:  clipHash,
					NodeId:    func() *string { s := nodeID; return &s }(),
					SizeBytes: func() *uint64 { s := del.GetSizeBytes(); return &s }(),
					TenantId: func() *string {
						if tenantID != "" {
							return &tenantID
						}
						return nil
					}(),
					InternalName: func() *string {
						if internalName != "" {
							return &internalName
						}
						return nil
					}(),
				}
				go func() {
					_ = decklogClient.SendClipLifecycle(clipData)
				}()
			}
		},
	)

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
	// Source selection (Mist pull) -> isSourceSelection=true (exclude replicated)
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, streamName, lat, lon, tagAdjust, clientIP, true)
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
	// Ingest -> isSourceSelection=true (though less relevant without streamName)
	bestNode, score, nodeLat, nodeLon, nodeName, err := lb.GetBestNodeWithScore(ctx, "", lat, lon, tagAdjust, "", true)
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

	// Unified resolution: Determine if this is Live (Key) or VOD (Artifact)
	target, _ := control.ResolveStream(c.Request.Context(), streamName)

	// 1. Fixed Node (VOD)
	if target.FixedNode != "" {
		// Redirect to storage node with original stream name
		// Edge node triggers will handle vod+ translation
		bestNode := target.FixedNode

		proto := query.Get("proto")
		vars := c.Request.URL.RawQuery

		if proto != "" && bestNode != "" {
			redirectURL := fmt.Sprintf("%s://%s/%s", proto, bestNode, streamName)
			if vars != "" {
				redirectURL += "?" + vars
			}
			c.Header("Location", redirectURL)
			c.String(http.StatusTemporaryRedirect, redirectURL)

			go postBalancingEvent(c, target.InternalName, bestNode, 0, lat, lon, "redirect", redirectURL, 0, 0, "")
			return
		}

		c.String(http.StatusOK, bestNode)
		go postBalancingEvent(c, target.InternalName, bestNode, 0, lat, lon, "success", "", 0, 0, "")
		return
	}

	// 2. Dynamic Balancing (Live)
	// Use resolved internal name for finding nodes, but preserve original name for redirect
	internalName := target.InternalName

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
	// Viewer selection -> isSourceSelection=false (allow replicated)
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, internalName, lat, lon, tagAdjust, "", false)
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
		go postBalancingEvent(c, internalName, bestNode, score, lat, lon, "redirect", redirectURL, nodeLat, nodeLon, nodeName)
		return
	}

	c.String(http.StatusOK, bestNode)

	if metrics != nil {
		metrics.RoutingDecisions.WithLabelValues("load_balancer", bestNode).Inc()
		metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
	}

	// Post successful balancing event to Firehose
	go postBalancingEvent(c, internalName, bestNode, score, lat, lon, "success", "", nodeLat, nodeLon, nodeName)
}

// Orchestrate clip creation: select ingest and storage, then call Helmsman on storage to pull clip from Mist
// HandleCreateClip - DELETED: migrated to gRPC CreateClip in internal/grpc/server.go

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
			tenantID = resolveResp.TenantId
			internalName = resolveResp.InternalName
		} else {
			logger.WithError(err).WithField("stream_name", streamName).Debug("Failed to resolve tenant via Commodore")
		}
	}

	// Bucketize client/node coords (privacy); also derive coarse centroids for compatibility
	clientBucket, clientCentLat, clientCentLon, hasClientBucket := geo.Bucket(lat, lon)
	nodeBucket, nodeCentLat, nodeCentLon, hasNodeBucket := geo.Bucket(nodeLat, nodeLon)

	// Create LoadBalancingData event
	event := &pb.LoadBalancingData{
		SelectedNode:   selectedNode,
		SelectedNodeId: func() *string { s := selectedNodeID; return &s }(),
		// Use bucket centroids instead of raw coordinates
		Latitude: func() float64 {
			if hasClientBucket {
				return clientCentLat
			}
			return 0
		}(),
		Longitude: func() float64 {
			if hasClientBucket {
				return clientCentLon
			}
			return 0
		}(),
		Status:        status,
		Details:       details,
		Score:         score,
		ClientIp:      "", // redact raw IP from emitted payload
		ClientCountry: country,
		NodeLatitude: func() float64 {
			if hasNodeBucket {
				return nodeCentLat
			}
			return 0
		}(),
		NodeLongitude: func() float64 {
			if hasNodeBucket {
				return nodeCentLon
			}
			return 0
		}(),
		NodeName: nodeName,
		RoutingDistanceKm: func() *float64 {
			d := routingDistanceKm
			if d == 0 {
				return nil
			}
			return &d
		}(),
		ClientBucket: clientBucket,
		NodeBucket:   nodeBucket,
		// Enrichment fields added by Foghorn
		TenantId: func() *string {
			if tenantID != "" {
				return &tenantID
			}
			return nil
		}(),
		InternalName: func() *string {
			if internalName != "" {
				return &internalName
			}
			return nil
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

// emitViewerRoutingEvent posts a routing decision for viewer playback (used by gRPC + generic HTTP helpers)
// Keeps it minimal to avoid duplicating the legacy postBalancingEvent gin-specific code.
func emitViewerRoutingEvent(req *pb.ViewerEndpointRequest, primary *pb.ViewerEndpoint, viewerLat, viewerLon, nodeLat, nodeLon float64, internalName string) {
	if decklogClient == nil || primary == nil {
		return
	}

	selectedNode := primary.BaseUrl
	if selectedNode == "" {
		selectedNode = primary.Url
	}

	selectedNodeID := primary.NodeId

	routingDistanceKm := 0.0
	if viewerLat != 0 && viewerLon != 0 && nodeLat != 0 && nodeLon != 0 {
		const toRad = math.Pi / 180.0
		lat1 := viewerLat * toRad
		lon1 := viewerLon * toRad
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

	var internalNamePtr *string
	if internalName != "" {
		internalNamePtr = &internalName
	}

	var selectedNodeIDPtr *string
	if selectedNodeID != "" {
		selectedNodeIDPtr = &selectedNodeID
	}

	clientBucket, clientCentLat, clientCentLon, hasClientBucket := geo.Bucket(viewerLat, viewerLon)
	nodeBucket, nodeCentLat, nodeCentLon, hasNodeBucket := geo.Bucket(nodeLat, nodeLon)

	event := &pb.LoadBalancingData{
		SelectedNode: selectedNode,
		Latitude: func() float64 {
			if hasClientBucket {
				return clientCentLat
			}
			return 0
		}(),
		Longitude: func() float64 {
			if hasClientBucket {
				return clientCentLon
			}
			return 0
		}(),
		Status:   "success",
		Details:  "play_rewrite",
		Score:    uint64(primary.LoadScore),
		ClientIp: "", // redact
		NodeLatitude: func() float64 {
			if hasNodeBucket {
				return nodeCentLat
			}
			return 0
		}(),
		NodeLongitude: func() float64 {
			if hasNodeBucket {
				return nodeCentLon
			}
			return 0
		}(),
		NodeName:       primary.NodeId,
		SelectedNodeId: selectedNodeIDPtr,
		RoutingDistanceKm: func() *float64 {
			if routingDistanceKm == 0 {
				return nil
			}
			return &routingDistanceKm
		}(),
		InternalName: internalNamePtr,
		ClientBucket: clientBucket,
		NodeBucket:   nodeBucket,
	}

	go func() {
		_ = decklogClient.SendLoadBalancing(event)
	}()
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

// HandleGetClips - DELETED: migrated to gRPC GetClips in internal/grpc/server.go

// HandleGetClip - DELETED: migrated to gRPC GetClip in internal/grpc/server.go

// HandleGetClipNode - DELETED: migrated to gRPC GetClipURLs in internal/grpc/server.go

// HandleResolveClip - DELETED: migrated to gRPC ResolveClipHash in internal/control/server.go

// HandleDeleteClip - DELETED: migrated to gRPC DeleteClip in internal/grpc/server.go

// === DVR MANAGEMENT ENDPOINTS ===

func getDVRContextByRequestID(requestID string) (string, string) {
	if db == nil || requestID == "" {
		return "", ""
	}
	var tenantID, internalName string
	_ = db.QueryRow(`SELECT tenant_id::text, internal_name FROM foghorn.dvr_requests WHERE request_hash = $1`, requestID).Scan(&tenantID, &internalName)
	return tenantID, internalName
}

// HandleStartDVRRecording - DELETED: migrated to gRPC StartDVR in internal/grpc/server.go

// HandleStopDVRRecording - DELETED: migrated to gRPC StopDVR in internal/grpc/server.go

// HandleGetDVRStatus - DELETED: migrated to gRPC GetDVRStatus in internal/grpc/server.go

// buildOutputCapabilities returns default capabilities for a given protocol and content type
func buildOutputCapabilities(protocol string, isLive bool) *pb.OutputCapability {
	caps := &pb.OutputCapability{
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
func buildOutputsMap(baseURL string, rawOutputs map[string]interface{}, streamName string, isLive bool) map[string]*pb.OutputEndpoint {
	outputs := make(map[string]*pb.OutputEndpoint)

	base := ensureTrailingSlash(baseURL)
	html := base + streamName + ".html"
	outputs["MIST_HTML"] = &pb.OutputEndpoint{Protocol: "MIST_HTML", Url: html, Capabilities: buildOutputCapabilities("MIST_HTML", isLive)}
	outputs["PLAYER_JS"] = &pb.OutputEndpoint{Protocol: "PLAYER_JS", Url: base + "player.js", Capabilities: buildOutputCapabilities("PLAYER_JS", isLive)}

	// Prefer explicit WHEP if present; otherwise derive from HTML
	if raw, ok := rawOutputs["WHEP"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WHEP"] = &pb.OutputEndpoint{Protocol: "WHEP", Url: u, Capabilities: buildOutputCapabilities("WHEP", isLive)}
		}
	}
	if _, ok := outputs["WHEP"]; !ok {
		if u := deriveWHEPFromHTML(html); u != "" {
			outputs["WHEP"] = &pb.OutputEndpoint{Protocol: "WHEP", Url: u, Capabilities: buildOutputCapabilities("WHEP", isLive)}
		}
	}

	if raw, ok := rawOutputs["HLS"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HLS"] = &pb.OutputEndpoint{Protocol: "HLS", Url: u, Capabilities: buildOutputCapabilities("HLS", isLive)}
		}
	}
	if raw, ok := rawOutputs["DASH"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["DASH"] = &pb.OutputEndpoint{Protocol: "DASH", Url: u, Capabilities: buildOutputCapabilities("DASH", isLive)}
		}
	}
	if raw, ok := rawOutputs["MP4"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["MP4"] = &pb.OutputEndpoint{Protocol: "MP4", Url: u, Capabilities: buildOutputCapabilities("MP4", isLive)}
		}
	}
	if raw, ok := rawOutputs["WEBM"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WEBM"] = &pb.OutputEndpoint{Protocol: "WEBM", Url: u, Capabilities: buildOutputCapabilities("WEBM", isLive)}
		}
	}
	if raw, ok := rawOutputs["HTTP"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HTTP"] = &pb.OutputEndpoint{Protocol: "HTTP", Url: u, Capabilities: buildOutputCapabilities("HTTP", isLive)}
		}
	}

	return outputs
}

func playbackTracksFromState(tracks []state.StreamTrack) []*pb.PlaybackTrack {
	if len(tracks) == 0 {
		return nil
	}
	out := make([]*pb.PlaybackTrack, 0, len(tracks))
	for _, tr := range tracks {
		pt := &pb.PlaybackTrack{
			Type:        tr.Type,
			Codec:       tr.Codec,
			BitrateKbps: int32(tr.Bitrate),
			Width:       int32(tr.Width),
			Height:      int32(tr.Height),
			Channels:    int32(tr.Channels),
			SampleRate:  int32(tr.SampleRate),
		}
		out = append(out, pt)
	}
	return out
}

func playbackInstancesFromState(instances map[string]state.StreamInstanceState) []*pb.PlaybackInstance {
	if len(instances) == 0 {
		return nil
	}
	keys := make([]string, 0, len(instances))
	for nodeID := range instances {
		keys = append(keys, nodeID)
	}
	sort.Strings(keys)
	out := make([]*pb.PlaybackInstance, 0, len(keys))
	for _, nodeID := range keys {
		inst := instances[nodeID]
		out = append(out, &pb.PlaybackInstance{
			NodeId:           nodeID,
			Viewers:          int32(inst.Viewers),
			BufferState:      inst.BufferState,
			BytesUp:          inst.BytesUp,
			BytesDown:        inst.BytesDown,
			TotalConnections: int32(inst.TotalConnections),
			Inputs:           int32(inst.Inputs),
			LastUpdate:       timestamppb.New(inst.LastUpdate),
		})
	}
	return out
}

func protocolHintsFromEndpoints(endpoints []*pb.ViewerEndpoint) []string {
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

func buildLivePlaybackMetadata(req *pb.ViewerEndpointRequest, endpoints []*pb.ViewerEndpoint) *pb.PlaybackMetadata {
	sm := state.DefaultManager()
	streamState := sm.GetStreamState(req.ContentId)
	if streamState == nil {
		return nil
	}

	meta := &pb.PlaybackMetadata{
		Status:        streamState.Status,
		IsLive:        strings.EqualFold(streamState.Status, "live"),
		Viewers:       int32(streamState.Viewers),
		BufferState:   streamState.BufferState,
		TenantId:      streamState.TenantID,
		ContentId:     req.ContentId,
		ContentType:   req.ContentType,
		Tracks:        playbackTracksFromState(streamState.Tracks),
		Instances:     playbackInstancesFromState(sm.GetStreamInstances(req.ContentId)),
		ProtocolHints: protocolHintsFromEndpoints(endpoints),
	}

	if meta.Status == "" {
		meta.Status = req.ContentType
	}
	if !meta.IsLive && strings.EqualFold(req.ContentType, "live") {
		meta.IsLive = true
	}

	return meta
}

// HandleResolveViewerEndpoint - DELETED: migrated to gRPC ResolveViewerEndpoint in internal/grpc/server.go

// HandleStreamMeta - DELETED: migrated to gRPC GetStreamMeta in internal/grpc/server.go

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
func resolveLiveViewerEndpoint(req *pb.ViewerEndpointRequest, lat, lon float64) (*pb.ViewerEndpointResponse, error) {
	// Resolve view key to internal name for load balancing
	viewKey := req.ContentId
	ctx := context.Background()
	target, err := control.ResolveStream(ctx, viewKey)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve stream: %v", err)
	}
	if target.InternalName == "" {
		return nil, fmt.Errorf("stream not found")
	}
	internalName := target.InternalName // e.g., "live+actual-internal-name"

	// Use load balancer with internal name to find nodes that have the stream
	lbctx := context.WithValue(ctx, "cap", "edge")
	// Viewer endpoint resolution -> isSourceSelection=false (allow replicated nodes)
	nodes, err := lb.GetTopNodesWithScores(lbctx, internalName, lat, lon, make(map[string]int), "", 5, false)
	if err != nil {
		return nil, fmt.Errorf("no suitable edge nodes available: %v", err)
	}

	var (
		endpoints      []*pb.ViewerEndpoint
		primaryNodeLat float64
		primaryNodeLon float64
	)

	for _, node := range nodes {
		// Get outputs from control package
		nodeOutputs, exists := control.GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		// Build URLs with view key (MistServer resolves via PLAY_REWRITE trigger)
		var protocol, nodeURL string
		if webrtcURL, ok := nodeOutputs.Outputs["WebRTC"].(string); ok {
			protocol = "webrtc"
			nodeURL = strings.Replace(webrtcURL, "$", viewKey, -1)
			nodeURL = strings.Replace(nodeURL, "HOST", strings.TrimPrefix(nodeOutputs.BaseURL, "https://"), -1)
		} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
			protocol = "hls"
			nodeURL = strings.Replace(hlsURL, "$", viewKey, -1)
			nodeURL = strings.Trim(nodeURL, "[\"")
		}

		if nodeURL == "" {
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

		endpoint := &pb.ViewerEndpoint{
			NodeId:      node.NodeID,
			BaseUrl:     nodeOutputs.BaseURL,
			Protocol:    protocol,
			Url:         nodeURL,
			GeoDistance: geoDistance,
			LoadScore:   float64(node.Score),
			Outputs:     buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, viewKey, true),
		}
		endpoints = append(endpoints, endpoint)

		// Keep geo for primary (first) node
		if len(endpoints) == 1 {
			primaryNodeLat = node.GeoLatitude
			primaryNodeLon = node.GeoLongitude
		}
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no nodes with suitable outputs available")
	}

	emitViewerRoutingEvent(req, endpoints[0], lat, lon, primaryNodeLat, primaryNodeLon, target.InternalName)

	return &pb.ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  buildLivePlaybackMetadata(req, endpoints),
	}, nil
}

// resolveDVRViewerEndpoint queries database for DVR storage node
func resolveDVRViewerEndpoint(req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
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
	`, req.ContentId).Scan(&tenantID, &internalName, &nodeID, &status, &duration, &recordingSize, &manifestPath, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("DVR recording not found")
		}
		return nil, fmt.Errorf("failed to query DVR: %v", err)
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)

	if !exists || nodeOutputs.Outputs == nil {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	// Use DVR hash directly in URLs (MistServer resolves via PLAY_REWRITE trigger)
	dvrHash := req.ContentId
	var protocol, nodeURL string

	if httpURL, ok := nodeOutputs.Outputs["HTTP"].(string); ok {
		protocol = "http"
		nodeURL = strings.Replace(httpURL, "$", dvrHash, -1)
		nodeURL = strings.Trim(nodeURL, "[\"")
	} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		nodeURL = strings.Replace(hlsURL, "$", dvrHash, -1)
		nodeURL = strings.Trim(nodeURL, "[\"")
	}

	if nodeURL == "" {
		return nil, fmt.Errorf("no suitable outputs for DVR playback")
	}

	endpoint := &pb.ViewerEndpoint{
		NodeId:   nodeID,
		BaseUrl:  nodeOutputs.BaseURL,
		Protocol: protocol,
		Url:      nodeURL,
		Outputs:  buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, dvrHash, false),
	}

	// Emit routing event for DVR playback (storage node choice)
	emitViewerRoutingEvent(req, endpoint, 0, 0, 0, 0, internalName)

	meta := &pb.PlaybackMetadata{
		Status:        status,
		IsLive:        false,
		TenantId:      tenantID,
		ContentId:     req.ContentId,
		ContentType:   req.ContentType,
		DvrStatus:     status,
		ProtocolHints: protocolHintsFromEndpoints([]*pb.ViewerEndpoint{endpoint}),
	}

	if duration.Valid {
		d := int32(duration.Int64)
		meta.DurationSeconds = &d
	}
	if recordingSize.Valid {
		meta.RecordingSizeBytes = &recordingSize.Int64
	}
	if manifestPath.Valid {
		meta.DvrSourceUri = manifestPath.String
	}
	if createdAt.Valid {
		meta.CreatedAt = timestamppb.New(createdAt.Time)
	}
	if internalName != "" {
		meta.ClipSource = &internalName
	}

	return &pb.ViewerEndpointResponse{
		Primary:  endpoint,
		Metadata: meta,
	}, nil
}

// resolveClipViewerEndpoint queries database for clip storage node
func resolveClipViewerEndpoint(req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
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
	`, req.ContentId).Scan(&tenantID, &nodeID, &status, &sourceStream, &title, &duration, &fileSize, &createdAt, &baseURL, &storagePath)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("clip not found")
		}
		return nil, fmt.Errorf("failed to query clip: %v", err)
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)

	if !exists || nodeOutputs.Outputs == nil {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	// Use clip hash directly in URLs (MistServer resolves via PLAY_REWRITE trigger)
	clipHash := req.ContentId
	var protocol, nodeURL string

	if httpURL, ok := nodeOutputs.Outputs["HTTP"].(string); ok {
		protocol = "http"
		nodeURL = strings.Replace(httpURL, "$", clipHash, -1)
		nodeURL = strings.Trim(nodeURL, "[\"")
	} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		nodeURL = strings.Replace(hlsURL, "$", clipHash, -1)
		nodeURL = strings.Trim(nodeURL, "[\"")
	}

	if nodeURL == "" {
		return nil, fmt.Errorf("no suitable outputs for clip playback")
	}

	endpoint := &pb.ViewerEndpoint{
		NodeId:   nodeID,
		BaseUrl:  nodeOutputs.BaseURL,
		Protocol: protocol,
		Url:      nodeURL,
		Outputs:  buildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, clipHash, false),
	}

	// Emit routing event for clip playback (storage node choice)
	emitViewerRoutingEvent(req, endpoint, 0, 0, 0, 0, sourceStream)

	meta := &pb.PlaybackMetadata{
		Status:        status,
		IsLive:        false,
		TenantId:      tenantID,
		ContentId:     req.ContentId,
		ContentType:   req.ContentType,
		ProtocolHints: protocolHintsFromEndpoints([]*pb.ViewerEndpoint{endpoint}),
	}
	if sourceStream != "" {
		meta.ClipSource = &sourceStream
	}
	if title.Valid {
		meta.Title = &title.String
	}
	if duration.Valid {
		totalMs := duration.Int64
		secs := int32(totalMs / 1000)
		if totalMs%1000 != 0 {
			secs++
		}
		if secs < 0 {
			secs = 0
		}
		meta.DurationSeconds = &secs
	}
	if fileSize.Valid {
		meta.RecordingSizeBytes = &fileSize.Int64
	}
	if createdAt.Valid {
		meta.CreatedAt = timestamppb.New(createdAt.Time)
	}
	if baseURL.Valid && storagePath.Valid {
		combined := strings.TrimRight(baseURL.String, "/") + "/" + strings.TrimLeft(storagePath.String, "/")
		meta.DvrSourceUri = combined
	} else if storagePath.Valid {
		meta.DvrSourceUri = storagePath.String
	}

	return &pb.ViewerEndpointResponse{
		Primary:  endpoint,
		Metadata: meta,
	}, nil
}

// HandleGenericViewerPlayback handles /play/* and /resolve/* endpoints for generic players
// Supports patterns:
//   - /play/:viewkey or /resolve/:viewkey -> Returns full JSON with all protocols
//   - /play/:viewkey/:protocol or /play/:viewkey.:protocol -> 307 redirect to edge node
//   - Auto-detects protocol from extension (.m3u8 -> HLS, .webrtc -> WebRTC, etc.)
func HandleGenericViewerPlayback(c *gin.Context) {
	// Extract the full path after /play or /resolve
	fullPath := c.Param("path")
	if fullPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing view key in path"})
		return
	}

	// Remove leading slash if present
	fullPath = strings.TrimPrefix(fullPath, "/")

	// Parse the path to extract view key and protocol
	var viewKey string
	var protocol string
	var manifestPath string

	// Split path by "/" and "."
	parts := strings.Split(fullPath, "/")

	if len(parts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid path format"})
		return
	}

	// First part might contain viewkey.protocol or just viewkey
	firstPart := parts[0]
	dotParts := strings.Split(firstPart, ".")

	viewKey = dotParts[0]

	// Check if protocol specified after dot (e.g., viewkey.hls or viewkey.m3u8)
	if len(dotParts) > 1 {
		protocol = dotParts[1]
	}

	// Check if protocol specified as second path segment (e.g., viewkey/hls)
	if len(parts) > 1 && protocol == "" {
		protocol = parts[1]
		// Remaining parts are manifest path (e.g., index.m3u8)
		if len(parts) > 2 {
			manifestPath = strings.Join(parts[2:], "/")
		}
	}

	// If no protocol but there's a manifest path, detect from extension
	if protocol == "" && len(parts) > 1 {
		lastPart := parts[len(parts)-1]
		if strings.HasSuffix(lastPart, ".m3u8") {
			protocol = "hls"
			manifestPath = strings.Join(parts[1:], "/")
		} else if strings.HasSuffix(lastPart, ".mpd") {
			protocol = "dash"
			manifestPath = strings.Join(parts[1:], "/")
		} else if strings.HasSuffix(lastPart, ".html") {
			protocol = "html"
			// No manifest path for HTML embed - it redirects directly to MistServer's embed page
		}
	}

	// Validate view key format (should be non-empty)
	if viewKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid view key"})
		return
	}

	// Resolve view key to internal name using Commodore
	if commodoreClient == nil {
		logger.Error("Commodore client not initialized")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Service configuration error"})
		return
	}

	ctx := context.Background()
	resolveResp, err := commodoreClient.ResolvePlaybackID(ctx, viewKey)
	if err != nil {
		logger.WithError(err).WithField("view_key", viewKey).Warn("Failed to resolve view key")
		c.JSON(http.StatusNotFound, gin.H{"error": "Invalid or expired view key"})
		return
	}

	if resolveResp.InternalName == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Stream not found"})
		return
	}

	// Determine content type (default to live for now, can be enhanced)
	contentType := "live"
	contentID := resolveResp.InternalName

	// Check if it's a clip (UUID format) or DVR hash
	if len(contentID) == 36 && strings.Count(contentID, "-") == 4 {
		// Could be a clip UUID, try to determine type
		// For now, assume live streams use internal_name format
		contentType = "live"
	}

	// Normalize protocol
	protocol = normalizeProtocol(protocol)

	// Build viewer endpoint request
	viewerIP := c.ClientIP()
	req := &pb.ViewerEndpointRequest{
		ContentType: contentType,
		ContentId:   contentID,
		ViewerIp:    proto.String(viewerIP),
	}

	// Get geo location for viewer
	var lat, lon float64
	if geoipReader != nil {
		if geoData := geoipReader.Lookup(viewerIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
		}
	}

	// Resolve endpoint
	var response *pb.ViewerEndpointResponse
	switch contentType {
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
			"view_key":      viewKey,
			"internal_name": contentID,
			"content_type":  contentType,
		}).Error("Failed to resolve viewer endpoint")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve playback endpoint"})
		return
	}

	// If no protocol or "any", return full JSON response
	if protocol == "" || protocol == "any" {
		// Record metrics
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("generic_viewer", contentType, response.Primary.NodeId).Inc()
		}

		logger.WithFields(logging.Fields{
			"view_key":      viewKey,
			"internal_name": contentID,
			"content_type":  contentType,
			"node_id":       response.Primary.NodeId,
		}).Info("Resolved generic viewer endpoint (JSON)")

		// Use protojson for proper JSON serialization of proto message
		jsonBytes, err := protojson.Marshal(response)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to serialize response"})
			return
		}
		c.Data(http.StatusOK, "application/json", jsonBytes)
		return
	}

	// Protocol specified - redirect to specific edge node
	// Find the URL for the requested protocol from the outputs
	var redirectURL string

	// Check primary outputs
	if response.Primary.Outputs != nil {
		redirectURL = findProtocolURL(response.Primary.Outputs, protocol)
	}

	// If not found and there's a direct URL, use it
	if redirectURL == "" && response.Primary.Url != "" {
		redirectURL = response.Primary.Url
	}

	if redirectURL == "" {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("Protocol '%s' not available for this stream", protocol),
		})
		return
	}

	// Append manifest path if specified
	if manifestPath != "" {
		// Check if redirect URL already has a path
		if !strings.HasSuffix(redirectURL, "/") && !strings.HasPrefix(manifestPath, "/") {
			redirectURL += "/"
		}
		redirectURL += manifestPath
	}

	// Record metrics
	if metrics != nil {
		metrics.RoutingDecisions.WithLabelValues("generic_viewer_redirect", protocol, response.Primary.NodeId).Inc()
	}

	logger.WithFields(logging.Fields{
		"view_key":      viewKey,
		"internal_name": contentID,
		"protocol":      protocol,
		"redirect_url":  redirectURL,
		"node_id":       response.Primary.NodeId,
	}).Info("Redirecting generic viewer to edge node")

	// Return 307 Temporary Redirect
	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}

// normalizeProtocol converts protocol hints to standard names
func normalizeProtocol(proto string) string {
	proto = strings.ToLower(proto)
	switch proto {
	case "m3u8", "hls":
		return "hls"
	case "mpd", "dash":
		return "dash"
	case "webrtc", "whep":
		return "webrtc"
	case "srt":
		return "srt"
	case "rtmp":
		return "rtmp"
	case "any", "all", "":
		return "any"
	default:
		return proto
	}
}

// findProtocolURL searches for a URL matching the requested protocol in outputs
func findProtocolURL(outputs map[string]*pb.OutputEndpoint, protocol string) string {
	// Normalize protocol for comparison
	protocol = strings.ToLower(protocol)

	// Try direct match first
	for outputName, output := range outputs {
		outputLower := strings.ToLower(outputName)
		if outputLower == protocol {
			return output.Url
		}
	}

	// Try fuzzy matching
	switch protocol {
	case "hls":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "hls") {
				return output.Url
			}
		}
	case "dash":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "dash") {
				return output.Url
			}
		}
	case "webrtc", "whep":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "webrtc") || strings.Contains(outputLower, "whep") {
				return output.Url
			}
		}
	case "html", "embed":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "html") || strings.Contains(outputLower, "embed") {
				return output.Url
			}
		}
	}

	return ""
}
