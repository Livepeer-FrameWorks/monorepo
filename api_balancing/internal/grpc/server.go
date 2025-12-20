package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/clips"
	"frameworks/pkg/dvr"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
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

// FoghornGRPCServer implements the Foghorn control plane gRPC services
type FoghornGRPCServer struct {
	pb.UnimplementedClipControlServiceServer
	pb.UnimplementedDVRControlServiceServer
	pb.UnimplementedViewerControlServiceServer
	pb.UnimplementedVodControlServiceServer

	db            *sql.DB
	logger        logging.Logger
	lb            *balancer.LoadBalancer
	geoipReader   *geoip.Reader
	decklogClient *decklog.BatchedClient
	s3Client      S3ClientInterface
}

// NewFoghornGRPCServer creates a new Foghorn gRPC server
func NewFoghornGRPCServer(
	db *sql.DB,
	logger logging.Logger,
	lb *balancer.LoadBalancer,
	geoReader *geoip.Reader,
	decklogClient *decklog.BatchedClient,
	s3Client S3ClientInterface,
) *FoghornGRPCServer {
	return &FoghornGRPCServer{
		db:            db,
		logger:        logger,
		lb:            lb,
		geoipReader:   geoReader,
		decklogClient: decklogClient,
		s3Client:      s3Client,
	}
}

// RegisterServices registers all Foghorn gRPC services with the server
func (s *FoghornGRPCServer) RegisterServices(grpcServer *grpc.Server) {
	pb.RegisterClipControlServiceServer(grpcServer, s)
	pb.RegisterDVRControlServiceServer(grpcServer, s)
	pb.RegisterViewerControlServiceServer(grpcServer, s)
	pb.RegisterVodControlServiceServer(grpcServer, s)
}

// emitRoutingEvent sends a LoadBalancingData event with dual-tenant attribution
// Called after successful viewer endpoint resolution to track routing decisions
// durationMs is the time taken to resolve the routing decision (request processing latency)
func (s *FoghornGRPCServer) emitRoutingEvent(
	primary *pb.ViewerEndpoint,
	viewerLat, viewerLon, nodeLat, nodeLon float64,
	internalName, streamTenantID string,
	durationMs float32,
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

	// Emit STAGE_REQUESTED event to Decklog (with enriched timing fields)
	if s.decklogClient != nil {
		clipData := buildClipLifecycleData(pb.ClipLifecycleData_STAGE_REQUESTED, req, reqID, "")
		go func() { _ = s.decklogClient.SendClipLifecycle(clipData) }()
	}

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
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, internal_name, tenant_id, user_id, status, request_id, manifest_path, format, retention_until, created_at, updated_at)
		VALUES ($1, 'clip', $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, 'requested', $5, $6, $7, NOW() + INTERVAL '30 days', NOW(), NOW())
	`, clipHash, req.InternalName, req.TenantId, req.GetUserId(), reqID, storagePath, format)

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
		SourceBaseUrl: deriveMistHTTPBase(ingestHost),
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
	}, nil
}

// GetClips returns clips filtered by internal_name (stream)
// NOTE: tenant_id filtering now happens at Commodore level (business registry)
// Foghorn returns lifecycle data by internal_name (stream) only
func (s *FoghornGRPCServer) GetClips(ctx context.Context, req *pb.GetClipsRequest) (*pb.GetClipsResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		// Without internal_name, we can't filter meaningfully
		// Caller should use Commodore.GetClips for tenant-wide queries
		return nil, status.Error(codes.InvalidArgument, "internal_name is required for artifact queries")
	}

	// Parse bidirectional keyset pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "artifact_hash",
	}

	// Build base WHERE clause - filter by internal_name and artifact_type
	baseWhere := "internal_name = $1 AND artifact_type = 'clip' AND status != 'deleted'"
	args := []interface{}{internalName}
	argIdx := 2

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM foghorn.artifacts WHERE %s", baseWhere)
	var total int32
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		s.logger.WithError(err).Error("Failed to count clips")
		return nil, status.Error(codes.Internal, "failed to count clips")
	}

	// Build select query with keyset pagination
	selectQuery := fmt.Sprintf(`
		SELECT a.artifact_hash, a.internal_name, COALESCE(a.manifest_path, ''),
		       a.size_bytes, a.status, a.access_count, a.created_at, a.updated_at,
		       COALESCE(a.storage_location, 'pending'), COALESCE(n.node_id, ''), COALESCE(n.file_path, '')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.artifact_nodes n ON a.artifact_hash = n.artifact_hash AND NOT n.is_orphaned
		WHERE %s`, baseWhere)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		selectQuery += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	selectQuery += " " + builder.OrderBy(params)
	selectQuery += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	// Fetch clips
	rows, err := s.db.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch clips")
		return nil, status.Error(codes.Internal, "failed to fetch clips")
	}
	defer rows.Close()

	var clips []*pb.ClipInfo
	for rows.Next() {
		var clipHash, streamName, storagePath, clipStatus, storageLocation, nodeID, filePath string
		var sizeBytes sql.NullInt64
		var accessCount int32
		var createdAt, updatedAt time.Time

		err := rows.Scan(
			&clipHash, &streamName, &storagePath,
			&sizeBytes, &clipStatus, &accessCount, &createdAt, &updatedAt,
			&storageLocation, &nodeID, &filePath,
		)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan clip")
			continue
		}

		clip := &pb.ClipInfo{
			Id:         clipHash, // Use hash as ID for lifecycle responses
			ClipHash:   clipHash,
			StreamName: streamName,
			// Title/Description now come from Commodore's business registry
			Title:           "",
			Description:     "",
			NodeId:          nodeID,
			StoragePath:     storagePath,
			Status:          clipStatus,
			AccessCount:     accessCount,
			CreatedAt:       timestamppb.New(createdAt),
			UpdatedAt:       timestamppb.New(updatedAt),
			StorageLocation: &storageLocation,
		}
		if sizeBytes.Valid {
			clip.SizeBytes = &sizeBytes.Int64
		}
		clips = append(clips, clip)
	}

	// Detect hasMore and trim results
	hasMore := len(clips) > params.Limit
	if hasMore {
		clips = clips[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(clips) > 0 {
		for i, j := 0, len(clips)-1; i < j; i, j = i+1, j-1 {
			clips[i], clips[j] = clips[j], clips[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(clips) > 0 {
		first := clips[0]
		last := clips[len(clips)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.Id)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.Id)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.GetClipsResponse{
		Clips: clips,
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

// GetClip returns a specific clip by hash
func (s *FoghornGRPCServer) GetClip(ctx context.Context, req *pb.GetClipRequest) (*pb.ClipInfo, error) {
	if req.ClipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level (business registry)
	// Foghorn only provides lifecycle data by artifact_hash

	// Query artifact lifecycle from foghorn.artifacts
	artifactQuery := `
		SELECT artifact_hash, internal_name, status, COALESCE(error_message, ''),
		       size_bytes, manifest_path, COALESCE(storage_location, 'pending'),
		       access_count, created_at, updated_at
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'clip' AND status != 'deleted'
	`

	var clipHash, internalName, clipStatus, errorMsg, storagePath, storageLocation string
	var sizeBytes sql.NullInt64
	var accessCount int32
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, artifactQuery, req.ClipHash).Scan(
		&clipHash, &internalName, &clipStatus, &errorMsg,
		&sizeBytes, &storagePath, &storageLocation,
		&accessCount, &createdAt, &updatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch clip artifact")
		return nil, status.Error(codes.Internal, "failed to fetch clip")
	}

	// Get node info from artifact_nodes
	var nodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.ClipHash).Scan(&nodeID)

	clip := &pb.ClipInfo{
		Id:         clipHash, // Use hash as ID for lifecycle responses
		ClipHash:   clipHash,
		StreamName: internalName,
		// Title/Description now come from Commodore's business registry
		Title:           "",
		Description:     "",
		NodeId:          nodeID,
		StoragePath:     storagePath,
		Status:          clipStatus,
		AccessCount:     accessCount,
		CreatedAt:       timestamppb.New(createdAt),
		UpdatedAt:       timestamppb.New(updatedAt),
		StorageLocation: &storageLocation,
	}
	if sizeBytes.Valid {
		clip.SizeBytes = &sizeBytes.Int64
	}
	// NOTE: errorMsg is stored in foghorn.artifacts but not exposed in ClipInfo proto

	return clip, nil
}

// GetClipURLs returns viewing URLs for a clip
func (s *FoghornGRPCServer) GetClipURLs(ctx context.Context, req *pb.GetClipURLsRequest) (*pb.ClipViewingURLs, error) {
	if req.ClipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Get artifact info
	var internalName string
	err := s.db.QueryRowContext(ctx, `
		SELECT internal_name FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'clip' AND status != 'deleted'
	`, req.ClipHash).Scan(&internalName)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		return nil, status.Error(codes.Internal, "failed to fetch clip")
	}

	// Get node_id from artifact_nodes
	var nodeID string
	err = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.ClipHash).Scan(&nodeID)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, status.Error(codes.Unavailable, "clip storage node unknown: no active node assignment found")
		}
		return nil, status.Errorf(codes.Unavailable, "clip storage node unknown: %v", err)
	}
	if nodeID == "" {
		return nil, status.Error(codes.Unavailable, "clip storage node unknown: empty node_id")
	}

	// Get node outputs
	nodeOutputs, exists := control.GetNodeOutputs(nodeID)
	if !exists || nodeOutputs.Outputs == nil {
		return nil, status.Error(codes.Unavailable, "storage node outputs not available")
	}

	// Build URLs using clip hash directly (MistServer resolves via PLAY_REWRITE trigger)
	urls := make(map[string]string)
	clipHash := req.ClipHash
	baseURL := ensureTrailingSlash(nodeOutputs.BaseURL)

	// Always add HTML player URL
	urls["MIST_HTML"] = baseURL + clipHash + ".html"

	// Add protocol-specific URLs if available
	if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		urls["HLS"] = resolveTemplateURL(hlsURL, baseURL, clipHash)
	}
	if dashURL, ok := nodeOutputs.Outputs["DASH"].(string); ok {
		urls["DASH"] = resolveTemplateURL(dashURL, baseURL, clipHash)
	}
	if mp4URL, ok := nodeOutputs.Outputs["MP4"].(string); ok {
		urls["MP4"] = resolveTemplateURL(mp4URL, baseURL, clipHash)
	}
	if webmURL, ok := nodeOutputs.Outputs["WEBM"].(string); ok {
		urls["WEBM"] = resolveTemplateURL(webmURL, baseURL, clipHash)
	}

	resp := &pb.ClipViewingURLs{
		Urls: urls,
	}
	// NOTE: ExpiresAt (retention_until) is now in Commodore's business registry
	return resp, nil
}

// DeleteClip deletes a clip
func (s *FoghornGRPCServer) DeleteClip(ctx context.Context, req *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error) {
	if req.ClipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Check current status from foghorn.artifacts
	var currentStatus string
	err := s.db.QueryRowContext(ctx, `
		SELECT status FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'clip'
	`, req.ClipHash).Scan(&currentStatus)

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
		return &pb.StartDVRResponse{
			Status:        "already_started",
			DvrHash:       existingHash,
			IngestHost:    baseURL,
			StorageHost:   storageHost,
			StorageNodeId: storageNodeID,
		}, nil
	}

	// Register DVR in Commodore business registry to get hash
	var dvrHash string
	if control.CommodoreClient != nil {
		regResp, err := control.CommodoreClient.RegisterDVR(ctx, &pb.RegisterDVRRequest{
			TenantId:     req.TenantId,
			UserId:       req.GetUserId(),
			StreamId:     "", // Commodore resolves stream_id from internal_name
			InternalName: req.InternalName,
		})
		if err != nil {
			s.logger.WithError(err).Error("Failed to register DVR with Commodore")
			return nil, status.Errorf(codes.Internal, "failed to register DVR: %v", err)
		}
		dvrHash = regResp.DvrHash
	} else {
		// Fallback: generate locally (legacy, should not happen in production)
		var err error
		dvrHash, err = dvr.GenerateDVRHash()
		if err != nil {
			s.logger.WithError(err).Error("Failed to generate DVR hash")
			return nil, status.Error(codes.Internal, "failed to generate DVR hash")
		}
	}

	// Generate request_id for tracing (distinct from artifact hash)
	requestID := uuid.New().String()

	// Store artifact lifecycle state in foghorn.artifacts
	// NOTE: Business registry (tenant, user, retention) is stored in commodore.dvr_recordings
	// tenant_id is denormalized here for fallback when Commodore is unavailable
	// retention_until defaults to 30 days (system default, not user-configured yet)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, internal_name, tenant_id, status, request_id, format, retention_until, created_at, updated_at)
		VALUES ($1, 'dvr', $2, NULLIF($3, '')::uuid, 'requested', $4, 'm3u8', NOW() + INTERVAL '30 days', NOW(), NOW())
	`, dvrHash, req.InternalName, req.TenantId, requestID)

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
	}, nil
}

// StopDVR stops an active DVR recording
func (s *FoghornGRPCServer) StopDVR(ctx context.Context, req *pb.StopDVRRequest) (*pb.StopDVRResponse, error) {
	if req.DvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Get DVR artifact info
	var dvrStatus, internalName string
	query := `SELECT status, internal_name FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'`

	err := s.db.QueryRowContext(ctx, query, req.DvrHash).Scan(&dvrStatus, &internalName)

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
	var dvrStatus, internalName string
	query := `SELECT status, internal_name FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'`

	err := s.db.QueryRowContext(ctx, query, req.DvrHash).Scan(&dvrStatus, &internalName)

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
	// Note: Analytics event is emitted when Helmsman confirms deletion (via dvrDeletedHandler)
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
		WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, req.DvrHash)
	if err != nil {
		s.logger.WithError(err).Error("Failed to delete DVR recording")
		return nil, status.Error(codes.Internal, "failed to delete DVR recording")
	}

	s.logger.WithField("dvr_hash", req.DvrHash).Info("DVR recording soft-deleted successfully")

	return &pb.DeleteDVRResponse{
		Success: true,
		Message: "DVR recording deleted successfully",
	}, nil
}

// GetDVRStatus returns status of a DVR recording
func (s *FoghornGRPCServer) GetDVRStatus(ctx context.Context, req *pb.GetDVRStatusRequest) (*pb.DVRInfo, error) {
	if req.DvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	// NOTE: tenant_id validation now happens at Commodore level

	// Query artifact lifecycle from foghorn.artifacts
	query := `
		SELECT artifact_hash, internal_name, status,
		       started_at, ended_at, duration_seconds, size_bytes, manifest_path,
		       error_message, created_at, updated_at,
		       COALESCE(storage_location, 'pending'), s3_url, frozen_at
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND status != 'deleted'
	`

	var dvrHash, internalName, dvrStatus string
	var startedAt, endedAt sql.NullTime
	var durationSec sql.NullInt32
	var sizeBytes sql.NullInt64
	var manifestPath, errorMessage sql.NullString
	var createdAt, updatedAt time.Time
	var storageLocation string
	var s3URL sql.NullString
	var frozenAt sql.NullTime

	err := s.db.QueryRowContext(ctx, query, req.DvrHash).Scan(
		&dvrHash, &internalName, &dvrStatus,
		&startedAt, &endedAt, &durationSec, &sizeBytes, &manifestPath,
		&errorMessage, &createdAt, &updatedAt,
		&storageLocation, &s3URL, &frozenAt,
	)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR status")
		return nil, status.Error(codes.Internal, "failed to fetch DVR status")
	}

	// Get node_id from artifact_nodes
	var storageNodeID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT node_id FROM foghorn.artifact_nodes
		WHERE artifact_hash = $1 AND NOT is_orphaned
		ORDER BY last_seen_at DESC LIMIT 1
	`, req.DvrHash).Scan(&storageNodeID)

	dvr := &pb.DVRInfo{
		DvrHash:         dvrHash,
		InternalName:    internalName,
		StorageNodeId:   storageNodeID,
		Status:          dvrStatus,
		ManifestPath:    manifestPath.String,
		ErrorMessage:    errorMessage.String,
		CreatedAt:       timestamppb.New(createdAt),
		UpdatedAt:       timestamppb.New(updatedAt),
		StorageLocation: &storageLocation,
	}

	if startedAt.Valid {
		dvr.StartedAt = timestamppb.New(startedAt.Time)
	}
	if endedAt.Valid {
		dvr.EndedAt = timestamppb.New(endedAt.Time)
	}
	if durationSec.Valid {
		dvr.DurationSeconds = &durationSec.Int32
	}
	if sizeBytes.Valid {
		dvr.SizeBytes = &sizeBytes.Int64
	}
	if s3URL.Valid {
		dvr.S3Url = &s3URL.String
	}
	if frozenAt.Valid {
		dvr.FrozenAt = timestamppb.New(frozenAt.Time)
	}
	// NOTE: ExpiresAt (retention_until) is now in Commodore's business registry

	return dvr, nil
}

// ListDVRRecordings lists DVR recordings for a stream (by internal_name)
// NOTE: Tenant-wide queries should go through Commodore (business registry owner)
func (s *FoghornGRPCServer) ListDVRRecordings(ctx context.Context, req *pb.ListDVRRecordingsRequest) (*pb.ListDVRRecordingsResponse, error) {
	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required for artifact queries - tenant-wide queries should go through Commodore")
	}

	// Parse bidirectional keyset pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "a.created_at",
		IDColumn:        "a.artifact_hash",
	}

	// Build base WHERE clause (always exclude deleted, filter by artifact_type)
	baseWhere := "a.internal_name = $1 AND a.artifact_type = 'dvr' AND a.status != 'deleted'"
	args := []interface{}{internalName}
	argIdx := 2

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM foghorn.artifacts a WHERE %s", baseWhere)
	var total int32
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		s.logger.WithError(err).Error("Failed to count DVR recordings")
		return nil, status.Error(codes.Internal, "failed to count DVR recordings")
	}

	// Build select query with keyset pagination
	// Join with artifact_nodes to get storage node info
	selectQuery := fmt.Sprintf(`
		SELECT a.artifact_hash, a.internal_name, COALESCE(an.node_id, ''), a.status,
		       a.started_at, a.ended_at, a.duration_seconds, a.size_bytes, a.manifest_path,
		       a.error_message, a.created_at, a.updated_at,
		       COALESCE(a.storage_location, 'pending'), a.s3_url, a.frozen_at
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.artifact_nodes an ON a.artifact_hash = an.artifact_hash
		WHERE %s`, baseWhere)

	// Add keyset condition if cursor provided
	if condition, cursorArgs := builder.Condition(params, argIdx); condition != "" {
		selectQuery += " AND " + condition
		args = append(args, cursorArgs...)
	}

	// Add ORDER BY and LIMIT
	selectQuery += " " + builder.OrderBy(params)
	selectQuery += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	// Fetch recordings
	rows, err := s.db.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR recordings")
		return nil, status.Error(codes.Internal, "failed to fetch DVR recordings")
	}
	defer rows.Close()

	var recordings []*pb.DVRInfo
	for rows.Next() {
		var dvrHash, internalNameVal, storageNodeID, dvrStatus string
		var startedAt, endedAt sql.NullTime
		var durationSec sql.NullInt32
		var sizeBytes sql.NullInt64
		var manifestPath, errorMessage sql.NullString
		var createdAt, updatedAt time.Time
		var storageLocation string
		var s3URL sql.NullString
		var frozenAt sql.NullTime

		err := rows.Scan(
			&dvrHash, &internalNameVal, &storageNodeID, &dvrStatus,
			&startedAt, &endedAt, &durationSec, &sizeBytes, &manifestPath,
			&errorMessage, &createdAt, &updatedAt,
			&storageLocation, &s3URL, &frozenAt,
		)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan DVR recording")
			continue
		}

		dvr := &pb.DVRInfo{
			DvrHash:         dvrHash,
			InternalName:    internalNameVal,
			StorageNodeId:   storageNodeID,
			Status:          dvrStatus,
			ManifestPath:    manifestPath.String,
			ErrorMessage:    errorMessage.String,
			CreatedAt:       timestamppb.New(createdAt),
			UpdatedAt:       timestamppb.New(updatedAt),
			StorageLocation: &storageLocation,
		}

		if startedAt.Valid {
			dvr.StartedAt = timestamppb.New(startedAt.Time)
		}
		if endedAt.Valid {
			dvr.EndedAt = timestamppb.New(endedAt.Time)
		}
		if durationSec.Valid {
			dvr.DurationSeconds = &durationSec.Int32
		}
		if sizeBytes.Valid {
			dvr.SizeBytes = &sizeBytes.Int64
		}
		if s3URL.Valid {
			dvr.S3Url = &s3URL.String
		}
		if frozenAt.Valid {
			dvr.FrozenAt = timestamppb.New(frozenAt.Time)
		}
		// NOTE: ExpiresAt/retention_until now in Commodore business registry

		recordings = append(recordings, dvr)
	}

	// Detect hasMore and trim results
	hasMore := len(recordings) > params.Limit
	if hasMore {
		recordings = recordings[:params.Limit]
	}

	// Reverse results if backward pagination
	if params.Direction == pagination.Backward && len(recordings) > 0 {
		for i, j := 0, len(recordings)-1; i < j; i, j = i+1, j-1 {
			recordings[i], recordings[j] = recordings[j], recordings[i]
		}
	}

	// Build cursors from results
	var startCursor, endCursor string
	if len(recordings) > 0 {
		first := recordings[0]
		last := recordings[len(recordings)-1]
		startCursor = pagination.EncodeCursor(first.CreatedAt.AsTime(), first.DvrHash)
		endCursor = pagination.EncodeCursor(last.CreatedAt.AsTime(), last.DvrHash)
	}

	// Build response with proper hasNextPage/hasPreviousPage
	resp := &pb.ListDVRRecordingsResponse{
		DvrRecordings: recordings,
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

// =============================================================================
// VIEWER CONTROL SERVICE IMPLEMENTATION
// =============================================================================

// ResolveViewerEndpoint resolves the best endpoint(s) for a viewer
func (s *FoghornGRPCServer) ResolveViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	if req.ContentId == "" {
		return nil, status.Error(codes.InvalidArgument, "content_id is required")
	}

	// Auto-detect content type if not specified (unified resolution)
	if req.ContentType == "" {
		resolution, err := control.ResolveContent(ctx, req.ContentId)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "failed to resolve content: %v", err)
		}
		req.ContentType = resolution.ContentType
		s.logger.WithFields(logging.Fields{
			"content_id":   req.ContentId,
			"content_type": req.ContentType,
		}).Info("Auto-detected content type")
	}

	if req.ContentType != "live" && req.ContentType != "dvr" && req.ContentType != "clip" {
		return nil, status.Error(codes.InvalidArgument, "content_type must be 'live', 'dvr', or 'clip'")
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
	var err error

	switch req.ContentType {
	case "live":
		response, err = s.resolveLiveViewerEndpoint(ctx, req, lat, lon)
	case "dvr":
		response, err = s.resolveDVRViewerEndpoint(ctx, req)
	case "clip":
		response, err = s.resolveClipViewerEndpoint(ctx, req)
	}

	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"content_type": req.ContentType,
			"content_id":   req.ContentId,
		}).Error("Failed to resolve viewer endpoint")
		return nil, err
	}

	// Enrich live metadata from unified state
	if req.ContentType == "live" && response.Metadata != nil {
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

	response, err := control.ResolveLivePlayback(ctx, deps, viewKey, target.InternalName)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	// Emit routing event for analytics
	if response.Primary != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		s.emitRoutingEvent(response.Primary, lat, lon, 0, 0, target.InternalName, target.TenantID, durationMs)
	}

	return response, nil
}

func (s *FoghornGRPCServer) resolveDVRViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	start := time.Now()
	// Delegate to consolidated control package function
	deps := &control.PlaybackDependencies{
		DB: s.db,
		LB: s.lb,
	}

	response, err := control.ResolveDVRPlayback(ctx, deps, req.ContentId)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		if strings.Contains(err.Error(), "not available") {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	// Emit routing event for analytics
	if response.Primary != nil && response.Metadata != nil {
		durationMs := float32(time.Since(start).Milliseconds())
		s.emitRoutingEvent(response.Primary, 0, 0, 0, 0, response.Metadata.GetClipSource(), response.Metadata.GetTenantId(), durationMs)
	}

	return response, nil
}

func (s *FoghornGRPCServer) resolveClipViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	start := time.Now()
	// Delegate to consolidated control package function
	deps := &control.PlaybackDependencies{
		DB: s.db,
		LB: s.lb,
	}

	response, err := control.ResolveClipPlayback(ctx, deps, req.ContentId)
	if err != nil {
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
		s.emitRoutingEvent(response.Primary, 0, 0, 0, 0, response.Metadata.GetClipSource(), response.Metadata.GetTenantId(), durationMs)
	}

	return response, nil
}

// GetStreamMeta returns MistServer JSON meta for a stream
func (s *FoghornGRPCServer) GetStreamMeta(ctx context.Context, req *pb.StreamMetaRequest) (*pb.StreamMetaResponse, error) {
	internalName := strings.TrimSpace(req.InternalName)
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}

	// Determine base URL
	base := strings.TrimSpace(req.GetTargetBaseUrl())
	if base == "" && req.TargetNodeId != nil && *req.TargetNodeId != "" {
		if no, ok := control.GetNodeOutputs(*req.TargetNodeId); ok {
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
		return nil, status.Error(codes.Unavailable, "no source node available")
	}

	// Build json URL
	encoded := url.PathEscape("live+" + internalName)
	jsonURL, err := url.JoinPath(strings.TrimRight(base, "/"), "json_"+encoded+".js")
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to build meta URL")
	}

	httpClient := &http.Client{Timeout: 4 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, jsonURL, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to build request")
	}
	httpReq.Header.Set("Accept", "application/json, text/javascript")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "failed to fetch meta")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, status.Errorf(codes.Unavailable, "edge response error: %d - %s", resp.StatusCode, string(body))
	}

	var raw map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, status.Error(codes.Unavailable, "failed to parse meta")
	}

	// Build summary
	summary := &pb.MetaSummary{
		IsLive:         false,
		BufferWindowMs: 0,
		JitterMs:       0,
		UnixOffsetMs:   0,
		Type:           "",
		Tracks:         []*pb.TrackSummary{},
	}

	if meta, ok := raw["meta"].(map[string]interface{}); ok {
		if live, ok := meta["live"].(float64); ok {
			summary.IsLive = live != 0
		}
		if bw, ok := meta["buffer_window"].(float64); ok {
			summary.BufferWindowMs = int64(bw)
		}
		if jit, ok := meta["jitter"].(float64); ok {
			summary.JitterMs = int64(jit)
		}
		if uo, ok := raw["unixoffset"].(float64); ok {
			summary.UnixOffsetMs = int64(uo)
		}
		if t, ok := raw["type"].(string); ok {
			summary.Type = t
		}
		if w, ok := raw["width"].(float64); ok {
			v := int32(w)
			summary.Width = &v
		}
		if h, ok := raw["height"].(float64); ok {
			v := int32(h)
			summary.Height = &v
		}
		// tracks
		if tracks, ok := meta["tracks"].(map[string]interface{}); ok {
			for id, tv := range tracks {
				if tm, ok := tv.(map[string]interface{}); ok {
					track := &pb.TrackSummary{
						Id:    id,
						Type:  stringOr(tm["type"]),
						Codec: stringOr(tm["codec"]),
					}
					if ch, ok := toInt(tm["channels"]); ok {
						track.Channels = &ch
					}
					if rate, ok := toInt(tm["rate"]); ok {
						track.Rate = &rate
					}
					if bw, ok := toInt(tm["bps"]); ok {
						track.BitrateBps = &bw
					}
					if w, ok := toInt(tm["width"]); ok {
						track.Width = &w
					}
					if h, ok := toInt(tm["height"]); ok {
						track.Height = &h
					}
					if now, ok := toInt64(tm["nowms"]); ok {
						track.NowMs = &now
					}
					if last, ok := toInt64(tm["lastms"]); ok {
						track.LastMs = &last
					}
					if first, ok := toInt64(tm["firstms"]); ok {
						track.FirstMs = &first
					}
					summary.Tracks = append(summary.Tracks, track)
				}
			}
		}
	}

	response := &pb.StreamMetaResponse{
		MetaSummary: summary,
	}

	if req.IncludeRaw {
		if rawBytes, err := json.Marshal(raw); err == nil {
			response.Raw = rawBytes
		}
	}

	return response, nil
}

// ResolveIngestEndpoint resolves the best ingest endpoint(s) for StreamCrafter
func (s *FoghornGRPCServer) ResolveIngestEndpoint(ctx context.Context, req *pb.IngestEndpointRequest) (*pb.IngestEndpointResponse, error) {
	streamKey := strings.TrimSpace(req.StreamKey)
	if streamKey == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_key is required")
	}

	// 1. Validate stream key via Commodore
	validation, err := control.CommodoreClient.ValidateStreamKey(ctx, streamKey)
	if err != nil {
		s.logger.WithError(err).WithField("stream_key", streamKey).Error("Failed to validate stream key")
		return nil, status.Errorf(codes.Internal, "failed to validate stream key: %v", err)
	}
	if !validation.Valid {
		errMsg := validation.Error
		if errMsg == "" {
			errMsg = "Invalid stream key"
		}
		return nil, status.Error(codes.NotFound, errMsg)
	}

	// 2. GeoIP resolution for geo-routing (optional)
	var lat, lon float64 = 0.0, 0.0
	viewerIP := req.GetViewerIp()
	var region string

	if viewerIP != "" && s.geoipReader != nil {
		if geoData := s.geoipReader.Lookup(viewerIP); geoData != nil {
			lat = geoData.Latitude
			lon = geoData.Longitude
			region = geoData.City
			if region == "" {
				region = geoData.CountryName
			}
		}
	}

	// 3. Get available ingest nodes (use isSourceSelection=true for ingest)
	// For ingest, we need nodes that can receive streams, so we use isSourceSelection=false
	// to find nodes that have capacity (not specifically for a particular stream)
	nodes, err := s.lb.GetTopNodesWithScores(ctx, "", lat, lon, nil, viewerIP, 5, false)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get ingest nodes")
		return nil, status.Error(codes.Unavailable, "no ingest nodes available")
	}
	if len(nodes) == 0 {
		return nil, status.Error(codes.Unavailable, "no ingest nodes available")
	}

	internalName := validation.InternalName

	// 4. Build primary endpoint
	primaryNode := nodes[0]
	primary, err := s.buildIngestEndpoint(primaryNode, internalName, region)
	if err != nil {
		s.logger.WithError(err).WithField("node_id", primaryNode.NodeID).Error("Failed to build primary ingest endpoint")
		return nil, status.Error(codes.Internal, "failed to build ingest endpoint")
	}

	// 5. Build fallback endpoints
	var fallbacks []*pb.IngestEndpoint
	for i := 1; i < len(nodes); i++ {
		fallback, err := s.buildIngestEndpoint(nodes[i], internalName, "")
		if err != nil {
			s.logger.WithError(err).WithField("node_id", nodes[i].NodeID).Warn("Failed to build fallback ingest endpoint")
			continue
		}
		fallbacks = append(fallbacks, fallback)
	}

	// 6. Build metadata
	metadata := &pb.IngestMetadata{
		StreamId:         internalName,
		StreamKey:        streamKey,
		TenantId:         validation.TenantId,
		RecordingEnabled: validation.IsRecordingEnabled,
	}

	s.logger.WithFields(logging.Fields{
		"stream_key":    streamKey,
		"internal_name": internalName,
		"primary_node":  primary.NodeId,
		"fallback_cnt":  len(fallbacks),
	}).Info("Resolved ingest endpoint")

	return &pb.IngestEndpointResponse{
		Primary:   primary,
		Fallbacks: fallbacks,
		Metadata:  metadata,
	}, nil
}

// buildIngestEndpoint constructs ingest URLs for a given node
func (s *FoghornGRPCServer) buildIngestEndpoint(node balancer.NodeWithScore, streamName string, region string) (*pb.IngestEndpoint, error) {
	// Get node outputs for the base URL
	nodeOutputs, ok := control.GetNodeOutputs(node.NodeID)
	if !ok || nodeOutputs.BaseURL == "" {
		return nil, fmt.Errorf("no base URL for node %s", node.NodeID)
	}

	baseURL := strings.TrimRight(nodeOutputs.BaseURL, "/")
	host := node.Host

	// Parse host to get hostname without protocol/port for RTMP/SRT
	if strings.Contains(host, "://") {
		if u, err := url.Parse(host); err == nil {
			host = u.Hostname()
		}
	}
	if strings.Contains(host, ":") {
		host, _, _ = net.SplitHostPort(host)
	}

	// If host is still empty, extract from baseURL
	if host == "" {
		if u, err := url.Parse(baseURL); err == nil {
			host = u.Hostname()
		}
	}

	// Build ingest URLs
	// WHIP: baseURL/webrtc/{streamName}
	whipURL := fmt.Sprintf("%s/webrtc/%s", baseURL, streamName)

	// RTMP: rtmp://{host}:1935/live/{streamName}
	rtmpURL := fmt.Sprintf("rtmp://%s:1935/live/%s", host, streamName)

	// SRT: srt://{host}:9000?streamid={streamName}
	srtURL := fmt.Sprintf("srt://%s:9000?streamid=%s", host, streamName)

	// Determine region from node location
	nodeRegion := region
	if nodeRegion == "" {
		nodeRegion = node.LocationName
	}

	// Calculate normalized load score (lower is better, 0-1 range)
	var loadScore float64
	if node.Score > 0 {
		// Score is 0-10000, convert to 0-1 (inverted: higher score = lower load)
		loadScore = 1.0 - (float64(node.Score) / 10000.0)
		if loadScore < 0 {
			loadScore = 0
		}
	}

	return &pb.IngestEndpoint{
		NodeId:    node.NodeID,
		BaseUrl:   baseURL,
		WhipUrl:   &whipURL,
		RtmpUrl:   &rtmpURL,
		SrtUrl:    &srtURL,
		Region:    &nodeRegion,
		LoadScore: &loadScore,
	}, nil
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func deriveMistHTTPBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
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

func ensureTrailingSlash(s string) string {
	if !strings.HasSuffix(s, "/") {
		return s + "/"
	}
	return s
}

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

func buildOutputsMapProto(baseURL string, rawOutputs map[string]interface{}, streamName string, isLive bool) map[string]*pb.OutputEndpoint {
	outputs := make(map[string]*pb.OutputEndpoint)

	base := ensureTrailingSlash(baseURL)
	html := base + streamName + ".html"
	outputs["MIST_HTML"] = &pb.OutputEndpoint{Protocol: "MIST_HTML", Url: html, Capabilities: buildOutputCapabilitiesProto("MIST_HTML", isLive)}
	outputs["PLAYER_JS"] = &pb.OutputEndpoint{Protocol: "PLAYER_JS", Url: base + "player.js", Capabilities: buildOutputCapabilitiesProto("PLAYER_JS", isLive)}

	// WHEP
	if raw, ok := rawOutputs["WHEP"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WHEP"] = &pb.OutputEndpoint{Protocol: "WHEP", Url: u, Capabilities: buildOutputCapabilitiesProto("WHEP", isLive)}
		}
	}
	if _, ok := outputs["WHEP"]; !ok {
		if u := deriveWHEPFromHTML(html); u != "" {
			outputs["WHEP"] = &pb.OutputEndpoint{Protocol: "WHEP", Url: u, Capabilities: buildOutputCapabilitiesProto("WHEP", isLive)}
		}
	}

	if raw, ok := rawOutputs["HLS"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HLS"] = &pb.OutputEndpoint{Protocol: "HLS", Url: u, Capabilities: buildOutputCapabilitiesProto("HLS", isLive)}
		}
	}
	if raw, ok := rawOutputs["DASH"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["DASH"] = &pb.OutputEndpoint{Protocol: "DASH", Url: u, Capabilities: buildOutputCapabilitiesProto("DASH", isLive)}
		}
	}
	if raw, ok := rawOutputs["MP4"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["MP4"] = &pb.OutputEndpoint{Protocol: "MP4", Url: u, Capabilities: buildOutputCapabilitiesProto("MP4", isLive)}
		}
	}
	if raw, ok := rawOutputs["WEBM"]; ok {
		if u := resolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WEBM"] = &pb.OutputEndpoint{Protocol: "WEBM", Url: u, Capabilities: buildOutputCapabilitiesProto("WEBM", isLive)}
		}
	}

	return outputs
}

func buildOutputCapabilitiesProto(protocol string, isLive bool) *pb.OutputCapability {
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

func buildLivePlaybackMetadataProto(req *pb.ViewerEndpointRequest, endpoints []*pb.ViewerEndpoint) *pb.PlaybackMetadata {
	meta := &pb.PlaybackMetadata{
		Status:      "live",
		IsLive:      true,
		ContentId:   req.ContentId,
		ContentType: "live",
	}

	if len(endpoints) > 0 {
		for _, ep := range endpoints {
			if ep.Outputs != nil {
				for proto := range ep.Outputs {
					meta.ProtocolHints = append(meta.ProtocolHints, proto)
				}
				break
			}
		}
	}

	return meta
}

func stringOr(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toInt(v interface{}) (int32, bool) {
	switch x := v.(type) {
	case float64:
		return int32(x), true
	case int:
		return int32(x), true
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

// =============================================================================
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
			id, artifact_hash, artifact_type, tenant_id, user_id, status,
			size_bytes, s3_url, format, retention_until, created_at, updated_at
		)
		VALUES ($1, $2, 'upload', NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, 'uploading',
		        $5, $6, $7, NOW() + INTERVAL '30 days', NOW(), NOW())
	`, artifactID, artifactHash, req.TenantId, req.UserId, req.SizeBytes, s.s3Client.BuildS3URL(s3Key), vodFormat)

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
	err := s.db.QueryRowContext(ctx, `
		SELECT v.artifact_hash, v.s3_key
		FROM foghorn.vod_metadata v
		JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
		WHERE v.s3_upload_id = $1 AND a.status = 'uploading'
	`, req.UploadId).Scan(&artifactHash, &s3Key)

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
			UPDATE foghorn.artifacts SET status = 'failed', error_message = $1, updated_at = NOW()
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
			go func() { _ = s.decklogClient.SendVodLifecycle(vodData) }()
		}
		return nil, status.Errorf(codes.Internal, "failed to complete upload: %v", err)
	}

	// Update artifact status to 'ready' (no validation/transcoding for now)
	// TODO: When we add ffprobe validation, change this to 'processing' and trigger async validation
	_, err = s.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'ready', storage_location = 's3', updated_at = NOW()
		WHERE artifact_hash = $1
	`, artifactHash)
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
	err := s.db.QueryRowContext(ctx, `
		SELECT v.artifact_hash, v.s3_key
		FROM foghorn.vod_metadata v
		JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
		WHERE v.s3_upload_id = $1 AND a.status = 'uploading'
	`, req.UploadId).Scan(&artifactHash, &s3Key)

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
	baseWhere := "a.artifact_type = 'upload' AND a.status != 'deleted'"
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
	var currentStatus, s3Key string
	err := s.db.QueryRowContext(ctx, `
		SELECT a.status, COALESCE(v.s3_key, '')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.artifact_type = 'upload'
	`, req.ArtifactHash).Scan(&currentStatus, &s3Key)

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
		WHERE artifact_hash = $1 AND artifact_type = 'upload'
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
		WHERE a.artifact_hash = $1 AND a.artifact_type = 'upload' AND a.status != 'deleted'
	`
	row := s.db.QueryRowContext(ctx, query, artifactHash)
	return s.scanVodAssetRow(row)
}

func (s *FoghornGRPCServer) getVodAssetInfoWithTenant(ctx context.Context, artifactHash, tenantID string) (*pb.VodAssetInfo, error) {
	query := `
		SELECT a.id, a.artifact_hash, a.status, a.size_bytes,
		       COALESCE(a.storage_location, 'pending'), COALESCE(a.s3_url, ''),
		       a.error_message, a.created_at, a.updated_at, a.retention_until,
		       COALESCE(v.filename, ''), COALESCE(v.title, ''), COALESCE(v.description, ''),
		       v.duration_ms, v.resolution, v.video_codec, v.audio_codec, v.bitrate_kbps,
		       COALESCE(v.s3_upload_id, ''), COALESCE(v.s3_key, '')
		FROM foghorn.artifacts a
		LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
		WHERE a.artifact_hash = $1 AND a.tenant_id = $2::uuid AND a.artifact_type = 'upload' AND a.status != 'deleted'
	`
	row := s.db.QueryRowContext(ctx, query, artifactHash, tenantID)
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
