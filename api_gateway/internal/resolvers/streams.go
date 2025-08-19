package resolvers

import (
	"context"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/pkg/api/commodore"
	"frameworks/pkg/models"
)

// DoGetStreams retrieves all streams for the authenticated user
func (r *Resolver) DoGetStreams(ctx context.Context) ([]*models.Stream, error) {
	// Extract JWT token from context (set by auth middleware)
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get streams from Commodore
	streams, err := r.Clients.Commodore.GetStreams(ctx, userToken)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get streams")
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}

	// Convert from []models.Stream to []*models.Stream
	result := make([]*models.Stream, len(*streams))
	for i := range *streams {
		result[i] = &(*streams)[i]
	}

	return result, nil
}

// DoGetStream retrieves a specific stream by ID
func (r *Resolver) DoGetStream(ctx context.Context, id string) (*models.Stream, error) {
	// Get all streams and find the one with matching ID
	// Note: Could be optimized with a dedicated GetStream endpoint
	streams, err := r.DoGetStreams(ctx)
	if err != nil {
		return nil, err
	}

	for _, stream := range streams {
		if stream.ID == id {
			return stream, nil
		}
	}

	return nil, fmt.Errorf("stream not found")
}

// DoCreateStream creates a new stream
func (r *Resolver) DoCreateStream(ctx context.Context, input model.CreateStreamInput) (*models.Stream, error) {
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

	var tenantIDPtr *string
	if validation.TenantID != "" {
		tenantIDPtr = &validation.TenantID
	}

	return &model.StreamValidation{
		Valid:     validation.Valid,
		StreamKey: streamKey, // Use the input streamKey since response doesn't include it
		Error:     errorPtr,
		TenantID:  tenantIDPtr,
	}, nil
}

// DoCreateClip creates a new clip
func (r *Resolver) DoCreateClip(ctx context.Context, input model.CreateClipInput) (*model.Clip, error) {
	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Convert to Commodore request format
	req := &commodore.CreateClipRequest{
		StreamID:  input.StreamID,
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
		StreamID:    clipResp.StreamID,
		TenantID:    clipResp.TenantID,
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
