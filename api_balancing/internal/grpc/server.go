package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FoghornGRPCServer implements the Foghorn control plane gRPC services
type FoghornGRPCServer struct {
	pb.UnimplementedClipControlServiceServer
	pb.UnimplementedDVRControlServiceServer
	pb.UnimplementedViewerControlServiceServer

	db            *sql.DB
	logger        logging.Logger
	lb            *balancer.LoadBalancer
	geoipReader   *geoip.Reader
	decklogClient *decklog.BatchedClient
}

// NewFoghornGRPCServer creates a new Foghorn gRPC server
func NewFoghornGRPCServer(
	db *sql.DB,
	logger logging.Logger,
	lb *balancer.LoadBalancer,
	geoReader *geoip.Reader,
	decklogClient *decklog.BatchedClient,
) *FoghornGRPCServer {
	return &FoghornGRPCServer{
		db:            db,
		logger:        logger,
		lb:            lb,
		geoipReader:   geoReader,
		decklogClient: decklogClient,
	}
}

// RegisterServices registers all Foghorn gRPC services with the server
func (s *FoghornGRPCServer) RegisterServices(grpcServer *grpc.Server) {
	pb.RegisterClipControlServiceServer(grpcServer, s)
	pb.RegisterDVRControlServiceServer(grpcServer, s)
	pb.RegisterViewerControlServiceServer(grpcServer, s)
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
	grpc_health_v1.RegisterHealthServer(grpcServer, hs)

	server.logger.WithField("addr", addr).Info("Starting Foghorn gRPC server")
	return grpcServer.Serve(lis)
}

// =============================================================================
// CLIP CONTROL SERVICE IMPLEMENTATION
// =============================================================================

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

	// Emit STAGE_REQUESTED event to Decklog
	if s.decklogClient != nil {
		clipData := &pb.ClipLifecycleData{
			Stage:     pb.ClipLifecycleData_STAGE_REQUESTED,
			RequestId: &reqID,
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
		go func() { _ = s.decklogClient.SendClipLifecycle(clipData) }()
	}

	// Get storage node ID
	storageNodeID := s.lb.GetNodeIDByHost(storageHost)
	if storageNodeID == "" {
		return nil, status.Error(codes.Unavailable, "storage node not connected")
	}

	// Generate secure clip hash
	var startMs, durationMs int64
	if req.StartMs != nil {
		startMs = *req.StartMs
	}
	if req.DurationSec != nil {
		durationMs = *req.DurationSec * 1000
	}

	clipHash, err := clips.GenerateClipHash(req.InternalName, startMs, durationMs)
	if err != nil {
		s.logger.WithError(err).Error("Failed to generate clip hash")
		return nil, status.Error(codes.Internal, "failed to generate clip hash")
	}

	// Store clip metadata in database
	clipID := uuid.New().String()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.clips (id, tenant_id, stream_id, user_id, clip_hash, stream_name, title, description,
		                           start_time, duration, node_id, storage_path, status, request_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW())
	`, clipID, req.TenantId, uuid.Nil, uuid.Nil, clipHash, req.InternalName, req.GetTitle(), req.GetDescription(),
		startMs, durationMs, storageNodeID, clips.BuildClipStoragePath(req.InternalName, clipHash, format), "requested", reqID)

	if err != nil {
		s.logger.WithError(err).Error("Failed to store clip metadata in database")
		return nil, status.Error(codes.Internal, "failed to store clip metadata")
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
		return nil, status.Errorf(codes.Unavailable, "storage node unavailable: %v", err)
	}

	// Emit STAGE_QUEUED event to Decklog
	if s.decklogClient != nil {
		clipData := &pb.ClipLifecycleData{
			Stage:       pb.ClipLifecycleData_STAGE_QUEUED,
			ClipHash:    clipHash,
			RequestId:   &reqID,
			CompletedAt: func() *int64 { t := time.Now().Unix(); return &t }(),
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

// GetClips returns all clips for a tenant
func (s *FoghornGRPCServer) GetClips(ctx context.Context, req *pb.GetClipsRequest) (*pb.GetClipsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Parse bidirectional keyset pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "id",
	}

	internalName := req.GetInternalName()

	// Build base WHERE clause
	var baseWhere string
	var args []interface{}
	argIdx := 1

	if internalName != "" {
		baseWhere = "tenant_id = $1 AND stream_name = $2 AND status != 'deleted'"
		args = []interface{}{tenantID, internalName}
		argIdx = 3
	} else {
		baseWhere = "tenant_id = $1 AND status != 'deleted'"
		args = []interface{}{tenantID}
		argIdx = 2
	}

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM foghorn.clips WHERE %s", baseWhere)
	var total int32
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		s.logger.WithError(err).Error("Failed to count clips")
		return nil, status.Error(codes.Internal, "failed to count clips")
	}

	// Build select query with keyset pagination
	selectQuery := fmt.Sprintf(`
		SELECT id, clip_hash, stream_name, COALESCE(title, ''), COALESCE(description, ''),
		       start_time, duration, COALESCE(node_id, ''), COALESCE(storage_path, ''),
		       size_bytes, status, access_count, created_at, updated_at
		FROM foghorn.clips
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
		var id, clipHash, streamName, title, description, nodeID, storagePath, clipStatus string
		var startTime, duration int64
		var sizeBytes sql.NullInt64
		var accessCount int32
		var createdAt, updatedAt time.Time

		err := rows.Scan(
			&id, &clipHash, &streamName, &title, &description,
			&startTime, &duration, &nodeID, &storagePath,
			&sizeBytes, &clipStatus, &accessCount, &createdAt, &updatedAt,
		)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan clip")
			continue
		}

		clip := &pb.ClipInfo{
			Id:          id,
			ClipHash:    clipHash,
			StreamName:  streamName,
			Title:       title,
			Description: description,
			StartTime:   startTime,
			Duration:    duration,
			NodeId:      nodeID,
			StoragePath: storagePath,
			Status:      clipStatus,
			AccessCount: accessCount,
			CreatedAt:   timestamppb.New(createdAt),
			UpdatedAt:   timestamppb.New(updatedAt),
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
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	query := `
		SELECT id, clip_hash, stream_name, COALESCE(title, ''), COALESCE(description, ''),
		       start_time, duration, COALESCE(node_id, ''), COALESCE(storage_path, ''),
		       size_bytes, status, access_count, created_at, updated_at
		FROM foghorn.clips
		WHERE clip_hash = $1 AND tenant_id = $2 AND status != 'deleted'
	`

	var id, clipHash, streamName, title, description, nodeID, storagePath, clipStatus string
	var startTime, duration int64
	var sizeBytes sql.NullInt64
	var accessCount int32
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, query, req.ClipHash, req.TenantId).Scan(
		&id, &clipHash, &streamName, &title, &description,
		&startTime, &duration, &nodeID, &storagePath,
		&sizeBytes, &clipStatus, &accessCount, &createdAt, &updatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch clip")
		return nil, status.Error(codes.Internal, "failed to fetch clip")
	}

	clip := &pb.ClipInfo{
		Id:          id,
		ClipHash:    clipHash,
		StreamName:  streamName,
		Title:       title,
		Description: description,
		StartTime:   startTime,
		Duration:    duration,
		NodeId:      nodeID,
		StoragePath: storagePath,
		Status:      clipStatus,
		AccessCount: accessCount,
		CreatedAt:   timestamppb.New(createdAt),
		UpdatedAt:   timestamppb.New(updatedAt),
	}
	if sizeBytes.Valid {
		clip.SizeBytes = &sizeBytes.Int64
	}

	return clip, nil
}

// GetClipURLs returns viewing URLs for a clip
func (s *FoghornGRPCServer) GetClipURLs(ctx context.Context, req *pb.GetClipURLsRequest) (*pb.ClipViewingURLs, error) {
	if req.ClipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get clip info including node_id
	var nodeID, streamName string
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(node_id, ''), stream_name
		FROM foghorn.clips
		WHERE clip_hash = $1 AND tenant_id = $2 AND status != 'deleted'
	`, req.ClipHash, req.TenantId).Scan(&nodeID, &streamName)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "clip not found")
	} else if err != nil {
		return nil, status.Error(codes.Internal, "failed to fetch clip")
	}

	if nodeID == "" {
		return nil, status.Error(codes.Unavailable, "clip storage node unknown")
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

	return &pb.ClipViewingURLs{
		Urls: urls,
	}, nil
}

// DeleteClip deletes a clip
func (s *FoghornGRPCServer) DeleteClip(ctx context.Context, req *pb.DeleteClipRequest) (*pb.DeleteClipResponse, error) {
	if req.ClipHash == "" {
		return nil, status.Error(codes.InvalidArgument, "clip_hash is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Check current status (with tenant isolation)
	var currentStatus string
	err := s.db.QueryRowContext(ctx, "SELECT status FROM foghorn.clips WHERE clip_hash = $1 AND tenant_id = $2", req.ClipHash, req.TenantId).Scan(&currentStatus)

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

	// Soft delete (with tenant isolation)
	_, err = s.db.ExecContext(ctx, "UPDATE foghorn.clips SET status = 'deleted', updated_at = NOW() WHERE clip_hash = $1 AND tenant_id = $2", req.ClipHash, req.TenantId)
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

	// Check for existing active DVR
	var existingHash string
	_ = s.db.QueryRowContext(ctx, `
		SELECT request_hash FROM foghorn.dvr_requests
		WHERE internal_name=$1 AND status IN ('requested','starting','recording')
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

	// Generate DVR hash
	dvrHash, err := dvr.GenerateDVRHash()
	if err != nil {
		s.logger.WithError(err).Error("Failed to generate DVR hash")
		return nil, status.Error(codes.Internal, "failed to generate DVR hash")
	}

	// Parse stream_id if provided
	var streamID *uuid.UUID
	if req.StreamId != nil && *req.StreamId != "" {
		if parsed, err := uuid.Parse(*req.StreamId); err == nil {
			streamID = &parsed
		}
	}

	// Store DVR request
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO foghorn.dvr_requests (request_hash, tenant_id, stream_id, internal_name,
		                                 storage_node_id, storage_node_url, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`, dvrHash, req.TenantId, streamID, req.InternalName, storageNodeID, storageHost, "requested")

	if err != nil {
		s.logger.WithError(err).Error("Failed to store DVR request in database")
		return nil, status.Error(codes.Internal, "failed to store DVR request")
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
		s.logger.WithFields(logging.Fields{"storage_node_id": storageNodeID, "error": err}).Error("Failed to send DVR start")
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
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Get DVR request info (with tenant isolation)
	var nodeID, dvrStatus, internalName string
	query := `SELECT COALESCE(storage_node_id, ''), status, internal_name
		FROM foghorn.dvr_requests WHERE request_hash = $1 AND tenant_id = $2`

	err := s.db.QueryRowContext(ctx, query, req.DvrHash, req.TenantId).Scan(&nodeID, &dvrStatus, &internalName)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR request")
		return nil, status.Error(codes.Internal, "failed to fetch DVR request")
	}

	if dvrStatus == "completed" || dvrStatus == "failed" {
		return &pb.StopDVRResponse{
			Success: false,
			Message: fmt.Sprintf("DVR recording already finished with status: %s", dvrStatus),
		}, nil
	}

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

	// Update status (with tenant isolation)
	_, err = s.db.ExecContext(ctx, "UPDATE foghorn.dvr_requests SET status = 'stopping', updated_at = NOW() WHERE request_hash = $1 AND tenant_id = $2", req.DvrHash, req.TenantId)
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

// GetDVRStatus returns status of a DVR recording
func (s *FoghornGRPCServer) GetDVRStatus(ctx context.Context, req *pb.GetDVRStatusRequest) (*pb.DVRInfo, error) {
	if req.DvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	query := `
		SELECT request_hash, internal_name, storage_node_id, status,
		       started_at, ended_at, duration_seconds, size_bytes, manifest_path,
		       error_message, created_at, updated_at
		FROM foghorn.dvr_requests
		WHERE request_hash = $1 AND tenant_id = $2
	`

	var dvrHash, internalName, storageNodeID, dvrStatus string
	var startedAt, endedAt sql.NullTime
	var durationSec sql.NullInt32
	var sizeBytes sql.NullInt64
	var manifestPath, errorMessage sql.NullString
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, query, req.DvrHash, req.TenantId).Scan(
		&dvrHash, &internalName, &storageNodeID, &dvrStatus,
		&startedAt, &endedAt, &durationSec, &sizeBytes, &manifestPath,
		&errorMessage, &createdAt, &updatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, status.Error(codes.NotFound, "DVR recording not found")
	} else if err != nil {
		s.logger.WithError(err).Error("Failed to fetch DVR status")
		return nil, status.Error(codes.Internal, "failed to fetch DVR status")
	}

	dvr := &pb.DVRInfo{
		DvrHash:       dvrHash,
		InternalName:  internalName,
		StorageNodeId: storageNodeID,
		Status:        dvrStatus,
		ManifestPath:  manifestPath.String,
		ErrorMessage:  errorMessage.String,
		CreatedAt:     timestamppb.New(createdAt),
		UpdatedAt:     timestamppb.New(updatedAt),
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

	return dvr, nil
}

// ListDVRRecordings lists all DVR recordings for a tenant
func (s *FoghornGRPCServer) ListDVRRecordings(ctx context.Context, req *pb.ListDVRRecordingsRequest) (*pb.ListDVRRecordingsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Parse bidirectional keyset pagination
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	builder := &pagination.KeysetBuilder{
		TimestampColumn: "created_at",
		IDColumn:        "request_hash",
	}

	internalName := req.GetInternalName()

	// Build base WHERE clause
	var baseWhere string
	var args []interface{}
	argIdx := 1

	if internalName != "" {
		baseWhere = "tenant_id = $1 AND internal_name = $2"
		args = []interface{}{tenantID, internalName}
		argIdx = 3
	} else {
		baseWhere = "tenant_id = $1"
		args = []interface{}{tenantID}
		argIdx = 2
	}

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM foghorn.dvr_requests WHERE %s", baseWhere)
	var total int32
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		s.logger.WithError(err).Error("Failed to count DVR recordings")
		return nil, status.Error(codes.Internal, "failed to count DVR recordings")
	}

	// Build select query with keyset pagination
	selectQuery := fmt.Sprintf(`
		SELECT request_hash, internal_name, storage_node_id, status,
		       started_at, ended_at, duration_seconds, size_bytes, manifest_path,
		       error_message, created_at, updated_at
		FROM foghorn.dvr_requests
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

		err := rows.Scan(
			&dvrHash, &internalNameVal, &storageNodeID, &dvrStatus,
			&startedAt, &endedAt, &durationSec, &sizeBytes, &manifestPath,
			&errorMessage, &createdAt, &updatedAt,
		)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan DVR recording")
			continue
		}

		dvr := &pb.DVRInfo{
			DvrHash:       dvrHash,
			InternalName:  internalNameVal,
			StorageNodeId: storageNodeID,
			Status:        dvrStatus,
			ManifestPath:  manifestPath.String,
			ErrorMessage:  errorMessage.String,
			CreatedAt:     timestamppb.New(createdAt),
			UpdatedAt:     timestamppb.New(updatedAt),
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
	if req.ContentType == "" || req.ContentId == "" {
		return nil, status.Error(codes.InvalidArgument, "content_type and content_id are required")
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
	// Resolve view key to internal name for load balancing
	viewKey := req.ContentId
	target, err := control.ResolveStream(ctx, viewKey)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to resolve stream: %v", err)
	}
	if target.InternalName == "" {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	internalName := target.InternalName // e.g., "live+actual-internal-name"

	// Use load balancer with internal name to find nodes that have the stream
	lbctx := context.WithValue(ctx, "cap", "edge")
	nodes, err := s.lb.GetTopNodesWithScores(lbctx, internalName, lat, lon, make(map[string]int), "", 5, false)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "no suitable edge nodes available: %v", err)
	}

	var endpoints []*pb.ViewerEndpoint

	for _, node := range nodes {
		nodeOutputs, exists := control.GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		// Build URLs with view key (MistServer resolves via PLAY_REWRITE trigger)
		var protocol, endpointURL string
		if webrtcURL, ok := nodeOutputs.Outputs["WebRTC"].(string); ok {
			protocol = "webrtc"
			endpointURL = strings.Replace(webrtcURL, "$", viewKey, -1)
			endpointURL = strings.Replace(endpointURL, "HOST", strings.TrimPrefix(nodeOutputs.BaseURL, "https://"), -1)
		} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
			protocol = "hls"
			endpointURL = strings.Replace(hlsURL, "$", viewKey, -1)
			endpointURL = strings.Trim(endpointURL, "[\"")
		}

		if endpointURL == "" {
			continue
		}

		// Calculate geo distance
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
			Url:         endpointURL,
			GeoDistance: geoDistance,
			LoadScore:   float64(node.Score),
			Outputs:     buildOutputsMapProto(nodeOutputs.BaseURL, nodeOutputs.Outputs, viewKey, true),
		}
		endpoints = append(endpoints, endpoint)
	}

	if len(endpoints) == 0 {
		return nil, status.Error(codes.Unavailable, "no nodes with suitable outputs available")
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  buildLivePlaybackMetadataProto(req, endpoints),
	}, nil
}

func (s *FoghornGRPCServer) resolveDVRViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	var tenantID, internalName, nodeID, dvrStatus string
	var duration, recordingSize sql.NullInt64
	var manifestPath sql.NullString
	var createdAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, internal_name, storage_node_id, status, duration_seconds, size_bytes, manifest_path, created_at
		FROM foghorn.dvr_requests
		WHERE request_hash = $1 AND status = 'completed'
	`, req.ContentId).Scan(&tenantID, &internalName, &nodeID, &dvrStatus, &duration, &recordingSize, &manifestPath, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, status.Error(codes.NotFound, "DVR recording not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to query DVR: %v", err)
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)
	if !exists || nodeOutputs.Outputs == nil {
		return nil, status.Error(codes.Unavailable, "storage node outputs not available")
	}

	// Use DVR hash directly in URLs (MistServer resolves via PLAY_REWRITE trigger)
	dvrHash := req.ContentId
	var protocol, endpointURL string

	if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		endpointURL = resolveTemplateURL(hlsURL, nodeOutputs.BaseURL, dvrHash)
	} else {
		endpointURL = ensureTrailingSlash(nodeOutputs.BaseURL) + dvrHash + ".html"
		protocol = "html"
	}

	endpoint := &pb.ViewerEndpoint{
		NodeId:      nodeID,
		BaseUrl:     nodeOutputs.BaseURL,
		Protocol:    protocol,
		Url:         endpointURL,
		GeoDistance: 0,
		LoadScore:   0,
		Outputs:     buildOutputsMapProto(nodeOutputs.BaseURL, nodeOutputs.Outputs, dvrHash, false),
	}

	metadata := &pb.PlaybackMetadata{
		Status:      "completed",
		IsLive:      false,
		DvrStatus:   "completed",
		TenantId:    tenantID,
		ContentId:   req.ContentId,
		ContentType: "dvr",
	}

	if duration.Valid {
		d := int32(duration.Int64)
		metadata.DurationSeconds = &d
	}
	if recordingSize.Valid {
		metadata.RecordingSizeBytes = &recordingSize.Int64
	}
	if createdAt.Valid {
		metadata.CreatedAt = timestamppb.New(createdAt.Time)
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoint,
		Fallbacks: []*pb.ViewerEndpoint{},
		Metadata:  metadata,
	}, nil
}

func (s *FoghornGRPCServer) resolveClipViewerEndpoint(ctx context.Context, req *pb.ViewerEndpointRequest) (*pb.ViewerEndpointResponse, error) {
	var tenantID, streamName, title, description, nodeID, clipStatus string
	var startTime, clipDuration int64
	var sizeBytes sql.NullInt64
	var createdAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, stream_name, COALESCE(title, ''), COALESCE(description, ''),
		       COALESCE(node_id, ''), status, start_time, duration, size_bytes, created_at
		FROM foghorn.clips
		WHERE clip_hash = $1 AND status != 'deleted'
	`, req.ContentId).Scan(&tenantID, &streamName, &title, &description, &nodeID, &clipStatus, &startTime, &clipDuration, &sizeBytes, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, status.Error(codes.NotFound, "clip not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to query clip: %v", err)
	}

	if nodeID == "" {
		return nil, status.Error(codes.Unavailable, "clip storage node unknown")
	}

	nodeOutputs, exists := control.GetNodeOutputs(nodeID)
	if !exists || nodeOutputs.Outputs == nil {
		return nil, status.Error(codes.Unavailable, "storage node outputs not available")
	}

	// Use clip hash directly in URLs (MistServer resolves via PLAY_REWRITE trigger)
	clipHash := req.ContentId
	var protocol, endpointURL string

	if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		endpointURL = resolveTemplateURL(hlsURL, nodeOutputs.BaseURL, clipHash)
	} else {
		endpointURL = ensureTrailingSlash(nodeOutputs.BaseURL) + clipHash + ".html"
		protocol = "html"
	}

	endpoint := &pb.ViewerEndpoint{
		NodeId:      nodeID,
		BaseUrl:     nodeOutputs.BaseURL,
		Protocol:    protocol,
		Url:         endpointURL,
		GeoDistance: 0,
		LoadScore:   0,
		Outputs:     buildOutputsMapProto(nodeOutputs.BaseURL, nodeOutputs.Outputs, clipHash, false),
	}

	metadata := &pb.PlaybackMetadata{
		Status:      clipStatus,
		IsLive:      false,
		TenantId:    tenantID,
		ContentId:   req.ContentId,
		ContentType: "clip",
		ClipSource:  &streamName,
	}

	if title != "" {
		metadata.Title = &title
	}
	if description != "" {
		metadata.Description = &description
	}
	if clipDuration > 0 {
		d := int32(clipDuration / 1000)
		metadata.DurationSeconds = &d
	}
	if sizeBytes.Valid {
		metadata.RecordingSizeBytes = &sizeBytes.Int64
	}
	if createdAt.Valid {
		metadata.CreatedAt = timestamppb.New(createdAt.Time)
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoint,
		Fallbacks: []*pb.ViewerEndpoint{},
		Metadata:  metadata,
	}, nil
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
