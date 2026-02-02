package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"frameworks/api_balancing/internal/triggers"
	"frameworks/pkg/clients/decklog"
	purserclient "frameworks/pkg/clients/purser"
	"frameworks/pkg/clips"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/x402"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// S3ClientInterface defines the S3 operations needed by FoghornGRPCServer
type S3ClientInterface interface {
	CreateMultipartUpload(ctx context.Context, key string, contentType string) (string, error)
	GeneratePresignedUploadParts(key, uploadID string, partCount int, expiry time.Duration) ([]storage.UploadPart, error)
	CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []storage.CompletedPart) error
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error
	BuildVodS3Key(tenantID, artifactHash, filename string) string
	BuildS3URL(key string) string
	Delete(ctx context.Context, key string) error
}

// CacheInvalidator is implemented by the trigger processor to invalidate and lookup cached tenant data
type CacheInvalidator interface {
	InvalidateTenantCache(tenantID string) int
	GetBillingStatus(ctx context.Context, internalName, tenantID string) *triggers.BillingStatus
}

// FoghornGRPCServer implements the Foghorn control plane gRPC services
type FoghornGRPCServer struct {
	pb.UnimplementedClipControlServiceServer
	pb.UnimplementedDVRControlServiceServer
	pb.UnimplementedViewerControlServiceServer
	pb.UnimplementedVodControlServiceServer
	pb.UnimplementedTenantControlServiceServer

	db               *sql.DB
	logger           logging.Logger
	lb               *balancer.LoadBalancer
	geoipReader      *geoip.Reader
	decklogClient    *decklog.BatchedClient
	s3Client         S3ClientInterface
	cacheInvalidator CacheInvalidator
	purserClient     *purserclient.GRPCClient
}

// NewFoghornGRPCServer creates a new Foghorn gRPC server
func NewFoghornGRPCServer(
	db *sql.DB,
	logger logging.Logger,
	lb *balancer.LoadBalancer,
	geoReader *geoip.Reader,
	decklogClient *decklog.BatchedClient,
	s3Client S3ClientInterface,
	purserClient *purserclient.GRPCClient,
) *FoghornGRPCServer {
	return &FoghornGRPCServer{
		db:            db,
		logger:        logger,
		lb:            lb,
		geoipReader:   geoReader,
		decklogClient: decklogClient,
		s3Client:      s3Client,
		purserClient:  purserClient,
	}
}

// RegisterServices registers all Foghorn gRPC services with the server
func (s *FoghornGRPCServer) RegisterServices(grpcServer *grpc.Server) {
	pb.RegisterClipControlServiceServer(grpcServer, s)
	pb.RegisterDVRControlServiceServer(grpcServer, s)
	pb.RegisterViewerControlServiceServer(grpcServer, s)
	pb.RegisterVodControlServiceServer(grpcServer, s)
	pb.RegisterTenantControlServiceServer(grpcServer, s)
}

// SetCacheInvalidator sets the cache invalidator for tenant cache management
func (s *FoghornGRPCServer) SetCacheInvalidator(ci CacheInvalidator) {
	s.cacheInvalidator = ci
}

// emitRoutingEvent sends a LoadBalancingData event with dual-tenant attribution
// Called after successful viewer endpoint resolution to track routing decisions
// durationMs is the time taken to resolve the routing decision (request processing latency)
func (s *FoghornGRPCServer) emitRoutingEvent(
	primary *pb.ViewerEndpoint,
	viewerLat, viewerLon, nodeLat, nodeLon float64,
	internalName, streamTenantID, streamID string,
	durationMs float32,
	candidatesCount int32,
	eventType, source string,
) {
	if s.decklogClient == nil || primary == nil {
		return
	}

	selectedNode := primary.BaseUrl
	if selectedNode == "" {
		selectedNode = primary.Url
	}

	// Calculate routing distance
	routingDistanceKm := 0.0
	if viewerLat != 0 && viewerLon != 0 && nodeLat != 0 && nodeLon != 0 {
		const toRad = math.Pi / 180.0
		lat1, lon1 := viewerLat*toRad, viewerLon*toRad
		lat2, lon2 := nodeLat*toRad, nodeLon*toRad
		val := math.Sin(lat1)*math.Sin(lat2) + math.Cos(lat1)*math.Cos(lat2)*math.Cos(lon1-lon2)
		if val > 1 {
			val = 1
		} else if val < -1 {
			val = -1
		}
		routingDistanceKm = 6371.0 * math.Acos(val)
	}

	// Get cached cluster info for dual-tenant attribution
	clusterID, ownerTenantID := handlers.GetClusterInfo()

	// Bucketize coordinates for privacy
	clientBucket, clientCentLat, clientCentLon, hasClientBucket := geo.Bucket(viewerLat, viewerLon)
	nodeBucket, nodeCentLat, nodeCentLon, hasNodeBucket := geo.Bucket(nodeLat, nodeLon)

	event := &pb.LoadBalancingData{
		SelectedNode:   selectedNode,
		SelectedNodeId: func() *string { s := primary.NodeId; return &s }(),
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
		Details:  "grpc_resolve",
		Score:    uint64(primary.LoadScore),
		ClientIp: "", // redacted
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
		NodeName: primary.NodeId,
		RoutingDistanceKm: func() *float64 {
			if routingDistanceKm == 0 {
				return nil
			}
			return &routingDistanceKm
		}(),
		InternalName: func() *string {
			if internalName != "" {
				return &internalName
			}
			return nil
		}(),
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

	go func() {
		if err := s.decklogClient.SendLoadBalancing(event); err != nil {
			s.logger.WithError(err).WithField("internal_name", internalName).Warn("Failed to send gRPC routing event to Decklog")
		}
	}()
}

// StartGRPCServer starts the Foghorn gRPC server
func StartGRPCServer(addr string, server *FoghornGRPCServer) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	grpcServer := grpc.NewServer()
	server.RegisterServices(grpcServer)

	// gRPC health service for Foghorn control APIs
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.ClipControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.DVRControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.ViewerControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(pb.VodControlService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcServer, hs)

	server.logger.WithField("addr", addr).Info("Starting Foghorn gRPC server")
	return grpcServer.Serve(lis)
}

// =============================================================================
// CLIP CONTROL SERVICE IMPLEMENTATION
// =============================================================================

// buildClipLifecycleData creates an enriched ClipLifecycleData with timing fields
// CRITICAL: This function fixes the missing enrichment bug documented in ipc.proto lines 575-580
func buildClipLifecycleData(stage pb.ClipLifecycleData_Stage, req *pb.CreateClipRequest, reqID, clipHash string) *pb.ClipLifecycleData {
	data := &pb.ClipLifecycleData{
		Stage:     stage,
		RequestId: &reqID,
	}
	if clipHash != "" {
		data.ClipHash = clipHash
	}
	if req.TenantId != "" {
		data.TenantId = &req.TenantId
	}
	if req.InternalName != "" {
		data.InternalName = &req.InternalName
	}
	if req.StreamId != nil && *req.StreamId != "" {
		data.StreamId = req.StreamId
	}
	// CRITICAL: Enrich with timing fields for analytics
	if req.StartUnix != nil {
		data.StartUnix = req.StartUnix
	}
	if req.StopUnix != nil {
		data.StopUnix = req.StopUnix
	}
	if req.StartMs != nil {
		data.StartMs = req.StartMs
	}
	if req.StopMs != nil {
		data.StopMs = req.StopMs
	}
	if req.DurationSec != nil {
		data.DurationSec = req.DurationSec
	}
	// Include mode for analytics
	if req.Mode != pb.ClipMode_CLIP_MODE_UNSPECIFIED {
		modeStr := req.Mode.String()
		data.ClipMode = &modeStr
	}
	if req.ExpiresAt != nil {
		data.ExpiresAt = req.ExpiresAt
	}
	if req.UserId != nil && *req.UserId != "" {
		data.UserId = req.UserId
	}
	return data
}

// CreateClip creates a new clip from a stream
func (s *FoghornGRPCServer) CreateClip(ctx context.Context, req *pb.CreateClipRequest) (*pb.CreateClipResponse, error) {
	if req.InternalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetArtifactInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_internal_name is required")
	}

	format := req.GetFormat()
	if format == "" {
		format = "mp4"
	}

	// Select ingest node (cap=ingest)
	ictx := context.WithValue(ctx, "cap", "ingest")
	ingestHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(ictx, req.InternalName, 0, 0, map[string]int{}, "", true)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "no ingest node available: %v", err)
	}

	// Select storage node (cap=storage)
	sctx := context.WithValue(ctx, "cap", "storage")
	storageHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(sctx, "", 0, 0, map[string]int{}, "", false)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "no storage node available: %v", err)
	}

	// Generate request_id for correlation
	reqID := uuid.New().String()

	// Get storage node ID
	storageNodeID := s.lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		return nil, status.Error(codes.Unavailable, "storage node not connected")
	}

	// Resolve timing for hash generation and DB storage
	// Use start_unix or start_ms depending on mode, convert to milliseconds for storage
	var startMs, durationMs int64
	if req.StartUnix != nil {
		startMs = *req.StartUnix * 1000 // Convert seconds to ms
	} else if req.StartMs != nil {
		startMs = *req.StartMs * 1000 // start_ms is actually seconds, convert to ms
	}
	if req.DurationSec != nil {
		durationMs = *req.DurationSec * 1000 // Convert seconds to ms
	} else if req.StopUnix != nil && req.StartUnix != nil {
		durationMs = (*req.StopUnix - *req.StartUnix) * 1000
	} else if req.StopMs != nil && req.StartMs != nil {
		durationMs = (*req.StopMs - *req.StartMs) * 1000
	}

	// Use provided clip_hash from Commodore if available, otherwise generate locally
	var clipHash string
	if req.GetClipHash() != "" {
		clipHash = req.GetClipHash()
	} else {
		// Fallback: generate locally (legacy, should not happen with Commodore integration)
		var err error
		clipHash, err = clips.GenerateClipHash(req.InternalName, startMs, durationMs)
		if err != nil {
			s.logger.WithError(err).Error("Failed to generate clip hash")
			return nil, status.Error(codes.Internal, "failed to generate clip hash")
		}
	}

	// Emit STAGE_REQUESTED event to Decklog (with enriched timing fields)
	if s.decklogClient != nil {
		clipData := buildClipLifecycleData(pb.ClipLifecycleData_STAGE_REQUESTED, req, reqID, clipHash)
		go func() { _ = s.decklogClient.SendClipLifecycle(clipData) }()
	}

	// Build requested_params JSON for audit
	requestedParams := map[string]interface{}{
		"mode": req.Mode.String(),
	}
	if req.StartUnix != nil {
		requestedParams["start_unix"] = *req.StartUnix
	}
	if req.StopUnix != nil {
		requestedParams["stop_unix"] = *req.StopUnix
	}
	if req.StartMs != nil {
		requestedParams["start_ms"] = *req.StartMs
	}
	if req.StopMs != nil {
		requestedParams["stop_ms"] = *req.StopMs
	}
	if req.DurationSec != nil {
		requestedParams["duration_sec"] = *req.DurationSec
	}
	// requestedParams is stored in Commodore business registry, not in Foghorn
	// Retention policy (ExpiresAt) is also managed in Commodore

	// Store artifact lifecycle state in foghorn.artifacts
	// NOTE: Business registry (tenant, user, title, etc.) is stored in commodore.clips
	// tenant_id and user_id are denormalized here for Decklog events and fallback when Commodore is unavailable
	// retention_until defaults to 30 days (system default, not user-configured yet)
	storagePath := clips.BuildClipStoragePath(req.InternalName, clipHash, format)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, internal_name, artifact_internal_name, tenant_id, user_id, status, request_id, manifest_path, format, retention_until, created_at, updated_at)
		VALUES ($1, 'clip', $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, 'requested', $6, $7, $8, NOW() + INTERVAL '30 days', NOW(), NOW())
	`, clipHash, req.InternalName, req.GetArtifactInternalName(), req.TenantId, req.GetUserId(), reqID, storagePath, format)

	if err != nil {
		// Commodore registration succeeded (clip_hash provided) but Foghorn insert failed
		// Accept eventual consistency - Commodore record remains for audit/billing
		// RetentionJob will eventually clean up orphan artifacts
		s.logger.WithFields(logging.Fields{
			"clip_hash":     clipHash,
			"internal_name": req.InternalName,
			"error":         err,
		}).Error("Failed to store clip artifact in database (Commodore record persists)")
		return nil, status.Error(codes.Internal, "failed to store artifact")
	}

	// Store node assignment in foghorn.artifact_nodes
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, base_url, cached_at)
		VALUES ($1, $2, $3, $4, NOW())
	`, clipHash, storageNodeID, storagePath, storageHost)

	if err != nil {
		s.logger.WithError(err).Error("Failed to store artifact node assignment")
		// Don't fail the request, the artifact was created
	}

	// Send gRPC message to storage Helmsman
	clipReq := &pb.ClipPullRequest{
		ClipHash:      clipHash,
		StreamName:    req.InternalName,
		Format:        format,
		OutputName:    clipHash,
		SourceBaseUrl: control.DeriveMistHTTPBase(ingestHost),
		RequestId:     reqID,
	}
	if req.StartUnix != nil {
		clipReq.StartUnix = req.StartUnix
	}
	if req.StopUnix != nil {
		clipReq.StopUnix = req.StopUnix
	}
	if req.StartMs != nil {
		clipReq.StartMs = req.StartMs
	}
	if req.StopMs != nil {
		clipReq.StopMs = req.StopMs
	}
	if req.DurationSec != nil {
		clipReq.DurationSec = req.DurationSec
	}

	if err := control.SendClipPull(storageNodeID, clipReq); err != nil {
		// Mark artifact as failed since we couldn't send to Helmsman
		_, _ = s.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW()
			WHERE artifact_hash = $2
		`, fmt.Sprintf("storage node unavailable: %v", err), clipHash)

		// Emit FAILED event to Decklog
		if s.decklogClient != nil {
			failedData := buildClipLifecycleData(pb.ClipLifecycleData_STAGE_FAILED, req, reqID, clipHash)
			failedData.Error = func() *string { e := fmt.Sprintf("storage node unavailable: %v", err); return &e }()
			go func() {
				if err := s.decklogClient.SendClipLifecycle(failedData); err != nil {
					s.logger.WithError(err).Error("Failed to emit clip failed event")
				}
			}()
		}

		s.logger.WithFields(logging.Fields{
			"clip_hash": clipHash,
			"node_id":   storageNodeID,
			"error":     err,
		}).Error("Failed to send clip request to storage node")
		return nil, status.Errorf(codes.Unavailable, "storage node unavailable: %v", err)
	}

	// Emit STAGE_QUEUED event to Decklog (with enriched timing fields)
	if s.decklogClient != nil {
		clipData := buildClipLifecycleData(pb.ClipLifecycleData_STAGE_QUEUED, req, reqID, clipHash)
		clipData.CompletedAt = func() *int64 { t := time.Now().Unix(); return &t }()
		go func() { _ = s.decklogClient.SendClipLifecycle(clipData) }()
	}

	// Update stream state
	state.DefaultManager().UpdateStreamInstanceInfo(req.InternalName, storageNodeID, map[string]interface{}{
		"clip_status":     "requested",
		"clip_request_id": reqID,
		"clip_format":     format,
	})

	return &pb.CreateClipResponse{
		Status:      "queued",
		IngestHost:  ingestHost,
		StorageHost: storageHost,
		NodeId:      storageNodeID,
		RequestId:   reqID,
		ClipHash:    clipHash,
		PlaybackId:  req.GetPlaybackId(),
	}, nil
}

// DeleteClip deletes a clip
func (s *FoghornGRPCServer) DeleteClip(ctx context.Context, req *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error) {
	if req.ClipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Check current status from foghorn.artifacts
	var (
		currentStatus  string
		sizeBytes      sql.NullInt64
		retentionUntil sql.NullTime
		internalName   sql.NullString
		denormTenantID sql.NullString
		denormUserID   sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT status, size_bytes, retention_until, internal_name, tenant_id, user_id
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'clip'
	`, req.ClipHash).Scan(&currentStatus, &sizeBytes, &retentionUntil, &internalName, &denormTenantID, &denormUserID)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		return nil, status.Error(codes.Internal, "failed to check clip existence")
	}

	if currentStatus == "deleted" {
		return &pb.DeleteClipResponse{
			Success: false,
			Message: "clip is already deleted",
		}, nil
	}

	// Get node_id from artifact_nodes
	var nodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.ClipHash).Scan(&nodeID)

	// Send delete request to Helmsman if we know the storage node
	if nodeID != "" {
		requestID := uuid.NewString()
		deleteReq := &pb.ClipDeleteRequest{
			ClipHash:  req.ClipHash,
			RequestId: requestID,
		}
		if err := control.SendClipDelete(nodeID, deleteReq); err != nil {
			// Log but don't fail - the soft delete still works, cleanup can happen later
			s.logger.WithFields(logging.Fields{
				"clip_hash": req.ClipHash,
				"node_id":   nodeID,
				"error":     err,
			}).Warn("Failed to send clip delete to storage node, will be cleaned up later")
		} else {
			s.logger.WithFields(logging.Fields{
				"clip_hash":  req.ClipHash,
				"node_id":    nodeID,
				"request_id": requestID,
			}).Info("Sent clip delete request to storage node")
		}
	}

	// Soft delete in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'clip'
	`, req.ClipHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete clip")
		return nil, status.Error(codes.Internal, "failed to delete clip")
	}

	s.logger.WithField("clip_hash", req.ClipHash).Info("Clip soft-deleted successfully")

	// Emit deletion lifecycle immediately (do not wait for node cleanup)
	s.emitClipDeletedLifecycle(ctx, req.ClipHash, nodeID, sizeBytes, retentionUntil, internalName, denormTenantID, denormUserID)

	return &pb.DeleteClipResponse{
		Success: true,
		Message: "clip deleted successfully",
	}, nil
}

// =============================================================================
// DVR CONTROL SERVICE IMPLEMENTATION
// =============================================================================

// StartDVR initiates DVR recording for a stream
func (s *FoghornGRPCServer) StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error) {
	if req.InternalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Resolve retention policy (default 30 days) for cleanup jobs.
	retentionUntil := time.Now().Add(30 * 24 * time.Hour)
	if req.ExpiresAt != nil && *req.ExpiresAt > 0 {
		retentionUntil = time.Unix(*req.ExpiresAt, 0)
	}

	// Resolve actual source node for this stream
	sourceNodeID, baseURL, ok := control.GetStreamSource(req.InternalName)
	if !ok {
		return nil, status.Error(codes.Unavailable, "no source node available")
	}

	// Select storage node
	sctx := context.WithValue(ctx, "cap", "storage")
	storageHost, _, _, _, _, err := s.lb.GetBestNodeWithScore(sctx, "", 0, 0, map[string]int{}, "", false)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "no storage node available: %v", err)
	}

	storageNodeID := s.lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		return nil, status.Error(codes.Unavailable, "storage node not connected")
	}

	// Check for existing active DVR in foghorn.artifacts
	var existingHash string
	_ = s.db.QueryRowContext(ctx, `
		SELECT artifact_hash FROM foghorn.artifacts
		WHERE internal_name=$1 AND artifact_type='dvr' AND status IN ('requested','starting','recording')
		ORDER BY created_at DESC LIMIT 1
	`, req.InternalName).Scan(&existingHash)

	if existingHash != "" {
		playbackID := ""
		if control.CommodoreClient != nil {
			if resp, err := control.CommodoreClient.ResolveDVRHash(ctx, existingHash); err == nil && resp.Found {
				playbackID = resp.PlaybackId
			}
		}
		return &pb.StartDVRResponse{
			Status:        "already_started",
			DvrHash:       existingHash,
			IngestHost:    baseURL,
			StorageHost:   storageHost,
			StorageNodeId: storageNodeID,
			PlaybackId:    playbackID,
		}, nil
	}

	// Register DVR in Commodore business registry to get hash
	var dvrHash string
	var artifactInternalName string
	var playbackID string
	var streamID string
	if control.CommodoreClient != nil {
		regReq := &pb.RegisterDVRRequest{
			TenantId:     req.TenantId,
			UserId:       req.GetUserId(),
			StreamId:     req.GetStreamId(),
			InternalName: req.InternalName,
		}
		// Pass retention from request if provided
		if req.GetExpiresAt() > 0 {
			regReq.RetentionUntil = timestamppb.New(time.Unix(req.GetExpiresAt(), 0))
		}
		regResp, err := control.CommodoreClient.RegisterDVR(ctx, regReq)
		if err != nil {
			s.logger.WithError(err).Error("Failed to register DVR with Commodore")
			return nil, status.Errorf(codes.Internal, "failed to register DVR: %v", err)
		}
		dvrHash = regResp.DvrHash
		artifactInternalName = regResp.GetArtifactInternalName()
		playbackID = regResp.GetPlaybackId()
		streamID = regResp.GetStreamId()
	} else {
		return nil, status.Error(codes.Unavailable, "Commodore not available")
	}

	// Generate request_id for tracing (distinct from artifact hash)
	requestID := uuid.New().String()

	// Store artifact lifecycle state in foghorn.artifacts
	// NOTE: Business registry (tenant, user, retention) is stored in commodore.dvr_recordings
	// tenant_id is denormalized here for fallback when Commodore is unavailable
	// retention_until defaults to 30 days (system default, not user-configured yet)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (
			artifact_hash, artifact_type, internal_name, artifact_internal_name,
			stream_id, tenant_id, user_id,
			status, request_id, format,
			retention_until, created_at, updated_at
		)
		VALUES ($1, 'dvr', $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid,
		        'requested', $7, 'm3u8', $8, NOW(), NOW())
	`, dvrHash, req.InternalName, artifactInternalName, streamID, req.TenantId, req.GetUserId(), requestID, retentionUntil)

	if err != nil {
		// Commodore registration succeeded but Foghorn insert failed
		// Accept eventual consistency - Commodore record remains for audit/billing
		// RetentionJob will eventually clean up orphan artifacts
		s.logger.WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"internal_name": req.InternalName,
			"error":         err,
		}).Error("Failed to store DVR artifact in database (Commodore record persists)")
		return nil, status.Error(codes.Internal, "failed to store DVR artifact")
	}

	// Store node assignment in foghorn.artifact_nodes
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, base_url, cached_at)
		VALUES ($1, $2, $3, NOW())
	`, dvrHash, storageNodeID, storageHost)

	if err != nil {
		s.logger.WithError(err).Error("Failed to store DVR artifact node assignment")
		// Don't fail the request, the artifact was created
	}

	// Default DVR configuration
	config := &pb.DVRConfig{
		Enabled:         true,
		RetentionDays:   30,
		Format:          "ts",
		SegmentDuration: 6,
	}

	// Build DTSC full URL
	fullDTSC := control.BuildDTSCURI(sourceNodeID, req.InternalName, true, s.logger)
	if fullDTSC == "" {
		return nil, status.Error(codes.Unavailable, "DTSC output not available on source node")
	}

	// Send gRPC control message to storage Helmsman
	dvrReq := &pb.DVRStartRequest{
		DvrHash:       dvrHash,
		InternalName:  req.InternalName,
		SourceBaseUrl: fullDTSC,
		RequestId:     dvrHash,
		Config:        config,
		StreamId:      streamID,
	}

	if err := control.SendDVRStart(storageNodeID, dvrReq); err != nil {
		// Mark artifact as failed since we couldn't send to Helmsman
		_, _ = s.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW()
			WHERE artifact_hash = $2
		`, fmt.Sprintf("storage node unavailable: %v", err), dvrHash)

		// Emit FAILED event to Decklog
		if s.decklogClient != nil {
			failedData := &pb.DVRLifecycleData{
				Status:  pb.DVRLifecycleData_STATUS_FAILED,
				DvrHash: dvrHash,
				Error:   func() *string { e := fmt.Sprintf("storage node unavailable: %v", err); return &e }(),
				StreamId: func() *string {
					if req.StreamId != nil && *req.StreamId != "" {
						return req.StreamId
					}
					return nil
				}(),
				TenantId: func() *string {
					if req.TenantId != "" {
						return &req.TenantId
					}
					return nil
				}(),
				InternalName: func() *string {
					if req.InternalName != "" {
						return &req.InternalName
					}
					return nil
				}(),
				UserId: func() *string {
					if req.UserId != nil && *req.UserId != "" {
						return req.UserId
					}
					return nil
				}(),
			}
			go func() {
				if err := s.decklogClient.SendDVRLifecycle(failedData); err != nil {
					s.logger.WithError(err).Error("Failed to emit DVR failed event")
				}
			}()
		}

		s.logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  storageNodeID,
			"error":    err,
		}).Error("Failed to send DVR start request to storage node")
		return nil, status.Error(codes.Internal, "failed to start DVR on storage node")
	}

	// Emit DVR STATUS_STARTED event to Decklog
	if s.decklogClient != nil {
		dvrData := &pb.DVRLifecycleData{
			Status:    pb.DVRLifecycleData_STATUS_STARTED,
			DvrHash:   dvrHash,
			StartedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
			StreamId: func() *string {
				if req.StreamId != nil && *req.StreamId != "" {
					return req.StreamId
				}
				return nil
			}(),
			TenantId: func() *string {
				if req.TenantId != "" {
					return &req.TenantId
				}
				return nil
			}(),
			InternalName: func() *string {
				if req.InternalName != "" {
					return &req.InternalName
				}
				return nil
			}(),
			UserId: func() *string {
				if req.UserId != nil && *req.UserId != "" {
					return req.UserId
				}
				return nil
			}(),
		}
		if req.ExpiresAt != nil {
			dvrData.ExpiresAt = req.ExpiresAt
		}
		go func() { _ = s.decklogClient.SendDVRLifecycle(dvrData) }()
	}

	// Update stream state
	state.DefaultManager().UpdateStreamInstanceInfo(req.InternalName, storageNodeID, map[string]interface{}{
		"dvr_status": "requested",
		"dvr_hash":   dvrHash,
	})

	return &pb.StartDVRResponse{
		Status:        "started",
		DvrHash:       dvrHash,
		IngestHost:    baseURL,
		StorageHost:   storageHost,
		StorageNodeId: storageNodeID,
		PlaybackId:    playbackID,
	}, nil
}

// StopDVR stops an active DVR recording
func (s *FoghornGRPCServer) StopDVR(ctx context.Context, req *pb.StopDVRRequest) (*pb.StopDVRResponse, error) {
	if req.DvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Get DVR artifact info
	var (
		dvrStatus      string
		internalName   string
		sizeBytes      sql.NullInt64
		retentionUntil sql.NullTime
		startedAt      sql.NullTime
		endedAt        sql.NullTime
		denormTenantID sql.NullString
		denormUserID   sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(internal_name, ''), size_bytes, retention_until, started_at, ended_at, tenant_id, user_id
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, req.DvrHash).Scan(&dvrStatus, &internalName, &sizeBytes, &retentionUntil, &startedAt, &endedAt, &denormTenantID, &denormUserID)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR artifact")
		return nil, status.Error(codes.Internal, "failed to fetch DVR artifact")
	}

	if dvrStatus == "completed" || dvrStatus == "failed" || dvrStatus == "ready" {
		return &pb.StopDVRResponse{
			Success: false,
			Message: fmt.Sprintf("DVR recording already finished with status: %s", dvrStatus),
		}, nil
	}

	// Get node_id from artifact_nodes
	var nodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.DvrHash).Scan(&nodeID)

	if nodeID == "" {
		return nil, status.Error(codes.Unavailable, "no storage node available for this DVR")
	}

	// Send stop command to storage Helmsman
	stopReq := &pb.DVRStopRequest{
		DvrHash:   req.DvrHash,
		RequestId: req.DvrHash,
	}

	if err := control.SendDVRStop(nodeID, stopReq); err != nil {
		return nil, status.Errorf(codes.Unavailable, "storage node unavailable: %v", err)
	}

	// Update status in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'stopping', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, req.DvrHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to update DVR status to stopping")
	}

	// Emit DVR STATUS_STOPPED event to Decklog
	if s.decklogClient != nil {
		dvrData := &pb.DVRLifecycleData{
			Status:  pb.DVRLifecycleData_STATUS_STOPPED,
			DvrHash: req.DvrHash,
			EndedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
			StreamId: func() *string {
				if req.StreamId != nil && *req.StreamId != "" {
					return req.StreamId
				}
				return nil
			}(),
			TenantId: func() *string {
				if req.TenantId != "" {
					return &req.TenantId
				}
				return nil
			}(),
			InternalName: func() *string {
				if internalName != "" {
					return &internalName
				}
				return nil
			}(),
			UserId: func() *string {
				if denormUserID.Valid && denormUserID.String != "" {
					return &denormUserID.String
				}
				return nil
			}(),
		}
		go func() { _ = s.decklogClient.SendDVRLifecycle(dvrData) }()
	}

	return &pb.StopDVRResponse{
		Success: true,
		Message: "DVR recording stopping",
	}, nil
}

// DeleteDVR deletes a DVR recording and its files
func (s *FoghornGRPCServer) DeleteDVR(ctx context.Context, req *pb.DeleteDVRRequest) (*pb.DeleteDVRResponse, error) {
	if req.DvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Get DVR artifact info
	var (
		dvrStatus      string
		internalName   string
		sizeBytes      sql.NullInt64
		retentionUntil sql.NullTime
		startedAt      sql.NullTime
		endedAt        sql.NullTime
		denormTenantID sql.NullString
		denormUserID   sql.NullString
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(internal_name, ''), size_bytes, retention_until, started_at, ended_at, tenant_id, user_id
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, req.DvrHash).Scan(&dvrStatus, &internalName, &sizeBytes, &retentionUntil, &startedAt, &endedAt, &denormTenantID, &denormUserID)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR artifact")
		return nil, status.Error(codes.Internal, "failed to fetch DVR artifact")
	}

	if dvrStatus == "deleted" {
		return &pb.DeleteDVRResponse{
			Success: false,
			Message: "DVR recording is already deleted",
		}, nil
	}

	// Get node_id from artifact_nodes
	var nodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.DvrHash).Scan(&nodeID)

	// If still recording, stop it first
	if dvrStatus == "recording" || dvrStatus == "requested" || dvrStatus == "starting" {
		if nodeID != "" {
			stopReq := &pb.DVRStopRequest{
				DvrHash:   req.DvrHash,
				RequestId: req.DvrHash,
			}
			if err := control.SendDVRStop(nodeID, stopReq); err != nil {
				s.logger.WithFields(logging.Fields{
					"dvr_hash": req.DvrHash,
					"node_id":  nodeID,
					"error":    err,
				}).Warn("Failed to send DVR stop before delete")
			}
		}
	}

	// Send delete request to Helmsman if we know the storage node
	if nodeID != "" {
		requestID := uuid.NewString()
		deleteReq := &pb.DVRDeleteRequest{
			DvrHash:   req.DvrHash,
			RequestId: requestID,
		}
		if err := control.SendDVRDelete(nodeID, deleteReq); err != nil {
			// Log but don't fail - the soft delete still works, cleanup can happen later
			s.logger.WithFields(logging.Fields{
				"dvr_hash": req.DvrHash,
				"node_id":  nodeID,
				"error":    err,
			}).Warn("Failed to send DVR delete to storage node, will be cleaned up later")
		} else {
			s.logger.WithFields(logging.Fields{
				"dvr_hash":   req.DvrHash,
				"node_id":    nodeID,
				"request_id": requestID,
			}).Info("Sent DVR delete request to storage node")
		}
	}

	// Soft delete in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, req.DvrHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete DVR recording")
		return nil, status.Error(codes.Internal, "failed to delete DVR recording")
	}

	s.logger.WithField("dvr_hash", req.DvrHash).Info("DVR recording soft-deleted successfully")

	// Emit deletion lifecycle immediately (do not wait for node cleanup)
	s.emitDVRDeletedLifecycle(ctx, req.DvrHash, nodeID, sizeBytes, retentionUntil, startedAt, endedAt, internalName, denormTenantID, denormUserID)

	return &pb.DeleteDVRResponse{
		Success: true,
		Message: "DVR recording deleted successfully",
	}, nil
}

// =============================================================================
// VIEWER CONTROL SERVICE IMPLEMENTATION
// =============================================================================

// ResolveViewerEndpoint resolves the best endpoint(s) for a viewer
func (s *FoghornGRPCServer) ResolveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	if req.ContentId == "" {
		return nil, status.Error(codes.InvalidArgument, "content_id is required")
	}

	// Always resolve content type from the public ID (do not trust caller-provided type)
	resolution, err := control.ResolveContent(ctx, req.ContentId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to resolve content: %v", err)
	}
	resolvedType := resolution.ContentType
	s.logger.WithFields(logging.Fields{
		"content_id":   req.ContentId,
		"content_type": resolvedType,
	}).Info("Resolved content type from ID")

	resourcePath := "viewer://" + req.ContentId
	x402Paid := x402PaidFromMetadata(ctx)
	paymentHeader := x402.GetPaymentHeaderFromContext(ctx)
	clientIP := req.GetViewerIp()

	if !x402Paid && paymentHeader != "" && s.purserClient != nil && resolution.TenantId != "" {
		paid, err := s.handleX402ViewerPayment(ctx, resolution.TenantId, resourcePath, paymentHeader, clientIP)
		if err != nil {
			return nil, err
		}
		x402Paid = paid
	}

	// Check billing status for the content owner
	if s.cacheInvalidator != nil && resolution.TenantId != "" {
		billing := s.cacheInvalidator.GetBillingStatus(ctx, resolution.InternalName, resolution.TenantId)
		if billing != nil {
			// Hard block: tenant suspended (balance < -$10)
			if billing.IsSuspended && !x402Paid {
				s.logger.WithFields(logging.Fields{
					"content_id": req.ContentId,
					"tenant_id":  resolution.TenantId,
				}).Warn("Rejecting viewer: content owner suspended")
				return nil, s.paymentRequiredError(ctx, resolution.TenantId, resourcePath, "payment required - owner account suspended")
			}
			// Soft block: balance negative for prepaid (return 402-equivalent)
			if billing.BillingModel == "prepaid" && billing.IsBalanceNegative && !x402Paid {
				s.logger.WithFields(logging.Fields{
					"content_id": req.ContentId,
					"tenant_id":  resolution.TenantId,
				}).Warn("Rejecting viewer: content owner balance exhausted (402)")
				return nil, s.paymentRequiredError(ctx, resolution.TenantId, resourcePath, "payment required - content owner needs to top up balance")
			}
		}
	}

	// GeoIP resolution
	var lat, lon float64 = 0.0, 0.0
	viewerIP := req.GetViewerIp()

	if viewerIP != "" && s.geoipReader != nil {
		if geoData := s.geoipReader.Lookup(viewerIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
		}
	}

	var response *pb.ViewerEndpointResponse

	switch resolvedType {
	case "live":
		response, err = s.resolveLiveViewerEndpoint(ctx, req, lat, lon)
	case "dvr", "clip", "vod":
		response, err = s.resolveArtifactViewerEndpoint(ctx, req)
	default:
		return nil, status.Error(codes.InvalidArgument, "content_type must resolve to 'live', 'dvr', 'clip', or 'vod'")
	}

	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"content_type": resolvedType,
			"content_id":   req.ContentId,
		}).Error("Failed to resolve viewer endpoint")
		return nil, err
	}

	// Create virtual viewer for live streams (consistent with HTTP handlers)
	if resolvedType == "live" && response.Primary != nil && response.Primary.NodeId != "" {
		internalName := resolution.InternalName
		if internalName == "" {
			internalName = req.ContentId
		}
		state.DefaultManager().CreateVirtualViewer(response.Primary.NodeId, internalName, clientIP)
	}

	// Enrich live metadata from unified state
	if resolvedType == "live" && response.Metadata != nil {
		st := state.DefaultManager().GetStreamState(req.ContentId)
		if st != nil {
			response.Metadata.IsLive = st.Status == "live"
			response.Metadata.Status = st.Status
			response.Metadata.Viewers = int32(st.Viewers)
			response.Metadata.BufferState = st.BufferState
		}
	}

	return response, nil
}

func (s *FoghornGRPCServer) resolveLiveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest, lat, lon float64) (*pb.ViewerEndpointResponse, error) {
	start := time.Now()
	// Delegate to consolidated control package function
	deps := &control.PlaybackDependencies{
		DB:     s.db,
		LB:     s.lb,
		GeoLat: lat,
		GeoLon: lon,
	}

	// Resolve view key to internal name
	viewKey := req.ContentId
	target, err := control.ResolveStream(ctx, viewKey)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to resolve stream: %v", err)
	}
	if target.InternalName == "" {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	response, err := control.ResolveLivePlayback(ctx, deps, viewKey, target.InternalName, target.StreamID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	// Emit routing event for analytics
	if response.Primary != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		candidatesCount := int32(0)
		if response.Primary != nil {
			candidatesCount = int32(1 + len(response.Fallbacks))
		}
		s.emitRoutingEvent(response.Primary, lat, lon, 0, 0, target.InternalName, target.TenantID, target.StreamID, durationMs, candidatesCount, "grpc_resolve", "grpc")
	}

	return response, nil
}

func (s *FoghornGRPCServer) resolveArtifactViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	start := time.Now()
	deps := &control.PlaybackDependencies{
		DB: s.db,
		LB: s.lb,
	}

	response, err := control.ResolveArtifactPlayback(ctx, deps, req.ContentId)
	if err != nil {
		var defrostErr *control.DefrostingError
		if errors.As(err, &defrostErr) {
			retryAfter := defrostErr.RetryAfterSeconds
			if retryAfter <= 0 {
				retryAfter = 10
			}
			_ = grpc.SetTrailer(ctx, metadata.Pairs("retry-after", strconv.Itoa(retryAfter)))
			return nil, status.Error(codes.Unavailable, defrostErr.Error())
		}
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		if strings.Contains(err.Error(), "not available") || strings.Contains(err.Error(), "unknown") {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
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
		s.emitRoutingEvent(response.Primary, 0, 0, 0, 0, internalName, response.Metadata.GetTenantId(), response.Metadata.GetStreamId(), durationMs, candidatesCount, "grpc_resolve", "grpc")
	}

	return response, nil
}

func x402PaidFromMetadata(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || md == nil {
		return false
	}
	values := md.Get("x402-paid")
	if len(values) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func (s *FoghornGRPCServer) handleX402ViewerPayment(ctx context.Context, tenantID, resourcePath, paymentHeader, clientIP string) (bool, error) {
	if tenantID == "" || paymentHeader == "" || s.purserClient == nil {
		return false, nil
	}

	result, err := x402.SettleX402Payment(ctx, x402.SettlementOptions{
		PaymentHeader: paymentHeader,
		Resource:      resourcePath,
		AuthTenantID:  "",
		ClientIP:      clientIP,
		Purser:        s.purserClient,
		Commodore:     nil,
		Logger:        s.logger,
		Resolution: &x402.ResourceResolution{
			Resource: resourcePath,
			Kind:     x402.ResourceKindViewer,
			TenantID: tenantID,
			Resolved: true,
		},
	})

	if err != nil {
		return false, s.mapSettlementErrorToGRPC(ctx, tenantID, resourcePath, err)
	}

	if result == nil || result.Settle == nil || !result.Settle.Success {
		return false, s.paymentFailedError(ctx, tenantID, resourcePath, "payment settlement failed")
	}

	return true, nil
}

func (s *FoghornGRPCServer) mapSettlementErrorToGRPC(ctx context.Context, tenantID, resourcePath string, err *x402.SettlementError) error {
	switch err.Code {
	case x402.ErrInvalidPayment:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	case x402.ErrBillingDetailsRequired:
		return s.billingDetailsRequiredError(err.Message)
	case x402.ErrAuthOnly:
		return s.paymentRequiredError(ctx, tenantID, resourcePath, "payment required - balance exhausted")
	case x402.ErrVerificationFailed:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	case x402.ErrSettlementFailed:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	default:
		return s.paymentFailedError(ctx, tenantID, resourcePath, err.Message)
	}
}

func (s *FoghornGRPCServer) billingDetailsRequiredError(message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "billing details required"
	}
	return status.Error(codes.FailedPrecondition, msg)
}

func (s *FoghornGRPCServer) paymentRequiredError(ctx context.Context, tenantID, resourcePath, message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "payment required"
	}
	st := status.New(codes.FailedPrecondition, msg)
	if s.purserClient != nil {
		reqs, err := s.purserClient.GetPaymentRequirements(ctx, tenantID, resourcePath)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to get x402 payment requirements")
		} else if reqs != nil {
			if stWith, err := st.WithDetails(reqs); err == nil {
				st = stWith
			}
		}
	}
	return st.Err()
}

func (s *FoghornGRPCServer) paymentFailedError(ctx context.Context, tenantID, resourcePath, message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "payment failed"
	}
	st := status.New(codes.FailedPrecondition, msg)
	if s.purserClient != nil {
		reqs, err := s.purserClient.GetPaymentRequirements(ctx, tenantID, resourcePath)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to get x402 payment requirements")
		} else if reqs != nil {
			if stWith, err := st.WithDetails(reqs); err == nil {
				st = stWith
			}
		}
	}
	return st.Err()
}

// VOD CONTROL SERVICE IMPLEMENTATION
// =============================================================================

// generateVodHash creates a unique hash for a VOD upload
func generateVodHash(tenantID, filename string, timestamp time.Time) string {
	data := fmt.Sprintf("%s:%s:%d", tenantID, filename, timestamp.UnixNano())
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])[:32] // 32 char hash like clips
}

// CreateVodUpload initiates a multipart upload and returns presigned URLs
func (s *FoghornGRPCServer) CreateVodUpload(ctx context.Context, req *pb.CreateVodUploadRequest) (*pb.CreateVodUploadResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.Filename == "" {
		return nil, status.Error(codes.InvalidArgument, "filename is required")
	}
	if req.SizeBytes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "size_bytes must be positive")
	}
	if req.GetArtifactInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_internal_name is required")
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}

	// Use hash from Commodore if provided, otherwise generate
	// Commodore is authoritative for hash generation in production flows
	artifactHash := req.GetVodHash()
	if artifactHash == "" {
		artifactHash = generateVodHash(req.TenantId, req.Filename, time.Now())
	}

	// Calculate part size and count
	partSize, partCount := storage.CalculatePartSize(req.SizeBytes)

	// Build S3 key
	s3Key := s.s3Client.BuildVodS3Key(req.TenantId, artifactHash, req.Filename)

	// Determine content type
	contentType := req.GetContentType()
	if contentType == "" {
		contentType = "video/mp4" // default
	}

	// Create S3 multipart upload
	uploadID, err := s.s3Client.CreateMultipartUpload(ctx, s3Key, contentType)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create S3 multipart upload")
		return nil, status.Errorf(codes.Internal, "failed to create upload: %v", err)
	}

	// Generate presigned URLs for all parts (2 hour expiry)
	parts, err := s.s3Client.GeneratePresignedUploadParts(s3Key, uploadID, partCount, 2*time.Hour)
	if err != nil {
		// Abort the multipart upload since we can't generate URLs
		_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		s.logger.WithError(err).Error("Failed to generate presigned URLs")
		return nil, status.Errorf(codes.Internal, "failed to generate upload URLs: %v", err)
	}

	// Generate artifact ID (UUID)
	artifactID := uuid.New().String()

	// Extract format from filename extension (e.g., "video.mp4" → "mp4")
	vodFormat := strings.TrimPrefix(filepath.Ext(req.Filename), ".")
	if vodFormat == "" {
		// Abort the upload - we need a file extension to determine format
		_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		return nil, status.Errorf(codes.InvalidArgument, "filename must have an extension to determine format")
	}

	// Store artifact in foghorn.artifacts with status='uploading'
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (
			id, artifact_hash, artifact_type, artifact_internal_name,
			tenant_id, user_id, status,
			sync_status, size_bytes, s3_url, format, retention_until, created_at, updated_at
		)
		VALUES ($1, $2, 'vod', $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, 'uploading',
		        'in_progress', $6, $7, $8, NOW() + INTERVAL '30 days', NOW(), NOW())
	`, artifactID, artifactHash, req.GetArtifactInternalName(), req.TenantId, req.UserId, req.SizeBytes, s.s3Client.BuildS3URL(s3Key), vodFormat)

	if err != nil {
		// Abort S3 upload since we can't track it
		_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		s.logger.WithError(err).Error("Failed to store VOD artifact")
		return nil, status.Errorf(codes.Internal, "failed to store artifact: %v", err)
	}

	// Store VOD metadata
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.vod_metadata (
			artifact_hash, filename, title, description, content_type,
			s3_upload_id, s3_key, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
	`, artifactHash, req.Filename, req.GetTitle(), req.GetDescription(), contentType, uploadID, s3Key)

	if err != nil {
		// Log but don't fail - primary artifact is created
		s.logger.WithError(err).Error("Failed to store VOD metadata")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     uploadID,
		"tenant_id":     req.TenantId,
		"filename":      req.Filename,
		"size_bytes":    req.SizeBytes,
		"part_count":    partCount,
		"part_size":     partSize,
	}).Info("Created VOD multipart upload")

	// Emit VOD lifecycle event to Decklog (STATUS_REQUESTED)
	if s.decklogClient != nil {
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_REQUESTED,
			VodHash:     artifactHash,
			UploadId:    &uploadID,
			Filename:    &req.Filename,
			ContentType: &contentType,
			SizeBytes:   proto.Uint64(uint64(req.SizeBytes)),
			TenantId:    &req.TenantId,
			StartedAt:   proto.Int64(time.Now().Unix()),
		}
		if req.UserId != "" {
			vodData.UserId = &req.UserId
		}
		go func() { _ = s.decklogClient.SendVodLifecycle(vodData) }()
	}

	// Convert storage.UploadPart to proto
	protoParts := make([]*pb.VodUploadPart, len(parts))
	for i, p := range parts {
		protoParts[i] = &pb.VodUploadPart{
			PartNumber:   int32(p.PartNumber),
			PresignedUrl: p.PresignedURL,
		}
	}

	return &pb.CreateVodUploadResponse{
		UploadId:     uploadID,
		ArtifactId:   artifactID,
		ArtifactHash: artifactHash,
		PartSize:     partSize,
		Parts:        protoParts,
		ExpiresAt:    timestamppb.New(time.Now().Add(2 * time.Hour)),
		PlaybackId:   req.GetPlaybackId(),
	}, nil
}

// CompleteVodUpload finalizes a multipart upload after all parts are uploaded
func (s *FoghornGRPCServer) CompleteVodUpload(ctx context.Context, req *pb.CompleteVodUploadRequest) (*pb.CompleteVodUploadResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.UploadId == "" {
		return nil, status.Error(codes.InvalidArgument, "upload_id is required")
	}
	if len(req.Parts) == 0 {
		return nil, status.Error(codes.InvalidArgument, "parts are required")
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}

	// Get artifact info by upload_id
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	var artifactHash, s3Key string
	var sizeBytes sql.NullInt64
	var userID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT v.artifact_hash, v.s3_key, a.size_bytes, a.user_id
		FROM foghorn.vod_metadata v
		JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
		WHERE v.s3_upload_id = $1 AND a.status = 'uploading'
	`, req.UploadId).Scan(&artifactHash, &s3Key, &sizeBytes, &userID)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "upload not found or already completed")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch upload info")
		return nil, status.Error(codes.Internal, "failed to fetch upload info")
	}

	// Convert proto parts to storage parts
	storageParts := make([]storage.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		storageParts[i] = storage.CompletedPart{
			PartNumber: int(p.PartNumber),
			ETag:       p.Etag,
		}
	}

	// Complete S3 multipart upload
	err = s.s3Client.CompleteMultipartUpload(ctx, s3Key, req.UploadId, storageParts)
	if err != nil {
		s.logger.WithError(err).Error("Failed to complete S3 multipart upload")
		// Update status to 'failed'
		_, _ = s.db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET status = 'failed',
			    sync_status = 'failed',
			    sync_error = $1,
			    error_message = $1,
			    last_sync_attempt = NOW(),
			    updated_at = NOW()
			WHERE artifact_hash = $2
		`, fmt.Sprintf("S3 upload failed: %v", err), artifactHash)
		// Emit VOD lifecycle event (STATUS_FAILED)
		if s.decklogClient != nil {
			errMsg := fmt.Sprintf("S3 upload failed: %v", err)
			vodData := &pb.VodLifecycleData{
				Status:      pb.VodLifecycleData_STATUS_FAILED,
				VodHash:     artifactHash,
				UploadId:    &req.UploadId,
				Error:       &errMsg,
				TenantId:    &req.TenantId,
				CompletedAt: proto.Int64(time.Now().Unix()),
			}
			if userID.Valid && userID.String != "" {
				vodData.UserId = &userID.String
			}
			if sizeBytes.Valid && sizeBytes.Int64 > 0 {
				vodData.SizeBytes = proto.Uint64(uint64(sizeBytes.Int64))
			}
			go func() { _ = s.decklogClient.SendVodLifecycle(vodData) }()
		}
		return nil, status.Errorf(codes.Internal, "failed to complete upload: %v", err)
	}

	// Update artifact status to 'ready' (no validation/transcoding for now)
	// TODO: When we add ffprobe validation, change this to 'processing' and trigger async validation
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'ready',
		    storage_location = 's3',
		    sync_status = 'synced',
		    sync_error = NULL,
		    last_sync_attempt = NOW(),
		    frozen_at = COALESCE(frozen_at, NOW()),
		    s3_url = COALESCE(s3_url, $2),
		    updated_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash, s.s3Client.BuildS3URL(s3Key))
	if err != nil {
		s.logger.WithError(err).Error("Failed to update artifact status")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     req.UploadId,
		"tenant_id":     req.TenantId,
		"parts":         len(req.Parts),
	}).Info("Completed VOD multipart upload")

	// Emit VOD lifecycle event (STATUS_COMPLETED)
	if s.decklogClient != nil {
		s3URL := s.s3Client.BuildS3URL(s3Key)
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_COMPLETED,
			VodHash:     artifactHash,
			UploadId:    &req.UploadId,
			S3Url:       &s3URL,
			TenantId:    &req.TenantId,
			CompletedAt: proto.Int64(time.Now().Unix()),
		}
		if userID.Valid && userID.String != "" {
			vodData.UserId = &userID.String
		}
		if sizeBytes.Valid && sizeBytes.Int64 > 0 {
			vodData.SizeBytes = proto.Uint64(uint64(sizeBytes.Int64))
		}
		go func() { _ = s.decklogClient.SendVodLifecycle(vodData) }()
	}

	// Fetch and return the asset
	asset, err := s.getVodAssetInfo(ctx, artifactHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch completed asset")
		// Return minimal response with READY status (no validation/transcoding yet)
		// FUTURE: When validation is added, this will return PROCESSING and the
		// Helmsman ffprobe handler will update to READY or FAILED after validation
		return &pb.CompleteVodUploadResponse{
			Asset: &pb.VodAssetInfo{
				ArtifactHash: artifactHash,
				Status:       pb.VodStatus_VOD_STATUS_READY,
			},
		}, nil
	}

	return &pb.CompleteVodUploadResponse{Asset: asset}, nil
}

// AbortVodUpload cancels an in-progress multipart upload
func (s *FoghornGRPCServer) AbortVodUpload(ctx context.Context, req *pb.AbortVodUploadRequest) (*pb.AbortVodUploadResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.UploadId == "" {
		return nil, status.Error(codes.InvalidArgument, "upload_id is required")
	}
	if s.s3Client == nil {
		return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
	}

	// Get artifact info by upload_id
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	var artifactHash, s3Key string
	var userID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT v.artifact_hash, v.s3_key, a.user_id
		FROM foghorn.vod_metadata v
		JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
		WHERE v.s3_upload_id = $1 AND a.status = 'uploading'
	`, req.UploadId).Scan(&artifactHash, &s3Key, &userID)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "upload not found or already completed")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch upload info")
		return nil, status.Error(codes.Internal, "failed to fetch upload info")
	}

	// Abort S3 multipart upload
	err = s.s3Client.AbortMultipartUpload(ctx, s3Key, req.UploadId)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to abort S3 multipart upload")
		// Continue to delete the database record anyway
	}

	// Delete artifact and metadata
	_, _ = s.db.ExecContext(ctx, `DELETE FROM foghorn.vod_metadata WHERE artifact_hash = $1`, artifactHash)
	_, err = s.db.ExecContext(ctx, `DELETE FROM foghorn.artifacts WHERE artifact_hash = $1`, artifactHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete aborted artifact")
		return nil, status.Error(codes.Internal, "failed to clean up aborted upload")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"upload_id":     req.UploadId,
		"tenant_id":     req.TenantId,
	}).Info("Aborted VOD multipart upload")

	// Emit VOD lifecycle event (STATUS_DELETED)
	if s.decklogClient != nil {
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_DELETED,
			VodHash:     artifactHash,
			UploadId:    &req.UploadId,
			TenantId:    &req.TenantId,
			CompletedAt: proto.Int64(time.Now().Unix()),
		}
		if userID.Valid && userID.String != "" {
			vodData.UserId = &userID.String
		}
		go func() { _ = s.decklogClient.SendVodLifecycle(vodData) }()
	}

	return &pb.AbortVodUploadResponse{
		Success: true,
		Message: "upload aborted successfully",
	}, nil
}

// GetVodAsset returns a single VOD asset by hash
func (s *FoghornGRPCServer) GetVodAsset(ctx context.Context, req *pb.GetVodAssetRequest) (*pb.VodAssetInfo, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.ArtifactHash == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_hash is required")
	}

	asset, err := s.getVodAssetInfo(ctx, req.ArtifactHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, status.Error(codes.NotFound, "VOD asset not found")
		}
		s.logger.WithError(err).Error("Failed to fetch VOD asset")
		return nil, status.Error(codes.Internal, "failed to fetch VOD asset")
	}

	return asset, nil
}

// ListVodAssets returns paginated list of VOD assets
// NOTE: Tenant-wide queries should go through Commodore.ListVodAssets (business registry owner)
// This Foghorn endpoint is for lifecycle data queries, matching clips pattern
func (s *FoghornGRPCServer) ListVodAssets(ctx context.Context, req *pb.ListVodAssetsRequest) (*pb.ListVodAssetsResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	// Tenant-wide VOD listing should go through Commodore.ListVodAssets
	// This endpoint returns lifecycle data for artifact-specific queries

	// Parse bidirectional keyset pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "a.created_at",
		IDColumn:        "a.artifact_hash",
	}

	// Build base WHERE clause - no tenant_id filter (matches clips pattern)
	baseWhere := "a.artifact_type = 'vod' AND a.status != 'deleted'"
	args := []interface{}{}
	argIdx := 1

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM foghorn.artifacts a WHERE %s", baseWhere)
	var total int32
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		s.logger.WithError(err).Error("Failed to count VOD assets")
		return nil, status.Error(codes.Internal, "failed to count VOD assets")
	}

	// Build select query with keyset pagination
	selectQuery := fmt.Sprintf(`
		SELECT a.id, a.artifact_hash, a.status, a.size_bytes,
		       COALESCE(a.storage_location, 'pending'), COALESCE(a.s3_url, ''),
		       a.error_message, a.created_at, a.updated_at, a.retention_until,
		       COALESCE(v.filename, ''), COALESCE(v.title, ''), COALESCE(v.description, ''),
		       v.duration_ms, v.resolution, v.video_codec, v.audio_codec, v.bitrate_kbps,
		       COALESCE(v.s3_upload_id, ''), COALESCE(v.s3_key, '')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE %s`, baseWhere)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		selectQuery += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	selectQuery += " " + builder.OrderBy(params)
	selectQuery += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	// Fetch assets
	rows, err := s.db.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch VOD assets")
		return nil, status.Error(codes.Internal, "failed to fetch VOD assets")
	}
	defer rows.Close()

	var assets []*pb.VodAssetInfo
	for rows.Next() {
		asset, err := s.scanVodAsset(rows)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan VOD asset")
			continue
		}
		assets = append(assets, asset)
	}

	// Detect hasMore and trim results
	hasMore := len(assets) > params.Limit
	if hasMore {
		assets = assets[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(assets) > 0 {
		for i, j := 0, len(assets)-1; i < j; i, j = i+1, j-1 {
			assets[i], assets[j] = assets[j], assets[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(assets) > 0 {
		first := assets[0]
		last := assets[len(assets)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.ArtifactHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.ArtifactHash)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListVodAssetsResponse{
		Assets: assets,
		Pagination: &pb.CursorPaginationResponse{
			TotalCount: total,
		},
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}
	if params.Direction == pagination.Forward {
		resp.Pagination.HasNextPage = hasMore
		resp.Pagination.HasPreviousPage = params.Cursor != nil
	} else {
		resp.Pagination.HasPreviousPage = hasMore
		resp.Pagination.HasNextPage = params.Cursor != nil
	}

	return resp, nil
}

// DeleteVodAsset deletes a VOD asset
func (s *FoghornGRPCServer) DeleteVodAsset(ctx context.Context, req *pb.DeleteVodAssetRequest) (*pb.DeleteVodAssetResponse, error) {
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	if req.ArtifactHash == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_hash is required")
	}

	// Check current status
	// NOTE: tenant_id validation happens at Commodore level (matches clips pattern)
	var (
		currentStatus  string
		s3Key          string
		sizeBytes      sql.NullInt64
		retentionUntil sql.NullTime
		userID         sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT a.status, COALESCE(v.s3_key, ''), a.size_bytes, a.retention_until, a.user_id
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = 'vod'
	`, req.ArtifactHash).Scan(&currentStatus, &s3Key, &sizeBytes, &retentionUntil, &userID)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "VOD asset not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to check VOD asset")
		return nil, status.Error(codes.Internal, "failed to check VOD asset")
	}

	if currentStatus == "deleted" {
		return &pb.DeleteVodAssetResponse{
			Success: false,
			Message: "VOD asset is already deleted",
		}, nil
	}

	// If uploading, abort the multipart upload first
	if currentStatus == "uploading" {
		var uploadID string
		_ = s.db.QueryRowContext(ctx, `
			SELECT s3_upload_id FROM foghorn.vod_metadata WHERE artifact_hash = $1
		`, req.ArtifactHash).Scan(&uploadID)

		if uploadID != "" && s3Key != "" && s.s3Client != nil {
			_ = s.s3Client.AbortMultipartUpload(ctx, s3Key, uploadID)
		}
	}

	// Send delete request to nodes that have this VOD cached
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
	`, req.ArtifactHash)
	if err == nil {
		defer rows.Close()
		requestID := uuid.NewString()
		for rows.Next() {
			var nodeID string
			if err := rows.Scan(&nodeID); err != nil {
				continue
			}
			deleteReq := &pb.VodDeleteRequest{
				VodHash:   req.ArtifactHash,
				RequestId: requestID,
			}
			if err := control.SendVodDelete(nodeID, deleteReq); err != nil {
				s.logger.WithFields(logging.Fields{
					"artifact_hash": req.ArtifactHash,
					"node_id":       nodeID,
					"error":         err,
				}).Warn("Failed to send VOD delete to storage node, will be cleaned up later")
			} else {
				s.logger.WithFields(logging.Fields{
					"artifact_hash": req.ArtifactHash,
					"node_id":       nodeID,
					"request_id":    requestID,
				}).Debug("Sent VOD delete request to storage node")
			}
		}
	}

	// Delete from S3 if we have a key and client
	if s3Key != "" && s.s3Client != nil && currentStatus != "uploading" {
		if err := s.s3Client.Delete(ctx, s3Key); err != nil {
			s.logger.WithFields(logging.Fields{
				"artifact_hash": req.ArtifactHash,
				"s3_key":        s3Key,
				"error":         err,
			}).Warn("Failed to delete from S3, will be cleaned up later")
		}
	}

	// Soft delete in foghorn.artifacts
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'vod'
	`, req.ArtifactHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete VOD asset")
		return nil, status.Error(codes.Internal, "failed to delete VOD asset")
	}

	s.logger.WithFields(logging.Fields{
		"artifact_hash": req.ArtifactHash,
		"tenant_id":     req.TenantId,
	}).Info("VOD asset soft-deleted successfully")

	// Emit VOD lifecycle event (STATUS_DELETED)
	if s.decklogClient != nil {
		vodData := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_DELETED,
			VodHash:     req.ArtifactHash,
			TenantId:    &req.TenantId,
			CompletedAt: proto.Int64(time.Now().Unix()),
		}
		if userID.Valid && userID.String != "" {
			vodData.UserId = &userID.String
		}
		if sizeBytes.Valid && sizeBytes.Int64 > 0 {
			sb := uint64(sizeBytes.Int64)
			vodData.SizeBytes = &sb
		}
		if retentionUntil.Valid {
			exp := retentionUntil.Time.Unix()
			vodData.ExpiresAt = &exp
		}
		go func() { _ = s.decklogClient.SendVodLifecycle(vodData) }()
	}

	return &pb.DeleteVodAssetResponse{
		Success: true,
		Message: "VOD asset deleted successfully",
	}, nil
}

// Helper functions for VOD service

func (s *FoghornGRPCServer) getVodAssetInfo(ctx context.Context, artifactHash string) (*pb.VodAssetInfo, error) {
	query := `
		SELECT a.id, a.artifact_hash, a.status, a.size_bytes,
		       COALESCE(a.storage_location, 'pending'), COALESCE(a.s3_url, ''),
		       a.error_message, a.created_at, a.updated_at, a.retention_until,
		       COALESCE(v.filename, ''), COALESCE(v.title, ''), COALESCE(v.description, ''),
		       v.duration_ms, v.resolution, v.video_codec, v.audio_codec, v.bitrate_kbps,
		       COALESCE(v.s3_upload_id, ''), COALESCE(v.s3_key, '')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = 'vod' AND a.status != 'deleted'
	`
	row := s.db.QueryRowContext(ctx, query, artifactHash)
	return s.scanVodAssetRow(row)
}

func (s *FoghornGRPCServer) scanVodAsset(rows *sql.Rows) (*pb.VodAssetInfo, error) {
	var id, artifactHash, statusStr, storageLocation, s3URL, filename, title, description string
	var videoCodec, audioCodec, resolution, s3UploadID, s3Key sql.NullString
	var sizeBytes sql.NullInt64
	var durationMs, bitrateKbps sql.NullInt32
	var errorMessage sql.NullString
	var createdAt, updatedAt time.Time
	var expiresAt sql.NullTime

	err := rows.Scan(
		&id, &artifactHash, &statusStr, &sizeBytes,
		&storageLocation, &s3URL, &errorMessage,
		&createdAt, &updatedAt, &expiresAt,
		&filename, &title, &description,
		&durationMs, &resolution, &videoCodec, &audioCodec, &bitrateKbps,
		&s3UploadID, &s3Key,
	)
	if err != nil {
		return nil, err
	}

	return buildVodAssetInfo(
		id, artifactHash, statusStr, storageLocation, filename, title, description,
		sizeBytes, durationMs, resolution, videoCodec, audioCodec, bitrateKbps,
		s3UploadID, s3Key, errorMessage, createdAt, updatedAt, expiresAt,
	), nil
}

func (s *FoghornGRPCServer) scanVodAssetRow(row *sql.Row) (*pb.VodAssetInfo, error) {
	var id, artifactHash, statusStr, storageLocation, s3URL, filename, title, description string
	var videoCodec, audioCodec, resolution, s3UploadID, s3Key sql.NullString
	var sizeBytes sql.NullInt64
	var durationMs, bitrateKbps sql.NullInt32
	var errorMessage sql.NullString
	var createdAt, updatedAt time.Time
	var expiresAt sql.NullTime

	err := row.Scan(
		&id, &artifactHash, &statusStr, &sizeBytes,
		&storageLocation, &s3URL, &errorMessage,
		&createdAt, &updatedAt, &expiresAt,
		&filename, &title, &description,
		&durationMs, &resolution, &videoCodec, &audioCodec, &bitrateKbps,
		&s3UploadID, &s3Key,
	)
	if err != nil {
		return nil, err
	}

	return buildVodAssetInfo(
		id, artifactHash, statusStr, storageLocation, filename, title, description,
		sizeBytes, durationMs, resolution, videoCodec, audioCodec, bitrateKbps,
		s3UploadID, s3Key, errorMessage, createdAt, updatedAt, expiresAt,
	), nil
}

func buildVodAssetInfo(
	id, artifactHash, statusStr, storageLocation, filename, title, description string,
	sizeBytes sql.NullInt64, durationMs sql.NullInt32, resolution, videoCodec, audioCodec sql.NullString,
	bitrateKbps sql.NullInt32, s3UploadID, s3Key, errorMessage sql.NullString,
	createdAt, updatedAt time.Time, expiresAt sql.NullTime,
) *pb.VodAssetInfo {
	// Map status string to proto enum
	var protoStatus pb.VodStatus
	switch statusStr {
	case "uploading":
		protoStatus = pb.VodStatus_VOD_STATUS_UPLOADING
	case "processing":
		protoStatus = pb.VodStatus_VOD_STATUS_PROCESSING
	case "ready":
		protoStatus = pb.VodStatus_VOD_STATUS_READY
	case "failed":
		protoStatus = pb.VodStatus_VOD_STATUS_FAILED
	case "deleted":
		protoStatus = pb.VodStatus_VOD_STATUS_DELETED
	default:
		protoStatus = pb.VodStatus_VOD_STATUS_UNSPECIFIED
	}

	asset := &pb.VodAssetInfo{
		Id:              id,
		ArtifactHash:    artifactHash,
		Title:           title,
		Description:     description,
		Filename:        filename,
		Status:          protoStatus,
		StorageLocation: storageLocation,
		CreatedAt:       timestamppb.New(createdAt),
		UpdatedAt:       timestamppb.New(updatedAt),
	}

	if sizeBytes.Valid {
		asset.SizeBytes = &sizeBytes.Int64
	}
	if durationMs.Valid {
		asset.DurationMs = &durationMs.Int32
	}
	if resolution.Valid {
		asset.Resolution = &resolution.String
	}
	if videoCodec.Valid {
		asset.VideoCodec = &videoCodec.String
	}
	if audioCodec.Valid {
		asset.AudioCodec = &audioCodec.String
	}
	if bitrateKbps.Valid {
		asset.BitrateKbps = &bitrateKbps.Int32
	}
	if s3UploadID.Valid {
		asset.S3UploadId = &s3UploadID.String
	}
	if s3Key.Valid {
		asset.S3Key = &s3Key.String
	}
	if errorMessage.Valid {
		asset.ErrorMessage = &errorMessage.String
	}
	if expiresAt.Valid {
		asset.ExpiresAt = timestamppb.New(expiresAt.Time)
	}

	return asset
}

func (s *FoghornGRPCServer) emitClipDeletedLifecycle(
	ctx context.Context,
	clipHash string,
	nodeID string,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
	internalName sql.NullString,
	denormTenantID sql.NullString,
	denormUserID sql.NullString,
) {
	if s.decklogClient == nil {
		return
	}

	var (
		tenantIDStr     string
		userIDStr       string
		internalNameStr string
		streamID        string
		clipMode        *string
		startUnix       *int64
		stopUnix        *int64
		startMs         *int64
		stopMs          *int64
		durationSec     *int64
	)

	if denormTenantID.Valid {
		tenantIDStr = denormTenantID.String
	}
	if denormUserID.Valid {
		userIDStr = denormUserID.String
	}
	if internalName.Valid {
		internalNameStr = internalName.String
	}

	if control.CommodoreClient != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := control.CommodoreClient.ResolveClipHash(cctx, clipHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantIDStr = resp.TenantId
			}
			if resp.UserId != "" {
				userIDStr = resp.UserId
			}
			if resp.InternalName != "" {
				internalNameStr = resp.InternalName
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
			if resp.ClipMode != "" {
				m := resp.ClipMode
				clipMode = &m
			}
			if resp.StartTime > 0 && resp.Duration > 0 {
				sMs := resp.StartTime
				eMs := resp.StartTime + resp.Duration
				sU := sMs / 1000
				eU := eMs / 1000
				dS := resp.Duration / 1000
				startMs, stopMs = &sMs, &eMs
				startUnix, stopUnix = &sU, &eU
				durationSec = &dS
			}
		}
	}

	clipData := &pb.ClipLifecycleData{
		Stage:    pb.ClipLifecycleData_STAGE_DELETED,
		ClipHash: clipHash,
	}
	if nodeID != "" {
		clipData.NodeId = &nodeID
	}
	if tenantIDStr != "" {
		clipData.TenantId = &tenantIDStr
	}
	if internalNameStr != "" {
		clipData.InternalName = &internalNameStr
	}
	if streamID != "" {
		clipData.StreamId = &streamID
	}
	if userIDStr != "" {
		clipData.UserId = &userIDStr
	}
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		clipData.SizeBytes = &sb
	}
	if retentionUntil.Valid {
		exp := retentionUntil.Time.Unix()
		clipData.ExpiresAt = &exp
	}
	clipData.ClipMode = clipMode
	clipData.StartUnix = startUnix
	clipData.StopUnix = stopUnix
	clipData.StartMs = startMs
	clipData.StopMs = stopMs
	clipData.DurationSec = durationSec

	go func() { _ = s.decklogClient.SendClipLifecycle(clipData) }()
}

func (s *FoghornGRPCServer) emitDVRDeletedLifecycle(
	ctx context.Context,
	dvrHash string,
	nodeID string,
	sizeBytes sql.NullInt64,
	retentionUntil sql.NullTime,
	startedAt sql.NullTime,
	endedAt sql.NullTime,
	internalName string,
	denormTenantID sql.NullString,
	denormUserID sql.NullString,
) {
	if s.decklogClient == nil {
		return
	}

	var (
		tenantIDStr     string
		userIDStr       string
		internalNameStr string
		streamID        string
	)

	if denormTenantID.Valid {
		tenantIDStr = denormTenantID.String
	}
	if denormUserID.Valid {
		userIDStr = denormUserID.String
	}
	if internalName != "" {
		internalNameStr = internalName
	}

	if control.CommodoreClient != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if resp, err := control.CommodoreClient.ResolveDVRHash(cctx, dvrHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantIDStr = resp.TenantId
			}
			if resp.UserId != "" {
				userIDStr = resp.UserId
			}
			if resp.InternalName != "" {
				internalNameStr = resp.InternalName
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
		}
	}

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
	if sizeBytes.Valid && sizeBytes.Int64 > 0 {
		sb := uint64(sizeBytes.Int64)
		dvrData.SizeBytes = &sb
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

	go func() { _ = s.decklogClient.SendDVRLifecycle(dvrData) }()
}

// TerminateTenantStreams stops all active streams for a suspended tenant
// Called by Purser when a tenant's prepaid balance drops below -$10
func (s *FoghornGRPCServer) TerminateTenantStreams(ctx context.Context, req *pb.TerminateTenantStreamsRequest) (*pb.TerminateTenantStreamsResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id": req.TenantId,
		"reason":    req.Reason,
	}).Info("Terminating tenant streams due to suspension")

	// Get all active streams for this tenant from the stream state manager
	streams := s.lb.GetStreamsByTenant(req.TenantId)
	if len(streams) == 0 {
		s.logger.WithField("tenant_id", req.TenantId).Debug("No active streams to terminate")
		return &pb.TerminateTenantStreamsResponse{
			StreamsTerminated:  0,
			SessionsTerminated: 0,
			StreamNames:        []string{},
		}, nil
	}

	// Group streams by node for efficient batch stop_sessions calls
	streamsByNode := make(map[string][]string)
	var allStreamNames []string
	for _, stream := range streams {
		allStreamNames = append(allStreamNames, stream.InternalName)
		// Get the node from stream instances
		instances := s.lb.GetStreamInstances(stream.InternalName)
		for nodeID := range instances {
			streamsByNode[nodeID] = append(streamsByNode[nodeID], stream.InternalName)
		}
	}

	// Send stop_sessions to each node
	sessionsTerminated := int32(0)
	for nodeID, nodeStreams := range streamsByNode {
		stopReq := &pb.StopSessionsRequest{
			StreamNames: nodeStreams,
			TenantId:    req.TenantId,
			Reason:      req.Reason,
		}
		if err := control.SendStopSessions(nodeID, stopReq); err != nil {
			s.logger.WithFields(logging.Fields{
				"node_id":   nodeID,
				"tenant_id": req.TenantId,
				"error":     err,
			}).Warn("Failed to send stop_sessions to node")
			// Continue trying other nodes
		} else {
			sessionsTerminated += int32(len(nodeStreams))
		}
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.TenantId,
		"streams_terminated":  len(allStreamNames),
		"sessions_terminated": sessionsTerminated,
		"stream_names":        allStreamNames,
	}).Info("Tenant stream termination completed")

	return &pb.TerminateTenantStreamsResponse{
		StreamsTerminated:  int32(len(allStreamNames)),
		SessionsTerminated: sessionsTerminated,
		StreamNames:        allStreamNames,
	}, nil
}

// InvalidateTenantCache clears cached suspension status for a tenant (called on reactivation)
func (s *FoghornGRPCServer) InvalidateTenantCache(ctx context.Context, req *pb.InvalidateTenantCacheRequest) (*pb.InvalidateTenantCacheResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	if s.cacheInvalidator == nil {
		s.logger.WithField("tenant_id", req.TenantId).Warn("Cache invalidator not configured, skipping cache invalidation")
		return &pb.InvalidateTenantCacheResponse{
			EntriesInvalidated: 0,
		}, nil
	}

	entriesInvalidated := s.cacheInvalidator.InvalidateTenantCache(req.TenantId)

	s.logger.WithFields(logging.Fields{
		"tenant_id":           req.TenantId,
		"reason":              req.Reason,
		"entries_invalidated": entriesInvalidated,
	}).Info("Invalidated tenant cache entries")

	return &pb.InvalidateTenantCacheResponse{
		EntriesInvalidated: int32(entriesInvalidated),
	}, nil
}
