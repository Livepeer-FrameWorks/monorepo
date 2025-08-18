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
