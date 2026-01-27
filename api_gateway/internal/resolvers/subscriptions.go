package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/globalid"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// DoStreamUpdates handles real-time stream updates via WebSocket
// Returns model.StreamEvent (canonical live stream event shape)
func (r *Resolver) DoStreamUpdates(ctx context.Context, streamID *string) (<-chan *model.StreamEvent, error) {
	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("subscription_streams", "requested").Inc()
		defer func() {
			r.Metrics.SubscriptionsActive.WithLabelValues("streams").Inc()
		}()
	}

	normalizedStreamID, err := normalizeStreamIDPtr(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream events subscription")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("subscription_streams", "demo").Inc()
		}
		ch := make(chan *model.StreamEvent, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateStreamSubscriptionEvents()
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
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

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

	var streamIDPtr *string
	if streamID != "" {
		streamIDPtr = &streamID
	}

	// Use subscription manager to subscribe to analytics updates
	ch, err := r.SubManager.SubscribeToAnalytics(ctx, config, streamIDPtr)
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

// DoConnectionEvents handles real-time viewer connection events via WebSocket
// Returns proto.ConnectionEvent (bound to GraphQL ConnectionEvent)
func (r *Resolver) DoConnectionEvents(ctx context.Context, streamID *string) (<-chan *pb.ConnectionEvent, error) {
	normalizedStreamID, err := normalizeStreamIDPtr(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo connection events subscription")
		ch := make(chan *pb.ConnectionEvent, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateConnectionEventSubscriptionEvents()
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

	r.Logger.Info("Setting up connection events subscription")

	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for connection subscriptions: %w", err)
	}

	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	ch, err := r.SubManager.SubscribeToConnections(ctx, config, streamID)
	if err != nil {
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":   user.UserID,
			"tenant_id": user.TenantID,
			"stream_id": streamID,
		}).Error("Failed to setup connection events subscription")
		return nil, fmt.Errorf("failed to setup connection events subscription: %w", err)
	}

	r.Logger.WithFields(logging.Fields{
		"user_id":   user.UserID,
		"tenant_id": user.TenantID,
		"stream_id": streamID,
	}).Info("Successfully setup connection events subscription")

	return ch, nil
}

// DoStorageEvents handles real-time storage lifecycle events via WebSocket
// Returns proto.StorageEvent (mapped from StorageLifecycleData)
func (r *Resolver) DoStorageEvents(ctx context.Context, streamID *string) (<-chan *pb.StorageEvent, error) {
	normalizedStreamID, err := normalizeStreamIDPtr(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo storage events subscription")
		ch := make(chan *pb.StorageEvent, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateStorageEventSubscriptionEvents()
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

	r.Logger.Info("Setting up storage events subscription")
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for storage subscriptions: %w", err)
	}

	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	ch, err := r.SubManager.SubscribeToStorageEvents(ctx, config, streamID)
	if err != nil {
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":   user.UserID,
			"tenant_id": user.TenantID,
			"stream_id": streamID,
		}).Error("Failed to setup storage events subscription")
		return nil, fmt.Errorf("failed to setup storage events subscription: %w", err)
	}

	r.Logger.WithFields(logging.Fields{
		"user_id":   user.UserID,
		"tenant_id": user.TenantID,
		"stream_id": streamID,
	}).Info("Successfully setup storage events subscription")

	return ch, nil
}

// DoProcessingEvents handles real-time processing/transcoding events via WebSocket
// Returns proto.ProcessingUsageRecord (mapped from ProcessBillingEvent)
func (r *Resolver) DoProcessingEvents(ctx context.Context, streamID *string) (<-chan *pb.ProcessingUsageRecord, error) {
	normalizedStreamID, err := normalizeStreamIDPtr(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedStreamID

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo processing events subscription")
		ch := make(chan *pb.ProcessingUsageRecord, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateProcessingEventSubscriptionEvents()
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

	r.Logger.Info("Setting up processing events subscription")
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for processing subscriptions: %w", err)
	}

	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	config := ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      jwtToken,
	}

	ch, err := r.SubManager.SubscribeToProcessingEvents(ctx, config, streamID)
	if err != nil {
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":   user.UserID,
			"tenant_id": user.TenantID,
			"stream_id": streamID,
		}).Error("Failed to setup processing events subscription")
		return nil, fmt.Errorf("failed to setup processing events subscription: %w", err)
	}

	r.Logger.WithFields(logging.Fields{
		"user_id":   user.UserID,
		"tenant_id": user.TenantID,
		"stream_id": streamID,
	}).Info("Successfully setup processing events subscription")

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
	normalizedID, err := normalizeStreamID(streamID)
	if err != nil {
		return nil, err
	}
	streamID = normalizedID

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

// DoMessageUpdates handles real-time message updates via WebSocket
// Returns model.Message (mapped from MessageLifecycleData)
func (r *Resolver) DoMessageUpdates(ctx context.Context, conversationID string) (<-chan *model.Message, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo message subscription")
		rawConversationID := conversationID
		if typ, rawID, ok := globalid.Decode(conversationID); ok && typ == globalid.TypeConversation {
			rawConversationID = rawID
		}
		ch := make(chan *model.Message, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateMessageSubscriptionEvents(rawConversationID)
			for _, event := range events {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(3 * time.Second)
			}
		}()
		return ch, nil
	}

	r.Logger.Info("Setting up message updates subscription")
	rawConversationID := conversationID
	if typ, rawID, ok := globalid.Decode(conversationID); ok && typ == globalid.TypeConversation {
		rawConversationID = rawID
	}
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for message subscriptions: %w", err)
	}

	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	config := ConnectionConfig{UserID: user.UserID, TenantID: user.TenantID, JWT: jwtToken}

	ch, err := r.SubManager.SubscribeToMessages(ctx, config, rawConversationID)
	if err != nil {
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":         user.UserID,
			"tenant_id":       user.TenantID,
			"conversation_id": conversationID,
		}).Error("Failed to setup message subscription")
		return nil, fmt.Errorf("failed to setup message subscription: %w", err)
	}

	r.Logger.WithFields(logging.Fields{
		"user_id":         user.UserID,
		"tenant_id":       user.TenantID,
		"conversation_id": conversationID,
	}).Info("Successfully setup message subscription")

	return ch, nil
}

// DoConversationUpdates handles real-time conversation lifecycle updates via WebSocket
// Returns model.Conversation (mapped from MessageLifecycleData)
func (r *Resolver) DoConversationUpdates(ctx context.Context, conversationID *string) (<-chan *model.Conversation, error) {
	rawConversationID := ""
	if conversationID != nil {
		rawConversationID = *conversationID
		if typ, rawID, ok := globalid.Decode(*conversationID); ok && typ == globalid.TypeConversation {
			rawConversationID = rawID
		}
	}

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo conversation subscription")
		ch := make(chan *model.Conversation, 10)
		go func() {
			defer close(ch)
			events := demo.GenerateConversationSubscriptionEvents(rawConversationID)
			for _, event := range events {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				time.Sleep(3 * time.Second)
			}
		}()
		return ch, nil
	}

	r.Logger.Info("Setting up conversation updates subscription")
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for conversation subscriptions: %w", err)
	}

	jwtToken := ""
	if token := ctx.Value("jwt_token"); token != nil {
		if tokenStr, ok := token.(string); ok {
			jwtToken = tokenStr
		}
	}

	config := ConnectionConfig{UserID: user.UserID, TenantID: user.TenantID, JWT: jwtToken}

	ch, err := r.SubManager.SubscribeToConversations(ctx, config, rawConversationID)
	if err != nil {
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":         user.UserID,
			"tenant_id":       user.TenantID,
			"conversation_id": rawConversationID,
		}).Error("Failed to setup conversation subscription")
		return nil, fmt.Errorf("failed to setup conversation subscription: %w", err)
	}

	r.Logger.WithFields(logging.Fields{
		"user_id":         user.UserID,
		"tenant_id":       user.TenantID,
		"conversation_id": rawConversationID,
	}).Info("Successfully setup conversation subscription")

	return ch, nil
}
