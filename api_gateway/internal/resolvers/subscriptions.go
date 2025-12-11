package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// DoStreamUpdates handles real-time stream updates via WebSocket
// Returns proto.StreamEvent directly (bound to GraphQL StreamEvent)
func (r *Resolver) DoStreamUpdates(ctx context.Context, streamID *string) (<-chan *pb.StreamEvent, error) {
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
		ch := make(chan *pb.StreamEvent, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateStreamEvents()
			for _, event := range events {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(2 * time.Second)
			}
		}()
		return ch, nil
	}

	r.Logger.Info("Setting up stream updates subscription")
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("subscription_streams", "auth_error").Inc()
		}
		return nil, fmt.Errorf("authentication required for stream subscriptions: %w", err)
	}

	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	config := ConnectionConfig{UserID: user.UserID, TenantID: user.TenantID, JWT: jwtToken}

	ch, err := r.SubManager.SubscribeToStreams(ctx, config, streamID)
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("subscription_streams", "error").Inc()
		}
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":   user.UserID,
			"tenant_id": user.TenantID,
			"stream_id": streamID,
		}).Error("Failed to setup stream events subscription")
		return nil, fmt.Errorf("failed to setup stream events subscription: %w", err)
	}

	r.Logger.WithFields(logging.Fields{
		"user_id":   user.UserID,
		"tenant_id": user.TenantID,
		"stream_id": streamID,
	}).Info("Successfully setup stream events subscription")

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("subscription_streams", "success").Inc()
	}
	return ch, nil
}

// DoAnalyticsUpdates handles real-time analytics updates via WebSocket
// Returns proto.ClientLifecycleUpdate (bound to GraphQL ViewerMetrics)
func (r *Resolver) DoAnalyticsUpdates(ctx context.Context, streamID string) (<-chan *pb.ClientLifecycleUpdate, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo analytics subscription")
		ch := make(chan *pb.ClientLifecycleUpdate, 10)
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

	// Use subscription manager to subscribe to analytics updates
	// TODO: Update SubscribeToAnalytics to accept stream filter parameter
	ch, err := r.SubManager.SubscribeToAnalytics(ctx, config)
	if err != nil {
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":   user.UserID,
			"tenant_id": user.TenantID,
			"stream_id": streamID,
		}).Error("Failed to setup analytics subscription")
		return nil, fmt.Errorf("failed to setup analytics subscription: %w", err)
	}

	r.Logger.WithFields(logging.Fields{
		"user_id":   user.UserID,
		"tenant_id": user.TenantID,
		"stream_id": streamID,
	}).Info("Successfully setup analytics subscription")

	return ch, nil
}

// DoSystemUpdates handles real-time system updates via WebSocket
// Returns proto.NodeLifecycleUpdate (bound to GraphQL SystemHealthEvent)
func (r *Resolver) DoSystemUpdates(ctx context.Context) (<-chan *pb.NodeLifecycleUpdate, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo system health subscription")
		ch := make(chan *pb.NodeLifecycleUpdate, 10)
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

	// Use subscription manager to subscribe to system updates
	return r.SubManager.SubscribeToSystem(ctx, config)
}

// DoTrackListUpdates handles real-time track list updates via WebSocket
// Returns proto.StreamTrackListTrigger directly (bound to GraphQL TrackListEvent)
func (r *Resolver) DoTrackListUpdates(ctx context.Context, streamID string) (<-chan *pb.StreamTrackListTrigger, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo track list subscription")
		ch := make(chan *pb.StreamTrackListTrigger, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateTrackListEvents()
			for _, event := range events {
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

	r.Logger.Info("Setting up track list updates subscription")
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for track list subscriptions: %w", err)
	}

	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	config := ConnectionConfig{UserID: user.UserID, TenantID: user.TenantID, JWT: jwtToken}
	return r.SubManager.SubscribeToTrackList(ctx, config, streamID)
}
