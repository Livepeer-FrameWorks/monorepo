package resolvers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/api/commodore"
	"frameworks/pkg/models"
)

// DoGetStreams retrieves all streams for the authenticated user
func (r *Resolver) DoGetStreams(ctx context.Context) ([]*models.Stream, error) {
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

	// Extract JWT token from context (set by auth middleware)
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streams", "error").Inc()
		}
		return nil, fmt.Errorf("user not authenticated")
	}

	streamsResp, err := r.getStreamsMemoized(ctx, userToken)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get streams")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streams", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}

	// Convert from []models.Stream to []*models.Stream
	result := make([]*models.Stream, len(*streamsResp))
	for i := range *streamsResp {
		result[i] = &(*streamsResp)[i]
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("streams", "success").Inc()
	}

	return result, nil
}

// DoGetStream retrieves a specific stream by ID
func (r *Resolver) DoGetStream(ctx context.Context, id string) (*models.Stream, error) {
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
			if stream.ID == id {
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

	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok || userToken == "" {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("stream", "error").Inc()
		}
		return nil, fmt.Errorf("user not authenticated")
	}

	stream, err := r.getStreamMemoized(ctx, userToken, id)
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
func (r *Resolver) DoCreateStream(ctx context.Context, input model.CreateStreamInput) (*models.Stream, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream creation")
		return &models.Stream{
			ID:    "demo_stream_" + time.Now().Format("20060102150405"),
			Title: input.Name,
			Description: func() string {
				if input.Description != nil {
					return *input.Description
				}
				return ""
			}(),
			StreamKey:  "sk_demo_" + time.Now().Format("150405"),
			PlaybackID: "pb_demo_" + time.Now().Format("150405"),
			Status:     "offline",
			IsRecording: func() bool {
				if input.Record != nil {
					return *input.Record
				}
				return false
			}(),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Convert to Commodore request format
	req := &commodore.CreateStreamRequest{
		Title: input.Name,
	}

	// Handle optional fields
	if input.Description != nil {
		req.Description = *input.Description
	}
	if input.Record != nil {
		req.IsRecording = *input.Record
	}

	// Call Commodore to create stream
	streamResp, err := r.Clients.Commodore.CreateStream(ctx, userToken, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create stream")
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	return streamResp, nil
}

// DoDeleteStream deletes a stream
func (r *Resolver) DoDeleteStream(ctx context.Context, id string) (bool, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream deletion")
		return true, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return false, fmt.Errorf("user not authenticated")
	}

	// Call Commodore to delete stream
	err := r.Clients.Commodore.DeleteStream(ctx, userToken, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to delete stream")
		return false, fmt.Errorf("failed to delete stream: %w", err)
	}

	return true, nil
}

// DoRefreshStreamKey refreshes the stream key for a stream
func (r *Resolver) DoRefreshStreamKey(ctx context.Context, id string) (*models.Stream, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream refresh")
		streams := demo.GenerateStreams()
		for _, stream := range streams {
			if stream.ID == id {
				// Generate new demo stream key
				stream.StreamKey = "sk_demo_refreshed_" + time.Now().Format("20060102150405")
				return stream, nil
			}
		}
		return nil, fmt.Errorf("demo stream not found")
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Call Commodore to refresh stream key
	streamResp, err := r.Clients.Commodore.RefreshStreamKey(ctx, userToken, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to refresh stream key")
		return nil, fmt.Errorf("failed to refresh stream key: %w", err)
	}

	return streamResp, nil
}

// DoValidateStreamKey validates a stream key
func (r *Resolver) DoValidateStreamKey(ctx context.Context, streamKey string) (*model.StreamValidation, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key validation")
		// Demo validation - validate demo stream keys
		valid := strings.HasPrefix(streamKey, "sk_demo_")
		errorPtr := (*string)(nil)
		if !valid {
			errorMsg := "Invalid demo stream key"
			errorPtr = &errorMsg
		}
		return &model.StreamValidation{
			Valid:     valid,
			StreamKey: streamKey,
			Error:     errorPtr,
		}, nil
	}

	// Call Commodore to validate stream key
	validation, err := r.Clients.Commodore.ValidateStreamKey(ctx, streamKey)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to validate stream key")
		return nil, fmt.Errorf("failed to validate stream key: %w", err)
	}

	// Convert to GraphQL model
	var errorPtr *string
	if validation.Error != "" {
		errorPtr = &validation.Error
	}

	return &model.StreamValidation{
		Valid:     validation.Valid,
		StreamKey: streamKey, // Use the input streamKey since response doesn't include it
		Error:     errorPtr,
	}, nil
}

// DoCreateClip creates a new clip
func (r *Resolver) DoCreateClip(ctx context.Context, input model.CreateClipInput) (*commodore.ClipResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip creation")
		return &commodore.ClipResponse{
			ID:       "clip_demo_" + time.Now().Format("20060102150405"),
			StreamID: input.Stream,
			Title:    input.Title,
			Description: func() string {
				if input.Description != nil {
					return *input.Description
				}
				return ""
			}(),
			StartTime:  int64(input.StartTime),
			EndTime:    int64(input.EndTime),
			Duration:   int64(input.EndTime - input.StartTime),
			PlaybackID: "pb_clip_demo_" + time.Now().Format("150405"),
			Status:     "processing",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Convert to Commodore request format
	req := &commodore.CreateClipRequest{
		StreamID:  input.Stream,
		StartTime: int64(input.StartTime),
		EndTime:   int64(input.EndTime),
		Title:     input.Title,
	}

	// Handle optional description
	if input.Description != nil {
		req.Description = *input.Description
	}

	// Call Commodore to create clip
	clipResp, err := r.Clients.Commodore.CreateClip(ctx, userToken, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create clip")
		return nil, fmt.Errorf("failed to create clip: %w", err)
	}

	return clipResp, nil
}

// === STREAM KEYS MANAGEMENT ===

// DoGetStreamKeys retrieves all stream keys for a specific stream
func (r *Resolver) DoGetStreamKeys(ctx context.Context, streamID string) ([]*models.StreamKey, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream keys")
		return []*models.StreamKey{
			{
				ID:         "sk_demo_1",
				TenantID:   "tenant_demo_1",
				UserID:     "user_demo_1",
				StreamID:   streamID,
				KeyValue:   "sk_demo_live_primary",
				KeyName:    func() *string { s := "Primary Key"; return &s }(),
				IsActive:   true,
				LastUsedAt: func() *time.Time { t := time.Now().Add(-1 * time.Hour); return &t }(),
				CreatedAt:  time.Now().Add(-7 * 24 * time.Hour),
				UpdatedAt:  time.Now().Add(-7 * 24 * time.Hour),
			},
			{
				ID:         "sk_demo_2",
				TenantID:   "tenant_demo_1",
				UserID:     "user_demo_1",
				StreamID:   streamID,
				KeyValue:   "sk_demo_live_backup",
				KeyName:    func() *string { s := "Backup Key"; return &s }(),
				IsActive:   false,
				LastUsedAt: func() *time.Time { t := time.Now().Add(-3 * 24 * time.Hour); return &t }(),
				CreatedAt:  time.Now().Add(-30 * 24 * time.Hour),
				UpdatedAt:  time.Now().Add(-30 * 24 * time.Hour),
			},
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get stream keys from Commodore
	keysResp, err := r.Clients.Commodore.GetStreamKeys(ctx, userToken, streamID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream keys")
		return nil, fmt.Errorf("failed to get stream keys: %w", err)
	}

	// Convert to GraphQL model
	keys := make([]*models.StreamKey, len(keysResp.StreamKeys))
	for i, key := range keysResp.StreamKeys {
		keys[i] = &models.StreamKey{
			ID:       key.ID,
			TenantID: key.TenantID,
			UserID:   key.UserID,
			StreamID: key.StreamID,
			KeyValue: key.KeyValue,
			KeyName: func() *string {
				if key.KeyName != "" {
					return &key.KeyName
				} else {
					return nil
				}
			}(),
			IsActive:   key.IsActive,
			LastUsedAt: key.LastUsedAt,
			CreatedAt:  key.CreatedAt,
			UpdatedAt:  key.UpdatedAt,
		}
	}

	return keys, nil
}

// DoCreateStreamKey creates a new stream key for a specific stream
func (r *Resolver) DoCreateStreamKey(ctx context.Context, streamID string, input model.CreateStreamKeyInput) (*models.StreamKey, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key creation")
		return &models.StreamKey{
			ID:         "sk_demo_new_" + time.Now().Format("20060102150405"),
			TenantID:   "tenant_demo_1",
			UserID:     "user_demo_1",
			StreamID:   streamID,
			KeyValue:   "sk_demo_" + time.Now().Format("150405"),
			KeyName:    &input.Name,
			IsActive:   true,
			LastUsedAt: nil,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Convert to Commodore request format
	req := &commodore.CreateStreamKeyRequest{
		KeyName: input.Name,
	}

	// Call Commodore to create stream key
	keyResp, err := r.Clients.Commodore.CreateStreamKey(ctx, userToken, streamID, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create stream key")
		return nil, fmt.Errorf("failed to create stream key: %w", err)
	}

	// Convert to GraphQL model
	return &models.StreamKey{
		ID:       keyResp.StreamKey.ID,
		TenantID: keyResp.StreamKey.TenantID,
		UserID:   keyResp.StreamKey.UserID,
		StreamID: keyResp.StreamKey.StreamID,
		KeyValue: keyResp.StreamKey.KeyValue,
		KeyName: func() *string {
			if keyResp.StreamKey.KeyName != "" {
				return &keyResp.StreamKey.KeyName
			} else {
				return nil
			}
		}(),
		IsActive:   keyResp.StreamKey.IsActive,
		LastUsedAt: keyResp.StreamKey.LastUsedAt,
		CreatedAt:  keyResp.StreamKey.CreatedAt,
		UpdatedAt:  keyResp.StreamKey.UpdatedAt,
	}, nil
}

// DoDeleteStreamKey deactivates a stream key
func (r *Resolver) DoDeleteStreamKey(ctx context.Context, streamID, keyID string) (bool, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream key deletion")
		return true, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return false, fmt.Errorf("user not authenticated")
	}

	// Call Commodore to deactivate stream key
	_, err := r.Clients.Commodore.DeactivateStreamKey(ctx, userToken, streamID, keyID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to deactivate stream key")
		return false, fmt.Errorf("failed to deactivate stream key: %w", err)
	}

	return true, nil
}

// === RECORDINGS MANAGEMENT ===

// DoGetRecordings retrieves all recordings for the authenticated user
func (r *Resolver) DoGetRecordings(ctx context.Context, streamID *string) ([]*commodore.Recording, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo recordings")
		now := time.Now()
		oneHour := 3600
		thirtyMin := 1800
		fileSize1 := int64(1048576)
		fileSize2 := int64(512000)
		pb1 := "pb_rec_demo_1"
		pb2 := "pb_rec_demo_2"
		thumb1 := "https://example.com/thumb1.jpg"
		return []*commodore.Recording{
			{
				ID:           "rec_demo_1",
				StreamID:     "stream_demo_1",
				Filename:     "Demo Recording #1",
				Duration:     &oneHour,
				Status:       "completed",
				PlaybackID:   &pb1,
				ThumbnailURL: &thumb1,
				FileSize:     &fileSize1,
				CreatedAt:    now.Add(-24 * time.Hour),
				UpdatedAt:    now.Add(-23 * time.Hour),
			},
			{
				ID:         "rec_demo_2",
				StreamID:   "stream_demo_2",
				Filename:   "Demo Recording #2",
				Duration:   &thirtyMin,
				Status:     "processing",
				PlaybackID: &pb2,
				FileSize:   &fileSize2,
				CreatedAt:  now.Add(-6 * time.Hour),
				UpdatedAt:  now.Add(-5 * time.Hour),
			},
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get recordings from Commodore
	recordingsResp, err := r.Clients.Commodore.GetRecordings(ctx, userToken, streamID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get recordings")
		return nil, fmt.Errorf("failed to get recordings: %w", err)
	}

	// Return pointers to commodore.Recording items
	recordings := make([]*commodore.Recording, len(recordingsResp.Recordings))
	for i := range recordingsResp.Recordings {
		recordings[i] = &recordingsResp.Recordings[i]
	}

	return recordings, nil
}

// === CLIPS MANAGEMENT ===

// DoGetClips retrieves all clips for the authenticated user
func (r *Resolver) DoGetClips(ctx context.Context, streamID *string) ([]*commodore.ClipResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clips")
		now := time.Now()
		return []*commodore.ClipResponse{
			{
				ID:          "clip_demo_1",
				StreamID:    "stream_demo_1",
				Title:       "Demo Highlight Reel #1",
				Description: "Amazing gameplay highlights from last night's stream",
				StartTime:   1640995200, // Jan 1, 2022 00:00:00 UTC
				EndTime:     1640995800, // Jan 1, 2022 00:10:00 UTC
				Duration:    600,        // 10 minutes
				PlaybackID:  "pb_clip_demo_1",
				Status:      "ready",
				CreatedAt:   now.Add(-24 * time.Hour),
				UpdatedAt:   now.Add(-23 * time.Hour),
			},
			{
				ID:          "clip_demo_2",
				StreamID:    "stream_demo_2",
				Title:       "Best Moments Compilation",
				Description: "Top 5 moments from this week's streams",
				StartTime:   1641081600, // Jan 2, 2022 00:00:00 UTC
				EndTime:     1641083400, // Jan 2, 2022 00:30:00 UTC
				Duration:    1800,       // 30 minutes
				PlaybackID:  "pb_clip_demo_2",
				Status:      "processing",
				CreatedAt:   now.Add(-6 * time.Hour),
				UpdatedAt:   now.Add(-5 * time.Hour),
			},
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get clips from Commodore
	clipsResp, err := r.Clients.Commodore.GetClips(ctx, userToken, streamID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clips")
		return nil, fmt.Errorf("failed to get clips: %w", err)
	}

	// Convert ClipFullResponse to ClipResponse
	clips := make([]*commodore.ClipResponse, len(clipsResp.Clips))
	for i, clip := range clipsResp.Clips {
		clips[i] = &commodore.ClipResponse{
			ID:          clip.ID,
			StreamID:    clip.StreamName, // Map stream_name to stream_id
			Title:       clip.Title,
			Description: "", // ClipFullResponse doesn't include description
			StartTime:   clip.StartTime,
			EndTime:     0, // Calculate from StartTime + Duration
			Duration:    clip.Duration,
			PlaybackID:  "", // ClipFullResponse doesn't include playback_id
			Status:      clip.Status,
			CreatedAt:   clip.CreatedAt,
			UpdatedAt:   clip.CreatedAt, // Use CreatedAt as UpdatedAt since ClipFullResponse doesn't have UpdatedAt
		}
		// Calculate EndTime from StartTime + Duration
		clips[i].EndTime = clip.StartTime + clip.Duration
	}

	return clips, nil
}

// DoGetClip retrieves a specific clip by ID
func (r *Resolver) DoGetClip(ctx context.Context, id string) (*commodore.ClipResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip")
		now := time.Now()
		return &commodore.ClipResponse{
			ID:          id,
			StreamID:    "stream_demo_1",
			Title:       "Demo Clip Details",
			Description: "This is a detailed view of a demo clip with all metadata",
			StartTime:   1640995200, // Jan 1, 2022 00:00:00 UTC
			EndTime:     1640995800, // Jan 1, 2022 00:10:00 UTC
			Duration:    600,        // 10 minutes
			PlaybackID:  "pb_clip_" + id,
			Status:      "ready",
			CreatedAt:   now.Add(-12 * time.Hour),
			UpdatedAt:   now.Add(-11 * time.Hour),
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get clip from Commodore
	clipFull, err := r.Clients.Commodore.GetClip(ctx, userToken, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clip")
		return nil, fmt.Errorf("failed to get clip: %w", err)
	}

	// Convert ClipFullResponse to ClipResponse
	clip := &commodore.ClipResponse{
		ID:          clipFull.ID,
		StreamID:    clipFull.StreamName, // Map stream_name to stream_id
		Title:       clipFull.Title,
		Description: "", // ClipFullResponse doesn't include description
		StartTime:   clipFull.StartTime,
		EndTime:     clipFull.StartTime + clipFull.Duration,
		Duration:    clipFull.Duration,
		PlaybackID:  "", // ClipFullResponse doesn't include playbook_id
		Status:      clipFull.Status,
		CreatedAt:   clipFull.CreatedAt,
		UpdatedAt:   clipFull.CreatedAt, // Use CreatedAt as UpdatedAt
	}

	return clip, nil
}

// DoGetClipViewingUrls retrieves viewing URLs for a specific clip
func (r *Resolver) DoGetClipViewingUrls(ctx context.Context, clipID string) (*model.ClipViewingUrls, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip viewing URLs")
		return &model.ClipViewingUrls{
			Hls:  stringPtr("https://demo-clips.example.com/clips/" + clipID + "/playlist.m3u8"),
			Dash: stringPtr("https://demo-clips.example.com/clips/" + clipID + "/manifest.mpd"),
			Mp4:  stringPtr("https://demo-clips.example.com/clips/" + clipID + "/clip.mp4"),
			Webm: stringPtr("https://demo-clips.example.com/clips/" + clipID + "/clip.webm"),
		}, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get clip URLs from Commodore
	clipURLs, err := r.Clients.Commodore.GetClipURLs(ctx, userToken, clipID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clip viewing URLs")
		return nil, fmt.Errorf("failed to get clip viewing URLs: %w", err)
	}

	// Convert Commodore response to GraphQL model
	urls := &model.ClipViewingUrls{
		Hls:  getStringFromMap(clipURLs.URLs, "hls"),
		Dash: getStringFromMap(clipURLs.URLs, "dash"),
		Mp4:  getStringFromMap(clipURLs.URLs, "mp4"),
		Webm: getStringFromMap(clipURLs.URLs, "webm"),
	}

	return urls, nil
}

// DoDeleteClip deletes a clip by ID
func (r *Resolver) DoDeleteClip(ctx context.Context, id string) (bool, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: simulating clip deletion")
		return true, nil
	}

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return false, fmt.Errorf("user not authenticated")
	}

	// Delete clip via Commodore
	err := r.Clients.Commodore.DeleteClip(ctx, userToken, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to delete clip")
		return false, fmt.Errorf("failed to delete clip: %w", err)
	}

	return true, nil
}

// Helper functions

// stringPtr returns a pointer to the string value
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// getStringFromMap safely extracts a string value from a map and returns a pointer
func getStringFromMap(m map[string]string, key string) *string {
	if value, exists := m[key]; exists && value != "" {
		return &value
	}
	return nil
}

// === DVR & Recording Config ===

// DoStartDVR starts a DVR recording
func (r *Resolver) DoStartDVR(ctx context.Context, internalName string, streamID *string) (*commodore.StartDVRResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: start DVR")
		return &commodore.StartDVRResponse{Status: "started", DVRHash: "dvr_demo_hash"}, nil
	}
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}
	req := &commodore.StartDVRRequest{InternalName: internalName}
	if streamID != nil {
		req.StreamID = *streamID
	}
	res, err := r.Clients.Commodore.StartDVR(ctx, userToken, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to start DVR")
		return nil, fmt.Errorf("failed to start DVR: %w", err)
	}
	return res, nil
}

// DoStopDVR stops an ongoing DVR recording
func (r *Resolver) DoStopDVR(ctx context.Context, dvrHash string) (bool, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: stop DVR")
		return true, nil
	}
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return false, fmt.Errorf("user not authenticated")
	}
	if err := r.Clients.Commodore.StopDVR(ctx, userToken, dvrHash); err != nil {
		r.Logger.WithError(err).Error("Failed to stop DVR")
		return false, fmt.Errorf("failed to stop DVR: %w", err)
	}
	return true, nil
}

// DoGetRecordingConfig retrieves recording configuration for a stream
func (r *Resolver) DoGetRecordingConfig(ctx context.Context, internalName string) (*commodore.RecordingConfig, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: get recording config")
		return &commodore.RecordingConfig{Enabled: false, RetentionDays: 30, Format: "ts", SegmentDuration: 6}, nil
	}
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}
	cfg, err := r.Clients.Commodore.GetRecordingConfig(ctx, userToken, internalName)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get recording config")
		return nil, fmt.Errorf("failed to get recording config: %w", err)
	}
	return cfg, nil
}

// DoSetRecordingConfig updates recording configuration for a stream
func (r *Resolver) DoSetRecordingConfig(ctx context.Context, internalName string, cfg commodore.RecordingConfig) (*commodore.RecordingConfig, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: set recording config")
		return &cfg, nil
	}
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}
	out, err := r.Clients.Commodore.SetRecordingConfig(ctx, userToken, internalName, cfg)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to set recording config")
		return nil, fmt.Errorf("failed to set recording config: %w", err)
	}
	return out, nil
}

// DoListDVRRequests lists DVR recordings
func (r *Resolver) DoListDVRRequests(ctx context.Context, internalName *string, status *string, page, limit *int) (*commodore.DVRListResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: list DVR requests")
		return &commodore.DVRListResponse{DVRRecordings: []commodore.DVRInfo{}, Total: 0, Page: 1, Limit: 20}, nil
	}
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}
	out, err := r.Clients.Commodore.ListDVRRequests(ctx, userToken, internalName, status, page, limit)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list DVR requests")
		return nil, fmt.Errorf("failed to list DVR requests: %w", err)
	}
	return out, nil
}

// DoGetStreamMeta retrieves metadata for a stream
func (r *Resolver) DoGetStreamMeta(ctx context.Context, streamKey string, targetBaseURL *string, targetNodeID *string, includeRaw *bool) (*model.StreamMetaResponse, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo: get stream meta")
		rawData := `{"isLive":true,"bufferWindow":5000,"jitter":100,"unixOffset":1000,"now":1640995200000,"last":1640995195000,"width":1920,"height":1080,"version":3,"type":"video"}`
		return &model.StreamMetaResponse{
			MetaSummary: &model.StreamMetaSummary{
				IsLive:         true,
				BufferWindowMs: 5000,
				JitterMs:       100,
				UnixOffsetMs:   1000,
				NowMs:          intPtr(1640995200000),
				LastMs:         intPtr(1640995195000),
				Width:          intPtr(1920),
				Height:         intPtr(1080),
				Version:        intPtr(3),
				Type:           stringPtr("video"),
			},
			Raw: func() *string {
				if includeRaw != nil && *includeRaw {
					return &rawData
				}
				return nil
			}(),
		}, nil
	}

	// Call Commodore to get stream metadata
	// Commodore's GetStreamMeta signature: (ctx, streamKey string, includeRaw bool, targetBaseURL, targetNodeID *string)
	includeRawBool := includeRaw != nil && *includeRaw
	metaResp, err := r.Clients.Commodore.GetStreamMeta(ctx, streamKey, includeRawBool, targetBaseURL, targetNodeID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get stream metadata")
		return nil, fmt.Errorf("failed to get stream metadata: %w", err)
	}

	// Convert foghorn.MetaSummary to GraphQL model.StreamMetaSummary
	result := &model.StreamMetaSummary{
		IsLive:         metaResp.MetaSummary.IsLive,
		BufferWindowMs: int(metaResp.MetaSummary.BufferWindowMs), // Convert int64 to int
		JitterMs:       int(metaResp.MetaSummary.JitterMs),       // Convert int64 to int
		UnixOffsetMs:   int(metaResp.MetaSummary.UnixOffset),     // Convert int64 to int
	}

	// Add optional fields if present
	if metaResp.MetaSummary.NowMs != nil {
		nowMs := int(*metaResp.MetaSummary.NowMs)
		result.NowMs = &nowMs
	}
	if metaResp.MetaSummary.LastMs != nil {
		lastMs := int(*metaResp.MetaSummary.LastMs)
		result.LastMs = &lastMs
	}
	if metaResp.MetaSummary.Width != nil {
		result.Width = metaResp.MetaSummary.Width
	}
	if metaResp.MetaSummary.Height != nil {
		result.Height = metaResp.MetaSummary.Height
	}
	if metaResp.MetaSummary.Version != nil {
		result.Version = metaResp.MetaSummary.Version
	}
	if metaResp.MetaSummary.Type != "" {
		result.Type = &metaResp.MetaSummary.Type
	}

	// Build the response
	response := &model.StreamMetaResponse{
		MetaSummary: result,
	}

	// Include raw response if requested
	if includeRawBool && metaResp.Raw != nil {
		// Convert the 'any' type to string via JSON marshalling
		if rawBytes, err := json.Marshal(metaResp.Raw); err == nil {
			rawStr := string(rawBytes)
			response.Raw = &rawStr
		}
	}

	return response, nil
}

// intPtr returns a pointer to the int value
func intPtr(i int) *int {
	return &i
}

func (r *Resolver) getStreamsMemoized(ctx context.Context, userToken string) (*[]models.Stream, error) {
	if lds := loaders.FromContext(ctx); lds != nil && lds.Memo != nil {
		key := fmt.Sprintf("commodore:get_streams:%s", userToken)
		val, err := lds.Memo.GetOrLoad(key, func() (interface{}, error) {
			return r.Clients.Commodore.GetStreams(ctx, userToken)
		})
		if err != nil {
			return nil, err
		}
		streams, ok := val.(*[]models.Stream)
		if !ok {
			return nil, fmt.Errorf("unexpected cache type for %s", key)
		}
		return streams, nil
	}
	return r.Clients.Commodore.GetStreams(ctx, userToken)
}

func (r *Resolver) getStreamMemoized(ctx context.Context, userToken, streamID string) (*models.Stream, error) {
	if lds := loaders.FromContext(ctx); lds != nil && lds.Memo != nil {
		key := fmt.Sprintf("commodore:get_stream:%s:%s", userToken, streamID)
		val, err := lds.Memo.GetOrLoad(key, func() (interface{}, error) {
			return r.Clients.Commodore.GetStream(ctx, userToken, streamID)
		})
		if err != nil {
			return nil, err
		}
		stream, ok := val.(*models.Stream)
		if !ok {
			return nil, fmt.Errorf("unexpected cache type for %s", key)
		}
		return stream, nil
	}
	return r.Clients.Commodore.GetStream(ctx, userToken, streamID)
}
