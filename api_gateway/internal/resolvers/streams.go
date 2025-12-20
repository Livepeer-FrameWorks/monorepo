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
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoGetStreams retrieves all streams for the authenticated user
func (r *Resolver) DoGetStreams(ctx context.Context) ([]*pb.Stream, error) {
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

	// gRPC uses context metadata for auth (set by userContextInterceptor)
	streamsResp, err := r.getStreamsMemoized(ctx)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get streams")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streams", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("streams", "success").Inc()
	}

	return streamsResp, nil
}

// DoGetStream retrieves a specific stream by ID
func (r *Resolver) DoGetStream(ctx context.Context, id string) (*pb.Stream, error) {
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
			if stream.InternalName == id {
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

	// gRPC uses context metadata for auth
	stream, err := r.getStreamMemoized(ctx, id)
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
func (r *Resolver) DoCreateStream(ctx context.Context, input model.CreateStreamInput) (*pb.Stream, error) {
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
		return &pb.Stream{
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
	req := &pb.CreateStreamRequest{
		Title: input.Name,
	}

	// Handle optional fields - proto uses non-pointer types
	if input.Description != nil {
		req.Description = *input.Description
	}
	if input.Record != nil {
		req.IsRecording = *input.Record
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

	return stream, nil
}

// DoDeleteStream deletes a stream
func (r *Resolver) DoDeleteStream(ctx context.Context, id string) (model.DeleteStreamResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream deletion")
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	_, err := r.Clients.Commodore.DeleteStream(ctx, id)
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

	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}

// DoRefreshStreamKey refreshes the stream key for a stream
func (r *Resolver) DoRefreshStreamKey(ctx context.Context, id string) (*pb.Stream, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream refresh")
		streams := demo.GenerateStreams()
		for _, stream := range streams {
			if stream.InternalName == id {
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
	return r.Clients.Commodore.GetStream(ctx, id)
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
func (r *Resolver) DoCreateClip(ctx context.Context, input model.CreateClipInput) (*pb.ClipInfo, error) {
	// Determine mode (default to ABSOLUTE for backward compatibility)
	mode := pb.ClipMode_CLIP_MODE_ABSOLUTE
	if input.Mode != nil {
		switch *input.Mode {
		case model.ClipCreationModeRelative:
			mode = pb.ClipMode_CLIP_MODE_RELATIVE
		case model.ClipCreationModeDuration:
			mode = pb.ClipMode_CLIP_MODE_DURATION
		case model.ClipCreationModeClipNow:
			mode = pb.ClipMode_CLIP_MODE_CLIP_NOW
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
		case pb.ClipMode_CLIP_MODE_CLIP_NOW:
			if input.Duration != nil {
				duration = int64(*input.Duration)
				startTime = now.Unix() - duration
			} else {
				duration = 60
				startTime = now.Unix() - 60
			}
		case pb.ClipMode_CLIP_MODE_DURATION:
			if input.Duration != nil {
				duration = int64(*input.Duration)
			}
			if input.StartUnix != nil {
				startTime = int64(*input.StartUnix)
			} else if input.StartTime != nil {
				startTime = int64(*input.StartTime)
			}
		default:
			// ABSOLUTE or legacy
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
		return &pb.ClipInfo{
			Id:          "clip_demo_" + now.Format("20060102150405"),
			StreamName:  input.Stream,
			Title:       input.Title,
			Description: description,
			StartTime:   startTime,
			Duration:    duration,
			ClipHash:    "pb_clip_demo_" + now.Format("150405"),
			Status:      "processing",
			CreatedAt:   timestamppb.New(now),
			UpdatedAt:   timestamppb.New(now),
			ClipMode:    &modeStr,
		}, nil
	}

	// Build gRPC request
	req := &pb.CreateClipRequest{
		InternalName: input.Stream,
		Title:        input.Title,
		Mode:         mode,
	}

	// Handle optional description
	if input.Description != nil {
		req.Description = *input.Description
	}

	// Populate timing fields based on mode
	switch mode {
	case pb.ClipMode_CLIP_MODE_ABSOLUTE:
		// Support legacy startTime/endTime or new startUnix/stopUnix
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

	case pb.ClipMode_CLIP_MODE_RELATIVE:
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

	case pb.ClipMode_CLIP_MODE_DURATION:
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

	case pb.ClipMode_CLIP_MODE_CLIP_NOW:
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
	return &pb.ClipInfo{
		Id:          clipResp.RequestId,
		ClipHash:    clipResp.ClipHash,
		StreamName:  input.Stream,
		Title:       input.Title,
		Description: description,
		StartTime:   startTime,
		Duration:    duration,
		NodeId:      clipResp.NodeId,
		Status:      clipResp.Status,
		CreatedAt:   timestamppb.New(now),
		UpdatedAt:   timestamppb.New(now),
		ClipMode:    &modeStr,
	}, nil
}

// === STREAM KEYS MANAGEMENT ===

// DoGetStreamKeys retrieves all stream keys for a specific stream
func (r *Resolver) DoGetStreamKeys(ctx context.Context, streamID string) ([]*pb.StreamKey, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream keys")
		now := time.Now()
		lastUsed1 := now.Add(-1 * time.Hour)
		lastUsed2 := now.Add(-3 * 24 * time.Hour)
		return []*pb.StreamKey{
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
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream keys connection")
		now := time.Now()
		lastUsed1 := now.Add(-1 * time.Hour)
		lastUsed2 := now.Add(-3 * 24 * time.Hour)
		keys := []*pb.StreamKey{
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
		}
		edges := make([]*model.StreamKeyEdge, len(keys))
		for i, key := range keys {
			cursor := pagination.EncodeCursor(key.CreatedAt.AsTime(), key.Id)
			edges[i] = &model.StreamKeyEdge{
				Cursor: cursor,
				Node:   key,
			}
		}
		pageInfo := &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
		}
		if len(edges) > 0 {
			pageInfo.StartCursor = &edges[0].Cursor
			pageInfo.EndCursor = &edges[len(edges)-1].Cursor
		}
		return &model.StreamKeysConnection{
			Edges:      edges,
			PageInfo:   pageInfo,
			TotalCount: len(keys),
		}, nil
	}

	// Fetch all stream keys (Commodore doesn't support pagination for keys yet)
	keysResp, err := r.Clients.Commodore.ListStreamKeys(ctx, streamID, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream keys")
		return nil, fmt.Errorf("failed to get stream keys: %w", err)
	}

	keys := keysResp.StreamKeys
	totalCount := len(keys)

	// Client-side pagination
	startIdx := 0
	endIdx := totalCount
	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Handle cursor-based pagination
	if after != nil && *after != "" {
		cursor, err := pagination.DecodeCursor(*after)
		if err == nil && cursor != nil {
			for i, key := range keys {
				if key.CreatedAt.AsTime().Equal(cursor.Timestamp) || key.CreatedAt.AsTime().After(cursor.Timestamp) {
					startIdx = i + 1
					break
				}
			}
		}
	}

	if startIdx+limit < endIdx {
		endIdx = startIdx + limit
	}

	// Slice keys
	if startIdx >= totalCount {
		keys = []*pb.StreamKey{}
	} else {
		keys = keys[startIdx:endIdx]
	}

	// Build edges
	edges := make([]*model.StreamKeyEdge, len(keys))
	for i, key := range keys {
		cursor := pagination.EncodeCursor(key.CreatedAt.AsTime(), key.Id)
		edges[i] = &model.StreamKeyEdge{
			Cursor: cursor,
			Node:   key,
		}
	}

	// Build page info
	pageInfo := &model.PageInfo{
		HasPreviousPage: startIdx > 0,
		HasNextPage:     endIdx < totalCount,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.StreamKeysConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoCreateStreamKey creates a new stream key for a specific stream
func (r *Resolver) DoCreateStreamKey(ctx context.Context, streamID string, input model.CreateStreamKeyInput) (*pb.StreamKey, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key creation")
		now := time.Now()
		return &pb.StreamKey{
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

	return keyResp.StreamKey, nil
}

// DoDeleteStreamKey deactivates a stream key
func (r *Resolver) DoDeleteStreamKey(ctx context.Context, streamID, keyID string) (model.DeleteStreamKeyResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key deletion")
		return &model.DeleteSuccess{Success: true, DeletedID: keyID}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	err := r.Clients.Commodore.DeactivateStreamKey(ctx, streamID, keyID)
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

	return &model.DeleteSuccess{Success: true, DeletedID: keyID}, nil
}

// === CLIPS MANAGEMENT ===

// DoGetClips retrieves all clips for the authenticated user
func (r *Resolver) DoGetClips(ctx context.Context, streamID *string) ([]*pb.ClipInfo, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clips")
		now := time.Now()
		return []*pb.ClipInfo{
			{
				Id:          "clip_demo_1",
				StreamName:  "stream_demo_1",
				Title:       "Demo Highlight Reel #1",
				Description: "Amazing gameplay highlights from last night's stream",
				StartTime:   1640995200, // Jan 1, 2022 00:00:00 UTC
				Duration:    600,        // 10 minutes
				ClipHash:    "pb_clip_demo_1",
				Status:      "ready",
				CreatedAt:   timestamppb.New(now.Add(-24 * time.Hour)),
				UpdatedAt:   timestamppb.New(now.Add(-23 * time.Hour)),
			},
			{
				Id:          "clip_demo_2",
				StreamName:  "stream_demo_2",
				Title:       "Best Moments Compilation",
				Description: "Top 5 moments from this week's streams",
				StartTime:   1641081600, // Jan 2, 2022 00:00:00 UTC
				Duration:    1800,       // 30 minutes
				ClipHash:    "pb_clip_demo_2",
				Status:      "processing",
				CreatedAt:   timestamppb.New(now.Add(-6 * time.Hour)),
				UpdatedAt:   timestamppb.New(now.Add(-5 * time.Hour)),
			},
		}, nil
	}

	// Get tenant_id from context
	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Call Commodore gRPC (context metadata carries auth)
	clipsResp, err := r.Clients.Commodore.GetClips(ctx, tenantID, streamID, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clips")
		return nil, fmt.Errorf("failed to get clips: %w", err)
	}

	return clipsResp.Clips, nil
}

// DoGetClip retrieves a specific clip by ID
func (r *Resolver) DoGetClip(ctx context.Context, id string) (*pb.ClipInfo, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip")
		now := time.Now()
		return &pb.ClipInfo{
			Id:          id,
			StreamName:  "stream_demo_1",
			Title:       "Demo Clip Details",
			Description: "This is a detailed view of a demo clip with all metadata",
			StartTime:   1640995200, // Jan 1, 2022 00:00:00 UTC
			Duration:    600,        // 10 minutes
			ClipHash:    "pb_clip_" + id,
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

	return clip, nil
}

// DoGetClipViewingUrls retrieves viewing URLs for a specific clip
func (r *Resolver) DoGetClipViewingUrls(ctx context.Context, clipID string) (*pb.ClipViewingURLs, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip viewing URLs")
		return &pb.ClipViewingURLs{
			Urls: map[string]string{
				"hls":  "https://demo-clips.example.com/clips/" + clipID + "/playlist.m3u8",
				"dash": "https://demo-clips.example.com/clips/" + clipID + "/manifest.mpd",
				"mp4":  "https://demo-clips.example.com/clips/" + clipID + "/clip.mp4",
				"webm": "https://demo-clips.example.com/clips/" + clipID + "/clip.webm",
			},
		}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	clipURLs, err := r.Clients.Commodore.GetClipURLs(ctx, clipID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clip viewing URLs")
		return nil, fmt.Errorf("failed to get clip viewing URLs: %w", err)
	}

	return clipURLs, nil
}

// DoDeleteClip deletes a clip by ID
func (r *Resolver) DoDeleteClip(ctx context.Context, id string) (model.DeleteClipResult, error) {
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

// === DVR & Recording Config ===

// DoStartDVR starts a DVR recording
func (r *Resolver) DoStartDVR(ctx context.Context, internalName string, streamID *string, expiresAt *int) (*pb.StartDVRResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: start DVR")
		return &pb.StartDVRResponse{Status: "started", DvrHash: "dvr_demo_hash"}, nil
	}

	// Build gRPC request - StreamId is *string in proto
	req := &pb.StartDVRRequest{InternalName: internalName}
	if streamID != nil {
		req.StreamId = streamID
	}
	if expiresAt != nil {
		exp := int64(*expiresAt)
		req.ExpiresAt = &exp
	}

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
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: delete DVR")
		return &model.DeleteSuccess{Success: true, DeletedID: dvrHash}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	if err := r.Clients.Commodore.DeleteDVR(ctx, dvrHash); err != nil {
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
	return &model.DeleteSuccess{Success: true, DeletedID: dvrHash}, nil
}

// DoListDVRRequests lists DVR recordings with cursor pagination
func (r *Resolver) DoListDVRRequests(ctx context.Context, internalName *string, pagination *pb.CursorPaginationRequest) (*pb.ListDVRRecordingsResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: list DVR requests")
		now := time.Now()
		duration1 := int32(3600)  // 1 hour
		duration2 := int32(1800)  // 30 minutes so far
		size1 := int64(5368709120) // ~5 GB
		size2 := int64(1073741824) // ~1 GB so far
		return &pb.ListDVRRecordingsResponse{
			DvrRecordings: []*pb.DVRInfo{
				{
					DvrHash:         "pb_dvr_demo_1",
					InternalName:    "stream_demo_1",
					Status:          "completed",
					StartedAt:       timestamppb.New(now.Add(-48 * time.Hour)),
					EndedAt:         timestamppb.New(now.Add(-47 * time.Hour)),
					DurationSeconds: &duration1,
					SizeBytes:       &size1,
					CreatedAt:       timestamppb.New(now.Add(-48 * time.Hour)),
					UpdatedAt:       timestamppb.New(now.Add(-47 * time.Hour)),
				},
				{
					DvrHash:         "pb_dvr_demo_2",
					InternalName:    "stream_demo_2",
					Status:          "recording",
					StartedAt:       timestamppb.New(now.Add(-30 * time.Minute)),
					DurationSeconds: &duration2,
					SizeBytes:       &size2,
					CreatedAt:       timestamppb.New(now.Add(-30 * time.Minute)),
					UpdatedAt:       timestamppb.New(now),
				},
			},
			Pagination: &pb.CursorPaginationResponse{
				TotalCount:  2,
				HasNextPage: false,
			},
		}, nil
	}

	// Get tenant_id from context
	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// Call Commodore gRPC (context metadata carries auth)
	out, err := r.Clients.Commodore.ListDVRRequests(ctx, tenantID, internalName, pagination)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list DVR requests")
		return nil, fmt.Errorf("failed to list DVR requests: %w", err)
	}
	return out, nil
}

// DoGetStreamMeta retrieves metadata for a stream
func (r *Resolver) DoGetStreamMeta(ctx context.Context, streamKey string, targetBaseURL *string, targetNodeID *string, includeRaw *bool) (*pb.StreamMetaResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: get stream meta")
		rawData := []byte(`{"isLive":true,"bufferWindow":5000,"jitter":100,"unixOffset":1000,"now":1640995200000,"last":1640995195000,"width":1920,"height":1080,"version":3,"type":"video"}`)
		resp := &pb.StreamMetaResponse{
			MetaSummary: &pb.MetaSummary{
				IsLive:         true,
				BufferWindowMs: 5000,
				JitterMs:       100,
				UnixOffsetMs:   1000,
				NowMs:          int64Ptr(1640995200000),
				LastMs:         int64Ptr(1640995195000),
				Width:          int32Ptr(1920),
				Height:         int32Ptr(1080),
				Version:        int32Ptr(3),
				Type:           "video",
			},
		}
		if includeRaw != nil && *includeRaw {
			resp.Raw = rawData
		}
		return resp, nil
	}

	// gRPC client signature: GetStreamMeta(ctx, internalName, contentType string, includeRaw bool, targetNodeID, targetBaseURL string)
	includeRawBool := includeRaw != nil && *includeRaw
	nodeID := ""
	if targetNodeID != nil {
		nodeID = *targetNodeID
	}
	baseURL := ""
	if targetBaseURL != nil {
		baseURL = *targetBaseURL
	}

	// Call Commodore gRPC (context metadata carries auth)
	metaResp, err := r.Clients.Commodore.GetStreamMeta(ctx, streamKey, "", includeRawBool, nodeID, baseURL)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream metadata")
		return nil, fmt.Errorf("failed to get stream metadata: %w", err)
	}

	// gRPC client returns *pb.StreamMetaResponse directly
	return metaResp, nil
}

// int32Ptr returns a pointer to the int32 value
func int32Ptr(i int32) *int32 {
	return &i
}

// int64Ptr returns a pointer to the int64 value
func int64Ptr(i int64) *int64 {
	return &i
}

func (r *Resolver) getStreamsMemoized(ctx context.Context) ([]*pb.Stream, error) {
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}

	var streams []*pb.Stream

	if lds := loaders.FromContext(ctx); lds != nil && lds.Memo != nil {
		// Use tenant_id from context for cache key (gRPC uses context metadata for auth)
		key := fmt.Sprintf("commodore:get_streams:%s", tenantID)
		val, err := lds.Memo.GetOrLoad(key, func() (interface{}, error) {
			resp, err := r.Clients.Commodore.ListStreams(ctx, nil)
			if err != nil {
				return nil, err
			}
			return resp.Streams, nil
		})
		if err != nil {
			return nil, err
		}
		var ok bool
		streams, ok = val.([]*pb.Stream)
		if !ok {
			return nil, fmt.Errorf("unexpected cache type for %s", key)
		}
	} else {
		resp, err := r.Clients.Commodore.ListStreams(ctx, nil)
		if err != nil {
			return nil, err
		}
		streams = resp.Streams
	}

	return streams, nil
}

func (r *Resolver) getStreamMemoized(ctx context.Context, streamID string) (*pb.Stream, error) {
	tenantID := ""
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	var stream *pb.Stream

	if lds := loaders.FromContext(ctx); lds != nil && lds.Memo != nil {
		// Use tenant_id from context for cache key (gRPC uses context metadata for auth)
		key := fmt.Sprintf("commodore:get_stream:%s:%s", tenantID, streamID)
		val, err := lds.Memo.GetOrLoad(key, func() (interface{}, error) {
			return r.Clients.Commodore.GetStream(ctx, streamID)
		})
		if err != nil {
			return nil, err
		}
		var ok bool
		stream, ok = val.(*pb.Stream)
		if !ok {
			return nil, fmt.Errorf("unexpected cache type for %s", key)
		}
	} else {
		var err error
		stream, err = r.Clients.Commodore.GetStream(ctx, streamID)
		if err != nil {
			return nil, err
		}
	}

	return stream, nil
}

// === CONNECTION-BASED PAGINATION ===

// DoGetStreamsConnection retrieves streams with Relay-style cursor pagination
func (r *Resolver) DoGetStreamsConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.StreamsConnection, error) {
	// TODO: Implement bidirectional keyset pagination once Commodore supports it
	_ = last
	_ = before
	start := time.Now()

	defer func() {
		duration := time.Since(start).Seconds()
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("streamsConnection").Observe(duration)
		}
	}()

	// Parse pagination parameters
	limit := pagination.DefaultLimit
	offset := 0

	if first != nil {
		limit = pagination.ClampLimit(*first)
	}
	if after != nil && *after != "" {
		// For cursor-based pagination, decode the cursor to get offset
		// Currently using index-based cursor - can enhance later with Commodore pagination support
	}

	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo streams connection")
		streams := demo.GenerateStreams()
		return r.buildStreamsConnection(streams, len(streams), false, offset), nil
	}

	// gRPC uses context metadata for auth (set by userContextInterceptor)
	streamsResp, err := r.getStreamsMemoized(ctx)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get streams")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streamsConnection", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}

	// Apply pagination in-memory (until Commodore supports cursor pagination)
	allStreams := streamsResp
	total := len(allStreams)

	// Apply offset and limit
	startIdx := offset
	if startIdx > total {
		startIdx = total
	}
	end := startIdx + limit
	if end > total {
		end = total
	}

	paginatedStreams := allStreams[startIdx:end]
	hasMore := end < total

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("streamsConnection", "success").Inc()
	}

	return r.buildStreamsConnection(paginatedStreams, total, hasMore, offset), nil
}

// buildStreamsConnection constructs a StreamsConnection from a slice of streams
func (r *Resolver) buildStreamsConnection(streams []*pb.Stream, total int, hasMore bool, offset int) *model.StreamsConnection {
	edges := make([]*model.StreamEdge, len(streams))
	for i, stream := range streams {
		// Use keyset cursor (timestamp + ID) for stable pagination
		cursor := pagination.EncodeCursor(stream.CreatedAt.AsTime(), stream.InternalName)
		edges[i] = &model.StreamEdge{
			Cursor: cursor,
			Node:   stream,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.StreamsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}

// DoGetClipsConnection retrieves clips with Relay-style cursor pagination
func (r *Resolver) DoGetClipsConnection(ctx context.Context, streamID *string, first *int, after *string, last *int, before *string) (*model.ClipsConnection, error) {
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
		r.Logger.Debug("Returning demo clips connection")
		clips, _ := r.DoGetClips(ctx, streamID)
		return r.buildClipsConnectionFromProto(clips, nil), nil
	}

	// Get tenant_id from context
	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	// gRPC uses context metadata for auth (set by userContextInterceptor)
	clipsResp, err := r.Clients.Commodore.GetClips(ctx, tenantID, streamID, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clips")
		return nil, fmt.Errorf("failed to get clips: %w", err)
	}

	// Enrich with lifecycle data from Periscope (size_bytes, status, storage_location, etc.)
	if l := loaders.FromContext(ctx); l != nil && l.ArtifactLifecycle != nil && len(clipsResp.Clips) > 0 {
		hashes := make([]string, len(clipsResp.Clips))
		for i, clip := range clipsResp.Clips {
			hashes[i] = clip.ClipHash
		}

		states, err := l.ArtifactLifecycle.LoadMany(ctx, tenantID, hashes)
		if err != nil {
			r.Logger.WithError(err).Warn("Failed to load clip lifecycle data")
		} else {
			for _, clip := range clipsResp.Clips {
				if state, ok := states[clip.ClipHash]; ok && state != nil {
					// Convert uint64 to int64 for size_bytes
					if state.SizeBytes != nil {
						sizeInt64 := int64(*state.SizeBytes)
						clip.SizeBytes = &sizeInt64
					}
					clip.Status = state.Stage
					if state.FilePath != nil {
						clip.StoragePath = *state.FilePath
					}
					if state.S3Url != nil {
						storageLocation := "s3"
						clip.StorageLocation = &storageLocation
					}
				}
			}
		}
	}

	return r.buildClipsConnectionFromProto(clipsResp.Clips, clipsResp.Pagination), nil
}

// buildClipsConnectionFromProto constructs a ClipsConnection from proto response with keyset pagination
func (r *Resolver) buildClipsConnectionFromProto(clips []*pb.ClipInfo, paginationResp *pb.CursorPaginationResponse) *model.ClipsConnection {
	edges := make([]*model.ClipEdge, len(clips))
	for i, clip := range clips {
		// Use keyset cursor (timestamp + clip_hash) for stable pagination
		cursor := pagination.EncodeCursor(clip.CreatedAt.AsTime(), clip.ClipHash)
		edges[i] = &model.ClipEdge{
			Cursor: cursor,
			Node:   clip,
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
	}

	totalCount := 0
	if paginationResp != nil {
		totalCount = int(paginationResp.TotalCount)
	} else {
		// Fallback for demo mode where pagination is nil
		totalCount = len(clips)
	}

	return &model.ClipsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}
}

// DoGetDVRRecordingsConnection retrieves DVR recordings with Relay-style cursor pagination
func (r *Resolver) DoGetDVRRecordingsConnection(ctx context.Context, internalName *string, first *int, after *string, last *int, before *string) (*model.DVRRecordingsConnection, error) {
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

	// Call the internal method that fetches from gRPC
	response, err := r.DoListDVRRequests(ctx, internalName, paginationReq)
	if err != nil {
		return nil, err
	}

	// Extract tenant_id for lifecycle lookup
	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}

	// Enrich with lifecycle data from Periscope (size_bytes, status, storage_location, etc.)
	if l := loaders.FromContext(ctx); l != nil && l.ArtifactLifecycle != nil && tenantID != "" && len(response.DvrRecordings) > 0 {
		hashes := make([]string, len(response.DvrRecordings))
		for i, dvr := range response.DvrRecordings {
			hashes[i] = dvr.DvrHash
		}

		states, err := l.ArtifactLifecycle.LoadMany(ctx, tenantID, hashes)
		if err != nil {
			r.Logger.WithError(err).Warn("Failed to load DVR lifecycle data")
		} else {
			for _, dvr := range response.DvrRecordings {
				if state, ok := states[dvr.DvrHash]; ok && state != nil {
					// Convert uint64 to int64 for size_bytes
					if state.SizeBytes != nil {
						sizeInt64 := int64(*state.SizeBytes)
						dvr.SizeBytes = &sizeInt64
					}
					dvr.Status = state.Stage
					if state.StartedAt != nil {
						dvr.StartedAt = state.StartedAt
					}
					if state.CompletedAt != nil {
						dvr.EndedAt = state.CompletedAt
					}
					if state.ManifestPath != nil {
						dvr.ManifestPath = *state.ManifestPath
					}
					if state.S3Url != nil {
						dvr.S3Url = state.S3Url
						storageLocation := "s3"
						dvr.StorageLocation = &storageLocation
					}
				}
			}
		}
	}

	// Build edges from proto response (DVRInfo maps to DVRRequest via autobind)
	edges := make([]*model.DVRRecordingEdge, len(response.DvrRecordings))
	for i, dvrInfo := range response.DvrRecordings {
		// Use keyset cursor (timestamp + dvr_hash) for stable pagination
		cursor := pagination.EncodeCursor(dvrInfo.CreatedAt.AsTime(), dvrInfo.DvrHash)
		edges[i] = &model.DVRRecordingEdge{
			Cursor: cursor,
			Node:   dvrInfo, // pb.DVRInfo autobinds to DVRRequest
		}
	}

	// Build page info from proto pagination response
	pageInfo := &model.PageInfo{
		HasPreviousPage: response.Pagination != nil && response.Pagination.HasPreviousPage,
		HasNextPage:     response.Pagination != nil && response.Pagination.HasNextPage,
	}
	if response.Pagination != nil {
		pageInfo.StartCursor = response.Pagination.StartCursor
		pageInfo.EndCursor = response.Pagination.EndCursor
	}

	totalCount := 0
	if response.Pagination != nil {
		totalCount = int(response.Pagination.TotalCount)
	}

	return &model.DVRRecordingsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}
