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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoGetStreams retrieves all streams for the authenticated user
func (r *Resolver) DoGetStreams(ctx context.Context) ([]*commodorepb.Stream, error) {
	start := time.Now()

	// Record metrics
	defer func() {
		duration := time.Since(start).Seconds()
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("streams").Observe(duration)
		}
	}()

	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo streams data")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streams", "success").Inc()
		}
		return demo.GenerateStreams(), nil
	}

	resp, err := r.Clients.Commodore.ListStreams(ctx, nil, "")
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get streams")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streams", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}

	if l := loaders.FromContext(ctx); l != nil && l.Stream != nil {
		tenantID := ctxkeys.GetTenantID(ctx)
		l.Stream.PrimeMany(tenantID, resp.Streams)
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("streams", "success").Inc()
	}

	return resp.Streams, nil
}

// DoGetStream retrieves a specific stream by ID
func (r *Resolver) DoGetStream(ctx context.Context, id string) (*commodorepb.Stream, error) {
	start := time.Now()

	// Record metrics
	defer func() {
		duration := time.Since(start).Seconds()
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("stream").Observe(duration)
		}
	}()

	if middleware.IsDemoMode(ctx) {
		streams := demo.GenerateStreams()
		for _, stream := range streams {
			if stream.StreamId == id {
				if r.Metrics != nil {
					r.Metrics.Operations.WithLabelValues("stream", "success").Inc()
				}
				return stream, nil
			}
		}
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("stream", "error").Inc()
		}
		return nil, fmt.Errorf("stream not found")
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	l := loaders.FromContext(ctx)
	var stream *commodorepb.Stream
	var err error
	if l != nil && l.Stream != nil && tenantID != "" {
		stream, err = l.Stream.Load(ctx, tenantID, id)
	} else {
		stream, err = r.Clients.Commodore.GetStream(ctx, id)
	}
	if err != nil {
		r.Logger.WithError(err).WithField("stream_id", id).Error("Failed to get stream")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("stream", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get stream: %w", err)
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("stream", "success").Inc()
	}
	return stream, nil
}

// DoCreateStream creates a new stream
func (r *Resolver) DoCreateStream(ctx context.Context, input model.CreateStreamInput) (*commodorepb.Stream, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream creation")
		now := time.Now()
		description := ""
		if input.Description != nil {
			description = *input.Description
		}
		isRecording := false
		if input.Record != nil {
			isRecording = *input.Record
		}
		streamID := uuid.NewString()
		return &commodorepb.Stream{
			StreamId:     streamID,
			InternalName: "demo_stream_" + now.Format("20060102150405"),
			Title:        input.Name,
			Description:  description,
			StreamKey:    "sk_demo_" + now.Format("150405"),
			PlaybackId:   "pb_demo_" + now.Format("150405"),
			Status:       "offline",
			IsRecording:  isRecording,
			CreatedAt:    timestamppb.New(now),
			UpdatedAt:    timestamppb.New(now),
		}, nil
	}

	// Build gRPC request
	req := &commodorepb.CreateStreamRequest{
		Title: input.Name,
	}

	// Handle optional fields - proto uses non-pointer types
	if input.Description != nil {
		req.Description = *input.Description
	}
	if input.Record != nil {
		req.IsRecording = *input.Record
	}
	if input.IngestMode != nil {
		req.IngestMode = string(*input.IngestMode)
	}
	if input.PullSource != nil {
		req.PullSource = input.PullSource
	}

	// Call Commodore gRPC (context metadata carries auth)
	createResp, err := r.Clients.Commodore.CreateStream(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create stream")
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	// Fetch full stream details after creation
	stream, err := r.Clients.Commodore.GetStream(ctx, createResp.Id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream after creation")
		return nil, fmt.Errorf("failed to get stream after creation: %w", err)
	}

	changedFields := []string{"title"}
	if input.Description != nil {
		changedFields = append(changedFields, "description")
	}
	if input.Record != nil {
		changedFields = append(changedFields, "is_recording")
	}
	if input.IngestMode != nil {
		changedFields = append(changedFields, "ingest_mode")
	}
	if input.PullSource != nil {
		changedFields = append(changedFields, "pull_source")
	}
	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventStreamCreated,
		ResourceType: "stream",
		ResourceId:   stream.StreamId,
		Payload: &ipcpb.ServiceEvent_StreamChangeEvent{
			StreamChangeEvent: &ipcpb.StreamChangeEvent{
				StreamId:      stream.StreamId,
				ChangedFields: changedFields,
			},
		},
	})

	return stream, nil
}

// DoDeleteStream deletes a stream
func (r *Resolver) DoDeleteStream(ctx context.Context, id string) (model.DeleteStreamResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream deletion")
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	resp, err := r.Clients.Commodore.DeleteStream(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to delete stream")
		// Check if it's a not found error
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "Stream not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "Stream",
				ResourceID:   id,
			}, nil
		}
		return nil, fmt.Errorf("failed to delete stream: %w", err)
	}

	// The two-phase deletion saga returns "deleted" only once the serving cell acked the cleanup tombstone;
	// otherwise it is deletion_pending and converges asynchronously via the outbox worker. Emit the TERMINAL
	// stream_deleted event ONLY on actual finalization — a pending deletion must not be broadcast as done.
	finalized := resp.GetDeletionStatus() == "deleted"
	if finalized {
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventStreamDeleted,
			ResourceType: "stream",
			ResourceId:   id,
			Payload: &ipcpb.ServiceEvent_StreamChangeEvent{
				StreamChangeEvent: &ipcpb.StreamChangeEvent{
					StreamId: id,
				},
			},
		})
	}

	// Surface the saga state truthfully: the delete was ACCEPTED (success), but pending=true until the serving cell
	// acks the tombstone (the outbox worker converges it). The client must not treat a pending delete as final.
	pending := !finalized
	return &model.DeleteSuccess{Success: true, DeletedID: id, Pending: &pending}, nil
}

// DoRefreshStreamKey refreshes the stream key for a stream
func (r *Resolver) DoRefreshStreamKey(ctx context.Context, id string) (*commodorepb.Stream, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream refresh")
		streams := demo.GenerateStreams()
		for _, stream := range streams {
			if stream.StreamId == id {
				// Generate new demo stream key
				stream.StreamKey = "sk_demo_refreshed_" + time.Now().Format("20060102150405")
				return stream, nil
			}
		}
		return nil, fmt.Errorf("demo stream not found")
	}

	// Call Commodore gRPC (context metadata carries auth)
	_, err := r.Clients.Commodore.RefreshStreamKey(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to refresh stream key")
		return nil, fmt.Errorf("failed to refresh stream key: %w", err)
	}

	// Refetch the stream to get full details with new key
	stream, err := r.Clients.Commodore.GetStream(ctx, id)
	if err != nil {
		return nil, err
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventStreamKeyRotated,
		ResourceType: "stream",
		ResourceId:   id,
		Payload: &ipcpb.ServiceEvent_StreamChangeEvent{
			StreamChangeEvent: &ipcpb.StreamChangeEvent{
				StreamId:      id,
				ChangedFields: []string{"stream_key"},
			},
		},
	})

	return stream, nil
}

// DoValidateStreamKey validates a stream key
func (r *Resolver) DoValidateStreamKey(ctx context.Context, streamKey string) (*model.StreamValidation, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key validation")
		// Demo validation - validate demo stream keys
		valid := strings.HasPrefix(streamKey, "sk_demo_")
		status := model.ValidationStatusValid
		errorPtr := (*string)(nil)
		if !valid {
			status = model.ValidationStatusInvalid
			errorMsg := "Invalid demo stream key"
			errorPtr = &errorMsg
		}
		return &model.StreamValidation{
			Status:    status,
			StreamKey: streamKey,
			Error:     errorPtr,
		}, nil
	}

	// Call Commodore to validate stream key
	validation, err := r.Clients.Commodore.ValidateStreamKey(ctx, streamKey)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to validate stream key")
		// Return ERROR status instead of failing the whole query
		errorMsg := err.Error()
		return &model.StreamValidation{
			Status:    model.ValidationStatusError,
			StreamKey: streamKey,
			Error:     &errorMsg,
		}, nil
	}

	// Convert to GraphQL model
	status := model.ValidationStatusValid
	var errorPtr *string
	if !validation.Valid {
		status = model.ValidationStatusInvalid
		if validation.Error != "" {
			errorPtr = &validation.Error
		}
	}

	return &model.StreamValidation{
		Status:    status,
		StreamKey: streamKey, // Use the input streamKey since response doesn't include it
		Error:     errorPtr,
	}, nil
}

// DoCreateClip creates a new clip
func (r *Resolver) DoCreateClip(ctx context.Context, input model.CreateClipInput) (model.CreateClipResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	streamID, err := normalizeStreamID(input.StreamID)
	if err != nil {
		return nil, err
	}
	if streamID == "" {
		return nil, fmt.Errorf("streamId required")
	}

	mode := sharedpb.ClipMode_CLIP_MODE_ABSOLUTE
	if input.Mode != nil {
		switch *input.Mode {
		case model.ClipCreationModeRelative:
			mode = sharedpb.ClipMode_CLIP_MODE_RELATIVE
		case model.ClipCreationModeDuration:
			mode = sharedpb.ClipMode_CLIP_MODE_DURATION
		case model.ClipCreationModeClipNow:
			mode = sharedpb.ClipMode_CLIP_MODE_CLIP_NOW
		}
	}

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip creation")
		now := time.Now()
		description := ""
		if input.Description != nil {
			description = *input.Description
		}
		// Calculate demo timing based on mode
		var startTime, duration int64
		switch mode {
		case sharedpb.ClipMode_CLIP_MODE_CLIP_NOW:
			if input.Duration != nil {
				duration = int64(*input.Duration)
				startTime = now.Unix() - duration
			} else {
				duration = 60
				startTime = now.Unix() - 60
			}
		case sharedpb.ClipMode_CLIP_MODE_DURATION:
			if input.Duration != nil {
				duration = int64(*input.Duration)
			}
			if input.StartUnix != nil {
				startTime = int64(*input.StartUnix)
			} else if input.StartTime != nil {
				startTime = int64(*input.StartTime)
			}
		default:
			if input.StartUnix != nil {
				startTime = int64(*input.StartUnix)
			} else if input.StartTime != nil {
				startTime = int64(*input.StartTime)
			}
			if input.StopUnix != nil {
				duration = int64(*input.StopUnix) - startTime
			} else if input.EndTime != nil {
				duration = int64(*input.EndTime) - startTime
			}
		}
		modeStr := mode.String()
		return &sharedpb.ClipInfo{
			Id:          "clip_demo_" + now.Format("20060102150405"),
			StreamId:    streamID,
			Title:       input.Title,
			Description: description,
			StartTime:   startTime,
			Duration:    duration,
			ClipHash:    "pb_clip_demo_" + now.Format("150405"),
			PlaybackId:  "pl_clip_demo_" + now.Format("150405"),
			Status:      "processing",
			CreatedAt:   timestamppb.New(now),
			UpdatedAt:   timestamppb.New(now),
			ClipMode:    &modeStr,
		}, nil
	}

	// Build gRPC request
	req := &sharedpb.CreateClipRequest{
		StreamId: &streamID,
		Title:    input.Title,
		Mode:     mode,
	}

	// Handle optional description
	if input.Description != nil {
		req.Description = *input.Description
	}

	switch mode {
	case sharedpb.ClipMode_CLIP_MODE_ABSOLUTE:
		if input.StartUnix != nil {
			startUnix := int64(*input.StartUnix)
			req.StartUnix = &startUnix
		} else if input.StartTime != nil {
			startUnix := int64(*input.StartTime)
			req.StartUnix = &startUnix
		}
		if input.StopUnix != nil {
			stopUnix := int64(*input.StopUnix)
			req.StopUnix = &stopUnix
		} else if input.EndTime != nil {
			stopUnix := int64(*input.EndTime)
			req.StopUnix = &stopUnix
		}
		// Calculate duration if both are set
		if req.StartUnix != nil && req.StopUnix != nil {
			durationSec := *req.StopUnix - *req.StartUnix
			req.DurationSec = &durationSec
		}

	case sharedpb.ClipMode_CLIP_MODE_RELATIVE:
		if input.StartMedia != nil {
			startMs := int64(*input.StartMedia)
			req.StartMs = &startMs
		}
		if input.StopMedia != nil {
			stopMs := int64(*input.StopMedia)
			req.StopMs = &stopMs
		}
		// Calculate duration if both are set
		if req.StartMs != nil && req.StopMs != nil {
			durationSec := *req.StopMs - *req.StartMs
			req.DurationSec = &durationSec
		}

	case sharedpb.ClipMode_CLIP_MODE_DURATION:
		if input.StartUnix != nil {
			startUnix := int64(*input.StartUnix)
			req.StartUnix = &startUnix
		} else if input.StartMedia != nil {
			startMs := int64(*input.StartMedia)
			req.StartMs = &startMs
		}
		if input.Duration != nil {
			durationSec := int64(*input.Duration)
			req.DurationSec = &durationSec
		}

	case sharedpb.ClipMode_CLIP_MODE_CLIP_NOW:
		if input.Duration != nil {
			dur := int64(*input.Duration)
			negDur := -dur
			req.StartUnix = &negDur // Negative = relative to now
			req.DurationSec = &dur
		}
	}

	if input.ExpiresAt != nil {
		exp := int64(*input.ExpiresAt)
		req.ExpiresAt = &exp
	}

	// Call Commodore gRPC (context metadata carries auth)
	clipResp, err := r.Clients.Commodore.CreateClip(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create clip")
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.InvalidArgument, codes.FailedPrecondition, codes.Unavailable:
				return &model.ValidationError{Message: st.Message()}, nil
			case codes.NotFound:
				return &model.NotFoundError{Message: st.Message(), ResourceType: "stream", ResourceID: streamID}, nil
			case codes.PermissionDenied, codes.Unauthenticated:
				return &model.AuthError{Message: st.Message()}, nil
			}
		}
		return nil, fmt.Errorf("failed to create clip: %w", err)
	}

	// Construct ClipInfo from response (CreateClipResponse only returns status info)
	now := time.Now()
	description := ""
	if input.Description != nil {
		description = *input.Description
	}

	// Calculate resolved start/duration for response
	var startTime, duration int64
	if req.StartUnix != nil {
		startTime = *req.StartUnix
		if startTime < 0 {
			// Clip now mode - resolve to actual time
			startTime = now.Unix() + startTime
		}
	}
	if req.DurationSec != nil {
		duration = *req.DurationSec
	} else if req.StopUnix != nil && req.StartUnix != nil {
		duration = *req.StopUnix - *req.StartUnix
	}

	modeStr := mode.String()
	clipInfo := &sharedpb.ClipInfo{
		Id:          clipResp.RequestId,
		ClipHash:    clipResp.ClipHash,
		PlaybackId:  clipResp.PlaybackId,
		StreamId:    streamID,
		Title:       input.Title,
		Description: description,
		StartTime:   startTime,
		Duration:    duration,
		NodeId:      clipResp.NodeId,
		Status:      clipResp.Status,
		CreatedAt:   timestamppb.New(now),
		UpdatedAt:   timestamppb.New(now),
		ClipMode:    &modeStr,
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventClipCreated,
		ResourceType: "clip",
		ResourceId:   clipResp.RequestId,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{
			ArtifactEvent: &ipcpb.ArtifactEvent{
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
				ArtifactId:   clipResp.RequestId,
				StreamId:     streamID,
				Status:       "requested",
			},
		},
	})

	return clipInfo, nil
}

// DoGetStreamKeys retrieves all stream keys for a specific stream
func (r *Resolver) DoGetStreamKeys(ctx context.Context, streamID string) ([]*commodorepb.StreamKey, error) {
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream keys")
		now := time.Now()
		lastUsed1 := now.Add(-1 * time.Hour)
		lastUsed2 := now.Add(-3 * 24 * time.Hour)
		return []*commodorepb.StreamKey{
			{
				Id:         "sk_demo_1",
				TenantId:   "tenant_demo_1",
				UserId:     "user_demo_1",
				StreamId:   streamID,
				KeyValue:   "sk_demo_live_primary",
				KeyName:    "Primary Key",
				IsActive:   true,
				LastUsedAt: timestamppb.New(lastUsed1),
				CreatedAt:  timestamppb.New(now.Add(-7 * 24 * time.Hour)),
				UpdatedAt:  timestamppb.New(now.Add(-7 * 24 * time.Hour)),
			},
			{
				Id:         "sk_demo_2",
				TenantId:   "tenant_demo_1",
				UserId:     "user_demo_1",
				StreamId:   streamID,
				KeyValue:   "sk_demo_live_backup",
				KeyName:    "Backup Key",
				IsActive:   false,
				LastUsedAt: timestamppb.New(lastUsed2),
				CreatedAt:  timestamppb.New(now.Add(-30 * 24 * time.Hour)),
				UpdatedAt:  timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			},
		}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	keysResp, err := r.Clients.Commodore.ListStreamKeys(ctx, streamID, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream keys")
		return nil, fmt.Errorf("failed to get stream keys: %w", err)
	}

	return keysResp.StreamKeys, nil
}

// DoGetStreamKeysConnection returns a Relay-style connection for stream keys.
// Stream keys accumulate over time and can grow unbounded.
func (r *Resolver) DoGetStreamKeysConnection(ctx context.Context, streamID string, first *int, after *string, last *int, before *string) (*model.StreamKeysConnection, error) {
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream keys connection")
		keys := demo.GenerateStreamKeys(streamID)
		return r.buildStreamKeysConnectionFromSlice(keys, first, after, last, before), nil
	}

	// Build bidirectional pagination request
	paginationReq := buildStreamsPaginationRequest(first, after, last, before)

	// Call Commodore with pagination
	resp, err := r.Clients.Commodore.ListStreamKeys(ctx, streamID, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream keys")
		return nil, fmt.Errorf("failed to get stream keys: %w", err)
	}

	loaders.PreloadStreams(ctx, ctxkeys.GetTenantID(ctx), []string{streamID})

	return r.buildStreamKeysConnectionFromResponse(resp), nil
}

// buildStreamKeysConnectionFromResponse constructs a connection from gRPC response
func (r *Resolver) buildStreamKeysConnectionFromResponse(resp *commodorepb.ListStreamKeysResponse) *model.StreamKeysConnection {
	keys := resp.GetStreamKeys()
	edges := make([]*model.StreamKeyEdge, len(keys))
	for i, key := range keys {
		cursor := pagination.EncodeCursor(key.CreatedAt.AsTime(), key.Id)
		edges[i] = &model.StreamKeyEdge{
			Cursor: cursor,
			Node:   key,
		}
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	edgeNodes := make([]*commodorepb.StreamKey, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamKeysConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildStreamKeysConnectionFromSlice constructs a connection from a slice (demo mode)
func (r *Resolver) buildStreamKeysConnectionFromSlice(keys []*commodorepb.StreamKey, first *int, after *string, last *int, before *string) *model.StreamKeysConnection {
	total := len(keys)

	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	} else if last != nil {
		limit = pagination.ClampLimit(*last)
	}

	if limit > total {
		limit = total
	}

	paginatedKeys := keys
	if len(keys) > limit {
		paginatedKeys = keys[:limit]
	}

	edges := make([]*model.StreamKeyEdge, len(paginatedKeys))
	for i, key := range paginatedKeys {
		cursor := pagination.EncodeCursor(key.CreatedAt.AsTime(), key.Id)
		edges[i] = &model.StreamKeyEdge{
			Cursor: cursor,
			Node:   key,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     len(keys) > limit,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*commodorepb.StreamKey, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamKeysConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}

// DoCreateStreamKey creates a new stream key for a specific stream
func (r *Resolver) DoCreateStreamKey(ctx context.Context, streamID string, input model.CreateStreamKeyInput) (*commodorepb.StreamKey, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key creation")
		now := time.Now()
		return &commodorepb.StreamKey{
			Id:        "sk_demo_new_" + now.Format("20060102150405"),
			TenantId:  "tenant_demo_1",
			UserId:    "user_demo_1",
			StreamId:  streamID,
			KeyValue:  "sk_demo_" + now.Format("150405"),
			KeyName:   input.Name,
			IsActive:  true,
			CreatedAt: timestamppb.New(now),
			UpdatedAt: timestamppb.New(now),
		}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	keyResp, err := r.Clients.Commodore.CreateStreamKey(ctx, streamID, input.Name)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create stream key")
		return nil, fmt.Errorf("failed to create stream key: %w", err)
	}

	if keyResp.StreamKey != nil {
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventStreamKeyCreated,
			ResourceType: "stream_key",
			ResourceId:   keyResp.StreamKey.Id,
			Payload: &ipcpb.ServiceEvent_StreamKeyEvent{
				StreamKeyEvent: &ipcpb.StreamKeyEvent{
					StreamId: streamID,
					KeyId:    keyResp.StreamKey.Id,
				},
			},
		})
	}

	return keyResp.StreamKey, nil
}

// DoDeleteStreamKey deactivates a stream key
func (r *Resolver) DoDeleteStreamKey(ctx context.Context, streamID, keyID string) (model.DeleteStreamKeyResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key deletion")
		return &model.DeleteSuccess{Success: true, DeletedID: keyID}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	err = r.Clients.Commodore.DeactivateStreamKey(ctx, streamID, keyID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to deactivate stream key")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "Stream key not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "StreamKey",
				ResourceID:   keyID,
			}, nil
		}
		return nil, fmt.Errorf("failed to deactivate stream key: %w", err)
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventStreamKeyDeleted,
		ResourceType: "stream_key",
		ResourceId:   keyID,
		Payload: &ipcpb.ServiceEvent_StreamKeyEvent{
			StreamKeyEvent: &ipcpb.StreamKeyEvent{
				StreamId: streamID,
				KeyId:    keyID,
			},
		},
	})

	return &model.DeleteSuccess{Success: true, DeletedID: keyID}, nil
}

// DoGetClip retrieves a specific clip by ID
func (r *Resolver) DoGetClip(ctx context.Context, id string) (*sharedpb.ClipInfo, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip")
		now := time.Now()
		return &sharedpb.ClipInfo{
			Id:          id,
			StreamId:    "stream_demo_1",
			Title:       "Demo Clip Details",
			Description: "This is a detailed view of a demo clip with all metadata",
			StartTime:   1640995200, // Jan 1, 2022 00:00:00 UTC
			Duration:    600,        // 10 minutes
			ClipHash:    "pb_clip_" + id,
			PlaybackId:  "pl_clip_" + id,
			Status:      "ready",
			CreatedAt:   timestamppb.New(now.Add(-12 * time.Hour)),
			UpdatedAt:   timestamppb.New(now.Add(-11 * time.Hour)),
		}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	clip, err := r.Clients.Commodore.GetClip(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clip")
		return nil, fmt.Errorf("failed to get clip: %w", err)
	}
	// hasLocalCopy (the proto's has_local_copy field) is placement-derived and sourced solely from
	// Periscope — clear any catalog frozen_at-derived value so a missing overlay reports null (unknown),
	// not the durable fact.
	clip.HasLocalCopy = nil
	if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
		if l := loaders.FromContext(ctx); l != nil && l.ArtifactLifecycle != nil {
			if state, stateErr := l.ArtifactLifecycle.Load(ctx, tenantID, clip.GetClipHash()); stateErr != nil {
				r.Logger.WithError(stateErr).Warn("Failed to load clip lifecycle data")
			} else if state != nil {
				applyArtifactStorageStateToClip(clip, state)
				if state.GetStreamId() != "" {
					clip.StreamId = state.GetStreamId()
				}
				if state.SizeBytes != nil {
					size := int64(*state.SizeBytes)
					clip.SizeBytes = &size
				}
				if artifactLifecycleStageCanOverrideRegistry(clip.Status, state.Stage) {
					clip.Status = state.Stage
				}
				if state.FilePath != nil {
					clip.StoragePath = *state.FilePath
				}
			}
		}
	}

	return clip, nil
}

// DoDeleteClip deletes a clip by ID
func (r *Resolver) DoDeleteClip(ctx context.Context, id string) (model.DeleteClipResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: simulating clip deletion")
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	err := r.Clients.Commodore.DeleteClip(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to delete clip")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "Clip not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "Clip",
				ResourceID:   id,
			}, nil
		}
		return nil, fmt.Errorf("failed to delete clip: %w", err)
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventClipDeleted,
		ResourceType: "clip",
		ResourceId:   id,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{
			ArtifactEvent: &ipcpb.ArtifactEvent{
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
				ArtifactId:   id,
				Status:       "deleted",
			},
		},
	})

	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}

// Helper functions

// stringPtr returns a pointer to the string value
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DoStartDVR starts a DVR recording
func (r *Resolver) DoStartDVR(ctx context.Context, streamID string) (*sharedpb.StartDVRResponse, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: start DVR")
		return &sharedpb.StartDVRResponse{Status: "started", DvrHash: "dvr_demo_hash", PlaybackId: "pl_dvr_demo_hash"}, nil
	}

	// Build gRPC request - StreamId is *string in proto
	req := &sharedpb.StartDVRRequest{StreamId: &streamID}

	// Call Commodore gRPC (context metadata carries auth)
	res, err := r.Clients.Commodore.StartDVR(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to start DVR")
		return nil, fmt.Errorf("failed to start DVR: %w", err)
	}
	return res, nil
}

// DoStopDVR stops an ongoing DVR recording
func (r *Resolver) DoStopDVR(ctx context.Context, dvrHash string) (model.StopDVRResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: stop DVR")
		return &model.DeleteSuccess{Success: true, DeletedID: dvrHash}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	if err := r.Clients.Commodore.StopDVR(ctx, dvrHash); err != nil {
		r.Logger.WithError(err).Error("Failed to stop DVR")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "DVR recording not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "DVRRequest",
				ResourceID:   dvrHash,
			}, nil
		}
		return nil, fmt.Errorf("failed to stop DVR: %w", err)
	}
	return &model.DeleteSuccess{Success: true, DeletedID: dvrHash}, nil
}

// DoDeleteDVR deletes a DVR recording and its files
func (r *Resolver) DoDeleteDVR(ctx context.Context, dvrHash string) (model.DeleteDVRResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: delete DVR")
		return &model.DeleteSuccess{Success: true, DeletedID: dvrHash}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	deleted, err := r.Clients.Commodore.DeleteDVR(ctx, dvrHash)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to delete DVR")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "DVR recording not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "DVRRequest",
				ResourceID:   dvrHash,
			}, nil
		}
		return nil, fmt.Errorf("failed to delete DVR: %w", err)
	}

	// Only emit the delete event on a REAL deletion. An already-deleted DVR (idempotent no-op)
	// must not fire a duplicate DVR-deleted event.
	if !deleted {
		return &model.DeleteSuccess{Success: true, DeletedID: dvrHash}, nil
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventDVRDeleted,
		ResourceType: "dvr",
		ResourceId:   dvrHash,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{
			ArtifactEvent: &ipcpb.ArtifactEvent{
				ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR,
				ArtifactId:   dvrHash,
				Status:       "deleted",
			},
		},
	})

	return &model.DeleteSuccess{Success: true, DeletedID: dvrHash}, nil
}

// DoGetStreamsConnection retrieves streams with Relay-style cursor pagination
func (r *Resolver) DoGetStreamsConnection(ctx context.Context, first *int, after *string, last *int, before *string, search *string) (*model.StreamsConnection, error) {
	start := time.Now()

	defer func() {
		duration := time.Since(start).Seconds()
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("streamsConnection").Observe(duration)
		}
	}()

	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo streams connection")
		streams := demo.GenerateStreams()
		return r.buildStreamsConnectionFromSlice(streams, first, after, last, before), nil
	}

	// Build bidirectional pagination request
	paginationReq := buildStreamsPaginationRequest(first, after, last, before)

	// Call Commodore with pagination + optional name search (context carries auth)
	resp, err := r.Clients.Commodore.ListStreams(ctx, paginationReq, strings.TrimSpace(strValue(search)))
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get streams")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streamsConnection", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("streamsConnection", "success").Inc()
	}

	if l := loaders.FromContext(ctx); l != nil && l.Stream != nil {
		tenantID := ctxkeys.GetTenantID(ctx)
		l.Stream.PrimeMany(tenantID, resp.GetStreams())

		// Warm the metrics cache in one batched Periscope call when the page actually
		// requests metrics, so the per-row Stream.metrics resolver doesn't fan out into
		// an N+1 GetStreamStatus per stream. Gated on selection so pages that omit
		// metrics don't pay an extra round-trip.
		if l.StreamMetrics != nil && selectionContainsField(ctx, "metrics") {
			names := make([]string, 0, len(resp.GetStreams()))
			for _, s := range resp.GetStreams() {
				if id := s.GetStreamId(); id != "" {
					names = append(names, id)
				}
			}
			if len(names) > 0 {
				if _, err := l.StreamMetrics.LoadMany(ctx, tenantID, names); err != nil {
					r.Logger.WithError(err).Warn("Failed to batch-prime stream metrics; per-row load will retry")
				}
			}
		}
	}

	return r.buildStreamsConnectionFromResponse(resp), nil
}

// selectionContainsField reports whether the current resolver's GraphQL selection set
// requests a field with the given name anywhere beneath it (descending through nested
// fields, inline fragments, and fragment spreads). Used to decide whether expensive
// per-row enrichment is worth pre-fetching for the whole page.
func selectionContainsField(ctx context.Context, name string) bool {
	fc := graphql.GetFieldContext(ctx)
	if fc == nil {
		return false
	}
	return selectionSetContainsField(fc.Field.SelectionSet, name)
}

func selectionSetContainsField(set ast.SelectionSet, name string) bool {
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if s.Name == name {
				return true
			}
			if selectionSetContainsField(s.SelectionSet, name) {
				return true
			}
		case *ast.InlineFragment:
			if selectionSetContainsField(s.SelectionSet, name) {
				return true
			}
		case *ast.FragmentSpread:
			if s.Definition != nil && selectionSetContainsField(s.Definition.SelectionSet, name) {
				return true
			}
		}
	}
	return false
}

// buildStreamsPaginationRequest creates a proto pagination request from GraphQL params
func buildStreamsPaginationRequest(first *int, after *string, last *int, before *string) *commonpb.CursorPaginationRequest {
	req := &commonpb.CursorPaginationRequest{}

	if first != nil {
		req.First = int32(pagination.ClampLimit(*first))
	} else if last == nil {
		req.First = int32(pagination.DefaultLimit)
	}

	if after != nil && *after != "" {
		req.After = after
	}

	if last != nil {
		req.Last = int32(pagination.ClampLimit(*last))
	}

	if before != nil && *before != "" {
		req.Before = before
	}

	return req
}

// buildStreamsConnectionFromResponse constructs a StreamsConnection from a gRPC response
func (r *Resolver) buildStreamsConnectionFromResponse(resp *commodorepb.ListStreamsResponse) *model.StreamsConnection {
	streams := resp.GetStreams()
	edges := make([]*model.StreamEdge, len(streams))
	for i, stream := range streams {
		cursor := pagination.EncodeCursor(stream.CreatedAt.AsTime(), stream.StreamId)
		edges[i] = &model.StreamEdge{
			Cursor: cursor,
			Node:   stream,
		}
	}

	// Use pagination info from backend response
	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	edgeNodes := make([]*commodorepb.Stream, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildStreamsConnectionFromSlice constructs a StreamsConnection from a slice (demo mode)
func (r *Resolver) buildStreamsConnectionFromSlice(streams []*commodorepb.Stream, first *int, after *string, last *int, before *string) *model.StreamsConnection {
	total := len(streams)

	// Apply in-memory pagination for demo mode
	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	} else if last != nil {
		limit = pagination.ClampLimit(*last)
	}

	if limit > total {
		limit = total
	}

	paginatedStreams := streams
	if len(streams) > limit {
		paginatedStreams = streams[:limit]
	}

	edges := make([]*model.StreamEdge, len(paginatedStreams))
	for i, stream := range paginatedStreams {
		cursor := pagination.EncodeCursor(stream.CreatedAt.AsTime(), stream.StreamId)
		edges[i] = &model.StreamEdge{
			Cursor: cursor,
			Node:   stream,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     len(streams) > limit,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*commodorepb.Stream, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.StreamsConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}

func applyArtifactStorageStateToClip(clip *sharedpb.ClipInfo, state *periscopepb.ArtifactState) {
	if clip == nil || state == nil {
		return
	}
	// Durable lifecycle (storageLocation/syncStatus/isSynced/isFinalized) is catalog-authoritative:
	// fill from Periscope only when the catalog hasn't projected it yet, never overwrite the durable
	// value — otherwise a Periscope wipe or lag would clobber the source of truth.
	if clip.StorageLocation == nil || *clip.StorageLocation == "" {
		if state.StorageLocation != nil && *state.StorageLocation != "" {
			clip.StorageLocation = state.StorageLocation
		} else if state.FilePath != nil && *state.FilePath != "" {
			loc := "local"
			clip.StorageLocation = &loc
		}
	}
	if clip.SyncStatus == nil {
		clip.SyncStatus = state.SyncStatus
	}
	if clip.IsSynced == nil {
		clip.IsSynced = state.IsSynced
	}
	if clip.IsFinalized == nil {
		clip.IsFinalized = state.IsFinalized
	}
	// hasLocalCopy (the proto's has_local_copy field) is PLACEMENT-derived (full-local-node-copy
	// presence, origin or cache), sourced solely from Periscope. The caller clears the catalog value
	// first, so null here means unknown placement.
	if state.HasLocalCopy != nil {
		clip.HasLocalCopy = state.HasLocalCopy
	}
}
