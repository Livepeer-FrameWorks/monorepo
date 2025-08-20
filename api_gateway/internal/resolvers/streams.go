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
func (r *Resolver) DoCreateClip(ctx context.Context, input model.CreateClipInput) (*model.Clip, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clip creation")
		return &model.Clip{
			ID:          "clip_demo_" + time.Now().Format("20060102150405"),
			Stream:      input.Stream,
			Title:       input.Title,
			Description: input.Description,
			StartTime:   input.StartTime,
			EndTime:     input.EndTime,
			Duration:    input.EndTime - input.StartTime,
			PlaybackID:  "pb_clip_demo_" + time.Now().Format("150405"),
			Status:      "processing",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
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

	// Convert to GraphQL model
	return &model.Clip{
		ID:          clipResp.ID,
		Stream:      clipResp.StreamID,
		Title:       clipResp.Title,
		Description: &clipResp.Description,
		StartTime:   int(clipResp.StartTime),
		EndTime:     int(clipResp.EndTime),
		Duration:    int(clipResp.Duration),
		PlaybackID:  clipResp.PlaybackID,
		Status:      clipResp.Status,
		CreatedAt:   clipResp.CreatedAt,
		UpdatedAt:   clipResp.UpdatedAt,
	}, nil
}
