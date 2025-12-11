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
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip creation")
		now := time.Now()
		description := ""
		if input.Description != nil {
			description = *input.Description
		}
		return &pb.ClipInfo{
			Id:          "clip_demo_" + now.Format("20060102150405"),
			StreamName:  input.Stream,
			Title:       input.Title,
			Description: description,
			StartTime:   int64(input.StartTime),
			Duration:    int64(input.EndTime - input.StartTime),
			ClipHash:    "pb_clip_demo_" + now.Format("150405"),
			Status:      "processing",
			CreatedAt:   timestamppb.New(now),
			UpdatedAt:   timestamppb.New(now),
		}, nil
	}

	// Build gRPC request - proto uses StartUnix/StopUnix
	startUnix := int64(input.StartTime)
	stopUnix := int64(input.EndTime)
	durationSec := stopUnix - startUnix

	req := &pb.CreateClipRequest{
		InternalName: input.Stream,
		StartUnix:    &startUnix,
		StopUnix:     &stopUnix,
		DurationSec:  &durationSec,
		Title:        input.Title,
	}

	// Handle optional description
	if input.Description != nil {
		req.Description = *input.Description
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
	return &pb.ClipInfo{
		Id:          clipResp.RequestId,
		ClipHash:    clipResp.ClipHash,
		StreamName:  input.Stream,
		Title:       input.Title,
		Description: description,
		StartTime:   startUnix,
		Duration:    durationSec,
		NodeId:      clipResp.NodeId,
		Status:      clipResp.Status,
		CreatedAt:   timestamppb.New(now),
		UpdatedAt:   timestamppb.New(now),
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

// === RECORDINGS MANAGEMENT ===

// DoGetRecordings retrieves all recordings for the authenticated user
func (r *Resolver) DoGetRecordings(ctx context.Context, streamID *string) ([]*pb.Recording, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo recordings")
		now := time.Now()
		oneHour := int32(3600)
		thirtyMin := int32(1800)
		fileSize1 := int64(1048576)
		fileSize2 := int64(512000)
		pb1 := "pb_rec_demo_1"
		pb2 := "pb_rec_demo_2"
		return []*pb.Recording{
			{
				Id:         "rec_demo_1",
				StreamId:   "stream_demo_1",
				Filename:   "Demo Recording #1",
				Duration:   &oneHour,
				Status:     "completed",
				PlaybackId: &pb1,
				FileSize:   &fileSize1,
				CreatedAt:  timestamppb.New(now.Add(-24 * time.Hour)),
				UpdatedAt:  timestamppb.New(now.Add(-23 * time.Hour)),
			},
			{
				Id:         "rec_demo_2",
				StreamId:   "stream_demo_2",
				Filename:   "Demo Recording #2",
				Duration:   &thirtyMin,
				Status:     "processing",
				PlaybackId: &pb2,
				FileSize:   &fileSize2,
				CreatedAt:  timestamppb.New(now.Add(-6 * time.Hour)),
				UpdatedAt:  timestamppb.New(now.Add(-5 * time.Hour)),
			},
		}, nil
	}

	// Call Commodore gRPC (context metadata carries auth)
	streamFilter := ""
	if streamID != nil {
		streamFilter = *streamID
	}
	recordingsResp, err := r.Clients.Commodore.ListRecordings(ctx, streamFilter, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get recordings")
		return nil, fmt.Errorf("failed to get recordings: %w", err)
	}

	return recordingsResp.Recordings, nil
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
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
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
func (r *Resolver) DoStartDVR(ctx context.Context, internalName string, streamID *string) (*pb.StartDVRResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: start DVR")
		return &pb.StartDVRResponse{Status: "started", DvrHash: "dvr_demo_hash"}, nil
	}

	// Build gRPC request - StreamId is *string in proto
	req := &pb.StartDVRRequest{InternalName: internalName}
	if streamID != nil {
		req.StreamId = streamID
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

// DoListDVRRequests lists DVR recordings with cursor pagination
func (r *Resolver) DoListDVRRequests(ctx context.Context, internalName *string, pagination *pb.CursorPaginationRequest) (*pb.ListDVRRecordingsResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: list DVR requests")
		return &pb.ListDVRRecordingsResponse{DvrRecordings: []*pb.DVRInfo{}}, nil
	}

	// Get tenant_id from context
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
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
	tenantID, _ := ctx.Value("tenant_id").(string)

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
	tenantID, _ := ctx.Value("tenant_id").(string)
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
		cursor := pagination.EncodeIndexCursor(offset + i)
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
		firstCursor := pagination.EncodeIndexCursor(offset)
		lastCursor := pagination.EncodeIndexCursor(offset + len(streams) - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}

	return &model.StreamsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}

// DoGetRecordingsConnection retrieves recordings with Relay-style cursor pagination
func (r *Resolver) DoGetRecordingsConnection(ctx context.Context, streamID *string, first *int, after *string, last *int, before *string) (*model.RecordingsConnection, error) {
	// TODO: Implement bidirectional keyset pagination once Commodore supports it
	_ = last
	_ = before
	// Parse pagination parameters
	limit := pagination.DefaultLimit
	offset := 0

	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo recordings connection")
		recordings, _ := r.DoGetRecordings(ctx, streamID)
		return r.buildRecordingsConnection(recordings, len(recordings), false, offset), nil
	}

	// gRPC uses context metadata for auth (set by userContextInterceptor)
	streamFilter := ""
	if streamID != nil {
		streamFilter = *streamID
	}
	recordingsResp, err := r.Clients.Commodore.ListRecordings(ctx, streamFilter, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get recordings")
		return nil, fmt.Errorf("failed to get recordings: %w", err)
	}

	// Apply pagination in-memory
	allRecordings := recordingsResp.Recordings
	total := len(allRecordings)

	startIdx := offset
	if startIdx > total {
		startIdx = total
	}
	end := startIdx + limit
	if end > total {
		end = total
	}

	paginatedRecordings := allRecordings[startIdx:end]
	hasMore := end < total

	return r.buildRecordingsConnection(paginatedRecordings, total, hasMore, offset), nil
}

// buildRecordingsConnection constructs a RecordingsConnection from a slice of recordings
func (r *Resolver) buildRecordingsConnection(recordings []*pb.Recording, total int, hasMore bool, offset int) *model.RecordingsConnection {
	edges := make([]*model.RecordingEdge, len(recordings))
	for i, recording := range recordings {
		cursor := pagination.EncodeIndexCursor(offset + i)
		edges[i] = &model.RecordingEdge{
			Cursor: cursor,
			Node:   recording,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		firstCursor := pagination.EncodeIndexCursor(offset)
		lastCursor := pagination.EncodeIndexCursor(offset + len(recordings) - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}

	return &model.RecordingsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}

// DoGetClipsConnection retrieves clips with Relay-style cursor pagination
func (r *Resolver) DoGetClipsConnection(ctx context.Context, streamID *string, first *int, after *string, last *int, before *string) (*model.ClipsConnection, error) {
	// TODO: Implement bidirectional keyset pagination once Commodore supports it
	_ = last
	_ = before
	// Parse pagination parameters
	limit := pagination.DefaultLimit
	offset := 0

	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Check for demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clips connection")
		clips, _ := r.DoGetClips(ctx, streamID)
		return r.buildClipsConnection(clips, len(clips), false, offset), nil
	}

	// Get tenant_id from context
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	// gRPC uses context metadata for auth (set by userContextInterceptor)
	clipsResp, err := r.Clients.Commodore.GetClips(ctx, tenantID, streamID, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clips")
		return nil, fmt.Errorf("failed to get clips: %w", err)
	}

	// gRPC client returns []*pb.ClipInfo directly
	allClips := clipsResp.Clips
	total := len(allClips)

	// Apply pagination in-memory
	startIdx := offset
	if startIdx > total {
		startIdx = total
	}
	end := startIdx + limit
	if end > total {
		end = total
	}

	paginatedClips := allClips[startIdx:end]
	hasMore := end < total

	return r.buildClipsConnection(paginatedClips, total, hasMore, offset), nil
}

// buildClipsConnection constructs a ClipsConnection from a slice of clips
func (r *Resolver) buildClipsConnection(clips []*pb.ClipInfo, total int, hasMore bool, offset int) *model.ClipsConnection {
	edges := make([]*model.ClipEdge, len(clips))
	for i, clip := range clips {
		cursor := pagination.EncodeIndexCursor(offset + i)
		edges[i] = &model.ClipEdge{
			Cursor: cursor,
			Node:   clip,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		firstCursor := pagination.EncodeIndexCursor(offset)
		lastCursor := pagination.EncodeIndexCursor(offset + len(clips) - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}

	return &model.ClipsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: total,
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

	// Build edges from proto response (DVRInfo maps to DVRRequest via autobind)
	edges := make([]*model.DVRRecordingEdge, len(response.DvrRecordings))
	for i, dvrInfo := range response.DvrRecordings {
		cursor := dvrInfo.DvrHash
		if cursor == "" {
			cursor = pagination.EncodeIndexCursor(i)
		}
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
