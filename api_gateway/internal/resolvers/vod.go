package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/catalogview"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// =============================================================================
// VOD ASSET OPERATIONS
// =============================================================================

// DoCreateVodUpload initiates a multipart upload and returns presigned URLs
func (r *Resolver) DoCreateVodUpload(ctx context.Context, input model.CreateVodUploadInput) (model.CreateVodUploadResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD upload session")
		return demo.GenerateVodUploadSession(input.Filename, input.SizeBytes), nil
	}

	// Get tenant and user from context
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	userID := ctxkeys.GetUserID(ctx)

	// Build gRPC request
	req := &sharedpb.CreateVodUploadRequest{
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

		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.FailedPrecondition:
				if strings.Contains(st.Message(), "S3 storage not configured") {
					return &model.ValidationError{
						Message: "VOD uploads are not available - S3 storage not configured",
						Field:   strPtr("storage"),
					}, nil
				}
			case codes.PermissionDenied:
				if strings.Contains(st.Message(), "account suspended") {
					return &model.AuthError{Message: "Account suspended - please top up your balance to upload videos"}, nil
				}
			}
		}

		// Fallback string matching (in case upstream changes don't propagate gRPC status cleanly)
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
	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventVodUploadCreated,
		ResourceType: "vod_upload",
		ResourceId:   resp.UploadId,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{
			ArtifactEvent: &ipcpb.ArtifactEvent{
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD,
				// artifact_id is the stable VOD hash; keep upload_id in ResourceId.
				ArtifactId: resp.ArtifactHash,
				Status:     "upload_created",
			},
		},
	})

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
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD upload completion")
		return demo.GenerateVodAsset(), nil
	}

	// Get tenant from context
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Convert parts from GraphQL to proto
	protoParts := make([]*sharedpb.VodCompletedPart, len(input.Parts))
	for i, p := range input.Parts {
		protoParts[i] = &sharedpb.VodCompletedPart{
			PartNumber: int32(p.PartNumber),
			Etag:       p.Etag,
		}
	}

	// Build gRPC request
	req := &sharedpb.CompleteVodUploadRequest{
		TenantId: tenantID,
		UploadId: input.UploadID,
		Parts:    protoParts,
	}

	// Call Foghorn gRPC
	resp, err := r.Clients.Commodore.CompleteVodUpload(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to complete VOD upload")

		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				return &model.NotFoundError{
					Message:      "Upload not found or already completed",
					Code:         strPtr("NOT_FOUND"),
					ResourceType: "VodUpload",
					ResourceID:   input.UploadID,
				}, nil
			case codes.PermissionDenied:
				if strings.Contains(st.Message(), "account suspended") {
					return &model.AuthError{Message: "Account suspended - please top up your balance to complete uploads"}, nil
				}
			}
		}

		// Fallback string matching
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
	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventVodUploadCompleted,
		ResourceType: "vod_upload",
		ResourceId:   input.UploadID,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{
			ArtifactEvent: &ipcpb.ArtifactEvent{
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD,
				// artifact_id is the stable VOD hash; keep upload_id in ResourceId.
				ArtifactId: resp.GetAsset().GetArtifactHash(),
				Status:     "upload_completed",
			},
		},
	})

	return protoToVodAsset(resp.Asset), nil
}

// DoGetVodUploadStatusProto fetches server-authoritative upload status via the Commodore proxy.
// Returns the proto response so the MCP tool can map to its own JSON shape; the GraphQL
// resolver builds a model.VodUploadStatusResult union value via DoGetVodUploadStatus.
// Tenant_id is taken from the auth context, never from the wire.
func (r *Resolver) DoGetVodUploadStatusProto(ctx context.Context, uploadID string) (*sharedpb.GetVodUploadStatusResponse, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if uploadID == "" {
		return nil, fmt.Errorf("upload_id is required")
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateVodUploadStatus(uploadID), nil
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}
	resp, err := r.Clients.Commodore.GetVodUploadStatus(ctx, tenantID, uploadID)
	if err != nil {
		r.Logger.WithError(err).WithField("upload_id", uploadID).Warn("Failed to read VOD upload status")
		return nil, err
	}
	return resp, nil
}

// DoGetVodUploadStatus is the GraphQL-facing entry that wraps DoGetVodUploadStatusProto and
// maps the proto response (or gRPC error) into a VodUploadStatusResult union value.
func (r *Resolver) DoGetVodUploadStatus(ctx context.Context, uploadID string) (model.VodUploadStatusResult, error) {
	if uploadID == "" {
		return &model.ValidationError{
			Message: "upload_id is required",
			Field:   strPtr("uploadId"),
			Code:    strPtr("VALIDATION_FAILED"),
		}, nil
	}

	resp, err := r.DoGetVodUploadStatusProto(ctx, uploadID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				return &model.NotFoundError{
					Message:      "Upload not found",
					Code:         strPtr("NOT_FOUND"),
					ResourceType: "VodUpload",
					ResourceID:   uploadID,
				}, nil
			case codes.Unauthenticated, codes.PermissionDenied:
				return &model.AuthError{Message: st.Message()}, nil
			case codes.InvalidArgument:
				return &model.ValidationError{
					Message: st.Message(),
					Code:    strPtr("VALIDATION_FAILED"),
				}, nil
			}
		}
		// Surface internal errors at the schema level rather than masking as NotFound.
		return nil, fmt.Errorf("failed to read VOD upload status: %w", err)
	}

	out := &model.VodUploadStatus{
		UploadID:      resp.UploadId,
		State:         protoToVodAssetStatus(resp.State),
		UploadedParts: resp.UploadedParts,
		MissingParts:  int32SliceToIntSlice(resp.MissingParts),
	}
	if resp.ExpiresAt != nil {
		t := resp.ExpiresAt.AsTime()
		out.ExpiresAt = &t
	}
	if resp.RetentionUntil != nil {
		t := resp.RetentionUntil.AsTime()
		out.RetentionUntil = &t
	}
	if resp.LastErrorCode != "" {
		s := resp.LastErrorCode
		out.LastErrorCode = &s
	}
	if resp.ArtifactHash != "" {
		s := resp.ArtifactHash
		out.ArtifactHash = &s
	}
	if resp.PlaybackId != "" {
		s := resp.PlaybackId
		out.PlaybackID = &s
	}
	return out, nil
}

func protoToVodAssetStatus(s sharedpb.VodStatus) model.VodAssetStatus {
	switch s {
	case sharedpb.VodStatus_VOD_STATUS_UPLOADING:
		return model.VodAssetStatusUploading
	case sharedpb.VodStatus_VOD_STATUS_PROCESSING:
		return model.VodAssetStatusProcessing
	case sharedpb.VodStatus_VOD_STATUS_READY:
		return model.VodAssetStatusReady
	case sharedpb.VodStatus_VOD_STATUS_FAILED:
		return model.VodAssetStatusFailed
	case sharedpb.VodStatus_VOD_STATUS_DELETED:
		return model.VodAssetStatusDeleted
	case sharedpb.VodStatus_VOD_STATUS_EXPIRED:
		return model.VodAssetStatusExpired
	default:
		return model.VodAssetStatusUploading
	}
}

func int32SliceToIntSlice(in []int32) []int {
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}

// DoAbortVodUpload cancels an in-progress multipart upload
func (r *Resolver) DoAbortVodUpload(ctx context.Context, uploadID string) (model.AbortVodUploadResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD upload abort")
		return &model.DeleteSuccess{Success: true, DeletedID: uploadID}, nil
	}

	// Get tenant from context
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Call Foghorn gRPC
	_, err := r.Clients.Commodore.AbortVodUpload(ctx, tenantID, uploadID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to abort VOD upload")

		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				return &model.NotFoundError{
					Message:      "Upload not found or already completed",
					Code:         strPtr("NOT_FOUND"),
					ResourceType: "VodUpload",
					ResourceID:   uploadID,
				}, nil
			case codes.PermissionDenied:
				if strings.Contains(st.Message(), "account suspended") {
					return &model.AuthError{Message: "Account suspended - please top up your balance to manage uploads"}, nil
				}
			}
		}

		// Fallback string matching
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

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventVodUploadAborted,
		ResourceType: "vod_upload",
		ResourceId:   uploadID,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{
			ArtifactEvent: &ipcpb.ArtifactEvent{
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD,
				// artifact_id is the stable VOD hash; keep upload_id in ResourceId.
				// Abort doesn't return the hash, so leave artifact_id empty.
				ArtifactId: "",
				Status:     "upload_aborted",
			},
		},
	})

	return &model.DeleteSuccess{Success: true, DeletedID: uploadID}, nil
}

// DoDeleteVodAsset deletes a VOD asset
func (r *Resolver) DoDeleteVodAsset(ctx context.Context, id string) (model.DeleteVodAssetResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD asset deletion")
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	// Get tenant from context
	tenantID := ctxkeys.GetTenantID(ctx)
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

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventVodAssetDeleted,
		ResourceType: "vod_asset",
		ResourceId:   id,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{
			ArtifactEvent: &ipcpb.ArtifactEvent{
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD,
				ArtifactId:   id,
				Status:       "deleted",
			},
		},
	})

	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}

// DoGetVodAsset retrieves a single VOD asset by artifact hash. An exact-hash, kind-restricted
// ListStorageArtifacts lookup is the source of truth for durable facts (derived lifecycle status,
// duration, finalized track summary, sync state); the live Periscope overlay then fills the
// transient has_local_copy placement signal, mirroring the connection resolver.
func (r *Resolver) DoGetVodAsset(ctx context.Context, id string) (*model.VodAsset, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo VOD asset")
		return demo.GenerateVodAsset(), nil
	}

	// Get tenant from context
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Restrict to VOD-kind artifacts. The catalog unions clips/DVRs/chapters/VODs, so without a
	// kind filter a clip/DVR hash could be returned through the VodAsset contract. A VodAsset
	// legitimately covers uploaded VODs AND finalized DVR chapters (both playable, origin_type
	// dvr_chapter), so both kinds are accepted here.
	resp, err := r.Clients.Commodore.ListStorageArtifacts(ctx, &commodorepb.ListStorageArtifactsRequest{
		TenantId:       tenantID,
		ArtifactHashes: []string{id},
		Kinds:          []string{"vod", "chapter"},
		Limit:          1,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get VOD asset")
		return nil, fmt.Errorf("failed to get VOD asset: %w", err)
	}
	arts := resp.GetArtifacts()
	if len(arts) == 0 {
		return nil, nil // GraphQL nullable field — return nil for not found
	}
	artifact := arts[0]

	// hasLocalCopy is a PLACEMENT fact (full-local-node-copy presence, origin or cache), carried on the
	// proto's has_local_copy field and sourced SOLELY from Periscope — the catalog's frozen_at-derived
	// value must never masquerade as placement. Clear it first so a missing/failed overlay reports null
	// (unknown placement), then overlay from actual placement.
	artifact.HasLocalCopy = nil
	if l := loaders.FromContext(ctx); l != nil && l.ArtifactLifecycle != nil {
		if state, lerr := l.ArtifactLifecycle.Load(ctx, tenantID, artifact.GetArtifactHash()); lerr != nil {
			r.Logger.WithError(lerr).Warn("VOD asset lifecycle overlay failed; using durable catalog lifecycle")
		} else if state != nil {
			applyArtifactStorageStateToStorageArtifact(artifact, state)
		}
	}
	return storageArtifactToVodAsset(artifact), nil
}

// storageStatusToVodAssetStatus maps the catalog's derived lifecycle status string onto the
// GraphQL VOD status enum.
func storageStatusToVodAssetStatus(s string) model.VodAssetStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ready", "completed", "complete", "done", "synced":
		return model.VodAssetStatusReady
	case "processing":
		return model.VodAssetStatusProcessing
	case "failed", "error":
		return model.VodAssetStatusFailed
	case "deleted", "expired", "evicted":
		return model.VodAssetStatusDeleted
	default:
		return model.VodAssetStatusUploading
	}
}

// storageArtifactToVodAsset maps a canonical catalog row onto the GraphQL VodAsset. Filename comes
// from secondary_label (the catalog's filename/content-type projection); description and
// error_message are projected on the catalog row (the latter from foghorn.artifacts.error_message).
func storageArtifactToVodAsset(a *commodorepb.StorageArtifactInfo) *model.VodAsset {
	if a == nil {
		return nil
	}
	vodID := a.GetArtifactHash()
	if vodID == "" {
		vodID = a.GetId()
	}
	resolution, videoCodec, audioCodec, bitrateKbps := catalogview.TrackSummary(a.GetTracks())
	asset := &model.VodAsset{
		ID:              globalid.Encode(globalid.TypeVodAsset, vodID),
		ArtifactHash:    a.GetArtifactHash(),
		PlaybackID:      a.GetPlaybackId(),
		Status:          storageStatusToVodAssetStatus(a.GetStatus()),
		StorageLocation: a.GetStorageLocation(),
		// hasLocalCopy is a placement fact: preserve the pointer so nil = live placement overlay
		// unavailable (unknown), NOT "no local copy". isSynced/isFinalized are durable catalog facts.
		IsSynced:        a.GetIsSynced(),
		IsFinalized:     a.GetIsFinalized(),
		HasLocalCopy:    a.HasLocalCopy,
		CreatedAt:       a.GetCreatedAt().AsTime(),
		UpdatedAt:       a.GetUpdatedAt().AsTime(),
		Resolution:      resolution,
		VideoCodec:      videoCodec,
		AudioCodec:      audioCodec,
		BitrateKbps:     bitrateKbps,
		ThumbnailAssets: a.GetThumbnailAssets(),
	}
	if v := a.GetSyncStatus(); v != "" {
		asset.SyncStatus = &v
	}
	if v := a.GetStreamId(); v != "" {
		asset.StreamID = &v
	}
	if v := a.GetOriginType(); v != "" {
		asset.OriginType = &v
	}
	if v := a.GetOriginId(); v != "" {
		asset.OriginID = &v
	}
	if v := a.GetTitle(); v != "" {
		asset.Title = &v
	}
	if v := a.GetSecondaryLabel(); v != "" {
		asset.Filename = &v
	}
	if v := a.GetDescription(); v != "" {
		asset.Description = &v
	}
	if v := a.GetErrorMessage(); v != "" {
		asset.ErrorMessage = &v
	}
	if a.SizeBytes != nil {
		sz := float64(a.GetSizeBytes())
		asset.SizeBytes = &sz
	}
	if a.DurationMs != nil {
		d := int(a.GetDurationMs())
		asset.DurationMs = &d
	}
	if a.ExpiresAt != nil {
		t := a.GetExpiresAt().AsTime()
		asset.ExpiresAt = &t
	}
	asset.EffectiveRetention = buildVodEffectiveRetention(asset.ExpiresAt, a.RetentionSource)
	return asset
}

// protoToVodAsset converts a proto VodAssetInfo to a GraphQL VodAsset
func protoToVodAsset(p *sharedpb.VodAssetInfo) *model.VodAsset {
	if p == nil {
		return nil
	}

	// Map proto status to GraphQL enum
	var status model.VodAssetStatus
	switch p.Status {
	case sharedpb.VodStatus_VOD_STATUS_UPLOADING:
		status = model.VodAssetStatusUploading
	case sharedpb.VodStatus_VOD_STATUS_PROCESSING:
		status = model.VodAssetStatusProcessing
	case sharedpb.VodStatus_VOD_STATUS_READY:
		status = model.VodAssetStatusReady
	case sharedpb.VodStatus_VOD_STATUS_FAILED:
		status = model.VodAssetStatusFailed
	case sharedpb.VodStatus_VOD_STATUS_DELETED:
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
	// The upload-completion response carries a concrete has_local_copy (plain bool) full-local-node-copy
	// signal; the GraphQL field is nullable, so surface the known value as a non-nil pointer.
	hasLocalCopy := p.HasLocalCopy
	asset := &model.VodAsset{
		ID:              globalid.Encode(globalid.TypeVodAsset, vodID),
		ArtifactHash:    p.ArtifactHash,
		PlaybackID:      "",
		Status:          status,
		StorageLocation: p.StorageLocation,
		IsSynced:        p.GetIsSynced(),
		IsFinalized:     p.GetIsFinalized(),
		HasLocalCopy:    &hasLocalCopy,
		CreatedAt:       p.CreatedAt.AsTime(),
		UpdatedAt:       p.UpdatedAt.AsTime(),
	}
	if p.GetSyncStatus() != "" {
		syncStatus := p.GetSyncStatus()
		asset.SyncStatus = &syncStatus
	}

	// Optional fields
	if p.PlaybackId != nil && *p.PlaybackId != "" {
		asset.PlaybackID = *p.PlaybackId
	}
	if p.StreamId != nil && *p.StreamId != "" {
		asset.StreamID = p.StreamId
	}
	if p.OriginType != nil && *p.OriginType != "" {
		asset.OriginType = p.OriginType
	}
	if p.OriginId != nil && *p.OriginId != "" {
		asset.OriginID = p.OriginId
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
	if p.ThumbnailAssets != nil {
		asset.ThumbnailAssets = p.ThumbnailAssets
	}
	asset.EffectiveRetention = buildVodEffectiveRetention(asset.ExpiresAt, p.RetentionSource)

	return asset
}

// buildVodEffectiveRetention maps the asset's retention_until + the
// cascade-source string stored on commodore.vod_assets.retention_source
// into the GraphQL EffectiveRetention shape. Returns nil when no
// retention_until is set (keep-forever VODs).
func buildVodEffectiveRetention(expiresAt *time.Time, source *string) *model.EffectiveRetention {
	if expiresAt == nil {
		return nil
	}
	until := *expiresAt
	days := int((time.Until(until) + 24*time.Hour - 1) / (24 * time.Hour))
	if days < 0 {
		days = 0
	}
	return &model.EffectiveRetention{
		RetentionDays:  days,
		RetentionUntil: &until,
		Source:         vodRetentionSource(source),
	}
}

// vodRetentionSource maps the cascade-source string (commodore writes
// values like "tenant_default" / "per_asset_override" / "tier_entitlement")
// onto the GraphQL enum. Unknown / empty strings fall back to
// TIER_ENTITLEMENT — the conservative default for assets that
// pre-date the retention-source column.
func vodRetentionSource(s *string) model.RetentionSource {
	if s == nil {
		return model.RetentionSourceTierEntitlement
	}
	switch *s {
	case "tenant_default":
		return model.RetentionSourceTenantDefault
	case "per_stream_override":
		return model.RetentionSourcePerStreamOverride
	case "per_asset_override":
		return model.RetentionSourcePerAssetOverride
	case "tier_entitlement":
		return model.RetentionSourceTierEntitlement
	default:
		return model.RetentionSourceTierEntitlement
	}
}
