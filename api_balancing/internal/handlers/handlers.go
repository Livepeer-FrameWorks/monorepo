package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
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
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/version"
	"frameworks/pkg/x402"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	db                  *sql.DB
	logger              logging.Logger
	lb                  *balancer.LoadBalancer
	decklogClient       *decklog.BatchedClient
	commodoreClient     *commodore.GRPCClient
	purserClient        *purserclient.GRPCClient
	quartermasterClient *qmclient.GRPCClient
	metrics             *FoghornMetrics
	geoipReader         *geoip.Reader
	geoipCache          *cache.Cache
	// Dual-tenant attribution (RFC: routing-events-dual-tenant-attribution)
	// clusterID = emitting cluster identifier
	// ownerTenantID = cluster operator tenant (infra owner) for event storage
	clusterID     string
	ownerTenantID string
)

// GetClusterInfo returns cached cluster attribution info for dual-tenant routing events
// Returns (clusterID, ownerTenantID) - used by gRPC server for event emission
func GetClusterInfo() (string, string) {
	return clusterID, ownerTenantID
}

func bootstrapClusterInfo(ctx context.Context) error {
	if quartermasterClient == nil {
		return fmt.Errorf("quartermaster client not configured")
	}
	advertiseHost := config.GetEnv("FOGHORN_HOST", "foghorn")
	reqClusterID := config.GetEnv("CLUSTER_ID", "")
	resp, err := quartermasterClient.BootstrapService(ctx, &pb.BootstrapServiceRequest{
		Type:           "foghorn",
		Version:        version.Version,
		Protocol:       "http",
		HealthEndpoint: func() *string { s := "/health"; return &s }(),
		Port:           18008,
		AdvertiseHost:  &advertiseHost,
		ClusterId: func() *string {
			if reqClusterID != "" {
				return &reqClusterID
			}
			return nil
		}(),
	})
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("quartermaster bootstrap returned nil response")
	}

	if resp.OwnerTenantId != nil && *resp.OwnerTenantId != "" {
		ownerTenantID = *resp.OwnerTenantId
		logger.WithFields(logging.Fields{
			"cluster_id":      resp.ClusterId,
			"owner_tenant_id": ownerTenantID,
		}).Info("Cached cluster owner tenant for dual-tenant attribution")
		if triggerProcessor != nil {
			triggerProcessor.SetOwnerTenantID(ownerTenantID)
		}
	}
	if clusterID == "" && resp.ClusterId != "" {
		clusterID = resp.ClusterId
		if triggerProcessor != nil {
			triggerProcessor.SetClusterID(clusterID)
		}
	}
	return nil
}

type clipLifecycleContext struct {
	ClipHash     string
	TenantID     string
	UserID       string
	InternalName string
	StreamID     string

	StartUnix   *int64
	StopUnix    *int64
	StartMs     *int64
	StopMs      *int64
	DurationSec *int64
	ClipMode    *string
}

func getClipLifecycleContextByRequestID(requestID string) clipLifecycleContext {
	if db == nil || requestID == "" {
		return clipLifecycleContext{}
	}

	queryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Get local context from foghorn.artifacts (denormalized fallback values).
	var clipHash, internalName string
	var fallbackTenantID sql.NullString
	var fallbackUserID sql.NullString
	err := db.QueryRowContext(queryCtx, `
		SELECT artifact_hash, internal_name, tenant_id, user_id
		FROM foghorn.artifacts
		WHERE request_id = $1 AND artifact_type = 'clip'
	`, requestID).Scan(&clipHash, &internalName, &fallbackTenantID, &fallbackUserID)
	if err != nil || clipHash == "" {
		return clipLifecycleContext{}
	}

	ctx := clipLifecycleContext{
		ClipHash:     clipHash,
		InternalName: internalName,
	}
	if fallbackTenantID.Valid {
		ctx.TenantID = fallbackTenantID.String
	}
	if fallbackUserID.Valid {
		ctx.UserID = fallbackUserID.String
	}

	// Prefer Commodore business registry for canonical tenant/stream attribution + timing enrichment.
	if commodoreClient == nil {
		return ctx
	}

	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := commodoreClient.ResolveClipHash(cctx, clipHash)
	if err != nil || !resp.Found {
		return ctx
	}

	if resp.TenantId != "" {
		ctx.TenantID = resp.TenantId
	}
	if resp.UserId != "" {
		ctx.UserID = resp.UserId
	}
	if resp.InternalName != "" {
		ctx.InternalName = resp.InternalName
	}
	if resp.StreamId != "" {
		ctx.StreamID = resp.StreamId
	}
	if resp.ClipMode != "" {
		mode := resp.ClipMode
		ctx.ClipMode = &mode
	}

	// Commodore stores clip timing as: start_time (unix ms), duration (ms).
	if resp.StartTime > 0 && resp.Duration > 0 {
		startMs := resp.StartTime
		stopMs := resp.StartTime + resp.Duration
		startUnix := startMs / 1000
		stopUnix := stopMs / 1000
		durationSec := resp.Duration / 1000

		ctx.StartMs = &startMs
		ctx.StopMs = &stopMs
		ctx.StartUnix = &startUnix
		ctx.StopUnix = &stopUnix
		ctx.DurationSec = &durationSec
	}

	return ctx
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

// StreamIDRegex matches public playback IDs and internal names (live+/vod+).
// Accepts alnum, underscore, hyphen, plus; blocks slashes and empty values.
var StreamIDRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+\-]{3,127}$`)

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
	pClient *purserclient.GRPCClient,
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
	purserClient = pClient
	quartermasterClient = qClient
	geoipReader = geo
	geoipCache = geoCache
	// Initialize cluster ID for dual-tenant attribution
	clusterID = config.GetEnv("CLUSTER_ID", "")

	// Share database connection with control package for clip operations
	control.SetDB(database)

	// Share Commodore client with control package for unified resolution logic
	control.CommodoreClient = cClient

	// Share Quartermaster client with control package
	control.SetQuartermasterClient(qClient)

	// Self-register Foghorn instance in Quartermaster and cache owner_tenant_id for dual-tenant attribution
	bootstrapCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := bootstrapClusterInfo(bootstrapCtx); err != nil {
		logger.WithError(err).Warn("Synchronous bootstrap failed, retrying async")
		go func() {
			retryCtx, retryCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer retryCancel()
			if err := bootstrapClusterInfo(retryCtx); err != nil {
				logger.WithError(err).Warn("Async bootstrap failed")
			}
		}()
	}
	cancel()

	// Register clip progress/done handlers to emit analytics
	control.SetClipHandlers(
		func(p *pb.ClipProgress) {
			if decklogClient == nil {
				return
			}
			cctx := getClipLifecycleContextByRequestID(p.GetRequestId())
			clipData := &pb.ClipLifecycleData{
				Stage:     pb.ClipLifecycleData_STAGE_PROGRESS,
				ClipHash:  cctx.ClipHash,
				RequestId: func() *string { s := p.GetRequestId(); return &s }(),
				StartedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
				// Enrichment fields added by Foghorn
				TenantId: func() *string {
					if cctx.TenantID != "" {
						return &cctx.TenantID
					} else {
						return nil
					}
				}(),
				InternalName: func() *string {
					if cctx.InternalName != "" {
						return &cctx.InternalName
					} else {
						return nil
					}
				}(),
				StreamId: func() *string {
					if cctx.StreamID != "" {
						return &cctx.StreamID
					}
					return nil
				}(),
				UserId: func() *string {
					if cctx.UserID != "" {
						return &cctx.UserID
					}
					return nil
				}(),
				StartUnix:   cctx.StartUnix,
				StopUnix:    cctx.StopUnix,
				StartMs:     cctx.StartMs,
				StopMs:      cctx.StopMs,
				DurationSec: cctx.DurationSec,
				ClipMode:    cctx.ClipMode,
			}
			if p.GetPercent() > 0 {
				percent := uint32(p.GetPercent())
				clipData.ProgressPercent = &percent
			}
			go func() {
				if err := decklogClient.SendClipLifecycle(clipData); err != nil {
					logger.WithError(err).WithField("request_id", clipData.GetRequestId()).Warn("Failed to send clip progress to Decklog")
				}
			}()
		},
		func(dn *pb.ClipDone) {
			if decklogClient == nil {
				return
			}
			cctx := getClipLifecycleContextByRequestID(dn.GetRequestId())
			stage := pb.ClipLifecycleData_STAGE_DONE
			if dn.GetStatus() != "success" {
				stage = pb.ClipLifecycleData_STAGE_FAILED
			}
			clipData := &pb.ClipLifecycleData{
				Stage:       stage,
				ClipHash:    cctx.ClipHash,
				RequestId:   func() *string { s := dn.GetRequestId(); return &s }(),
				CompletedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
				// Enrichment fields added by Foghorn
				TenantId: func() *string {
					if cctx.TenantID != "" {
						return &cctx.TenantID
					} else {
						return nil
					}
				}(),
				InternalName: func() *string {
					if cctx.InternalName != "" {
						return &cctx.InternalName
					} else {
						return nil
					}
				}(),
				StreamId: func() *string {
					if cctx.StreamID != "" {
						return &cctx.StreamID
					}
					return nil
				}(),
				UserId: func() *string {
					if cctx.UserID != "" {
						return &cctx.UserID
					}
					return nil
				}(),
				StartUnix:   cctx.StartUnix,
				StopUnix:    cctx.StopUnix,
				StartMs:     cctx.StartMs,
				StopMs:      cctx.StopMs,
				DurationSec: cctx.DurationSec,
				ClipMode:    cctx.ClipMode,
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
				if err := decklogClient.SendClipLifecycle(clipData); err != nil {
					logger.WithError(err).WithField("request_id", clipData.GetRequestId()).Warn("Failed to send clip done to Decklog")
				}
			}()
		},
		func(del *pb.ArtifactDeleted) {
			clipHash := del.GetClipHash()
			nodeID := del.GetNodeId()
			reason := del.GetReason()

			// This message indicates node-local deletion/eviction. Remove the node cache record.
			_, err := db.Exec(`DELETE FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2`, clipHash, nodeID)
			if err != nil {
				logger.WithError(err).WithField("clip_hash", clipHash).Error("Failed to remove artifact node assignment")
			}

			// If the artifact has no remaining cached nodes and is synced, reflect that it is now S3-only.
			var hasAnyNodes bool
			_ = db.QueryRow(`
				SELECT EXISTS(
					SELECT 1 FROM foghorn.artifact_nodes
					WHERE artifact_hash = $1 AND NOT is_orphaned
				)
			`, clipHash).Scan(&hasAnyNodes)
			if !hasAnyNodes {
				_, _ = db.Exec(`
					UPDATE foghorn.artifacts
					SET storage_location = CASE
						WHEN sync_status = 'synced' THEN 's3'
						ELSE storage_location
					END,
					updated_at = NOW()
					WHERE artifact_hash = $1
				`, clipHash)
			}

			// Evictions must never be treated as global deletes.
			if reason == "eviction" {
				logger.WithFields(logging.Fields{
					"clip_hash": clipHash,
					"node_id":   nodeID,
				}).Info("Clip evicted from node cache")
				return
			}

			// Only emit DELETED when the artifact is already soft-deleted in foghorn.artifacts.
			// This avoids conflating node-local cleanup with user-initiated deletion.
			var artifactStatus string
			if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1`, clipHash).Scan(&artifactStatus); err != nil {
				logger.WithError(err).WithField("clip_hash", clipHash).Warn("Failed to read artifact status for deletion lifecycle")
				return
			}
			if artifactStatus != "deleted" {
				logger.WithFields(logging.Fields{
					"clip_hash": clipHash,
					"node_id":   nodeID,
					"reason":    reason,
				}).Info("Clip removed from node but not globally deleted")
				return
			}

		},
	)

	control.SetDVRStoppedHandler(func(dvrHash string, finalStatus string, nodeID string, sizeBytes uint64, manifestPath string, errorMsg string) {
		if decklogClient == nil {
			return
		}

		status := pb.DVRLifecycleData_STATUS_STOPPED
		if finalStatus == "failed" {
			status = pb.DVRLifecycleData_STATUS_FAILED
		}

		var (
			tenantIDStr     string
			userIDStr       string
			internalNameStr string
			streamID        string
			retentionUntil  sql.NullTime
			startedAt       sql.NullTime
			endedAt         sql.NullTime
		)

		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if commodoreClient != nil {
			if resp, err := commodoreClient.ResolveDVRHash(cctx, dvrHash); err == nil && resp.Found {
				tenantIDStr = resp.TenantId
				userIDStr = resp.UserId
				internalNameStr = resp.InternalName
				streamID = resp.StreamId
			}
		}

		_ = db.QueryRowContext(cctx, `
			SELECT retention_until, started_at, ended_at
			FROM foghorn.artifacts
			WHERE artifact_hash = $1
			  AND artifact_type = 'dvr'
			  AND COALESCE(tenant_id::text, '') = $2
		`, dvrHash, tenantIDStr).Scan(&retentionUntil, &startedAt, &endedAt)

		dvrData := &pb.DVRLifecycleData{
			Status:  status,
			DvrHash: dvrHash,
		}
		if nodeID != "" {
			dvrData.NodeId = &nodeID
		}
		if tenantIDStr != "" {
			dvrData.TenantId = &tenantIDStr
		}
		if internalNameStr != "" {
			dvrData.InternalName = &internalNameStr
		}
		if streamID != "" {
			dvrData.StreamId = &streamID
		}
		if userIDStr != "" {
			dvrData.UserId = &userIDStr
		}
		if sizeBytes > 0 {
			dvrData.SizeBytes = &sizeBytes
		}
		if manifestPath != "" {
			dvrData.ManifestPath = &manifestPath
		}
		if errorMsg != "" {
			dvrData.Error = &errorMsg
		}
		if retentionUntil.Valid {
			exp := retentionUntil.Time.Unix()
			dvrData.ExpiresAt = &exp
		}
		if startedAt.Valid {
			st := startedAt.Time.Unix()
			dvrData.StartedAt = &st
		}
		if endedAt.Valid {
			et := endedAt.Time.Unix()
			dvrData.EndedAt = &et
		}

		go func() {
			if err := decklogClient.SendDVRLifecycle(dvrData); err != nil {
				logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to send DVR stopped event to Decklog")
			}
		}()
	})

	control.SetDVRDeletedHandler(func(dvrHash string, sizeBytes uint64, nodeID string) {
		if decklogClient == nil {
			return
		}

		var (
			tenantIDStr     string
			userIDStr       string
			internalNameStr string
			streamID        string
			retentionUntil  sql.NullTime
			startedAt       sql.NullTime
			endedAt         sql.NullTime
		)

		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if commodoreClient != nil {
			if resp, err := commodoreClient.ResolveDVRHash(cctx, dvrHash); err == nil && resp.Found {
				tenantIDStr = resp.TenantId
				userIDStr = resp.UserId
				internalNameStr = resp.InternalName
				streamID = resp.StreamId
			}
		}

		_ = db.QueryRowContext(cctx, `
			SELECT retention_until, started_at, ended_at
			FROM foghorn.artifacts
			WHERE artifact_hash = $1
			  AND artifact_type = 'dvr'
			  AND COALESCE(tenant_id::text, '') = $2
		`, dvrHash, tenantIDStr).Scan(&retentionUntil, &startedAt, &endedAt)

		dvrData := &pb.DVRLifecycleData{
			Status:  pb.DVRLifecycleData_STATUS_DELETED,
			DvrHash: dvrHash,
		}
		if nodeID != "" {
			dvrData.NodeId = &nodeID
		}
		if tenantIDStr != "" {
			dvrData.TenantId = &tenantIDStr
		}
		if internalNameStr != "" {
			dvrData.InternalName = &internalNameStr
		}
		if streamID != "" {
			dvrData.StreamId = &streamID
		}
		if userIDStr != "" {
			dvrData.UserId = &userIDStr
		}
		if sizeBytes > 0 {
			dvrData.SizeBytes = &sizeBytes
		}
		if retentionUntil.Valid {
			exp := retentionUntil.Time.Unix()
			dvrData.ExpiresAt = &exp
		}
		if startedAt.Valid {
			st := startedAt.Time.Unix()
			dvrData.StartedAt = &st
		}
		if endedAt.Valid {
			et := endedAt.Time.Unix()
			dvrData.EndedAt = &et
		}

		go func() {
			if err := decklogClient.SendDVRLifecycle(dvrData); err != nil {
				logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to send DVR deleted event to Decklog")
			}
		}()
	})

	// Set dtsh sync handler for incremental .dtsh uploads
	// Called when periodic artifact scan detects .dtsh exists locally but wasn't synced
	state.SetDtshSyncHandler(control.TriggerDtshSync)

	// Set clip hash resolver - uses Commodore for tenant context
	control.SetClipHashResolver(func(clipHash string) (string, string, error) {
		if commodoreClient == nil {
			return "", "", nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Try clip first
		if resp, err := commodoreClient.ResolveClipHash(ctx, clipHash); err == nil && resp.Found {
			return resp.TenantId, resp.InternalName, nil
		}

		// Fallback to DVR
		if resp, err := commodoreClient.ResolveDVRHash(ctx, clipHash); err == nil && resp.Found {
			return resp.TenantId, resp.InternalName, nil
		}

		return "", "", nil
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
	if streamName == "" || !StreamIDRegex.MatchString(streamName) {
		c.String(http.StatusBadRequest, "Invalid stream name")
		return
	}

	handleStreamBalancing(c, streamName)
}

// HandleNodesOverview receives a request for an overview of all nodes with capabilities, limits, and artifacts.
// When ?full=true is passed, includes full Foghorn state: DB artifacts, processing jobs, and stream instances.
func HandleNodesOverview(c *gin.Context) {
	capFilter := c.Query("cap")
	offsetStr := c.Query("offset")
	limitStr := c.Query("limit")
	fullState := c.Query("full") == "true"
	includeStale := c.Query("include_stale") == "true"
	var offset, limit int
	if v, err := strconv.Atoi(offsetStr); err == nil {
		offset = v
	}
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}

	snapshot := state.DefaultManager().GetBalancerSnapshotAtomicWithOptions(includeStale)
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
				"node_id":          n.NodeID,
				"host":             n.Host,
				"roles":            n.Roles,
				"operational_mode": n.OperationalMode,
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
				"is_active": n.IsActive,
				"is_stale": func() bool {
					if ns := state.DefaultManager().GetNodeState(n.NodeID); ns != nil {
						return ns.IsStale
					}
					return false
				}(),
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
				"bw_limit":        n.BWLimit,
				"avail_bandwidth": n.AvailBandwidth,
				"add_bandwidth":   n.AddBandwidth,
				"tags":            n.Tags,
				// networking
				"port":           n.Port,
				"dtsc_port":      n.DTSCPort,
				"config_streams": n.ConfigStreams,
				// storage
				"storage_local":    n.StorageLocal,
				"storage_bucket":   n.StorageBucket,
				"storage_prefix":   n.StoragePrefix,
				"disk_total_bytes": n.DiskTotalBytes,
				"disk_used_bytes":  n.DiskUsedBytes,
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

	// If not full state, return just the nodes array (backwards compatible)
	if !fullState {
		c.JSON(http.StatusOK, out)
		return
	}

	// Full state mode: include DB artifacts, processing jobs, and stream instances
	response := map[string]interface{}{
		"nodes":      out,
		"node_count": len(out),
	}

	// Query DB artifacts with vod_metadata
	if db != nil {
		artifactRows, err := db.Query(`
			SELECT
				a.artifact_hash, a.artifact_type, a.status, a.internal_name, a.tenant_id,
				a.storage_location, a.sync_status, a.s3_url, a.format, a.size_bytes,
				a.manifest_path, a.duration_seconds, a.dtsh_synced, a.retention_until,
				a.created_at, a.updated_at,
				v.video_codec, v.audio_codec, v.resolution, v.duration_ms, v.bitrate_kbps,
				v.filename, v.title
			FROM foghorn.artifacts a
			LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
			WHERE a.status != 'deleted'
			ORDER BY a.created_at DESC
			LIMIT 500
		`)
		if err == nil {
			defer artifactRows.Close()
			artifacts := []map[string]interface{}{}
			for artifactRows.Next() {
				var hash, artType, status, storageLocation, syncStatus string
				var internalName, tenantID, s3URL, format, manifestPath, retentionUntil sql.NullString
				var sizeBytes sql.NullInt64
				var durationSeconds sql.NullInt32
				var dtshSynced sql.NullBool
				var createdAt, updatedAt time.Time
				var videoCodec, audioCodec, resolution, filename, title sql.NullString
				var durationMs, bitrateKbps sql.NullInt32

				errScan := artifactRows.Scan(
					&hash, &artType, &status, &internalName, &tenantID,
					&storageLocation, &syncStatus, &s3URL, &format, &sizeBytes,
					&manifestPath, &durationSeconds, &dtshSynced, &retentionUntil,
					&createdAt, &updatedAt,
					&videoCodec, &audioCodec, &resolution, &durationMs, &bitrateKbps,
					&filename, &title,
				)
				if errScan != nil {
					continue
				}

				art := map[string]interface{}{
					"artifact_hash":    hash,
					"artifact_type":    artType,
					"status":           status,
					"storage_location": storageLocation,
					"sync_status":      syncStatus,
					"dtsh_synced":      dtshSynced.Bool,
					"created_at":       createdAt.Format(time.RFC3339),
					"updated_at":       updatedAt.Format(time.RFC3339),
				}
				if internalName.Valid {
					art["internal_name"] = internalName.String
				}
				if tenantID.Valid {
					art["tenant_id"] = tenantID.String
				}
				if s3URL.Valid {
					art["s3_url"] = s3URL.String
				}
				if format.Valid {
					art["format"] = format.String
				}
				if sizeBytes.Valid {
					art["size_bytes"] = sizeBytes.Int64
				}
				if manifestPath.Valid {
					art["manifest_path"] = manifestPath.String
				}
				if durationSeconds.Valid {
					art["duration_seconds"] = durationSeconds.Int32
				}
				if retentionUntil.Valid {
					art["retention_until"] = retentionUntil.String
				}
				// VOD metadata
				if videoCodec.Valid {
					art["video_codec"] = videoCodec.String
				}
				if audioCodec.Valid {
					art["audio_codec"] = audioCodec.String
				}
				if resolution.Valid {
					art["resolution"] = resolution.String
				}
				if durationMs.Valid {
					art["duration_ms"] = durationMs.Int32
				}
				if bitrateKbps.Valid {
					art["bitrate_kbps"] = bitrateKbps.Int32
				}
				if filename.Valid {
					art["filename"] = filename.String
				}
				if title.Valid {
					art["title"] = title.String
				}

				// Query nodes hosting this artifact
				art["nodes"] = func() []string {
					nodeRows, errQuery := db.QueryContext(context.Background(), `
						SELECT node_id FROM foghorn.artifact_nodes
						WHERE artifact_hash = $1 AND NOT is_orphaned
					`, hash)
					if errQuery != nil {
						return nil
					}
					defer func() { _ = nodeRows.Close() }()
					var nodeIDs []string
					for nodeRows.Next() {
						var nodeID string
						if errScan := nodeRows.Scan(&nodeID); errScan == nil {
							nodeIDs = append(nodeIDs, nodeID)
						}
					}
					return nodeIDs
				}()

				artifacts = append(artifacts, art)
			}
			response["artifacts"] = artifacts
			response["artifact_count"] = len(artifacts)
		} else {
			response["artifacts_error"] = err.Error()
		}

		// Query processing jobs
		jobRows, err := db.Query(`
			SELECT
				job_id, tenant_id, artifact_hash, job_type, status, progress,
				use_gateway, processing_node_id, routing_reason, error_message, retry_count,
				created_at, started_at, completed_at
			FROM foghorn.processing_jobs
			WHERE status NOT IN ('completed', 'failed') OR created_at > NOW() - INTERVAL '1 hour'
			ORDER BY created_at DESC
			LIMIT 100
		`)
		if err == nil {
			defer jobRows.Close()
			jobs := []map[string]interface{}{}
			for jobRows.Next() {
				var jobID, tenantID, jobType, status string
				var artifactHash, processingNode, routingReason, errorMessage sql.NullString
				var progress, retryCount int
				var useGateway bool
				var createdAt time.Time
				var startedAt, completedAt sql.NullTime

				errScan := jobRows.Scan(
					&jobID, &tenantID, &artifactHash, &jobType, &status, &progress,
					&useGateway, &processingNode, &routingReason, &errorMessage, &retryCount,
					&createdAt, &startedAt, &completedAt,
				)
				if errScan != nil {
					continue
				}

				job := map[string]interface{}{
					"job_id":      jobID,
					"tenant_id":   tenantID,
					"job_type":    jobType,
					"status":      status,
					"progress":    progress,
					"use_gateway": useGateway,
					"retry_count": retryCount,
					"created_at":  createdAt.Format(time.RFC3339),
				}
				if artifactHash.Valid {
					job["artifact_hash"] = artifactHash.String
				}
				if processingNode.Valid {
					job["processing_node"] = processingNode.String
				}
				if routingReason.Valid {
					job["routing_reason"] = routingReason.String
				}
				if errorMessage.Valid {
					job["error_message"] = errorMessage.String
				}
				if startedAt.Valid {
					job["started_at"] = startedAt.Time.Format(time.RFC3339)
				}
				if completedAt.Valid {
					job["completed_at"] = completedAt.Time.Format(time.RFC3339)
				}

				jobs = append(jobs, job)
			}
			response["processing_jobs"] = jobs
			response["processing_job_count"] = len(jobs)
		} else {
			response["processing_jobs_error"] = err.Error()
		}
	}

	// Get in-memory stream state with instances
	sm := state.DefaultManager()
	if sm != nil {
		allStreams := sm.GetAllStreamStates()
		streamInfos := []map[string]interface{}{}
		for _, s := range allStreams {
			info := map[string]interface{}{
				"internal_name": s.InternalName,
				"status":        s.Status,
				"viewers":       s.Viewers,
				"tenant_id":     s.TenantID,
				"buffer_state":  s.BufferState,
				"inputs":        s.Inputs,
				"bytes_up":      s.BytesUp,
				"bytes_down":    s.BytesDown,
			}
			// Get per-node instances
			instances := sm.GetStreamInstances(s.InternalName)
			if len(instances) > 0 {
				instMap := make(map[string]interface{})
				for nodeID, inst := range instances {
					instMap[nodeID] = map[string]interface{}{
						"viewers":      inst.Viewers,
						"buffer_state": inst.BufferState,
						"bytes_up":     inst.BytesUp,
						"bytes_down":   inst.BytesDown,
						"inputs":       inst.Inputs,
						"last_update":  inst.LastUpdate,
					}
				}
				info["instances"] = instMap
			}
			streamInfos = append(streamInfos, info)
		}
		response["streams"] = streamInfos
		response["stream_count"] = len(streamInfos)
	}

	c.JSON(http.StatusOK, response)
}

type nodeOperationalModeRequest struct {
	Mode  string `json:"mode"`
	SetBy string `json:"set_by"`
}

// stateToProtoMode converts internal state mode to protobuf enum
func stateToProtoMode(mode state.NodeOperationalMode) pb.NodeOperationalMode {
	switch mode {
	case state.NodeModeDraining:
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING
	case state.NodeModeMaintenance:
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE
	default:
		return pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
	}
}

func HandleSetNodeMaintenanceMode(c *gin.Context) {
	nodeID := strings.TrimSpace(c.Param("node_id"))
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node_id is required"})
		return
	}

	var req nodeOperationalModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	mode := state.NodeOperationalMode(strings.ToLower(strings.TrimSpace(req.Mode)))
	if err := state.DefaultManager().SetNodeOperationalMode(c.Request.Context(), nodeID, mode, strings.TrimSpace(req.SetBy)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Push mode to connected Helmsman via ConfigSeed
	protoMode := stateToProtoMode(mode)
	if err := control.PushOperationalMode(nodeID, protoMode); err != nil {
		// Log but don't fail - node might not be connected, will get mode on next connect
		logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"mode":    mode,
			"error":   err,
		}).Warn("Failed to push operational mode to node (may not be connected)")
	}

	c.JSON(http.StatusOK, gin.H{
		"node_id":          nodeID,
		"operational_mode": state.DefaultManager().GetNodeOperationalMode(nodeID),
		"active_viewers":   state.DefaultManager().GetNodeActiveViewers(nodeID),
	})
}

func HandleGetNodeDrainStatus(c *gin.Context) {
	nodeID := strings.TrimSpace(c.Param("node_id"))
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node_id is required"})
		return
	}

	if state.DefaultManager().GetNodeState(nodeID) == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"node_id":          nodeID,
		"operational_mode": state.DefaultManager().GetNodeOperationalMode(nodeID),
		"active_viewers":   state.DefaultManager().GetNodeActiveViewers(nodeID),
	})
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
		ctx = context.WithValue(ctx, ctxkeys.KeyCapability, requireCap)
	}
	// Source selection (Mist pull) -> isSourceSelection=true (exclude replicated)
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, streamName, lat, lon, tagAdjust, clientIP, true)
	if err != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", "failed").Inc()
		}
		// Post failed event
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "failed", err.Error(), 0, 0, "", durationMs)

		fallback := query.Get("fallback")
		if fallback == "" {
			fallback = "dtsc://localhost:4200"
		}
		// Log at Info level: MistServer is asking where to pull this stream from.
		// No node has it with active inputs, so MistServer will use push/local input instead.
		logger.WithFields(logging.Fields{
			"stream":   streamName,
			"fallback": fallback,
		}).Info("Source lookup: no node has active input for stream; returning fallback (MistServer will use push/local)")
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
	durationMs := float32(time.Since(start).Milliseconds())
	if query.Get("redirect") == "1" {
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", bestNode).Inc()
			metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
		}
		// Post redirect event
		go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "redirect", dtscURL, nodeLat, nodeLon, nodeName, durationMs)
		c.Redirect(http.StatusFound, dtscURL)
	} else {
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", bestNode).Inc()
			metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
		}
		// Post success event
		go postBalancingEvent(c, streamName, bestNode, score, lat, lon, "success", "", nodeLat, nodeLon, nodeName, durationMs)
		c.String(http.StatusOK, dtscURL)
	}
}

// handleFindIngest implements /?ingest=<cpu> (EXACT C++ implementation)
func handleFindIngest(c *gin.Context, cpuUsage string, query url.Values) {
	start := time.Now()
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
	ctx := context.WithValue(c.Request.Context(), ctxkeys.KeyCapability, requireCap)
	// Ingest -> isSourceSelection=true (though less relevant without streamName)
	bestNode, score, nodeLat, nodeLon, nodeName, err := lb.GetBestNodeWithScore(ctx, "", lat, lon, tagAdjust, "", true)
	if err != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		// Post failed ingest event
		go postBalancingEvent(c, "ingest", "", 0, lat, lon, "failed", err.Error(), 0, 0, "", durationMs)
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
					durationMs := float32(time.Since(start).Milliseconds())
					// Node would be overloaded, return fallback (like C++ FAIL_MSG("No ingest point found!"))
					go postBalancingEvent(c, "ingest", "", 0, lat, lon, "failed", "CPU overload", 0, 0, "", durationMs)
					c.String(http.StatusOK, "FULL") // C++ fallback for CPU overload
					return
				}
				break
			}
		}
	}

	durationMs := float32(time.Since(start).Milliseconds())
	// Post successful ingest event
	go postBalancingEvent(c, "ingest", bestNode, score, lat, lon, "success", "", nodeLat, nodeLon, nodeName, durationMs)
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
	internalName := mist.ExtractInternalName(target.InternalName)

	// Prepaid billing check (402)
	if target.TenantID != "" {
		billing := getBillingStatus(c.Request.Context(), internalName, target.TenantID)
		if billing != nil && billing.BillingModel == "prepaid" && (billing.IsSuspended || billing.IsBalanceNegative) {
			paymentHeader := x402.GetPaymentHeaderFromRequest(c.Request)
			resourcePath := c.Request.URL.Path
			paid, decision := settleX402PaymentForPlayback(c.Request.Context(), target.TenantID, resourcePath, paymentHeader, c.ClientIP(), logger)
			if decision != nil {
				respondX402Decision(c, decision)
				return
			}
			if !paid {
				message := "payment required - stream owner needs to top up balance"
				if billing.IsSuspended {
					message = "payment required - owner account suspended"
				}
				respondX402Billing(c, c.Request.Context(), target.TenantID, resourcePath, message)
				return
			}
		}
	}

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

			durationMs := float32(time.Since(start).Milliseconds())
			go postBalancingEvent(c, target.InternalName, bestNode, 0, lat, lon, "redirect", redirectURL, 0, 0, "", durationMs)
			return
		}

		durationMs := float32(time.Since(start).Milliseconds())
		c.String(http.StatusOK, bestNode)
		go postBalancingEvent(c, target.InternalName, bestNode, 0, lat, lon, "success", "", 0, 0, "", durationMs)
		return
	}

	// 2. Dynamic Balancing (Live)
	// Use resolved internal name for finding nodes, but preserve original name for redirect
	if internalName == "" {
		internalName = target.InternalName
	}

	// Optional capability filter
	requireCap := query.Get("cap")

	var bestNode string
	var score uint64
	var nodeLat, nodeLon float64
	var nodeName string
	var err error
	ctx := c.Request.Context()
	if requireCap != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyCapability, requireCap)
	}
	// Viewer selection -> isSourceSelection=false (allow replicated)
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, internalName, lat, lon, tagAdjust, "", false)
	if err != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		logger.WithError(err).Error("Load balancer failed to select a node")
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("load_balancer", "failed").Inc()
		}
		c.String(http.StatusOK, "localhost") // fallback like C++

		// Post failure event to Firehose
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "failed", err.Error(), 0, 0, "", durationMs)
		return
	}

	// Create virtual viewer to track this redirect
	// This adds a bandwidth penalty immediately, before USER_NEW confirms the connection
	viewerID := ""
	if nodeID := lb.GetNodeIDByHost(bestNode); nodeID != "" {
		clientIP := c.ClientIP()
		viewerID = state.DefaultManager().CreateVirtualViewer(nodeID, internalName, clientIP)
	}

	// Check if redirect is requested (like C++)
	proto := query.Get("proto")
	vars := c.Request.URL.RawQuery
	if proto != "" && bestNode != "" {
		redirectURL := fmt.Sprintf("%s://%s/%s", proto, bestNode, streamName)
		if vars != "" {
			redirectURL += "?" + vars
		}
		if viewerID != "" {
			redirectURL = appendCorrelationID(redirectURL, viewerID)
		}
		c.Header("Location", redirectURL)
		c.String(http.StatusTemporaryRedirect, redirectURL)

		durationMs := float32(time.Since(start).Milliseconds())
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("load_balancer", bestNode).Inc()
			metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
		}

		// Post redirect event to Firehose
		go postBalancingEvent(c, internalName, bestNode, score, lat, lon, "redirect", redirectURL, nodeLat, nodeLon, nodeName, durationMs)
		return
	}

	durationMs := float32(time.Since(start).Milliseconds())
	c.String(http.StatusOK, bestNode)

	if metrics != nil {
		metrics.RoutingDecisions.WithLabelValues("load_balancer", bestNode).Inc()
		metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
	}

	// Post successful balancing event to Firehose
	go postBalancingEvent(c, internalName, bestNode, score, lat, lon, "success", "", nodeLat, nodeLon, nodeName, durationMs)
}

// postBalancingEvent posts load balancing decisions to Decklog via gRPC
// durationMs is the time taken to resolve the routing decision (request processing latency)
func postBalancingEvent(c *gin.Context, streamName, selectedNode string, score uint64, lat, lon float64, status, details string, nodeLat, nodeLon float64, nodeName string, durationMs float32) {

	// Extract client IP in priority order:
	// 1. CF-Connecting-IP (Cloudflare's real client IP, most accurate when behind CF)
	// 2. X-Forwarded-For (standard proxy header)
	// 3. X-Real-IP (nginx convention)
	// 4. Direct connection IP
	clientIP := c.GetHeader("CF-Connecting-IP")
	if clientIP == "" {
		clientIP = c.GetHeader("X-Forwarded-For")
	}
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
		if geoData := geoip.LookupCached(c.Request.Context(), geoipReader, geoipCache, clientIP); geoData != nil {
			country = geoData.CountryCode
			logger.WithFields(logging.Fields{
				"client_ip":    clientIP,
				"country_code": geoData.CountryCode,
				"city":         geoData.City,
				"latitude":     geoData.Latitude,
				"longitude":    geoData.Longitude,
			}).Info("Used GeoIP fallback for load balancing event")
		}
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
	if geo.IsValidLatLon(lat, lon) && geo.IsValidLatLon(nodeLat, nodeLon) {
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
	// MistServer sends stream names with live+/vod+ prefix; strip it for DB lookup
	var tenantID, internalName, streamID string
	if commodoreClient != nil && streamName != "" {
		bareInternal := mist.ExtractInternalName(streamName)
		var resolveResp *pb.ResolveInternalNameResponse
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			resolveResp, err = commodoreClient.ResolveInternalName(ctx, bareInternal)
			cancel()
			if err == nil {
				break
			}
			if attempt == 0 {
				time.Sleep(50 * time.Millisecond)
			}
		}
		if err == nil && resolveResp != nil {
			tenantID = resolveResp.TenantId
			internalName = resolveResp.InternalName
			streamID = resolveResp.StreamId
		} else {
			logger.WithError(err).WithField("stream_name", streamName).Warn("Failed to resolve tenant via Commodore after retry")
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
		ClientIp:      clientIP,
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
		InternalName: func() *string {
			if internalName != "" {
				return &internalName
			}
			return nil
		}(),
		// Dual-tenant attribution (RFC: routing-events-dual-tenant-attribution)
		// TenantId = infra owner (cluster operator) for event storage
		// StreamTenantId = stream owner (customer) for filtering
		// ClusterId = emitting cluster identifier
		TenantId: func() *string {
			if ownerTenantID != "" {
				return &ownerTenantID
			}
			return nil
		}(),
		StreamTenantId: func() *string {
			if tenantID != "" {
				return &tenantID
			}
			return nil
		}(),
		StreamId: func() *string {
			if streamID != "" {
				return &streamID
			}
			return nil
		}(),
		ClusterId: func() *string {
			if clusterID != "" {
				return &clusterID
			}
			return nil
		}(),
		LatencyMs: func() *float32 {
			if durationMs > 0 {
				return &durationMs
			}
			return nil
		}(),
	}

	if decklogClient == nil {
		logger.Error("Decklog gRPC client not initialized")
		return
	}

	if streamID == "" {
		logger.WithField("stream_name", streamName).Warn("LoadBalancingData missing stream_id")
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
	}).Info("Successfully sent balancing event to Decklog")
}

// emitViewerRoutingEvent posts a routing decision for viewer playback (used by gRPC + generic HTTP helpers)
// Keeps it minimal to avoid duplicating the legacy postBalancingEvent gin-specific code.
// durationMs is the time taken to resolve the routing decision (request processing latency)
func emitViewerRoutingEvent(req *pb.ViewerEndpointRequest, primary *pb.ViewerEndpoint, viewerLat, viewerLon, nodeLat, nodeLon float64, internalName, streamTenantID, streamID string, durationMs float32, candidatesCount int32, eventType, source string) {
	if decklogClient == nil || primary == nil {
		return
	}

	selectedNode := primary.BaseUrl
	if selectedNode == "" {
		selectedNode = primary.Url
	}

	selectedNodeID := primary.NodeId

	routingDistanceKm := 0.0
	if geo.IsValidLatLon(viewerLat, viewerLon) && geo.IsValidLatLon(nodeLat, nodeLon) {
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
		// Dual-tenant attribution (RFC: routing-events-dual-tenant-attribution)
		// TenantId = infra owner (cluster operator) for event storage
		// StreamTenantId = stream owner (customer) for filtering
		// ClusterId = emitting cluster identifier
		TenantId: func() *string {
			if ownerTenantID != "" {
				return &ownerTenantID
			}
			return nil
		}(),
		StreamTenantId: func() *string {
			if streamTenantID != "" {
				return &streamTenantID
			}
			return nil
		}(),
		StreamId: func() *string {
			if streamID != "" {
				return &streamID
			}
			return nil
		}(),
		CandidatesCount: func() *uint32 {
			if candidatesCount > 0 {
				v := uint32(candidatesCount)
				return &v
			}
			return nil
		}(),
		EventType: func() *string {
			if eventType != "" {
				return &eventType
			}
			return nil
		}(),
		Source: func() *string {
			if source != "" {
				return &source
			}
			return nil
		}(),
		ClusterId: func() *string {
			if clusterID != "" {
				return &clusterID
			}
			return nil
		}(),
		LatencyMs: func() *float32 {
			if durationMs > 0 {
				return &durationMs
			}
			return nil
		}(),
	}

	if streamID == "" {
		logger.WithField("content_id", req.GetContentId()).Warn("LoadBalancingData missing stream_id")
	}

	go func() {
		if err := decklogClient.SendLoadBalancing(event); err != nil {
			logger.WithError(err).WithField("content_id", req.GetContentId()).Warn("Failed to send viewer routing event to Decklog")
		}
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
	return math.NaN()
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
func resolveLiveViewerEndpoint(req *pb.ViewerEndpointRequest, lat, lon float64, internalName, streamTenantID, streamID string) (*pb.ViewerEndpointResponse, error) {
	start := time.Now()
	// Delegate to consolidated control package function
	deps := &control.PlaybackDependencies{
		DB:     db,
		LB:     lb,
		GeoLat: lat,
		GeoLon: lon,
	}

	if internalName == "" {
		return nil, fmt.Errorf("stream not found")
	}

	ctx := context.Background()
	response, err := control.ResolveLivePlayback(ctx, deps, req.ContentId, internalName, streamID, streamTenantID)
	if err != nil {
		return nil, err
	}

	// Emit routing event for analytics
	if response.Primary != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		candidatesCount := int32(0)
		if response.Primary != nil {
			candidatesCount = int32(1 + len(response.Fallbacks))
		}
		emitViewerRoutingEvent(req, response.Primary, lat, lon, 0, 0, internalName, streamTenantID, streamID, durationMs, candidatesCount, "play_rewrite", "http")
	}

	return response, nil
}

// resolveArtifactViewerEndpoint queries database for VOD/Clip/DVR storage nodes via a single resolver.
// It derives type from the public ID and does not depend on any caller-provided content type.
func resolveArtifactViewerEndpoint(req *pb.ViewerEndpointRequest, lat, lon float64) (*pb.ViewerEndpointResponse, error) {
	start := time.Now()
	deps := &control.PlaybackDependencies{
		DB:     db,
		LB:     lb,
		GeoLat: lat,
		GeoLon: lon,
	}

	ctx := context.Background()
	response, err := control.ResolveArtifactPlayback(ctx, deps, req.ContentId)
	if err != nil {
		return nil, err
	}

	// Emit routing event for analytics
	if response.Primary != nil && response.Metadata != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		candidatesCount := int32(0)
		if response.Primary != nil {
			candidatesCount = int32(1 + len(response.Fallbacks))
		}
		internalName := ""
		if target, _ := control.ResolveStream(ctx, req.ContentId); target != nil {
			internalName = target.InternalName
		}
		emitViewerRoutingEvent(req, response.Primary, 0, 0, 0, 0, internalName, response.Metadata.GetTenantId(), response.Metadata.GetStreamId(), durationMs, candidatesCount, "play_rewrite", "http")
	}

	return response, nil
}

func respondPlaybackError(c *gin.Context, status int, code, message string, extra gin.H) {
	payload := gin.H{
		"error":   strings.ToLower(code),
		"message": message,
		"code":    code,
	}
	for key, value := range extra {
		payload[key] = value
	}
	c.JSON(status, payload)
}

func respondX402Decision(c *gin.Context, decision *x402Decision) {
	// x402 decision body already has {error, message, code} + extras
	c.JSON(decision.Status, decision.Body)
}

func respondX402Billing(c *gin.Context, ctx context.Context, tenantID, resourcePath, message string) {
	resp := buildInsufficientBalanceResponse(ctx, tenantID, resourcePath, message)
	c.JSON(http.StatusPaymentRequired, resp)
}

// HandleGenericViewerPlayback handles /play/* and /resolve/* endpoints for generic players
// Supports patterns:
//   - /play/:viewkey or /resolve/:viewkey -> Returns full JSON with all protocols
//   - /play/:viewkey/:protocol or /play/:viewkey.:protocol -> 307 redirect to edge node
//   - Auto-detects protocol from extension (.m3u8 -> HLS, .webrtc -> WebRTC, etc.)
//   - Supports view keys (live), clip hashes, and DVR hashes via unified resolution
func HandleGenericViewerPlayback(c *gin.Context) {
	// Extract the full path after /play or /resolve
	fullPath := c.Param("path")
	if fullPath == "" {
		respondPlaybackError(c, http.StatusBadRequest, "MISSING_VIEW_KEY", "Missing view key in path", nil)
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
		respondPlaybackError(c, http.StatusBadRequest, "INVALID_PATH", "Invalid path format", nil)
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
		respondPlaybackError(c, http.StatusBadRequest, "INVALID_VIEW_KEY", "Invalid view key", nil)
		return
	}

	// UNIFIED RESOLUTION: derive the content type from the public ID.
	// Never trust or require a caller-provided content type.
	ctx := context.Background()
	resolution, err := control.ResolveContent(ctx, viewKey)
	if err != nil {
		logger.WithError(err).WithField("view_key", viewKey).Warn("Failed to resolve content")
		respondPlaybackError(c, http.StatusNotFound, "VIEW_KEY_NOT_FOUND", "Invalid or expired view key", nil)
		return
	}

	contentType := resolution.ContentType
	contentID := resolution.ContentId
	if contentID == "" {
		contentID = viewKey // Fallback to original input
	}
	internalName := mist.ExtractInternalName(resolution.InternalName)

	logger.WithFields(logging.Fields{
		"view_key":     viewKey,
		"content_type": contentType,
		"content_id":   contentID,
		"fixed_node":   resolution.FixedNode,
	}).Info("Resolved content via unified resolution")

	// Prepaid billing check (402)
	if resolution.TenantId != "" {
		billing := getBillingStatus(c.Request.Context(), internalName, resolution.TenantId)
		if billing != nil && billing.BillingModel == "prepaid" && (billing.IsSuspended || billing.IsBalanceNegative) {
			paymentHeader := x402.GetPaymentHeaderFromRequest(c.Request)
			resourcePath := c.Request.URL.Path
			paid, decision := settleX402PaymentForPlayback(c.Request.Context(), resolution.TenantId, resourcePath, paymentHeader, c.ClientIP(), logger)
			if decision != nil {
				respondX402Decision(c, decision)
				return
			}
			if !paid {
				message := "payment required - stream owner needs to top up balance"
				if billing.IsSuspended {
					message = "payment required - owner account suspended"
				}
				respondX402Billing(c, c.Request.Context(), resolution.TenantId, resourcePath, message)
				return
			}
		}
	}

	// Normalize protocol
	protocol = normalizeProtocol(protocol)

	// Build viewer endpoint request (content type is derived, not supplied)
	viewerIP := c.ClientIP()
	req := &pb.ViewerEndpointRequest{
		ContentId: contentID,
		ViewerIp:  proto.String(viewerIP),
	}

	// Get geo location for viewer
	var lat, lon float64
	if geoipReader != nil {
		if geoData := geoip.LookupCached(c.Request.Context(), geoipReader, geoipCache, viewerIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
		}
	}

	// Resolve endpoint
	var response *pb.ViewerEndpointResponse
	if contentType == "live" {
		response, err = resolveLiveViewerEndpoint(req, lat, lon, internalName, resolution.TenantId, resolution.StreamId)
	} else {
		response, err = resolveArtifactViewerEndpoint(req, lat, lon)
	}

	if err != nil {
		var defrostErr *control.DefrostingError
		if errors.As(err, &defrostErr) {
			retryAfter := defrostErr.RetryAfterSeconds
			if retryAfter <= 0 {
				retryAfter = 10
			}
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			respondPlaybackError(c, http.StatusAccepted, "DEFROSTING", "content defrosting in progress", gin.H{
				"status":      "defrosting",
				"retryAfter":  retryAfter,
				"contentType": contentType,
				"contentId":   contentID,
			})
			return
		}

		logger.WithError(err).WithFields(logging.Fields{
			"view_key":      viewKey,
			"internal_name": contentID,
			"content_type":  contentType,
		}).Error("Failed to resolve viewer endpoint")
		respondPlaybackError(c, http.StatusInternalServerError, "PLAYBACK_RESOLUTION_FAILED", "Failed to resolve playback endpoint", nil)
		return
	}

	// Create virtual viewer for live streams to track this redirect
	// This adds a bandwidth penalty immediately, before USER_NEW confirms the connection
	viewerID := ""
	if contentType == "live" && response.Primary != nil && response.Primary.NodeId != "" {
		if internalName == "" {
			internalName = contentID
		}
		viewerID = state.DefaultManager().CreateVirtualViewer(response.Primary.NodeId, internalName, viewerIP)
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
			respondPlaybackError(c, http.StatusInternalServerError, "SERIALIZATION_FAILED", "Failed to serialize response", nil)
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
		respondPlaybackError(c, http.StatusNotFound, "PROTOCOL_NOT_AVAILABLE", fmt.Sprintf("Protocol '%s' not available for this stream", protocol), nil)
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
	if viewerID != "" {
		redirectURL = appendCorrelationID(redirectURL, viewerID)
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

func appendCorrelationID(redirectURL, viewerID string) string {
	if viewerID == "" || redirectURL == "" {
		return redirectURL
	}

	parsedURL, err := url.Parse(redirectURL)
	if err != nil {
		return redirectURL
	}

	query := parsedURL.Query()
	// Always override any existing fwcid to avoid stale/malicious correlation IDs.
	query.Set("fwcid", viewerID)
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String()
}

// normalizeProtocol converts protocol hints to standard names
func normalizeProtocol(proto string) string {
	proto = strings.ToLower(proto)
	switch proto {
	// Adaptive streaming
	case "m3u8", "hls":
		return "hls"
	case "mpd", "dash":
		return "dash"
	case "cmaf", "llhls", "ll-hls":
		return "cmaf"
	// Low latency
	case "webrtc", "whep":
		return "webrtc"
	case "srt":
		return "srt"
	// Legacy streaming
	case "rtmp":
		return "rtmp"
	case "rtsp":
		return "rtsp"
	// Container formats
	case "mp4", "progressive":
		return "mp4"
	case "webm":
		return "webm"
	case "mkv", "matroska":
		return "mkv"
	case "ts", "mpegts", "mpeg-ts":
		return "ts"
	case "flv", "flash":
		return "flv"
	case "aac", "audio":
		return "aac"
	// Microsoft/Adobe
	case "smooth", "smoothstreaming", "hss":
		return "smoothstreaming"
	case "hds", "f4m", "dynamic":
		// MistServer uses /dynamic/ path for HDS
		return "hds"
	// Other
	case "sdp":
		return "sdp"
	case "h264", "rawh264", "raw":
		return "h264"
	case "dtsc", "mist":
		return "dtsc"
	case "wsmp4":
		return "wsmp4"
	case "wswebrtc":
		return "wswebrtc"
	// Wildcards
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

	// Try fuzzy matching for protocols that MistServer may report with different names
	switch protocol {
	case "hls":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "hls") {
				return output.Url
			}
		}
	case "hlscmaf", "cmaf":
		// CMAF output serves LL-HLS, DASH, and Smooth Streaming based on manifest path
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "cmaf") || strings.Contains(outputLower, "ll-hls") || strings.Contains(outputLower, "llhls") || strings.Contains(outputLower, "low latency") {
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
	case "ts", "mpegts", "mpeg-ts":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "ts") || strings.Contains(outputLower, "mpeg") {
				return output.Url
			}
		}
	case "mp4":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "mp4") || strings.Contains(outputLower, "progressive") {
				return output.Url
			}
		}
	case "webm":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "webm") {
				return output.Url
			}
		}
	case "mkv", "matroska":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "mkv") || strings.Contains(outputLower, "matroska") {
				return output.Url
			}
		}
	case "flv", "flash":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "flv") || strings.Contains(outputLower, "flash") {
				return output.Url
			}
		}
	case "aac":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "aac") {
				return output.Url
			}
		}
	case "rtsp":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "rtsp") {
				return output.Url
			}
		}
	case "rtmp":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "rtmp") {
				return output.Url
			}
		}
	case "srt":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "srt") {
				return output.Url
			}
		}
	case "smoothstreaming", "smooth", "hss":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "smooth") || strings.Contains(outputLower, "hss") {
				return output.Url
			}
		}
	case "hds":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "hds") || strings.Contains(outputLower, "adobe") {
				return output.Url
			}
		}
	case "sdp":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "sdp") {
				return output.Url
			}
		}
	case "h264", "raw", "rawh264":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "h264") || strings.Contains(outputLower, "raw") {
				return output.Url
			}
		}
	case "dtsc":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "dtsc") || strings.Contains(outputLower, "mist") {
				return output.Url
			}
		}
	case "wsmp4":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if (strings.Contains(outputLower, "ws") || strings.Contains(outputLower, "websocket")) && strings.Contains(outputLower, "mp4") {
				return output.Url
			}
		}
	case "wswebrtc":
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if (strings.Contains(outputLower, "ws") || strings.Contains(outputLower, "websocket")) && strings.Contains(outputLower, "webrtc") {
				return output.Url
			}
		}
	}

	return ""
}

// SetQuartermasterClient injects a Quartermaster client after initialization.
func SetQuartermasterClient(client *qmclient.GRPCClient) {
	quartermasterClient = client
	control.SetQuartermasterClient(client)
	if client == nil || logger == nil {
		return
	}
	go func() {
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := bootstrapClusterInfo(bootstrapCtx); err != nil {
			logger.WithError(err).Warn("Async bootstrap failed after Quartermaster reconnect")
		}
	}()
}

// SetCommodoreClient injects a Commodore client after initialization.
func SetCommodoreClient(client *commodore.GRPCClient) {
	commodoreClient = client
	control.SetCommodoreClient(client)
}
