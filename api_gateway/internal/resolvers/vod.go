package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/globalid"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"
)

// =============================================================================
// VOD ASSET OPERATIONS
// =============================================================================

// DoCreateVodUpload initiates a multipart upload and returns presigned URLs
func (r *Resolver) DoCreateVodUpload(ctx context.Context, input model.CreateVodUploadInput) (model.CreateVodUploadResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD upload session")
		return demo.GenerateVodUploadSession(input.Filename, input.SizeBytes), nil
	}

	// Get tenant and user from context
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	userID := ""
	if v, ok := ctx.Value("user_id").(string); ok {
		userID = v
	}

	// Build gRPC request
	req := &pb.CreateVodUploadRequest{
		TenantId:  tenantID,
		UserId:    userID,
		Filename:  input.Filename,
		SizeBytes: int64(input.SizeBytes),
	}
	if input.ContentType != nil {
		req.ContentType = input.ContentType
	}
	if input.Title != nil {
		req.Title = input.Title
	}
	if input.Description != nil {
		req.Description = input.Description
	}

	// Call Foghorn gRPC
	resp, err := r.Clients.Commodore.CreateVodUpload(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create VOD upload")
		if strings.Contains(err.Error(), "S3 storage not configured") {
			return &model.ValidationError{
				Message: "VOD uploads are not available - S3 storage not configured",
				Field:   strPtr("storage"),
			}, nil
		}
		if strings.Contains(err.Error(), "account suspended") {
			return &model.AuthError{Message: "Account suspended - please top up your balance to upload videos"}, nil
		}
		return nil, fmt.Errorf("failed to create VOD upload: %w", err)
	}

	// Convert to GraphQL model
	return &model.VodUploadSession{
		ID:           resp.UploadId,
		ArtifactID:   resp.ArtifactId,
		ArtifactHash: resp.ArtifactHash,
		PlaybackID:   resp.PlaybackId,
		PartSize:     float64(resp.PartSize),
		Parts:        resp.Parts, // VodUploadPart autobind
		ExpiresAt:    resp.ExpiresAt.AsTime(),
	}, nil
}

// DoCompleteVodUpload finalizes a multipart upload after all parts are uploaded
func (r *Resolver) DoCompleteVodUpload(ctx context.Context, input model.CompleteVodUploadInput) (model.CompleteVodUploadResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD upload completion")
		return demo.GenerateVodAsset(), nil
	}

	// Get tenant from context
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Convert parts from GraphQL to proto
	protoParts := make([]*pb.VodCompletedPart, len(input.Parts))
	for i, p := range input.Parts {
		protoParts[i] = &pb.VodCompletedPart{
			PartNumber: int32(p.PartNumber),
			Etag:       p.Etag,
		}
	}

	// Build gRPC request
	req := &pb.CompleteVodUploadRequest{
		TenantId: tenantID,
		UploadId: input.UploadID,
		Parts:    protoParts,
	}

	// Call Foghorn gRPC
	resp, err := r.Clients.Commodore.CompleteVodUpload(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to complete VOD upload")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "Upload not found or already completed",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "VodUpload",
				ResourceID:   input.UploadID,
			}, nil
		}
		if strings.Contains(err.Error(), "account suspended") {
			return &model.AuthError{Message: "Account suspended - please top up your balance to complete uploads"}, nil
		}
		return nil, fmt.Errorf("failed to complete VOD upload: %w", err)
	}

	// Convert to GraphQL model
	return protoToVodAsset(resp.Asset), nil
}

// DoAbortVodUpload cancels an in-progress multipart upload
func (r *Resolver) DoAbortVodUpload(ctx context.Context, uploadID string) (model.AbortVodUploadResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD upload abort")
		return &model.DeleteSuccess{Success: true, DeletedID: uploadID}, nil
	}

	// Get tenant from context
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Call Foghorn gRPC
	_, err := r.Clients.Commodore.AbortVodUpload(ctx, tenantID, uploadID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to abort VOD upload")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "Upload not found or already completed",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "VodUpload",
				ResourceID:   uploadID,
			}, nil
		}
		if strings.Contains(err.Error(), "account suspended") {
			return &model.AuthError{Message: "Account suspended - please top up your balance to manage uploads"}, nil
		}
		return nil, fmt.Errorf("failed to abort VOD upload: %w", err)
	}

	return &model.DeleteSuccess{Success: true, DeletedID: uploadID}, nil
}

// DoDeleteVodAsset deletes a VOD asset
func (r *Resolver) DoDeleteVodAsset(ctx context.Context, id string) (model.DeleteVodAssetResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD asset deletion")
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	// Get tenant from context
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Call Foghorn gRPC
	_, err := r.Clients.Commodore.DeleteVodAsset(ctx, tenantID, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to delete VOD asset")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "VOD asset not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "VodAsset",
				ResourceID:   id,
			}, nil
		}
		return nil, fmt.Errorf("failed to delete VOD asset: %w", err)
	}

	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}

// DoGetVodAsset retrieves a single VOD asset by ID
// Business metadata comes from Commodore, lifecycle data from Periscope
func (r *Resolver) DoGetVodAsset(ctx context.Context, id string) (*model.VodAsset, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD asset")
		return demo.GenerateVodAsset(), nil
	}

	// Get tenant from context
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// 1. Get business metadata from Commodore
	asset, err := r.Clients.Commodore.GetVodAsset(ctx, tenantID, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get VOD asset")
		if strings.Contains(err.Error(), "not found") {
			return nil, nil // GraphQL nullable field - return nil for not found
		}
		return nil, fmt.Errorf("failed to get VOD asset: %w", err)
	}

	// 2. Enrich with lifecycle data from Periscope via ArtifactLifecycleLoader
	if l := loaders.FromContext(ctx); l != nil && l.ArtifactLifecycle != nil {
		state, err := l.ArtifactLifecycle.Load(ctx, tenantID, asset.ArtifactHash)
		if err != nil {
			r.Logger.WithError(err).Warn("Failed to load VOD lifecycle data")
		} else if state != nil {
			// Merge lifecycle data into proto
			enrichVodAssetWithLifecycle(asset, state)
		}
	}

	return protoToVodAsset(asset), nil
}

// DoGetVodAssetsConnection retrieves VOD assets with Relay-style cursor pagination
// Business metadata comes from Commodore, lifecycle data from Periscope
func (r *Resolver) DoGetVodAssetsConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.VodAssetsConnection, error) {
	// Build cursor pagination request with bidirectional support
	paginationReq := &pb.CursorPaginationRequest{
		First: int32(pagination.DefaultLimit),
	}
	if first != nil {
		paginationReq.First = int32(pagination.ClampLimit(*first))
	}
	if after != nil && *after != "" {
		paginationReq.After = after
	}
	if last != nil {
		paginationReq.Last = int32(pagination.ClampLimit(*last))
	}
	if before != nil && *before != "" {
		paginationReq.Before = before
	}

	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD assets connection")
		assets := demo.GenerateVodAssets()
		return r.buildVodAssetsConnection(assets, nil), nil
	}

	// Get tenant from context
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// 1. Get business metadata from Commodore
	resp, err := r.Clients.Commodore.ListVodAssets(ctx, tenantID, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list VOD assets")
		return nil, fmt.Errorf("failed to list VOD assets: %w", err)
	}

	// 2. Batch enrich with lifecycle data from Periscope via ArtifactLifecycleLoader
	if l := loaders.FromContext(ctx); l != nil && l.ArtifactLifecycle != nil && len(resp.Assets) > 0 {
		hashes := make([]string, len(resp.Assets))
		for i, a := range resp.Assets {
			hashes[i] = a.ArtifactHash
		}

		states, err := l.ArtifactLifecycle.LoadMany(ctx, tenantID, hashes)
		if err != nil {
			r.Logger.WithError(err).Warn("Failed to load VOD lifecycle data")
		} else {
			// 3. Merge lifecycle data into each asset
			for _, asset := range resp.Assets {
				if state, ok := states[asset.ArtifactHash]; ok && state != nil {
					enrichVodAssetWithLifecycle(asset, state)
				}
			}
		}
	}

	// Convert proto assets to model assets
	assets := make([]*model.VodAsset, len(resp.Assets))
	for i, a := range resp.Assets {
		assets[i] = protoToVodAsset(a)
	}

	return r.buildVodAssetsConnection(assets, resp.Pagination), nil
}

// buildVodAssetsConnection constructs a VodAssetsConnection from a slice of assets
func (r *Resolver) buildVodAssetsConnection(assets []*model.VodAsset, paginationResp *pb.CursorPaginationResponse) *model.VodAssetsConnection {
	edges := make([]*model.VodAssetEdge, len(assets))
	for i, asset := range assets {
		cursor := pagination.EncodeCursor(asset.CreatedAt, asset.ArtifactHash)
		edges[i] = &model.VodAssetEdge{
			Cursor: cursor,
			Node:   asset,
		}
	}

	// Build page info from proto pagination response
	pageInfo := &model.PageInfo{
		HasPreviousPage: paginationResp != nil && paginationResp.HasPreviousPage,
		HasNextPage:     paginationResp != nil && paginationResp.HasNextPage,
	}
	if paginationResp != nil {
		pageInfo.StartCursor = paginationResp.StartCursor
		pageInfo.EndCursor = paginationResp.EndCursor
	} else if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	totalCount := 0
	if paginationResp != nil {
		totalCount = int(paginationResp.TotalCount)
	} else {
		totalCount = len(assets)
	}

	edgeNodes := make([]*model.VodAsset, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.VodAssetsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}
}

// protoToVodAsset converts a proto VodAssetInfo to a GraphQL VodAsset
func protoToVodAsset(p *pb.VodAssetInfo) *model.VodAsset {
	if p == nil {
		return nil
	}

	// Map proto status to GraphQL enum
	var status model.VodAssetStatus
	switch p.Status {
	case pb.VodStatus_VOD_STATUS_UPLOADING:
		status = model.VodAssetStatusUploading
	case pb.VodStatus_VOD_STATUS_PROCESSING:
		status = model.VodAssetStatusProcessing
	case pb.VodStatus_VOD_STATUS_READY:
		status = model.VodAssetStatusReady
	case pb.VodStatus_VOD_STATUS_FAILED:
		status = model.VodAssetStatusFailed
	case pb.VodStatus_VOD_STATUS_DELETED:
		status = model.VodAssetStatusDeleted
	default:
		status = model.VodAssetStatusUploading // default fallback
	}

	if p.ExpiresAt != nil && p.ExpiresAt.AsTime().Before(time.Now()) {
		status = model.VodAssetStatusDeleted
	}

	vodID := p.ArtifactHash
	if vodID == "" {
		vodID = p.Id
	}
	asset := &model.VodAsset{
		ID:              globalid.Encode(globalid.TypeVodAsset, vodID),
		ArtifactHash:    p.ArtifactHash,
		PlaybackID:      "",
		Status:          status,
		StorageLocation: p.StorageLocation,
		CreatedAt:       p.CreatedAt.AsTime(),
		UpdatedAt:       p.UpdatedAt.AsTime(),
	}

	// Optional fields
	if p.PlaybackId != nil && *p.PlaybackId != "" {
		asset.PlaybackID = *p.PlaybackId
	}
	if p.Title != "" {
		asset.Title = &p.Title
	}
	if p.Description != "" {
		asset.Description = &p.Description
	}
	if p.Filename != "" {
		asset.Filename = &p.Filename
	}
	if p.SizeBytes != nil {
		size := float64(*p.SizeBytes)
		asset.SizeBytes = &size
	}
	if p.DurationMs != nil {
		dur := int(*p.DurationMs)
		asset.DurationMs = &dur
	}
	if p.Resolution != nil {
		asset.Resolution = p.Resolution
	}
	if p.VideoCodec != nil {
		asset.VideoCodec = p.VideoCodec
	}
	if p.AudioCodec != nil {
		asset.AudioCodec = p.AudioCodec
	}
	if p.BitrateKbps != nil {
		br := int(*p.BitrateKbps)
		asset.BitrateKbps = &br
	}
	if p.ExpiresAt != nil {
		t := p.ExpiresAt.AsTime()
		asset.ExpiresAt = &t
	}
	if p.ErrorMessage != nil {
		asset.ErrorMessage = p.ErrorMessage
	}

	return asset
}

// enrichVodAssetWithLifecycle merges lifecycle data from Periscope into VOD proto
// Maps ArtifactState fields to VodAssetInfo fields
func enrichVodAssetWithLifecycle(asset *pb.VodAssetInfo, state *pb.ArtifactState) {
	if asset == nil || state == nil {
		return
	}

	// Map stage to VodStatus
	switch state.Stage {
	case "requested", "queued":
		asset.Status = pb.VodStatus_VOD_STATUS_UPLOADING
	case "processing":
		asset.Status = pb.VodStatus_VOD_STATUS_PROCESSING
	case "completed":
		asset.Status = pb.VodStatus_VOD_STATUS_READY
	case "failed":
		asset.Status = pb.VodStatus_VOD_STATUS_FAILED
	case "deleted":
		asset.Status = pb.VodStatus_VOD_STATUS_DELETED
	}

	// Size from lifecycle (actual file size, not expected)
	if state.SizeBytes != nil {
		sizeInt64 := int64(*state.SizeBytes)
		asset.SizeBytes = &sizeInt64
	}

	// Storage location from S3 URL presence
	if state.S3Url != nil && *state.S3Url != "" {
		asset.StorageLocation = "s3"
	} else if state.FilePath != nil && *state.FilePath != "" {
		asset.StorageLocation = "local"
	}

	// Error message
	if state.ErrorMessage != nil {
		asset.ErrorMessage = state.ErrorMessage
	}

	// Expiration from lifecycle
	if state.ExpiresAt != nil {
		asset.ExpiresAt = state.ExpiresAt
	}
}
