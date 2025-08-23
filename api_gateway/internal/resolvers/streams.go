package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
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

	// Get streams from Commodore
	streams, err := r.Clients.Commodore.GetStreams(ctx, userToken)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get streams")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("streams", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}

	// Convert from []models.Stream to []*models.Stream
	result := make([]*models.Stream, len(*streams))
	for i := range *streams {
		result[i] = &(*streams)[i]
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

	// Get all streams and find the one with matching ID
	// Note: Could be optimized with a dedicated GetStream endpoint
	streams, err := r.DoGetStreams(ctx)
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("stream", "error").Inc()
		}
		return nil, err
	}

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
