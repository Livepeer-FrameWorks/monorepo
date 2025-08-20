package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
)

// DoStreamUpdates handles real-time stream updates via WebSocket
func (r *Resolver) DoStreamUpdates(ctx context.Context, streamID *string) (<-chan *model.StreamEvent, error) {
	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("subscription_streams", "requested").Inc()
		defer func() {
			r.Metrics.SubscriptionsActive.WithLabelValues("streams").Inc()
		}()
	}

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream events subscription")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("subscription_streams", "demo").Inc()
		}
		ch := make(chan *model.StreamEvent, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateStreamEvents()
			for _, event := range events {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(2 * time.Second) // Send events every 2 seconds
			}
		}()
		return ch, nil
	}

	r.Logger.Info("Setting up stream updates subscription")

	// Get user from context - subscriptions require authentication
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("subscription_streams", "auth_error").Inc()
		}
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
	ch, err := r.wsManager.SubscribeToStreams(ctx, config, streamID)
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("subscription_streams", "error").Inc()
		}
		return nil, err
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("subscription_streams", "success").Inc()
	}
	return ch, err
}

// DoAnalyticsUpdates handles real-time analytics updates via WebSocket
func (r *Resolver) DoAnalyticsUpdates(ctx context.Context, streamID string) (<-chan *model.ViewerMetrics, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo analytics subscription")
		ch := make(chan *model.ViewerMetrics, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateViewerMetricsEvents()
			for _, event := range events {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(3 * time.Second) // Send analytics every 3 seconds
			}
		}()
		return ch, nil
	}

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
	// TODO: Update SubscribeToAnalytics to accept stream filter parameter
	return r.wsManager.SubscribeToAnalytics(ctx, config)
}

// DoSystemUpdates handles real-time system updates via WebSocket
func (r *Resolver) DoSystemUpdates(ctx context.Context) (<-chan *model.SystemHealthEvent, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo system health subscription")
		ch := make(chan *model.SystemHealthEvent, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateSystemHealthEvents()
			for _, event := range events {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(5 * time.Second) // Send health updates every 5 seconds
			}
		}()
		return ch, nil
	}

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
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo track list subscription")
		ch := make(chan *model.TrackListEvent, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateTrackListEvents()
			for _, event := range events {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(4 * time.Second) // Send track updates every 4 seconds
			}
		}()
		return ch, nil
	}

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
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo tenant events subscription")
		ch := make(chan model.TenantEvent, 10)
		go func() {
			defer close(ch)
			// Mix different types of tenant events
			streamEvents := demo.GenerateStreamEvents()
			viewerEvents := demo.GenerateViewerMetricsEvents()
			trackEvents := demo.GenerateTrackListEvents()

			// Send mixed events
			for i := 0; i < len(streamEvents) || i < len(viewerEvents) || i < len(trackEvents); i++ {
				if i < len(streamEvents) {
					select {
					case ch <- streamEvents[i]:
					case <-ctx.Done():
						return
					}
					time.Sleep(2 * time.Second)
				}
				if i < len(viewerEvents) {
					select {
					case ch <- viewerEvents[i]:
					case <-ctx.Done():
						return
					}
					time.Sleep(3 * time.Second)
				}
				if i < len(trackEvents) {
					select {
					case ch <- trackEvents[i]:
					case <-ctx.Done():
						return
					}
					time.Sleep(4 * time.Second)
				}
			}
		}()
		return ch, nil
	}

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
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo user events subscription")
		ch := make(chan model.TenantEvent, 10)
		go func() {
			defer close(ch)
			// Mix different types of tenant events
			streamEvents := demo.GenerateStreamEvents()
			viewerEvents := demo.GenerateViewerMetricsEvents()
			trackEvents := demo.GenerateTrackListEvents()

			// Send stream events
			for _, event := range streamEvents {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(2 * time.Second)
			}

			// Send viewer metrics
			for _, event := range viewerEvents {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(3 * time.Second)
			}

			// Send track list events
			for _, event := range trackEvents {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(4 * time.Second)
			}
		}()
		return ch, nil
	}

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
