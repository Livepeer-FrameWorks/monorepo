package resolvers

import (
	"context"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
)

// DoStreamUpdates handles real-time stream updates via WebSocket
func (r *Resolver) DoStreamUpdates(ctx context.Context, streamID *string) (<-chan *model.StreamEvent, error) {
	r.Logger.Info("Setting up stream updates subscription")

	// Get user from context - subscriptions require authentication
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for stream subscriptions: %w", err)
	}

	// Extract JWT token from context (this would be set by the auth middleware)
	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	// Create connection config
	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	// Use WebSocket manager to subscribe to stream updates
	return r.wsManager.SubscribeToStreams(ctx, config, streamID)
}

// DoAnalyticsUpdates handles real-time analytics updates via WebSocket
func (r *Resolver) DoAnalyticsUpdates(ctx context.Context) (<-chan *model.ViewerMetrics, error) {
	r.Logger.Info("Setting up analytics updates subscription")

	// Get user from context - subscriptions require authentication
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for analytics subscriptions: %w", err)
	}

	// Extract JWT token from context
	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	// Create connection config
	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	// Use WebSocket manager to subscribe to analytics updates
	return r.wsManager.SubscribeToAnalytics(ctx, config)
}

// DoSystemUpdates handles real-time system updates via WebSocket
func (r *Resolver) DoSystemUpdates(ctx context.Context) (<-chan *model.SystemHealthEvent, error) {
	r.Logger.Info("Setting up system updates subscription")

	// Get user from context - subscriptions require authentication
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for system subscriptions: %w", err)
	}

	// System updates are tenant-scoped - users can see updates for their own tenant's infrastructure
	// Super admins can see global system updates across all tenants
	// Regular users can only see their tenant's infrastructure updates

	// Extract JWT token from context
	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	// Create connection config
	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	// Use WebSocket manager to subscribe to system updates
	return r.wsManager.SubscribeToSystem(ctx, config)
}

// DoTrackListUpdates handles real-time track list updates via WebSocket
func (r *Resolver) DoTrackListUpdates(ctx context.Context, streamID string) (<-chan *model.TrackListEvent, error) {
	r.Logger.Info("Setting up track list updates subscription")

	// Get user from context - subscriptions require authentication
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for track list subscriptions: %w", err)
	}

	// Extract JWT token from context
	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	// Create connection config
	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	// Use WebSocket manager to subscribe to track list updates
	return r.wsManager.SubscribeToTrackList(ctx, config, streamID)
}

// DoTenantEvents handles real-time tenant events via WebSocket
func (r *Resolver) DoTenantEvents(ctx context.Context, tenantID string) (<-chan model.TenantEvent, error) {
	r.Logger.Info("Setting up tenant events subscription")

	// Get user from context - subscriptions require authentication
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for tenant event subscriptions: %w", err)
	}

	// Verify user has access to the requested tenant
	if user.TenantID != tenantID {
		return nil, fmt.Errorf("access denied: user cannot access tenant %s", tenantID)
	}

	// Extract JWT token from context
	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	// Create connection config
	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	// Use WebSocket manager to subscribe to tenant events
	return r.wsManager.SubscribeToTenantEvents(ctx, config, tenantID)
}

// DoUserEvents handles real-time user events via WebSocket (tenant determined from auth context)
func (r *Resolver) DoUserEvents(ctx context.Context) (<-chan model.TenantEvent, error) {
	r.Logger.Info("Setting up user events subscription")

	// Get user from context - subscriptions require authentication
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for user event subscriptions: %w", err)
	}

	// Extract JWT token from context
	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	// Create connection config
	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	// Use WebSocket manager to subscribe to user's tenant events
	return r.wsManager.SubscribeToTenantEvents(ctx, config, user.TenantID)
}
