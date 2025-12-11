package resolvers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_gateway/graph/model"
	signalmanclient "frameworks/pkg/clients/signalman"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// SubscriptionManager manages gRPC streaming connections to Signalman for GraphQL subscriptions
type SubscriptionManager struct {
	clients                 map[string]*signalmanclient.GRPCClient // Key: userID:tenantID
	logger                  logging.Logger
	mutex                   sync.RWMutex
	signalmanAddr           string
	serviceToken            string      // Service token for service-to-service authentication
	cleanup                 chan string // Channel for cleanup signals
	stopChan                chan struct{}
	metrics                 *GraphQLMetrics
	maxConnectionsPerTenant int
	tenantConnectionCounts  map[string]int
}

func (sm *SubscriptionManager) incrementTenantConnection(tenantID string) {
	if sm.maxConnectionsPerTenant <= 0 || tenantID == "" {
		return
	}
	sm.tenantConnectionCounts[tenantID]++
}

func (sm *SubscriptionManager) decrementTenantConnection(tenantID string) {
	if sm.maxConnectionsPerTenant <= 0 || tenantID == "" {
		return
	}
	if current, ok := sm.tenantConnectionCounts[tenantID]; ok {
		if current <= 1 {
			delete(sm.tenantConnectionCounts, tenantID)
		} else {
			sm.tenantConnectionCounts[tenantID] = current - 1
		}
	}
}

func (sm *SubscriptionManager) removeClientLocked(key string, client *signalmanclient.GRPCClient, tenantID string) {
	client.Close()
	delete(sm.clients, key)
	sm.decrementTenantConnection(tenantID)
}

// ConnectionConfig represents configuration for a gRPC connection
type ConnectionConfig struct {
	UserID   string
	TenantID string
	JWT      string // JWT is kept for compatibility but not used in gRPC (auth via metadata if needed)
}

// NewSubscriptionManager creates a new gRPC subscription connection manager
func NewSubscriptionManager(signalmanAddr, serviceToken string, logger logging.Logger, metrics *GraphQLMetrics, maxConnectionsPerTenant int) *SubscriptionManager {
	sm := &SubscriptionManager{
		clients:                 make(map[string]*signalmanclient.GRPCClient),
		logger:                  logger,
		signalmanAddr:           signalmanAddr,
		serviceToken:            serviceToken,
		cleanup:                 make(chan string, 10),
		stopChan:                make(chan struct{}),
		metrics:                 metrics,
		maxConnectionsPerTenant: maxConnectionsPerTenant,
		tenantConnectionCounts:  make(map[string]int),
	}

	// Start cleanup goroutine
	go sm.cleanupWorker()

	return sm
}

// GetOrCreateConnection gets an existing connection or creates a new one for a user/tenant
func (sm *SubscriptionManager) GetOrCreateConnection(ctx context.Context, config ConnectionConfig) (*signalmanclient.GRPCClient, error) {
	key := fmt.Sprintf("%s:%s", config.UserID, config.TenantID)

	sm.mutex.RLock()
	if client, exists := sm.clients[key]; exists && client.IsConnected() {
		sm.mutex.RUnlock()
		return client, nil
	}
	sm.mutex.RUnlock()

	// Need to create a new connection
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Double-check after acquiring write lock
	if client, exists := sm.clients[key]; exists && client.IsConnected() {
		return client, nil
	}
	if client, exists := sm.clients[key]; exists {
		sm.removeClientLocked(key, client, config.TenantID)
	}

	if sm.maxConnectionsPerTenant > 0 && sm.tenantConnectionCounts[config.TenantID] >= sm.maxConnectionsPerTenant {
		sm.logger.WithFields(logging.Fields{
			"tenant_id": config.TenantID,
			"limit":     sm.maxConnectionsPerTenant,
		}).Warn("Reached max Signalman connections for tenant")
		return nil, fmt.Errorf("tenant %s has reached the max number of active subscriptions", config.TenantID)
	}

	// Create new gRPC client
	client, err := signalmanclient.NewGRPCClient(signalmanclient.GRPCConfig{
		GRPCAddr:     sm.signalmanAddr,
		Timeout:      30 * time.Second,
		Logger:       sm.logger,
		UserID:       config.UserID,
		TenantID:     config.TenantID,
		ServiceToken: sm.serviceToken,
	})
	if err != nil {
		sm.logger.WithError(err).WithFields(logging.Fields{
			"user_id":   config.UserID,
			"tenant_id": config.TenantID,
		}).Error("Failed to create Signalman gRPC client")

		if sm.metrics != nil {
			sm.metrics.WebSocketMessages.WithLabelValues("outbound", "connection_error").Inc()
		}
		return nil, fmt.Errorf("failed to create Signalman gRPC client: %w", err)
	}

	// Connect the stream
	if err := client.Connect(ctx); err != nil {
		client.Close()
		sm.logger.WithError(err).WithFields(logging.Fields{
			"user_id":   config.UserID,
			"tenant_id": config.TenantID,
		}).Error("Failed to connect to Signalman gRPC")

		if sm.metrics != nil {
			sm.metrics.WebSocketMessages.WithLabelValues("outbound", "connection_error").Inc()
		}
		return nil, fmt.Errorf("failed to connect to Signalman gRPC: %w", err)
	}

	sm.clients[key] = client
	sm.incrementTenantConnection(config.TenantID)

	// Record successful connection
	if sm.metrics != nil {
		sm.metrics.WebSocketConnections.WithLabelValues(config.TenantID).Inc()
		sm.metrics.WebSocketMessages.WithLabelValues("outbound", "connection_success").Inc()
	}

	sm.logger.WithFields(logging.Fields{
		"user_id":   config.UserID,
		"tenant_id": config.TenantID,
		"key":       key,
	}).Info("Created new gRPC connection to Signalman")

	return client, nil
}

// SubscribeToStreams subscribes to stream events and returns a channel of updates
// Returns proto.StreamEvent directly (bound to GraphQL StreamEvent)
func (sm *SubscriptionManager) SubscribeToStreams(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.StreamEvent, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to streams: %w", err)
	}

	updates := make(chan *pb.StreamEvent, 10)
	go sm.processStreamMessages(ctx, client, updates, streamID)
	return updates, nil
}

// SubscribeToAnalytics subscribes to analytics events and returns a channel of updates
// Returns proto.ClientLifecycleUpdate directly (bound to GraphQL ViewerMetrics)
func (sm *SubscriptionManager) SubscribeToAnalytics(ctx context.Context, config ConnectionConfig) (<-chan *pb.ClientLifecycleUpdate, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.ClientLifecycleUpdate, 10)
	go sm.processAnalyticsMessages(ctx, client, updates)
	return updates, nil
}

// SubscribeToSystem subscribes to system events and returns a channel of updates
// Returns proto.NodeLifecycleUpdate directly (bound to GraphQL SystemHealthEvent)
func (sm *SubscriptionManager) SubscribeToSystem(ctx context.Context, config ConnectionConfig) (<-chan *pb.NodeLifecycleUpdate, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToSystem(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to system: %w", err)
	}

	updates := make(chan *pb.NodeLifecycleUpdate, 10)
	go sm.processSystemMessages(ctx, client, updates)
	return updates, nil
}

// SubscribeToTrackList subscribes to track list events and returns a channel of updates
// Returns proto.StreamTrackListTrigger directly (bound to GraphQL TrackListEvent)
func (sm *SubscriptionManager) SubscribeToTrackList(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.StreamTrackListTrigger, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to track list updates: %w", err)
	}

	updates := make(chan *pb.StreamTrackListTrigger, 10)
	go sm.processTrackListMessages(ctx, client, updates, streamID)
	return updates, nil
}

// SubscribeToLifecycle subscribes to lifecycle events (clip) and returns a channel
// Returns proto.ClipLifecycleData directly (bound to GraphQL ClipLifecycle)
func (sm *SubscriptionManager) SubscribeToLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.ClipLifecycleData, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to lifecycle: %w", err)
	}
	updates := make(chan *pb.ClipLifecycleData, 10)
	go sm.processLifecycleMessages(ctx, client, updates, streamID)
	return updates, nil
}

// SubscribeToDVRLifecycle subscribes to DVR lifecycle events and returns a channel
// Returns proto.DVRLifecycleData directly (bound to GraphQL DVREvent)
func (sm *SubscriptionManager) SubscribeToDVRLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.DVRLifecycleData, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to DVR lifecycle: %w", err)
	}
	updates := make(chan *pb.DVRLifecycleData, 10)
	go sm.processDVRLifecycleMessages(ctx, client, updates, streamID)
	return updates, nil
}

// SubscribeToFirehose subscribes to ALL events (streams, analytics, system) and returns a unified channel
func (sm *SubscriptionManager) SubscribeToFirehose(ctx context.Context, config ConnectionConfig) (<-chan *model.TenantEvent, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	// Subscribe to all channels
	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to streams for firehose: %w", err)
	}
	if err := client.SubscribeToAnalytics(); err != nil {
		sm.logger.Warn("Failed to subscribe to analytics for firehose", "error", err)
		// Continue - analytics subscription is optional
	}
	if err := client.SubscribeToSystem(); err != nil {
		sm.logger.Warn("Failed to subscribe to system for firehose", "error", err)
		// Continue - system subscription is optional
	}

	updates := make(chan *model.TenantEvent, 50) // Larger buffer for firehose
	go sm.processFirehoseMessages(ctx, client, updates)
	return updates, nil
}

// processFirehoseMessages processes ALL events from Signalman and converts them to TenantEvent
func (sm *SubscriptionManager) processFirehoseMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.TenantEvent) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			tenantEvent := sm.convertProtoToTenantEvent(event)
			if tenantEvent != nil {
				select {
				case output <- tenantEvent:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// convertProtoToTenantEvent converts any Signalman proto event to a unified TenantEvent
// Passes proto types directly - they're bound via gqlgen.yml
func (sm *SubscriptionManager) convertProtoToTenantEvent(event *pb.SignalmanEvent) *model.TenantEvent {
	if event == nil {
		return nil
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	}

	eventType := protoEventTypeToString(event.EventType)
	channel := sm.getChannelForEventType(event.EventType)

	tenantEvent := &model.TenantEvent{
		Type:      eventType,
		Channel:   channel,
		Timestamp: timestamp,
	}

	if event.Data == nil {
		return tenantEvent
	}

	// Populate the appropriate event type based on the channel/event type
	// Pass proto types directly - they're bound via gqlgen.yml
	switch {
	case sm.isStreamEvent(event.EventType):
		tenantEvent.StreamEvent = sm.extractStreamEvent(event)

	case event.EventType == pb.EventType_EVENT_TYPE_VIEWER_CONNECT ||
		event.EventType == pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT ||
		event.EventType == pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE:
		// Pass proto ClientLifecycleUpdate directly (bound to ViewerMetrics)
		tenantEvent.ViewerMetrics = event.Data.GetClientLifecycle()

	case event.EventType == pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST:
		// Pass proto StreamTrackListTrigger directly (bound to TrackListUpdate)
		tenantEvent.TrackListUpdate = event.Data.GetTrackList()

	case event.EventType == pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE:
		// Pass proto ClipLifecycleData directly (bound to ClipLifecycle)
		tenantEvent.ClipLifecycle = event.Data.GetClipLifecycle()

	case event.EventType == pb.EventType_EVENT_TYPE_DVR_LIFECYCLE:
		// DVR lifecycle - pass DVRLifecycleData
		// Note: GraphQL dvrLifecycle subscription returns ClipEvent, but the types are different
		// For firehose we could add a separate DVREvent field if needed
		tenantEvent.DvrEvent = event.Data.GetDvrLifecycle()

	case event.EventType == pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE:
		// Pass proto NodeLifecycleUpdate directly (bound to SystemHealthEvent)
		tenantEvent.SystemHealthEvent = event.Data.GetNodeLifecycle()
	}

	return tenantEvent
}

// getChannelForEventType returns the channel name for a given event type
func (sm *SubscriptionManager) getChannelForEventType(eventType pb.EventType) string {
	switch eventType {
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST,
		pb.EventType_EVENT_TYPE_STREAM_BUFFER,
		pb.EventType_EVENT_TYPE_STREAM_END,
		pb.EventType_EVENT_TYPE_STREAM_SOURCE,
		pb.EventType_EVENT_TYPE_PLAY_REWRITE:
		return "STREAMS"

	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		return "SYSTEM"

	default:
		return "ANALYTICS"
	}
}

// processStreamMessages processes stream messages from Signalman gRPC
// Passes proto.StreamEvent directly without conversion
func (sm *SubscriptionManager) processStreamMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.StreamEvent, streamID *string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter by event type
			if !sm.isStreamEvent(event.EventType) {
				continue
			}

			// Filter by stream ID if specified
			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID != "" && msgStreamID != *streamID {
					continue
				}
			}

			if update := sm.extractStreamEvent(event); update != nil {
				select {
				case output <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// processAnalyticsMessages processes analytics messages from Signalman gRPC
// Passes proto.ClientLifecycleUpdate directly without conversion
func (sm *SubscriptionManager) processAnalyticsMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ClientLifecycleUpdate) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for client lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_VIEWER_CONNECT &&
				event.EventType != pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT &&
				event.EventType != pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE {
				continue
			}

			// Extract ClientLifecycleUpdate directly from proto
			if event.Data != nil {
				if cl := event.Data.GetClientLifecycle(); cl != nil {
					select {
					case output <- cl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processSystemMessages processes system messages from Signalman gRPC
// Passes proto.NodeLifecycleUpdate directly without conversion
func (sm *SubscriptionManager) processSystemMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.NodeLifecycleUpdate) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for node lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE {
				continue
			}

			// Extract NodeLifecycleUpdate directly from proto
			if event.Data != nil {
				if nl := event.Data.GetNodeLifecycle(); nl != nil {
					select {
					case output <- nl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processTrackListMessages processes track list messages from Signalman gRPC
// Passes proto.StreamTrackListTrigger directly without conversion
func (sm *SubscriptionManager) processTrackListMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.StreamTrackListTrigger, streamID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			if event.EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST {
				continue
			}

			// Filter by stream ID if specified
			if streamID != "" {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID != "" && msgStreamID != streamID {
					continue
				}
			}

			// Extract StreamTrackListTrigger directly from proto
			if event.Data != nil {
				if tl := event.Data.GetTrackList(); tl != nil {
					select {
					case output <- tl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processLifecycleMessages processes clip/dvr lifecycle messages from Signalman gRPC
// Passes proto.ClipLifecycleData directly without conversion
func (sm *SubscriptionManager) processLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ClipLifecycleData, streamID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for clip lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE {
				continue
			}

			// Extract ClipLifecycleData directly from proto
			if event.Data != nil {
				if cl := event.Data.GetClipLifecycle(); cl != nil {
					// Filter by stream ID if specified
					if streamID != "" && cl.GetInternalName() != streamID {
						continue
					}
					select {
					case output <- cl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

func (sm *SubscriptionManager) processDVRLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.DVRLifecycleData, streamID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for DVR lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_DVR_LIFECYCLE {
				continue
			}

			// Extract DVRLifecycleData directly from proto
			if event.Data != nil {
				if dvr := event.Data.GetDvrLifecycle(); dvr != nil {
					// Filter by stream ID if specified
					if streamID != "" && dvr.GetInternalName() != streamID {
						continue
					}
					select {
					case output <- dvr:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// extractClientLifecycle extracts ClientLifecycleUpdate from a Signalman event
// Returns nil if event is not a viewer/client lifecycle event
func (sm *SubscriptionManager) extractClientLifecycle(event *pb.SignalmanEvent) *pb.ClientLifecycleUpdate {
	if event.EventType != pb.EventType_EVENT_TYPE_VIEWER_CONNECT &&
		event.EventType != pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT &&
		event.EventType != pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE {
		return nil
	}
	if event.Data == nil {
		return nil
	}
	return event.Data.GetClientLifecycle()
}

// extractNodeLifecycle extracts NodeLifecycleUpdate from a Signalman event
// Returns nil if event is not a node lifecycle event
func (sm *SubscriptionManager) extractNodeLifecycle(event *pb.SignalmanEvent) *pb.NodeLifecycleUpdate {
	if event.EventType != pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE {
		return nil
	}
	if event.Data == nil {
		return nil
	}
	return event.Data.GetNodeLifecycle()
}

// extractTrackList extracts StreamTrackListTrigger from a Signalman event
// Returns nil if event is not a track list event
func (sm *SubscriptionManager) extractTrackList(event *pb.SignalmanEvent) *pb.StreamTrackListTrigger {
	if event.EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST {
		return nil
	}
	if event.Data == nil {
		return nil
	}
	return event.Data.GetTrackList()
}

// CleanupConnection removes a connection from the pool
func (sm *SubscriptionManager) CleanupConnection(userID, tenantID string) {
	key := fmt.Sprintf("%s:%s", userID, tenantID)

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if client, exists := sm.clients[key]; exists {
		sm.removeClientLocked(key, client, tenantID)

		sm.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
			"key":       key,
		}).Info("Cleaned up gRPC connection")
	}
}

// cleanupWorker handles cleanup requests
func (sm *SubscriptionManager) cleanupWorker() {
	ticker := time.NewTicker(5 * time.Minute) // Periodic cleanup
	defer ticker.Stop()

	for {
		select {
		case <-sm.stopChan:
			return
		case key := <-sm.cleanup:
			// Handle specific cleanup request
			sm.mutex.Lock()
			if client, exists := sm.clients[key]; exists {
				// Extract tenant ID from key
				tenantID := ""
				if len(key) > 0 {
					if idx := len(key) - 1; idx > 0 {
						// key is userID:tenantID format, extract tenantID
						for i := len(key) - 1; i >= 0; i-- {
							if key[i] == ':' {
								tenantID = key[i+1:]
								break
							}
						}
					}
				}
				sm.removeClientLocked(key, client, tenantID)
			}
			sm.mutex.Unlock()
		case <-ticker.C:
			// Periodic cleanup of disconnected clients
			sm.periodicCleanup()
		}
	}
}

// periodicCleanup removes disconnected clients
func (sm *SubscriptionManager) periodicCleanup() {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	type clientInfo struct {
		key      string
		tenantID string
	}
	var toRemove []clientInfo

	for key, client := range sm.clients {
		if !client.IsConnected() {
			// Extract tenant ID from key
			tenantID := ""
			for i := len(key) - 1; i >= 0; i-- {
				if key[i] == ':' {
					tenantID = key[i+1:]
					break
				}
			}
			toRemove = append(toRemove, clientInfo{key: key, tenantID: tenantID})
		}
	}

	for _, info := range toRemove {
		if client, exists := sm.clients[info.key]; exists {
			sm.removeClientLocked(info.key, client, info.tenantID)
		}
	}

	if len(toRemove) > 0 {
		sm.logger.WithFields(logging.Fields{
			"cleaned_connections": len(toRemove),
		}).Info("Periodic cleanup removed disconnected gRPC connections")
	}
}

// Shutdown gracefully shuts down the subscription manager
func (sm *SubscriptionManager) Shutdown() error {
	close(sm.stopChan)

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Close all connections
	for key, client := range sm.clients {
		// Extract tenant ID from key
		tenantID := ""
		for i := len(key) - 1; i >= 0; i-- {
			if key[i] == ':' {
				tenantID = key[i+1:]
				break
			}
		}
		sm.removeClientLocked(key, client, tenantID)
	}

	sm.logger.Info("Subscription manager shutdown completed")
	return nil
}

// getStreamIDFromProtoEvent extracts stream ID from a proto SignalmanEvent
func getStreamIDFromProtoEvent(event *pb.SignalmanEvent) string {
	if event.Data == nil {
		return ""
	}

	// Check each possible payload type for stream identification
	if cl := event.Data.GetClientLifecycle(); cl != nil {
		return cl.InternalName
	}
	if nl := event.Data.GetNodeLifecycle(); nl != nil {
		return "" // NodeLifecycle doesn't have stream context
	}
	if tl := event.Data.GetTrackList(); tl != nil {
		return tl.StreamName
	}
	if cl := event.Data.GetClipLifecycle(); cl != nil {
		return cl.GetInternalName()
	}
	if dl := event.Data.GetDvrLifecycle(); dl != nil {
		return dl.GetInternalName()
	}
	if lb := event.Data.GetLoadBalancing(); lb != nil {
		return lb.GetInternalName()
	}
	if pr := event.Data.GetPushRewrite(); pr != nil {
		return pr.StreamName
	}
	if pos := event.Data.GetPushOutStart(); pos != nil {
		return pos.StreamName
	}
	if pe := event.Data.GetPushEnd(); pe != nil {
		return pe.StreamName
	}
	if vc := event.Data.GetViewerConnect(); vc != nil {
		return vc.StreamName
	}
	if vd := event.Data.GetViewerDisconnect(); vd != nil {
		return vd.StreamName
	}
	if se := event.Data.GetStreamEnd(); se != nil {
		return se.StreamName
	}
	if sb := event.Data.GetStreamBandwidth(); sb != nil {
		return sb.StreamName
	}
	if rec := event.Data.GetRecording(); rec != nil {
		return rec.StreamName
	}
	return ""
}

// isStreamEvent checks if the event type is a stream-related event
func (sm *SubscriptionManager) isStreamEvent(eventType pb.EventType) bool {
	switch eventType {
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST,
		pb.EventType_EVENT_TYPE_STREAM_BUFFER,
		pb.EventType_EVENT_TYPE_STREAM_END,
		pb.EventType_EVENT_TYPE_STREAM_SOURCE,
		pb.EventType_EVENT_TYPE_PLAY_REWRITE:
		return true
	default:
		return false
	}
}

// extractStreamEvent extracts and constructs a proto.StreamEvent from a Signalman event
func (sm *SubscriptionManager) extractStreamEvent(event *pb.SignalmanEvent) *pb.StreamEvent {
	streamID := getStreamIDFromProtoEvent(event)
	status := ""

	switch event.EventType {
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE:
		status = "LIVE"
	case pb.EventType_EVENT_TYPE_STREAM_END:
		status = "ENDED"
	case pb.EventType_EVENT_TYPE_STREAM_BUFFER, pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST:
		status = "LIVE"
	default:
		return nil
	}

	return &pb.StreamEvent{
		Timestamp:    event.Timestamp,
		EventType:    protoEventTypeToString(event.EventType),
		Status:       status,
		InternalName: streamID,
	}
}

// extractClipLifecycle extracts ClipLifecycleData from a Signalman event
// Returns nil if event is not a clip lifecycle event
func (sm *SubscriptionManager) extractClipLifecycle(event *pb.SignalmanEvent) *pb.ClipLifecycleData {
	if event.EventType != pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE {
		return nil
	}
	if event.Data == nil {
		return nil
	}
	return event.Data.GetClipLifecycle()
}

// extractDvrLifecycle extracts DVRLifecycleData from a Signalman event
// Returns nil if event is not a DVR lifecycle event
func (sm *SubscriptionManager) extractDvrLifecycle(event *pb.SignalmanEvent) *pb.DVRLifecycleData {
	if event.EventType != pb.EventType_EVENT_TYPE_DVR_LIFECYCLE {
		return nil
	}
	if event.Data == nil {
		return nil
	}
	return event.Data.GetDvrLifecycle()
}

// protoEventTypeToString converts proto EventType to string for backward compatibility
func protoEventTypeToString(et pb.EventType) string {
	switch et {
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE:
		return "stream_lifecycle_update"
	case pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST:
		return "stream_track_list"
	case pb.EventType_EVENT_TYPE_STREAM_BUFFER:
		return "stream_buffer"
	case pb.EventType_EVENT_TYPE_STREAM_END:
		return "stream_end"
	case pb.EventType_EVENT_TYPE_STREAM_SOURCE:
		return "stream_source"
	case pb.EventType_EVENT_TYPE_PLAY_REWRITE:
		return "play_rewrite"
	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE:
		return "node_lifecycle_update"
	case pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		return "load_balancing"
	case pb.EventType_EVENT_TYPE_VIEWER_CONNECT:
		return "viewer_connect"
	case pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT:
		return "viewer_disconnect"
	case pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE:
		return "client_lifecycle_update"
	case pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE:
		return "clip_lifecycle"
	case pb.EventType_EVENT_TYPE_DVR_LIFECYCLE:
		return "dvr_lifecycle"
	case pb.EventType_EVENT_TYPE_PUSH_REWRITE:
		return "push_rewrite"
	case pb.EventType_EVENT_TYPE_PUSH_OUT_START:
		return "push_out_start"
	case pb.EventType_EVENT_TYPE_PUSH_END:
		return "push_end"
	case pb.EventType_EVENT_TYPE_STREAM_BANDWIDTH:
		return "stream_bandwidth"
	case pb.EventType_EVENT_TYPE_RECORDING_COMPLETE:
		return "recording_complete"
	default:
		return "unknown"
	}
}

func int64PtrFrom(v *int64) *int64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func uint32PtrFrom(v *uint32) *uint32 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func uint64PtrFrom(v *uint64) *uint64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
