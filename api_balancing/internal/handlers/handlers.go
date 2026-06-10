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
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/triggers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/x402"

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
	// Self-geo: cached from infrastructure_nodes.external_ip via GeoIP on bootstrap
	selfLat      float64
	selfLon      float64
	selfLocation string
)

// routingEventsDisabled short-circuits the fire-and-forget postBalancingEvent
// telemetry. It is only ever set true by the test binary (set once, never
// restored, so the async readers never race a writer); production leaves it
// false. The routing decision itself is unaffected — only the Decklog emit is.
var routingEventsDisabled bool

func SetSelfGeo(lat, lon float64, location string) {
	selfLat = lat
	selfLon = lon
	selfLocation = location
}

func GetSelfGeo() (float64, float64, string) {
	return selfLat, selfLon, selfLocation
}

// GetClusterInfo returns cached cluster attribution info for dual-tenant routing events
// Returns (clusterID, ownerTenantID) - used by gRPC server for event emission
func GetClusterInfo() (string, string) {
	return clusterID, ownerTenantID
}

// ApplyBootstrapMetadata extracts cluster attribution and self-geo from a
// bootstrap response. Called once from main.go after the single gRPC bootstrap.
func ApplyBootstrapMetadata(resp *quartermasterpb.BootstrapServiceResponse) {
	if resp == nil {
		return
	}

	if resp.OwnerTenantId != nil && *resp.OwnerTenantId != "" {
		ownerTenantID = *resp.OwnerTenantId
		if logger != nil {
			logger.WithFields(logging.Fields{
				"cluster_id":      resp.ClusterId,
				"owner_tenant_id": ownerTenantID,
			}).Info("Cached cluster owner tenant for dual-tenant attribution")
		}
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

	if node := resp.GetNode(); node != nil && node.ExternalIp != nil && geoipReader != nil {
		if result := geoipReader.Lookup(*node.ExternalIp); result != nil {
			loc := result.City
			if result.CountryName != "" {
				if loc != "" {
					loc += ", "
				}
				loc += result.CountryName
			}
			SetSelfGeo(result.Latitude, result.Longitude, loc)
			if logger != nil {
				logger.WithFields(logging.Fields{
					"lat":      result.Latitude,
					"lon":      result.Longitude,
					"location": loc,
				}).Info("Foghorn self-geo resolved from infrastructure node")
			}
		}
	}
}

// FoghornMetrics holds all Prometheus metrics for Foghorn.
// DB connection-pool stats are registered separately via
// monitoring.MetricsCollector.RegisterDBStats and read from db.Stats()
// at scrape time, so they do not appear on this struct.
type FoghornMetrics struct {
	RoutingDecisions      *prometheus.CounterVec
	NodeSelectionDuration *prometheus.HistogramVec

	// LivepeerAuthRejected counts Livepeer gateway auth-webhook rejections by reason.
	// Reasons: stream_not_found, stream_not_live, peer_context_missing,
	// peer_unreachable, commodore_unreachable, invalid_request.
	LivepeerAuthRejected *prometheus.CounterVec

	// StorageMint counts MintStorageURLs federation-handler outcomes.
	// Labels: result. Values: accepted, tenant_mismatch,
	// storage_not_owned_here, unsupported_artifact_type,
	// unsupported_operation, s3_error.
	StorageMint *prometheus.CounterVec
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

	// Cluster metadata (owner_tenant_id, self-geo) is now extracted from
	// the single gRPC bootstrap in main.go via ApplyBootstrapMetadata.

	// Register the artifact-deleted handler (node-local eviction/deletion
	// reconciliation + DELETED lifecycle emission). Clip progress/done
	// analytics are emitted by the processing pipeline, not here.
	control.SetArtifactDeletedHandler(
		func(ctx context.Context, del *ipcpb.ArtifactDeleted) {
			artifactHash := del.GetArtifactHash()
			nodeID := del.GetNodeId()
			reason := del.GetReason()

			// This message indicates node-local deletion/eviction. Remove the node cache record.
			_, err := db.ExecContext(ctx, `DELETE FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2`, artifactHash, nodeID)
			if err != nil {
				logger.WithError(err).WithField("artifact_hash", artifactHash).Error("Failed to remove artifact node assignment")
			}

			// If the artifact has no remaining cached nodes and is synced, reflect that it is now S3-only.
			var hasAnyNodes bool
			_ = db.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM foghorn.artifact_nodes
					WHERE artifact_hash = $1 AND NOT is_orphaned
				)
				`, artifactHash).Scan(&hasAnyNodes)
			if !hasAnyNodes {
				_, _ = db.ExecContext(ctx, `
					UPDATE foghorn.artifacts
					SET storage_location = CASE
						WHEN sync_status = 'synced' THEN 's3'
						ELSE storage_location
					END,
					updated_at = NOW()
					WHERE artifact_hash = $1
					`, artifactHash)
			}

			// Evictions must never be treated as global deletes.
			if reason == "eviction" {
				logger.WithFields(logging.Fields{
					"artifact_hash": artifactHash,
					"node_id":       nodeID,
				}).Info("Clip evicted from node cache")
				return
			}

			// Only emit DELETED when the artifact is already soft-deleted in foghorn.artifacts.
			// This avoids conflating node-local cleanup with user-initiated deletion.
			var artifactStatus string
			if err := db.QueryRowContext(ctx, `SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1`, artifactHash).Scan(&artifactStatus); err != nil {
				logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to read artifact status for deletion lifecycle")
				return
			}
			if artifactStatus != "deleted" {
				logger.WithFields(logging.Fields{
					"artifact_hash": artifactHash,
					"node_id":       nodeID,
					"reason":        reason,
				}).Info("Clip removed from node but not globally deleted")
				return
			}

		},
	)

	control.SetDVRStoppedHandler(func(dvrHash string, finalStatus string, nodeID string, sizeBytes uint64, manifestPath string, errorMsg string) {
		if decklogClient == nil {
			return
		}

		status := ipcpb.DVRLifecycleData_STATUS_STOPPED
		if finalStatus == "failed" {
			status = ipcpb.DVRLifecycleData_STATUS_FAILED
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
		var rowTenantID, rowUserID, rowStreamID, rowInternalName sql.NullString
		if err := db.QueryRowContext(cctx, `
			SELECT tenant_id::text,
			       user_id::text,
			       stream_id::text,
			       stream_internal_name,
			       retention_until,
			       started_at,
			       ended_at
			FROM foghorn.artifacts
			WHERE artifact_hash = $1
			  AND artifact_type = 'dvr'
		`, dvrHash).Scan(&rowTenantID, &rowUserID, &rowStreamID, &rowInternalName, &retentionUntil, &startedAt, &endedAt); err != nil {
			logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to read DVR artifact context for lifecycle event")
		} else {
			tenantIDStr = rowTenantID.String
			userIDStr = rowUserID.String
			streamID = rowStreamID.String
			internalNameStr = rowInternalName.String
		}
		if commodoreClient != nil {
			if resp, err := commodoreClient.ResolveDVRHash(cctx, dvrHash); err == nil && resp.Found {
				if resp.TenantId != "" {
					tenantIDStr = resp.TenantId
				}
				if resp.UserId != "" {
					userIDStr = resp.UserId
				}
				if resp.StreamInternalName != "" {
					internalNameStr = resp.StreamInternalName
				}
				if resp.StreamId != "" {
					streamID = resp.StreamId
				}
			}
		}

		dvrData := &ipcpb.DVRLifecycleData{
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
			dvrData.StreamInternalName = &internalNameStr
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
			if err := artifactoutbox.EnqueueDVRLifecycle(dvrData); err != nil {
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
				internalNameStr = resp.StreamInternalName
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

		dvrData := &ipcpb.DVRLifecycleData{
			Status:  ipcpb.DVRLifecycleData_STATUS_DELETED,
			DvrHash: dvrHash,
		}
		if nodeID != "" {
			dvrData.NodeId = &nodeID
		}
		if tenantIDStr != "" {
			dvrData.TenantId = &tenantIDStr
		}
		if internalNameStr != "" {
			dvrData.StreamInternalName = &internalNameStr
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
			if err := artifactoutbox.EnqueueDVRLifecycle(dvrData); err != nil {
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
			return resp.TenantId, resp.StreamInternalName, nil
		}

		// Fallback to DVR
		if resp, err := commodoreClient.ResolveDVRHash(ctx, clipHash); err == nil && resp.Found {
			return resp.TenantId, resp.StreamInternalName, nil
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

	if strings.HasPrefix(path, sourceByNodePathPrefix) {
		if source := query.Get("source"); source != "" {
			handleGetSource(c, source, query)
			return
		}
		c.String(http.StatusBadRequest, "Missing source")
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
					"viewers":    s.Viewers,
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
				"processing_classes":     n.ProcessingClasses,
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

	// Full state is opt-in; the compact response is the public default.
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
		artifactRows, err := db.QueryContext(c.Request.Context(), `
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
		jobRows, err := db.QueryContext(c.Request.Context(), `
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

// StateToProtoMode converts internal state mode to protobuf enum
func StateToProtoMode(mode state.NodeOperationalMode) ipcpb.NodeOperationalMode {
	switch mode {
	case state.NodeModeDraining:
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING
	case state.NodeModeMaintenance:
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE
	default:
		return ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL
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
	protoMode := StateToProtoMode(mode)
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

// arrangeRemoteOriginPullFromSource is the /source HTTP entry's
// cross-cluster path. Unlike arrangeOriginPull (which the gRPC viewer
// router calls and which picks the puller via LB), the caller IS the
// puller — a Mist edge that fired STREAM_SOURCE on cold load. The
// caller node ID comes from the Foghorn-issued balancer path where
// available, with client IP matching only when the path identity is
// absent. Fails closed (returns "") when the caller can't be identified
// or the arrangement fails so the puller and registry never diverge.
func arrangeRemoteOriginPullFromSource(ctx context.Context, streamName string, lat, lon float64, callerNodeID, clientIP string) (dtscURL, remoteCluster string) {
	candidate, cluster, tenantID := resolveRemoteSourceCandidate(ctx, streamName, lat, lon)
	if candidate == nil {
		return "", ""
	}
	internalName := mist.ExtractInternalName(streamName)
	if internalName == "" {
		internalName = streamName
	}

	if callerNodeID == "" {
		logger.WithFields(logging.Fields{
			"stream":         streamName,
			"client_ip":      clientIP,
			"remote_cluster": cluster,
		}).Warn("/source cross-cluster: caller IP did not match a known node; refusing to arrange untracked pull")
		return "", ""
	}

	deps := &federation.ArrangeOriginPullDeps{
		Cache:        remoteEdgeCache,
		PeerResolver: peerManager,
		FedClient:    federationClient,
		InstanceID:   originPullInstanceID,
		ClusterID:    clusterID,
		Logger:       logger,
		EventEmitter: emitFederationEvent,
	}
	result, err := deps.ArrangeOriginPull(ctx, federation.ArrangeOriginPullRequest{
		InternalName:    internalName,
		Remote:          candidate,
		RemoteCluster:   cluster,
		TenantID:        tenantID,
		DestNodeID:      callerNodeID,
		DestNodeBaseURL: "", // unknown from clientIP alone; MarkReplicating accepts empty
		Lat:             lat,
		Lon:             lon,
	})
	if err != nil || result == nil {
		logger.WithError(err).WithFields(logging.Fields{
			"stream":         streamName,
			"remote_cluster": cluster,
			"caller_node":    callerNodeID,
		}).Warn("/source cross-cluster: ArrangeOriginPull failed; refusing untracked pull")
		return "", ""
	}
	return result.PullDTSCURL, cluster
}

// resolveRemoteSource attempts cross-cluster source lookup when no
// local node has the stream. Returns the DTSC URL from the origin
// cluster and the cluster ID, or empty strings when unavailable. Thin
// wrapper around resolveRemoteSourceCandidate for callers that only
// need the URL.
func resolveRemoteSource(ctx context.Context, streamName string, lat, lon float64) (dtscURL, remoteCluster string) {
	candidate, cluster, _ := resolveRemoteSourceCandidate(ctx, streamName, lat, lon)
	if candidate == nil {
		return "", ""
	}
	return candidate.DtscUrl, cluster
}

// resolveRemoteSourceCandidate is the full version that returns the
// chosen EdgeCandidate. Needed by callers that want to arrange a
// tracked origin-pull (NotifyOriginPull needs candidate.NodeId).
// tenantID returned so the arrangement carries it through.
func resolveRemoteSourceCandidate(ctx context.Context, streamName string, lat, lon float64) (*foghornfederationpb.EdgeCandidate, string, string) {
	if federationClient == nil || peerManager == nil {
		return nil, "", ""
	}

	internalName := mist.ExtractInternalName(streamName)
	if internalName == "" {
		internalName = streamName
	}

	// Try cached stream context first (populated from PUSH_REWRITE validation)
	var tenantID, originClusterID string
	var clusterPeers []*clusterpeerpb.TenantClusterPeer
	if triggerProcessor != nil {
		tenantID, originClusterID = triggerProcessor.GetStreamOrigin(internalName)
	}

	// Fallback: ask Commodore for the stream's origin cluster (and the fresh
	// cluster-peer envelope used to authorize the federation below).
	if originClusterID == "" && commodoreClient != nil {
		if resp, err := commodoreClient.ResolveInternalName(ctx, internalName); err == nil {
			tenantID = resp.TenantId
			originClusterID = resp.OriginClusterId
			clusterPeers = resp.GetClusterPeers()
		}
	}

	if originClusterID == "" || originClusterID == clusterID {
		return nil, "", ""
	}

	// Front-door reauthorization: a cross-cluster source must be in the
	// tenant's current cluster_peers before we federate (mirrors the dvr+
	// arrange gate and /play). The cached fast path carries origin but no
	// peers, so resolve them fresh; fail closed so a revoked peer can't keep
	// being federated off stale cached origin state.
	if clusterPeers == nil && commodoreClient != nil {
		if resp, err := commodoreClient.ResolveInternalName(ctx, internalName); err == nil {
			clusterPeers = resp.GetClusterPeers()
		}
	}
	if !control.AuthoritativeClusterServable(originClusterID, clusterPeers) {
		logger.WithFields(logging.Fields{
			"stream":         streamName,
			"origin_cluster": originClusterID,
		}).Warn("/source cross-cluster: origin cluster not in tenant peer envelope; refusing to federate")
		return nil, "", ""
	}

	addr := peerManager.GetPeerAddr(originClusterID)
	if addr == "" {
		return nil, "", ""
	}

	resp, err := federationClient.QueryStream(ctx, originClusterID, addr, &foghornfederationpb.QueryStreamRequest{
		StreamName:        streamName,
		ViewerLat:         lat,
		ViewerLon:         lon,
		RequestingCluster: clusterID,
		TenantId:          tenantID,
		IsSourceSelection: true,
	})
	if err != nil || resp == nil || len(resp.Candidates) == 0 {
		return nil, "", ""
	}

	// Prefer origin node (has active input), otherwise best scored
	best := resp.Candidates[0]
	for _, c := range resp.Candidates {
		if c.IsOrigin {
			best = c
			break
		}
	}

	return best, originClusterID, tenantID
}

// resolvePullSourceForSource looks up the full pull-source row for a
// pull+<internal_name> stream via Commodore. Returns nil on any failure or if
// the source is disabled. The caller uses both the upstream URI and the
// allowed_cluster_ids list for placement enforcement.
func resolvePullSourceForSource(ctx context.Context, streamName string) *commodorepb.ResolvePullSourceByInternalNameResponse {
	internalName := strings.TrimPrefix(streamName, "pull+")
	if internalName == "" {
		return nil
	}
	if control.CommodoreClient == nil {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := control.CommodoreClient.ResolvePullSourceByInternalName(lookupCtx, internalName)
	if err != nil || resp == nil || !resp.GetFound() || !resp.GetEnabled() {
		return nil
	}
	return resp
}

// summarizePullPlacementRejects flattens FilterPlacementClusters rejections
// into a single log line. Mirrors api_balancing/internal/control:
// summarizePlacementRejects so logs and errors describe the same reasons.
func summarizePullPlacementRejects(rejects []pullsource.PlacementReject) string {
	parts := make([]string, 0, len(rejects))
	for _, r := range rejects {
		switch r.Reason {
		case pullsource.PlacementRejectEmptyForPrivate:
			parts = append(parts, "private/multicast source has no allowed_cluster_ids configured")
		case pullsource.PlacementRejectUnknownCluster:
			parts = append(parts, fmt.Sprintf("cluster %q is not in allowed_cluster_ids", r.ClusterID))
		case pullsource.PlacementRejectMissingPrivateCapability:
			parts = append(parts, fmt.Sprintf("cluster %q does not allow private pull sources", r.ClusterID))
		default:
			parts = append(parts, fmt.Sprintf("cluster %q rejected: %s", r.ClusterID, r.Reason))
		}
	}
	return strings.Join(parts, "; ")
}

func pullUpstreamScore(upstream string) uint64 {
	if !pullsource.IsValid(upstream) {
		return 0
	}
	return 1
}

func handleGetPullSource(c *gin.Context, streamName string, lat, lon float64, tagAdjust map[string]int, callerNodeID, clientIP string, ctx context.Context, start time.Time) {
	src := resolvePullSourceForSource(ctx, streamName)
	if src == nil {
		logger.WithField("stream", streamName).Warn("Source lookup: pull stream upstream URI unavailable")
		c.String(http.StatusOK, control.OfflineNotConfigured)
		return
	}
	upstream := src.GetSourceUri()
	class, classErr := pullsource.Classify(upstream)
	if class == pullsource.ClassBlocked {
		logger.WithError(classErr).WithField("stream", streamName).Warn("Source lookup: pull stream upstream URI is in the always-blocked set")
		c.String(http.StatusOK, control.OfflineBlockedURI)
		return
	}
	// /source runs on a specific Foghorn — which serves a specific media
	// cluster. Re-run the same placement filter we apply at viewer routing
	// + STREAM_SOURCE so a stale route handed to this endpoint cannot leak
	// the upstream onto a cluster the pull source isn't pinned to.
	localClusterID := config.GetEnv("CLUSTER_ID", "")
	localCapability := false
	if class == pullsource.ClassPrivate {
		localCapability = control.ClusterAllowsPrivatePulls(ctx, localClusterID)
	}
	localCandidates := []pullsource.ClusterCapability{}
	if localClusterID != "" {
		localCandidates = append(localCandidates, pullsource.ClusterCapability{
			ID:                      localClusterID,
			AllowPrivatePullSources: localCapability,
		})
	}
	eligible, rejects := pullsource.FilterPlacementClusters(class, src.GetAllowedClusterIds(), localCandidates)
	if len(eligible) == 0 {
		// This cluster can't dial upstream itself (allowed_cluster_ids
		// excludes it). Fall through to cross-cluster federation: query
		// the origin cluster (which IS allowed and presumably pulling)
		// for its edge DTSC URL so this cluster serves viewers via DTSC
		// rather than 404. Arrange the pull so it's tracked end-to-end.
		remoteDTSC, remoteCluster := arrangeRemoteOriginPullFromSource(ctx, streamName, lat, lon, callerNodeID, clientIP)
		if remoteDTSC != "" {
			durationMs := float32(time.Since(start).Milliseconds())
			if metrics != nil {
				metrics.RoutingDecisions.WithLabelValues("source", "pull_federated").Inc()
				metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
			}
			logger.WithFields(logging.Fields{
				"stream":         streamName,
				"cluster_id":     localClusterID,
				"remote_cluster": remoteCluster,
				"dtsc_url":       remoteDTSC,
			}).Info("Source lookup: pull source federated from allowed cluster")
			go postBalancingEventEx(c, streamName, "remote", 0, lat, lon, "pull_federated", remoteDTSC, 0, 0, "", durationMs, remoteCluster)
			c.String(http.StatusOK, remoteDTSC)
			return
		}
		logger.WithFields(logging.Fields{
			"stream":     streamName,
			"cluster_id": localClusterID,
			"rejects":    summarizePullPlacementRejects(rejects),
		}).Warn("Source lookup: pull source not placeable on this cluster and no federated peer pulling")
		c.String(http.StatusOK, control.OfflineNotPlaced)
		return
	}

	upstreamScore := pullUpstreamScore(upstream)
	if upstreamScore == 0 {
		logger.WithField("stream", streamName).Warn("Source lookup: pull stream upstream URI failed validation")
		c.String(http.StatusOK, control.OfflineInvalidUpstream)
		return
	}

	durationMs := float32(time.Since(start).Milliseconds())
	if metrics != nil {
		metrics.RoutingDecisions.WithLabelValues("source", "pull_upstream").Inc()
		metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
	}
	logger.WithFields(logging.Fields{
		"stream":         streamName,
		"upstream_score": upstreamScore,
	}).Info("Source lookup: pull stream returning upstream origin")
	go postBalancingEvent(c, streamName, "", 0, lat, lon, "pull_upstream", pullsource.Redact(upstream), 0, 0, "", durationMs)
	c.String(http.StatusOK, upstream)
}

const sourceByNodePathPrefix = "/source/by-node/"

func sourceCallerNodeID(c *gin.Context, query url.Values, clientIP string) string {
	path := strings.TrimSpace(c.Request.URL.Path)
	if strings.HasPrefix(path, sourceByNodePathPrefix) {
		rest := strings.TrimPrefix(path, sourceByNodePathPrefix)
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			rest = rest[:idx]
		}
		if nodeID, err := url.PathUnescape(rest); err == nil && strings.TrimSpace(nodeID) != "" {
			return strings.TrimSpace(nodeID)
		}
	}
	return state.DefaultManager().NodeIDByClientIP(clientIP)
}

// handleGetSource implements /?source=<stream> (EXACT C++ implementation)
func handleGetSource(c *gin.Context, streamName string, query url.Values) {
	start := time.Now()
	lat := getLatLon(c, query, "lat", "X-Latitude")
	lon := getLatLon(c, query, "lon", "X-Longitude")
	tagAdjust := getTagAdjustments(c, query)

	// Get client IP for same-host detection (like C++)
	clientIP := c.ClientIP()
	callerNodeID := sourceCallerNodeID(c, query, clientIP)

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
	if streamTenantID := getStreamTenantID(streamName); streamTenantID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyClusterScope, streamTenantID)
	}
	// Active origin-pull check first — this covers every runtime name
	// that can be federated (live+, pull+, dvr+, bare mist-native).
	// When an origin-pull is arranged, the peer DTSC URL is the right
	// answer regardless of any prefix-specific resolver below.
	if remoteDTSC, handled := activeReplicationSource(ctx, streamName, callerNodeID); handled {
		if remoteDTSC == "" {
			// Replication exists but pinned to another local edge.
			// Refuse so this caller doesn't start a duplicate pull.
			if metrics != nil {
				metrics.RoutingDecisions.WithLabelValues("source", "replication_pinned_elsewhere").Inc()
			}
			logger.WithFields(logging.Fields{
				"stream":      streamName,
				"caller_node": callerNodeID,
				"client_ip":   clientIP,
			}).Info("Source lookup: replication pinned to a different local edge; refusing")
			c.String(http.StatusOK, "offline:replication-pinned-elsewhere")
			return
		}
		durationMs := float32(time.Since(start).Milliseconds())
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", "active_replication").Inc()
			metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
		}
		logger.WithFields(logging.Fields{
			"stream":   streamName,
			"dtsc_url": remoteDTSC,
		}).Info("Source lookup: using active origin-pull source")
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "active_replication", remoteDTSC, 0, 0, "", durationMs)
		c.String(http.StatusOK, remoteDTSC)
		return
	}
	if strings.HasPrefix(streamName, "pull+") {
		handleGetPullSource(c, streamName, lat, lon, tagAdjust, callerNodeID, clientIP, ctx, start)
		return
	}
	// Source selection (Mist pull) -> isSourceSelection=true (exclude replicated)
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, streamName, lat, lon, tagAdjust, clientIP, true)
	if err != nil {
		// Cross-cluster source lookup: arrange a tracked origin-pull
		// when the stream lives on a peer cluster. Caller (Mist edge
		// firing /source) IS the puller, so identify it from clientIP
		// and pass to ArrangeOriginPull as DestNodeID. Fails closed
		// when the caller can't be identified or the arrangement fails.
		remoteDTSC, remoteCluster := arrangeRemoteOriginPullFromSource(ctx, streamName, lat, lon, callerNodeID, clientIP)
		if remoteDTSC != "" {
			durationMs := float32(time.Since(start).Milliseconds())
			if metrics != nil {
				metrics.RoutingDecisions.WithLabelValues("source", "remote").Inc()
				metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
			}
			logger.WithFields(logging.Fields{
				"stream":   streamName,
				"dtsc_url": remoteDTSC,
			}).Info("Source lookup: resolved via cross-cluster federation")
			go postBalancingEventEx(c, streamName, "remote", 0, lat, lon, "remote_source", remoteDTSC, 0, 0, "", durationMs, remoteCluster)
			c.String(http.StatusOK, remoteDTSC)
			return
		}

		durationMs := float32(time.Since(start).Milliseconds())
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("source", "failed").Inc()
		}
		go postBalancingEvent(c, streamName, "", 0, lat, lon, "failed", err.Error(), 0, 0, "", durationMs)

		// Per-stream-type terminal answer when no node has the input.
		// live+: return push:// unconditionally — publishers boot the
		// ingest buffer via input_buffer; viewers fail the balancer's
		// non-provider pre-check and get a clean STRMSTAT_OFFLINE
		// disconnect. Live streams never explicitly return offline:
		// because a publisher may arrive any moment.
		// Other prefixes (bare/native fallback): empty body. The
		// balancer's `if (!source.size())` branch writes
		// STRMSTAT_OFFLINE and exits cleanly. Same effective behavior
		// as an explicit "offline:" body.
		var fallback string
		if strings.HasPrefix(streamName, "live+") {
			fallback = "push://"
		} else {
			fallback = query.Get("fallback")
		}
		logger.WithFields(logging.Fields{
			"stream":   streamName,
			"fallback": fallback,
		}).Info("Source lookup: no node has active input for stream; returning terminal answer")
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
				stream.Viewers,   // event-confirmed playback viewers
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
			totalViewers += stream.Viewers
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
	if target.TenantID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyClusterScope, target.TenantID)
	}
	// Viewer selection -> isSourceSelection=false (allow replicated)
	bestNode, score, nodeLat, nodeLon, nodeName, err = lb.GetBestNodeWithScore(ctx, internalName, lat, lon, tagAdjust, "", false)

	// Remote edge scoring: check if a remote cluster has a better edge
	if remoteEdgeCache != nil && triggerProcessor != nil {
		rawInternal := mist.ExtractInternalName(internalName)
		if rawInternal == "" {
			rawInternal = internalName
		}
		if peers := triggerProcessor.GetClusterPeers(rawInternal, target.TenantID); len(peers) > 0 {
			remoteEdges := collectRemoteEdges(ctx, peers)
			if len(remoteEdges) > 0 {
				remoteNodes := lb.ScoreRemoteEdges(remoteEdges, lat, lon)
				bestRemote := findBestRemoteNode(remoteNodes)
				if bestRemote != nil {
					localScore := score
					if err != nil {
						localScore = 0
					}
					if bestRemote.Score > localScore && confirmRemoteStream(ctx, bestRemote.ClusterID, internalName, target.TenantID, lat, lon) {
						durationMs := float32(time.Since(start).Milliseconds())
						if metrics != nil {
							metrics.RoutingDecisions.WithLabelValues("load_balancer", "remote").Inc()
							metrics.NodeSelectionDuration.WithLabelValues().Observe(time.Since(start).Seconds())
						}
						proto := query.Get("proto")
						if proto != "" {
							redirectURL := fmt.Sprintf("%s://%s/%s", proto, bestRemote.Host, streamName)
							if vars := c.Request.URL.RawQuery; vars != "" {
								redirectURL += "?" + vars
							}
							go postBalancingEventEx(c, internalName, bestRemote.Host, bestRemote.Score, lat, lon, "remote_redirect", redirectURL, bestRemote.GeoLatitude, bestRemote.GeoLongitude, "", durationMs, bestRemote.ClusterID)
							c.Header("Location", redirectURL)
							c.String(http.StatusTemporaryRedirect, redirectURL)
						} else {
							go postBalancingEventEx(c, internalName, bestRemote.Host, bestRemote.Score, lat, lon, "remote_redirect", "", bestRemote.GeoLatitude, bestRemote.GeoLongitude, "", durationMs, bestRemote.ClusterID)
							c.String(http.StatusOK, bestRemote.Host)
						}
						return
					}
				}
			}
		}
	}

	if err != nil {
		// Cross-cluster: check if an origin-pull was arranged for this stream
		if control.StreamRegistryInstance != nil {
			if loc, ok := control.StreamRegistryInstance.LocalReplication(ctx, internalName); ok && loc.PullDTSCURL != "" {
				if u, parseErr := url.Parse(loc.PullDTSCURL); parseErr == nil && u.Host != "" {
					logger.WithFields(logging.Fields{
						"stream":         streamName,
						"dtsc_host":      u.Host,
						"source_cluster": loc.ReplicatingFrom,
					}).Info("Cross-cluster balance: returning remote DTSC source")
					c.String(http.StatusOK, u.Host)

					durationMs := float32(time.Since(start).Milliseconds())
					go postBalancingEventEx(c, internalName, u.Host, 0, lat, lon, "cross_cluster_dtsc", loc.PullDTSCURL, 0, 0, "", durationMs, loc.ReplicatingFrom)
					return
				}
			}
		}

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

// emitFederationEvent sends a federation lifecycle event to Decklog.
// Automatically enriches with local/remote geo from cached self-geo and peer geo.
func emitFederationEvent(data *ipcpb.FederationEventData) {
	if decklogClient == nil {
		return
	}
	if data.TenantId == nil {
		tenantID := ownerTenantID
		if tenantID != "" {
			data.TenantId = &tenantID
		}
	}
	if data.GetTenantId() == "" {
		logger.WithFields(logging.Fields{
			"event_type":    data.GetEventType().String(),
			"local_cluster": data.GetLocalCluster(),
		}).Warn("Skipping federation event without tenant_id")
		return
	}
	if data.LocalCluster == "" {
		data.LocalCluster = clusterID
	}
	if data.LocalLat == nil && (selfLat != 0 || selfLon != 0) {
		data.LocalLat = &selfLat
		data.LocalLon = &selfLon
	}
	if data.RemoteLat == nil && data.RemoteCluster != "" && peerManager != nil {
		rLat, rLon := peerManager.GetPeerGeo(data.RemoteCluster)
		if rLat != 0 || rLon != 0 {
			data.RemoteLat = &rLat
			data.RemoteLon = &rLon
		}
	}
	go func() {
		if err := artifactoutbox.EnqueueFederationEvent(data); err != nil {
			logger.WithError(err).Debug("Failed to emit federation event")
		}
	}()
}

// sendRoutingEvent builds a LoadBalancingData proto from a RoutingEvent and
// sends it to Decklog. Shared by HTTP and gRPC routing emission paths.
func sendRoutingEvent(e *RoutingEvent) {
	if decklogClient == nil {
		logger.Error("Decklog gRPC client not initialized")
		return
	}

	event := BuildLoadBalancingData(e)

	if e.StreamID == "" {
		logger.WithField("stream_name", e.StreamName).Warn("LoadBalancingData missing stream_id")
	}

	if err := decklogClient.SendLoadBalancing(event); err != nil {
		logger.WithFields(logging.Fields{
			"error":       err,
			"stream_name": e.StreamName,
			"node":        e.SelectedNode,
		}).Error("Failed to send routing event to Decklog")
		return
	}

	logger.WithFields(logging.Fields{
		"stream_name": e.StreamName,
		"node":        e.SelectedNode,
		"status":      e.Status,
	}).Info("Routing event sent to Decklog")
}

// SendRoutingEvent is the exported version for use by the gRPC server.
// The caller must provide a *decklog.BatchedClient since the gRPC server
// uses an instance-level client rather than the package-level one.
func SendRoutingEvent(client *decklog.BatchedClient, e *RoutingEvent) {
	if client == nil {
		return
	}

	event := BuildLoadBalancingData(e)

	if err := client.SendLoadBalancing(event); err != nil {
		logger.WithFields(logging.Fields{
			"error":       err,
			"stream_name": e.StreamName,
			"node":        e.SelectedNode,
		}).Warn("Failed to send routing event to Decklog")
	}
}

// postBalancingEvent enriches a routing decision with gin request context
// (client IP, country, GeoIP fallback, Commodore tenant resolution) and
// sends it to Decklog for the /balance HTTP endpoint.
func postBalancingEvent(c *gin.Context, streamName, selectedNode string, score uint64, lat, lon float64, status, details string, nodeLat, nodeLon float64, nodeName string, durationMs float32) {
	postBalancingEventEx(c, streamName, selectedNode, score, lat, lon, status, details, nodeLat, nodeLon, nodeName, durationMs, "")
}

// postBalancingEventEx is postBalancingEvent with an explicit remoteClusterID
// for cross-cluster routing decisions.
func postBalancingEventEx(c *gin.Context, streamName, selectedNode string, score uint64, lat, lon float64, status, details string, nodeLat, nodeLon float64, nodeName string, durationMs float32, remoteClusterID string) {
	if routingEventsDisabled {
		return
	}
	// Extract client IP: CF-Connecting-IP > X-Forwarded-For > X-Real-IP > direct
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

	// Country: CF-IPCountry > X-Country-Code > GeoIP fallback
	country := c.GetHeader("CF-IPCountry")
	if country == "" {
		country = c.GetHeader("X-Country-Code")
	}
	if country == "" && geoipReader != nil && clientIP != "" {
		if geoData := geoip.LookupCached(c.Request.Context(), geoipReader, geoipCache, clientIP); geoData != nil {
			country = geoData.CountryCode
			if !geoip.IsValidLatLon(lat, lon) && geoip.IsValidLatLon(geoData.Latitude, geoData.Longitude) {
				lat = geoData.Latitude
				lon = geoData.Longitude
			}
			logger.WithFields(logging.Fields{
				"client_ip":    clientIP,
				"country_code": geoData.CountryCode,
				"city":         geoData.City,
				"latitude":     geoData.Latitude,
				"longitude":    geoData.Longitude,
			}).Debug("GeoIP fallback for routing event")
		}
	}

	// Resolve node ID from load balancer
	selectedNodeID := ""
	if lb != nil {
		if id := lb.GetNodeIDByHost(selectedNode); id != "" {
			selectedNodeID = id
		}
	}

	// Enrich with tenant info via Commodore
	var streamTenantID, internalName, streamID string
	if commodoreClient != nil && streamName != "" {
		if identity, err := resolveRoutingStreamIdentity(streamName); err == nil && identity != nil {
			streamTenantID = identity.GetTenantId()
			internalName = identity.GetInternalName()
			streamID = identity.GetStreamId()
		} else {
			logger.WithError(err).WithField("stream_name", streamName).Warn("Failed to resolve tenant via Commodore after retry")
		}
	}

	sendRoutingEvent(&RoutingEvent{
		Status:          status,
		Details:         details,
		Score:           score,
		StreamName:      streamName,
		InternalName:    internalName,
		StreamID:        streamID,
		StreamTenantID:  streamTenantID,
		ClientIP:        clientIP,
		ClientCountry:   country,
		ClientLat:       lat,
		ClientLon:       lon,
		SelectedNode:    selectedNode,
		SelectedNodeID:  selectedNodeID,
		NodeLat:         nodeLat,
		NodeLon:         nodeLon,
		NodeName:        nodeName,
		LatencyMs:       durationMs,
		RemoteClusterID: remoteClusterID,
	})
}

func resolveRoutingStreamIdentity(streamName string) (*commodorepb.ResolveStreamContextResponse, error) {
	bareInternal := mist.ExtractInternalName(streamName)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		resp, err := commodoreClient.ResolveStreamContext(ctx, "", "", bareInternal, clusterID)
		cancel()
		if err == nil && resp != nil && resp.GetStreamId() != "" {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		}
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		resp, err = commodoreClient.ResolveStreamContext(ctx, "", streamName, "", clusterID)
		cancel()
		if err == nil && resp != nil && resp.GetStreamId() != "" {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		}
		if attempt == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("stream identity not found")
}

// emitViewerRoutingEvent posts a routing decision for viewer playback.
// Extracts node identity from the ViewerEndpoint proto and delegates to sendRoutingEvent.
func emitViewerRoutingEvent(req *sharedpb.ViewerEndpointRequest, primary *sharedpb.ViewerEndpoint, viewerLat, viewerLon, nodeLat, nodeLon float64, internalName, streamTenantID, streamID string, durationMs float32, candidatesCount int32, eventType, source string) {
	if decklogClient == nil || primary == nil {
		return
	}

	selectedNode := primary.BaseUrl
	if selectedNode == "" {
		selectedNode = primary.Url
	}

	go sendRoutingEvent(&RoutingEvent{
		Status:          "success",
		Details:         "play_rewrite",
		Score:           uint64(primary.LoadScore),
		StreamName:      req.GetContentId(),
		InternalName:    internalName,
		StreamID:        streamID,
		StreamTenantID:  streamTenantID,
		ClientLat:       viewerLat,
		ClientLon:       viewerLon,
		SelectedNode:    selectedNode,
		SelectedNodeID:  primary.NodeId,
		NodeLat:         nodeLat,
		NodeLon:         nodeLon,
		NodeName:        primary.NodeId,
		LatencyMs:       durationMs,
		CandidatesCount: candidatesCount,
		EventType:       eventType,
		Source:          source,
	})
}

// Helper functions

func getStreamTenantID(streamName string) string {
	if triggerProcessor == nil {
		return ""
	}
	tenantID, _ := triggerProcessor.GetStreamOrigin(mist.ExtractInternalName(streamName))
	return tenantID
}

func viewerPlaybackTokenFromHTTPRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	if token := strings.TrimSpace(req.URL.Query().Get("jwt")); token != "" {
		return token
	}
	if token := strings.TrimSpace(req.Header.Get("X-Frameworks-Playback-JWT")); token != "" {
		return token
	}
	if token := strings.TrimSpace(req.Header.Get("X-Playback-JWT")); token != "" {
		return token
	}
	authz := strings.TrimSpace(req.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[len("Bearer "):])
	}
	if cookie, err := req.Cookie("jwt"); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

func enforceHTTPResolvePlaybackPolicy(ctx context.Context, req *sharedpb.ViewerEndpointRequest, internalName string) bool {
	if commodoreClient == nil {
		logger.WithFields(logging.Fields{
			"content_id": req.GetContentId(),
			"reason":     "policy-client-unavailable",
		}).Warn("Rejecting protected HTTP resolve request")
		return false
	}
	target := control.ResolvePlaybackPolicyTarget(ctx, req.GetContentId(), internalName)
	policy, err := commodoreClient.ResolvePlaybackPolicyForEnforcement(ctx, target.ContentID)
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"content_id": req.GetContentId(),
			"reason":     "policy-fetch-failed",
		}).Warn("Rejecting protected HTTP resolve request")
		return false
	}
	policyInternalName := mist.ExtractInternalName(target.InternalName)
	if policyInternalName == "" {
		policyInternalName = internalName
	}
	decision := triggers.EvaluatePlaybackPolicyWithRecorder(ctx, logger, policyInternalName, &ipcpb.ViewerConnectTrigger{
		StreamName:  policyInternalName,
		SessionId:   "resolve:" + req.GetContentId(),
		Host:        req.GetViewerIp(),
		RequestUrl:  "viewer://" + req.GetContentId(),
		ViewerToken: req.GetViewerToken(),
		Connector:   "resolve-http",
	}, policy, commodoreClient)
	return decision == "true"
}

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
		total += stream.Viewers
	}
	return total
}

// resolveLiveViewerEndpoint uses load balancer to find optimal edge nodes with fallbacks
func resolveLiveViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest, lat, lon float64, internalName, streamTenantID, streamID string, clusterPeers []*clusterpeerpb.TenantClusterPeer) (*sharedpb.ViewerEndpointResponse, error) {
	start := time.Now()
	// Delegate to consolidated control package function
	deps := &control.PlaybackDependencies{
		DB:             db,
		LB:             lb,
		GeoLat:         lat,
		GeoLon:         lon,
		LocalClusterID: clusterID,
	}

	if internalName == "" {
		return nil, fmt.Errorf("stream not found")
	}

	// Loop prevention: if we're already pulling this stream via origin-pull, skip remote
	// edge scoring entirely — let local scoring handle it (once DTSC pull completes, the
	// stream appears locally and gets StreamBonus).
	skipRemote := false
	if control.StreamRegistryInstance != nil {
		if _, ok := control.StreamRegistryInstance.LocalReplication(ctx, internalName); ok {
			skipRemote = true
		}
	}

	// Collect remote edge candidates from federation cache.
	// Primary source: cluster peers from control-plane resolution (free with every Commodore call).
	// Fallback: trigger processor cache (for streams ingesting locally).
	allPeers := clusterPeers
	if !skipRemote && remoteEdgeCache != nil && len(allPeers) > 0 {
		deps.RemoteEdges = collectRemoteEdges(ctx, allPeers)
	}
	if !skipRemote && remoteEdgeCache != nil && len(deps.RemoteEdges) == 0 && triggerProcessor != nil {
		if tpPeers := triggerProcessor.GetClusterPeers(internalName, streamTenantID); len(tpPeers) > 0 {
			deps.RemoteEdges = collectRemoteEdges(ctx, tpPeers)
			if len(allPeers) == 0 {
				allPeers = tpPeers
			}
		}
	}
	// Cold start: EdgeSummary cache empty but peers exist — fan out QueryStream
	if !skipRemote && len(deps.RemoteEdges) == 0 && len(allPeers) > 0 {
		deps.RemoteEdges = queryStreamFanOut(ctx, internalName, streamTenantID, lat, lon, allPeers)
	}

	response, err := control.ResolveLivePlayback(ctx, deps, req.ContentId, internalName, streamID, streamTenantID)
	if err != nil {
		return nil, err
	}

	// If a remote cluster won the summary-level comparison, confirm with QueryStream.
	// The remote foghorn scores its own local nodes and returns actual play-ready
	// endpoints. An infra-error from arrangement bubbles up as 5xx rather than
	// silently degrading to the summary-level redirect.
	if response.Primary != nil && response.Primary.ClusterId != "" {
		confirmed, confirmErr := confirmRemoteEndpoint(ctx, response, req.ContentId, internalName, streamTenantID, lat, lon)
		if confirmErr != nil {
			return nil, confirmErr
		}
		if confirmed != nil {
			response = confirmed
		}
		// If confirmation soft-failed (nil, nil), response keeps the summary-level redirect — usable as fallback
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

func collectRemoteEdges(ctx context.Context, peers []*clusterpeerpb.TenantClusterPeer) []balancer.RemoteEdgeCandidate {
	var candidates []balancer.RemoteEdgeCandidate
	for _, peer := range peers {
		if peer.GetClusterId() == clusterID || peer.GetClusterId() == "" || control.IsServedCluster(peer.GetClusterId()) {
			continue
		}
		// Liveness gate: a peer's EdgeSummary (60s TTL) outlives its heartbeat (30s TTL),
		// so a peer dead 30–60s still has a cached summary. Skip it once the heartbeat key
		// has expired, so stale telemetry can't attract cross-cluster routing.
		if hb, hbErr := remoteEdgeCache.GetPeerHeartbeat(ctx, peer.GetClusterId()); hbErr != nil || hb == nil {
			continue
		}
		record, err := remoteEdgeCache.GetEdgeSummary(ctx, peer.GetClusterId())
		if err != nil || record == nil {
			continue
		}
		for _, edge := range record.Edges {
			candidates = append(candidates, balancer.RemoteEdgeCandidate{
				ClusterID:   peer.GetClusterId(),
				NodeID:      edge.NodeID,
				BaseURL:     edge.BaseURL,
				GeoLat:      edge.GeoLat,
				GeoLon:      edge.GeoLon,
				BWAvailable: edge.BWAvailableAvg,
				CPUPercent:  edge.CPUPercentAvg,
				RAMUsed:     edge.RAMUsed,
				RAMMax:      edge.RAMMax,
			})
		}
	}
	return candidates
}

// findBestRemoteNode returns the highest-scored remote node, or nil if none.
func findBestRemoteNode(nodes []balancer.NodeWithScore) *balancer.NodeWithScore {
	if len(nodes) == 0 {
		return nil
	}
	best := &nodes[0]
	for i := 1; i < len(nodes); i++ {
		if nodes[i].Score > best.Score {
			best = &nodes[i]
		}
	}
	return best
}

// confirmRemoteStream verifies that a remote cluster actually has the stream
// by issuing a QueryStream RPC. Returns true only if the remote cluster responds
// with at least one candidate. Uses a 3s timeout to avoid blocking the viewer.
func confirmRemoteStream(ctx context.Context, remoteClusterID, internalName, tenantID string, lat, lon float64) bool {
	if federationClient == nil || peerManager == nil {
		return false
	}
	addr := peerManager.GetPeerAddr(remoteClusterID)
	if addr == "" {
		return false
	}
	qCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := federationClient.QueryStream(qCtx, remoteClusterID, addr, &foghornfederationpb.QueryStreamRequest{
		StreamName:        internalName,
		ViewerLat:         lat,
		ViewerLon:         lon,
		RequestingCluster: clusterID,
		TenantId:          tenantID,
	})
	return err == nil && resp != nil && len(resp.Candidates) > 0
}

// confirmRemoteEndpoint validates a summary-level remote win by calling
// QueryStream on the winning cluster's foghorn. Three return shapes:
//
//	(non-nil, nil) — confirmed; caller swaps response for this richer one.
//	(nil, nil)     — soft failure (peer unreachable, no candidates, no
//	                  DTSC URL, LB miss); caller keeps the summary-level
//	                  redirect.
//	(nil, err)     — arrangement infra failure; caller surfaces 5xx to
//	                  the viewer instead of silently degrading.
func confirmRemoteEndpoint(ctx context.Context, response *sharedpb.ViewerEndpointResponse, viewKey, internalName, tenantID string, lat, lon float64) (*sharedpb.ViewerEndpointResponse, error) {
	if federationClient == nil || peerManager == nil {
		return nil, nil
	}

	// Collect unique remote clusters from response (primary + fallbacks with ClusterId)
	type remoteHit struct {
		clusterID string
		score     float64
	}
	var remotes []remoteHit
	seen := make(map[string]bool)

	if response.Primary != nil && response.Primary.ClusterId != "" && !seen[response.Primary.ClusterId] {
		seen[response.Primary.ClusterId] = true
		remotes = append(remotes, remoteHit{clusterID: response.Primary.ClusterId, score: response.Primary.LoadScore})
	}
	for _, fb := range response.Fallbacks {
		if fb.ClusterId != "" && !seen[fb.ClusterId] {
			seen[fb.ClusterId] = true
			remotes = append(remotes, remoteHit{clusterID: fb.ClusterId, score: fb.LoadScore})
		}
	}
	if len(remotes) == 0 {
		return nil, nil
	}

	// Fan out QueryStream to all candidate remote clusters in parallel
	type queryResult struct {
		clusterID string
		resp      *foghornfederationpb.QueryStreamResponse
	}
	ch := make(chan queryResult, len(remotes))
	var wg sync.WaitGroup

	for _, r := range remotes {
		addr := peerManager.GetPeerAddr(r.clusterID)
		if addr == "" {
			continue
		}
		wg.Add(1)
		go func(cid, caddr string) {
			defer wg.Done()
			qCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			resp, err := federationClient.QueryStream(qCtx, cid, caddr, &foghornfederationpb.QueryStreamRequest{
				StreamName:        internalName,
				ViewerLat:         lat,
				ViewerLon:         lon,
				RequestingCluster: clusterID,
				TenantId:          tenantID,
			})
			if err != nil || resp == nil || len(resp.Candidates) == 0 {
				return
			}
			ch <- queryResult{clusterID: cid, resp: resp}
		}(r.clusterID, addr)
	}
	go func() { wg.Wait(); close(ch) }()

	// Pick the best candidate across all QueryStream responses
	var bestCandidate *foghornfederationpb.EdgeCandidate
	var bestCluster string
	for qr := range ch {
		for _, c := range qr.resp.Candidates {
			if bestCandidate == nil || c.BwScore > bestCandidate.BwScore {
				bestCandidate = c
				bestCluster = qr.clusterID
			}
		}
	}
	if bestCandidate == nil {
		return nil, nil
	}

	// Origin-pull arrangement: if we have local edge capacity, pull stream locally via DTSC
	// so subsequent viewers are served from our cluster without cross-cluster hops.
	// Infra-error propagates so the HTTP handler surfaces a 5xx instead of a
	// silently-degraded redirect.
	if remoteEdgeCache != nil && bestCandidate.DtscUrl != "" {
		arranged, arrangeErr := arrangeOriginPull(ctx, bestCandidate, bestCluster, internalName, tenantID, viewKey, lat, lon, response.Metadata)
		if arrangeErr != nil {
			return nil, arrangeErr
		}
		if arranged != nil {
			return arranged, nil
		}
	}

	// No origin-pull possible — redirect viewer to the remote cluster
	playURL := control.PlaybackEdgeRedirectURL(bestCandidate.BaseUrl, viewKey)
	confirmed := &sharedpb.ViewerEndpointResponse{
		Primary: &sharedpb.ViewerEndpoint{
			NodeId:    bestCandidate.NodeId,
			BaseUrl:   bestCandidate.BaseUrl,
			Protocol:  "redirect",
			Url:       playURL,
			LoadScore: float64(bestCandidate.BwScore),
			ClusterId: bestCluster,
		},
		Metadata: response.Metadata,
	}

	// Keep any local fallbacks from the original response
	for _, fb := range response.Fallbacks {
		if fb.ClusterId == "" {
			confirmed.Fallbacks = append(confirmed.Fallbacks, fb)
		}
	}

	logger.WithFields(logging.Fields{
		"stream":         internalName,
		"remote_cluster": bestCluster,
		"remote_node":    bestCandidate.NodeId,
		"remote_score":   bestCandidate.BwScore,
	}).Info("Remote endpoint confirmed via QueryStream — redirecting (no local capacity)")

	return confirmed, nil
}

// arrangeOriginPull attempts to set up a local DTSC pull from a remote
// source. Three return shapes:
//
//	(non-nil, nil) — arrangement succeeded; viewer goes to local edge.
//	(nil, nil)     — soft refusal (LB miss, contention, loop prevention);
//	                  caller falls through to peer-redirect fallback.
//	(nil, err)     — infra failure (registry/deps/peer/notify); caller
//	                  surfaces 5xx via the HTTP handler. Use
//	                  federation.IsArrangeInfraError to discriminate.
//
// Thin wrapper around federation.ArrangeOriginPull — supplies the LB
// picker so the helper selects a local edge with capacity, then builds
// the ViewerEndpoint from the resulting Location.
func arrangeOriginPull(ctx context.Context, remote *foghornfederationpb.EdgeCandidate, remoteCluster, internalName, tenantID, viewKey string, lat, lon float64, metadata *sharedpb.PlaybackMetadata) (*sharedpb.ViewerEndpointResponse, error) {
	deps := &federation.ArrangeOriginPullDeps{
		Cache:        remoteEdgeCache,
		PeerResolver: peerManager,
		FedClient:    federationClient,
		InstanceID:   originPullInstanceID,
		ClusterID:    clusterID,
		Logger:       logger,
		EventEmitter: emitFederationEvent,
	}
	result, err := deps.ArrangeOriginPull(ctx, federation.ArrangeOriginPullRequest{
		InternalName:  internalName,
		Remote:        remote,
		RemoteCluster: remoteCluster,
		TenantID:      tenantID,
		Lat:           lat,
		Lon:           lon,
		LBPicker: func(pickCtx context.Context, pickLat, pickLon float64, pickTenant string) (string, string, error) {
			lbCtx := context.WithValue(pickCtx, ctxkeys.KeyCapability, "edge")
			if pickTenant != "" {
				lbCtx = context.WithValue(lbCtx, ctxkeys.KeyClusterScope, pickTenant)
			}
			host, _, _, _, _, pickErr := lb.GetBestNodeWithScore(lbCtx, "", pickLat, pickLon, nil, "", false)
			if pickErr != nil {
				return "", "", pickErr
			}
			return host, lb.GetNodeIDByHost(host), nil
		},
	})
	if err != nil {
		if federation.IsArrangeInfraError(err) {
			logger.WithError(err).WithFields(logging.Fields{
				"stream":         internalName,
				"remote_cluster": remoteCluster,
			}).Error("ArrangeOriginPull: infra failure; refusing to silently redirect")
			return nil, err
		}
		return nil, nil
	}
	if result == nil {
		return nil, nil
	}
	endpoint := buildLocalEndpointFromReplication(result.DestNodeID, viewKey)
	if endpoint != nil {
		return &sharedpb.ViewerEndpointResponse{Primary: endpoint, Metadata: metadata}, nil
	}
	return nil, nil
}

// buildLocalEndpointFromReplication constructs a ViewerEndpoint from a local node
// that has an in-flight or completed origin-pull replication.
func buildLocalEndpointFromReplication(destNodeID, viewKey string) *sharedpb.ViewerEndpoint {
	nodeOutputs, exists := control.GetNodeOutputs(destNodeID)
	if !exists || nodeOutputs.Outputs == nil {
		return nil
	}
	return control.BuildViewerEndpointFromOutputs(destNodeID, nodeOutputs, viewKey, true)
}

// queryStreamFanOut performs cold-start QueryStream fan-out to peer clusters when
// no EdgeSummary data is cached. Returns RemoteEdgeCandidates for scoring.
func queryStreamFanOut(ctx context.Context, internalName, tenantID string, lat, lon float64, peers []*clusterpeerpb.TenantClusterPeer) []balancer.RemoteEdgeCandidate {
	if federationClient == nil || peerManager == nil {
		return nil
	}

	fanOutStart := time.Now()

	type result struct {
		candidates []balancer.RemoteEdgeCandidate
	}
	ch := make(chan result, len(peers))
	var wg sync.WaitGroup
	var queriedCount uint32

	for _, peer := range peers {
		if peer.GetClusterId() == clusterID || peer.GetClusterId() == "" || control.IsServedCluster(peer.GetClusterId()) {
			continue
		}
		addr := peerManager.GetPeerAddr(peer.GetClusterId())
		if addr == "" {
			continue
		}
		queriedCount++
		wg.Add(1)
		go func(peerID, peerAddr string) {
			defer wg.Done()
			qCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			resp, err := federationClient.QueryStream(qCtx, peerID, peerAddr, &foghornfederationpb.QueryStreamRequest{
				StreamName:        internalName,
				ViewerLat:         lat,
				ViewerLon:         lon,
				RequestingCluster: clusterID,
				TenantId:          tenantID,
			})
			if err != nil || resp == nil || len(resp.Candidates) == 0 {
				ch <- result{}
				return
			}
			var cands []balancer.RemoteEdgeCandidate
			for _, c := range resp.Candidates {
				cands = append(cands, balancer.RemoteEdgeCandidate{
					ClusterID:   peerID,
					NodeID:      c.NodeId,
					BaseURL:     c.BaseUrl,
					GeoLat:      c.GeoLat,
					GeoLon:      c.GeoLon,
					BWAvailable: c.BwAvailable,
					CPUPercent:  c.CpuPercent,
					RAMUsed:     c.RamUsed,
					RAMMax:      c.RamMax,
				})
			}
			ch <- result{candidates: cands}
		}(peer.GetClusterId(), addr)
	}
	go func() { wg.Wait(); close(ch) }()

	var all []balancer.RemoteEdgeCandidate
	var respondingCount uint32
	for r := range ch {
		if len(r.candidates) > 0 {
			respondingCount++
		}
		all = append(all, r.candidates...)
	}

	if queriedCount > 0 {
		latMs := float32(time.Since(fanOutStart).Milliseconds())
		totalCandidates := uint32(len(all))
		emitFederationEvent(&ipcpb.FederationEventData{
			EventType:          ipcpb.FederationEventType_FEDERATION_QUERY,
			StreamName:         &internalName,
			LatencyMs:          &latMs,
			QueriedClusters:    &queriedCount,
			RespondingClusters: &respondingCount,
			TotalCandidates:    &totalCandidates,
		})
	}

	return all
}

// resolveArtifactViewerEndpoint queries database for VOD/Clip/DVR storage nodes via a single resolver.
// It derives type from the public ID and does not depend on any caller-provided content type.
// resolveDVRViewerEndpoint dispatches a DVR viewer request the same way
// the gRPC server does: active recording → live-style edge selection
// (so any healthy edge can serve via DTSC pull from the recording
// origin), finalized → artifact warm-cache routing.
//
// resolution.InternalName is "dvr+<dvr_internal_name>" out of
// ResolveContent. Fail-closed for active-DVR ambiguity — never silently
// reroute live viewers through the archive lane.
func resolveDVRViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest, lat, lon float64, resolution *control.ContentResolution) (*sharedpb.ViewerEndpointResponse, error) {
	dvrInternalName := mist.ExtractInternalName(resolution.InternalName)
	dispatch, derr := control.ResolveDVRArtifactDispatch(ctx, dvrInternalName)
	if derr != nil {
		logger.WithError(derr).WithFields(logging.Fields{
			"content_id":    req.GetContentId(),
			"internal_name": dvrInternalName,
		}).Warn("DVR dispatch lookup failed")
		return nil, fmt.Errorf("DVR routing unavailable")
	}
	if dispatch != nil && dispatch.Status != "" && control.IsActiveDVRStatus(dispatch.Status) {
		if dispatch.RecordingNode == "" {
			logger.WithFields(logging.Fields{
				"content_id":    req.GetContentId(),
				"internal_name": dvrInternalName,
				"status":        dispatch.Status,
			}).Warn("Active DVR has no resolvable recording origin; refusing to fall back to archive routing")
			return nil, fmt.Errorf("active DVR recording origin not yet registered; retry")
		}
		resp, err := resolveLiveViewerEndpoint(ctx, req, lat, lon, resolution.InternalName, resolution.TenantId, resolution.StreamId, resolution.ClusterPeers)
		if err != nil {
			return nil, err
		}
		// Rewrite live-shaped metadata to DVR identity.
		if resp != nil && resp.Metadata != nil {
			resp.Metadata.ContentType = "dvr"
			resp.Metadata.Status = "recording"
			resp.Metadata.DvrStatus = "recording"
		}
		return resp, nil
	}
	// Finalized DVR: the rolling surface is gone. Match the gRPC path
	// (api_balancing/internal/grpc/server.go::resolveDVRViewerEndpoint)
	// and require the client to query dvrChapters() then play a chapter
	// playbackId — falling through to artifact playback would surface
	// the parent DVR row, which has no playable artifact.
	return nil, fmt.Errorf("DVR is no longer active; query dvrChapters and play a chapter playbackId")
}

func resolveArtifactViewerEndpoint(req *sharedpb.ViewerEndpointRequest, lat, lon float64) (*sharedpb.ViewerEndpointResponse, error) {
	start := time.Now()
	deps := &control.PlaybackDependencies{
		DB:             db,
		LB:             lb,
		GeoLat:         lat,
		GeoLon:         lon,
		FedClient:      federationClient,
		PeerResolver:   peerManager,
		LocalClusterID: clusterID,
		RemoteArtifacts: func() control.RemoteArtifactLookup {
			if remoteEdgeCache == nil {
				return nil
			}
			return &httpRemoteArtifactAdapter{cache: remoteEdgeCache}
		}(),
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

	viewKey, protocol, manifestPath := parsePlaybackPath(fullPath)

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
		billingTarget := control.ResolvePlaybackPolicyTarget(c.Request.Context(), contentID, resolution.InternalName)
		billingInternalName := mist.ExtractInternalName(billingTarget.InternalName)
		if billingInternalName == "" {
			billingInternalName = internalName
		}
		billing := getBillingStatus(c.Request.Context(), billingInternalName, resolution.TenantId)
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
	req := &sharedpb.ViewerEndpointRequest{
		ContentId: contentID,
		ViewerIp:  proto.String(viewerIP),
	}
	if token := viewerPlaybackTokenFromHTTPRequest(c.Request); token != "" {
		req.ViewerToken = proto.String(token)
	}
	if resolution.RequiresAuth {
		if !enforceHTTPResolvePlaybackPolicy(c.Request.Context(), req, internalName) {
			respondPlaybackError(c, http.StatusForbidden, "PLAYBACK_ACCESS_DENIED", "Playback access denied", nil)
			return
		}
	}

	// Get geo location for viewer
	var lat, lon float64
	if geoipReader != nil {
		if geoData := geoip.LookupCached(c.Request.Context(), geoipReader, geoipCache, viewerIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
		}
	}

	// Resolve endpoint. Active DVR (recording in progress) takes the
	// live-style lane so any healthy edge can serve via DTSC pull from
	// the recording origin; finalized DVRs and clip/VOD ride the
	// artifact warm-cache lane. The gRPC server has the same dispatch
	// in resolveDVRViewerEndpoint — keep them in lockstep.
	var response *sharedpb.ViewerEndpointResponse
	switch contentType {
	case "live":
		response, err = resolveLiveViewerEndpoint(c.Request.Context(), req, lat, lon, resolution.RoutingInternalName(), resolution.TenantId, resolution.StreamId, resolution.ClusterPeers)
	case "dvr":
		response, err = resolveDVRViewerEndpoint(c.Request.Context(), req, lat, lon, resolution)
	default:
		response, err = resolveArtifactViewerEndpoint(req, lat, lon)
	}

	if err != nil {
		if errors.Is(err, control.ErrCrossClusterArtifactUnavailable) {
			// Fail-fast — peer origin hasn't pushed the artifact to S3
			// yet. Surface as 503 (Service Unavailable) so callers retry
			// at the app layer instead of us hiding a long polling loop
			// behind a 202.
			respondPlaybackError(c, http.StatusServiceUnavailable, "REMOTE_ARTIFACT_UNAVAILABLE", err.Error(), gin.H{
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
		viewerInternalName := resolution.RoutingInternalName()
		if viewerInternalName == "" {
			viewerInternalName = contentID
		}
		viewerID = state.DefaultManager().CreateVirtualViewer(response.Primary.NodeId, viewerInternalName, viewerIP)
	}

	// If no protocol or "any", return full JSON response
	if protocol == "" || protocol == "any" {
		control.AppendViewerCorrelationID(response, viewerID)

		// Record metrics
		if metrics != nil {
			metrics.RoutingDecisions.WithLabelValues("generic_viewer_"+contentType, response.Primary.NodeId).Inc()
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
	redirectURL = appendManifestPath(redirectURL, manifestPath)
	if viewerID != "" {
		redirectURL = appendCorrelationID(redirectURL, viewerID)
	}

	// Record metrics
	if metrics != nil {
		metrics.RoutingDecisions.WithLabelValues("generic_viewer_redirect_"+protocol, response.Primary.NodeId).Inc()
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
	return control.AppendCorrelationID(redirectURL, viewerID)
}

// appendManifestPath joins a requested manifest path (e.g. "index.m3u8") onto a
// resolved edge output URL, but only when that URL is a container base rather than
// an already-complete manifest URL. MistServer reports some outputs as a bare
// directory (CMAF: ".../cmaf/<stream>/") and others as a full manifest URL
// (HLS: ".../hls/<stream>/index.m3u8"); appending unconditionally would yield
// ".../index.m3u8/index.m3u8". Any query string on the resolved URL is preserved
// (the manifest segment lands on the path, never after the query).
func appendManifestPath(redirectURL, manifestPath string) string {
	if manifestPath == "" {
		return redirectURL
	}
	parsed, err := url.Parse(redirectURL)
	if err != nil {
		return redirectURL
	}
	if isManifestPath(parsed.Path) {
		return redirectURL
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + strings.TrimPrefix(manifestPath, "/")
	parsed.RawPath = ""
	return parsed.String()
}

// isManifestPath reports whether a URL path already ends in a streaming manifest
// file, in which case it is a complete playback URL needing no manifest suffix.
func isManifestPath(p string) bool {
	last := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		last = p[i+1:]
	}
	if last == "" {
		return false
	}
	return strings.HasSuffix(last, ".m3u8") ||
		strings.HasSuffix(last, ".mpd") ||
		strings.HasSuffix(last, ".f4m") ||
		last == "Manifest"
}

// parsePlaybackPath splits a /play/ path tail into view key, protocol, and
// manifest path. It accepts both "<viewkey>.<proto>" (e.g. vk.mp4, vk.html) and
// "<viewkey>/<proto>/<manifest>" (e.g. vk/cmaf/index.mpd) forms. An empty view
// key signals an invalid path.
func parsePlaybackPath(fullPath string) (viewKey, protocol, manifestPath string) {
	fullPath = strings.TrimPrefix(fullPath, "/")
	parts := strings.Split(fullPath, "/")

	// First segment may be "viewkey.protocol" (e.g. viewkey.hls / viewkey.m3u8).
	dotParts := strings.Split(parts[0], ".")
	viewKey = dotParts[0]
	if len(dotParts) > 1 {
		protocol = dotParts[1]
	}

	// Or protocol as the second path segment (e.g. viewkey/hls/index.m3u8).
	if len(parts) > 1 && protocol == "" {
		protocol = parts[1]
		if len(parts) > 2 {
			manifestPath = strings.Join(parts[2:], "/")
		}
	}
	return viewKey, protocol, manifestPath
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
func findProtocolURL(outputs map[string]*sharedpb.OutputEndpoint, protocol string) string {
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
		// MistServer has no separate DASH output by default; the DASH manifest is
		// served from the CMAF container (.../cmaf/<stream>/index.mpd). Fall back
		// to a CMAF output so /play/<id>/dash/index.mpd still resolves.
		for outputName, output := range outputs {
			outputLower := strings.ToLower(outputName)
			if strings.Contains(outputLower, "cmaf") || strings.Contains(outputLower, "ll-hls") || strings.Contains(outputLower, "llhls") || strings.Contains(outputLower, "low latency") {
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
}

// SetCommodoreClient injects a Commodore client after initialization.
func SetCommodoreClient(client *commodore.GRPCClient) {
	commodoreClient = client
	control.SetCommodoreClient(client)
}
