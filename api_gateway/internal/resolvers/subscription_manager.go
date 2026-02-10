package resolvers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"frameworks/api_gateway/graph/model"
	signalmanclient "frameworks/pkg/clients/signalman"
	"frameworks/pkg/globalid"
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
	if err := client.Close(); err != nil {
		sm.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": tenantID,
		}).Warn("Failed to close Signalman gRPC client")
	}
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
		if closeErr := client.Close(); closeErr != nil {
			sm.logger.WithError(closeErr).WithFields(logging.Fields{
				"user_id":   config.UserID,
				"tenant_id": config.TenantID,
			}).Warn("Failed to close Signalman gRPC client after connect failure")
		}
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
// Returns model.StreamEvent (canonical live stream event shape)
func (sm *SubscriptionManager) SubscribeToStreams(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *model.StreamEvent, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to streams: %w", err)
	}

	updates := make(chan *model.StreamEvent, 10)
	go sm.processStreamMessages(ctx, client, updates, streamID, config.TenantID)
	return updates, nil
}

// SubscribeToAnalytics subscribes to analytics events and returns a channel of updates
// Returns proto.ClientLifecycleUpdate directly (bound to GraphQL ViewerMetrics)
func (sm *SubscriptionManager) SubscribeToAnalytics(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.ClientLifecycleUpdate, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.ClientLifecycleUpdate, 10)
	go sm.processAnalyticsMessages(ctx, client, updates, streamID, config.TenantID)
	return updates, nil
}

// SubscribeToConnections subscribes to viewer connection events and returns a channel of updates
// Returns proto.ConnectionEvent directly (bound to GraphQL ConnectionEvent)
func (sm *SubscriptionManager) SubscribeToConnections(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.ConnectionEvent, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.ConnectionEvent, 10)
	go sm.processConnectionMessages(ctx, client, updates, streamID, config.TenantID)
	return updates, nil
}

// SubscribeToStorageEvents subscribes to storage lifecycle events and returns a channel of updates
// Returns proto.StorageEvent (mapped from StorageLifecycleData)
func (sm *SubscriptionManager) SubscribeToStorageEvents(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.StorageEvent, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.StorageEvent, 10)
	go sm.processStorageMessages(ctx, client, updates, streamID, config.TenantID)
	return updates, nil
}

// SubscribeToProcessingEvents subscribes to processing/transcoding events and returns a channel of updates
// Returns proto.ProcessingUsageRecord (mapped from ProcessBillingEvent)
func (sm *SubscriptionManager) SubscribeToProcessingEvents(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.ProcessingUsageRecord, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.ProcessingUsageRecord, 10)
	go sm.processProcessingMessages(ctx, client, updates, streamID, config.TenantID)
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
	go sm.processSystemMessages(ctx, client, updates, config.TenantID)
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
	go sm.processTrackListMessages(ctx, client, updates, streamID, config.TenantID)
	return updates, nil
}

// SubscribeToLifecycle subscribes to lifecycle events (clip) and returns a channel
// Returns proto.ClipLifecycleData directly (bound to GraphQL ClipLifecycle)
func (sm *SubscriptionManager) SubscribeToLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.ClipLifecycleData, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to lifecycle: %w", err)
	}
	updates := make(chan *pb.ClipLifecycleData, 10)
	go sm.processLifecycleMessages(ctx, client, updates, streamID, config.TenantID)
	return updates, nil
}

// SubscribeToDVRLifecycle subscribes to DVR lifecycle events and returns a channel
// Returns proto.DVRLifecycleData directly (bound to GraphQL DVREvent)
func (sm *SubscriptionManager) SubscribeToDVRLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.DVRLifecycleData, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to DVR lifecycle: %w", err)
	}
	updates := make(chan *pb.DVRLifecycleData, 10)
	go sm.processDVRLifecycleMessages(ctx, client, updates, streamID, config.TenantID)
	return updates, nil
}

// SubscribeToVodLifecycle subscribes to VOD lifecycle events and returns a channel
// Returns proto.VodLifecycleData directly (bound via gqlgen.yml)
func (sm *SubscriptionManager) SubscribeToVodLifecycle(ctx context.Context, config ConnectionConfig) (<-chan *pb.VodLifecycleData, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	// VOD lifecycle events are delivered on the analytics channel.
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to VOD lifecycle: %w", err)
	}
	updates := make(chan *pb.VodLifecycleData, 10)
	go sm.processVodLifecycleMessages(ctx, client, updates, config.TenantID)
	return updates, nil
}

// SubscribeToMessages subscribes to messaging events and returns a channel
// Returns model.Message (mapped from MessageLifecycleData)
func (sm *SubscriptionManager) SubscribeToMessages(ctx context.Context, config ConnectionConfig, conversationID string) (<-chan *model.Message, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToMessaging(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to messaging: %w", err)
	}
	updates := make(chan *model.Message, 10)
	go sm.processMessageMessages(ctx, client, updates, conversationID, config.TenantID)
	return updates, nil
}

// SubscribeToConversations subscribes to messaging events and returns conversation updates
func (sm *SubscriptionManager) SubscribeToConversations(ctx context.Context, config ConnectionConfig, conversationID string) (<-chan *model.Conversation, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToMessaging(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to messaging: %w", err)
	}
	updates := make(chan *model.Conversation, 10)
	go sm.processConversationMessages(ctx, client, updates, conversationID, config.TenantID)
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
	if err := client.SubscribeToAI(); err != nil {
		sm.logger.Warn("Failed to subscribe to AI for firehose", "error", err)
	}

	updates := make(chan *model.TenantEvent, 50) // Larger buffer for firehose
	go sm.processFirehoseMessages(ctx, client, updates, config.TenantID)
	return updates, nil
}

// processFirehoseMessages processes ALL events from Signalman and converts them to TenantEvent
func (sm *SubscriptionManager) processFirehoseMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.TenantEvent, tenantID string) {
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

			if tenantMismatch(tenantID, event) {
				continue
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
// Uses proto enum strings for event type (e.g., EVENT_TYPE_STREAM_LIFECYCLE_UPDATE)
// Passes proto types directly where possible via gqlgen.yml bindings
func (sm *SubscriptionManager) convertProtoToTenantEvent(event *pb.SignalmanEvent) *model.TenantEvent {
	if event == nil {
		return nil
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	}

	// Use proto enum string directly (EVENT_TYPE_STREAM_LIFECYCLE_UPDATE, etc.)
	eventType := event.EventType.String()
	channel := sm.getChannelForEventType(event.EventType)
	if event.Channel != pb.Channel_CHANNEL_UNSPECIFIED {
		channel = channelToTenantChannel(event.Channel)
	}

	tenantEvent := &model.TenantEvent{
		Type:      eventType,
		Channel:   channel,
		Timestamp: timestamp,
	}

	if event.Data == nil {
		return tenantEvent
	}

	// Populate the appropriate event type based on the channel/event type
	// Pass proto types directly where possible via gqlgen.yml bindings
	switch event.EventType {
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_STREAM_END,
		pb.EventType_EVENT_TYPE_STREAM_BUFFER,
		pb.EventType_EVENT_TYPE_PUSH_REWRITE,
		pb.EventType_EVENT_TYPE_STREAM_SOURCE,
		pb.EventType_EVENT_TYPE_PLAY_REWRITE:
		tenantEvent.StreamEvent = mapSignalmanStreamEvent(event)

	case pb.EventType_EVENT_TYPE_VIEWER_CONNECT,
		pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT:
		tenantEvent.ConnectionEvent = mapSignalmanConnectionEvent(event)

	case pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE:
		// Pass proto ClientLifecycleUpdate directly (bound to ViewerMetrics)
		tenantEvent.ViewerMetrics = event.Data.GetClientLifecycle()

	case pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST:
		// Pass proto StreamTrackListTrigger directly (bound to TrackListUpdate)
		tenantEvent.TrackListUpdate = event.Data.GetTrackList()

	case pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE:
		// Pass proto ClipLifecycleData directly (bound to ClipLifecycle)
		tenantEvent.ClipLifecycle = event.Data.GetClipLifecycle()

	case pb.EventType_EVENT_TYPE_DVR_LIFECYCLE:
		// Pass proto DVRLifecycleData directly (bound to DVREvent)
		tenantEvent.DvrEvent = event.Data.GetDvrLifecycle()

	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE:
		// Pass proto NodeLifecycleUpdate directly (bound to SystemHealthEvent)
		tenantEvent.SystemHealthEvent = event.Data.GetNodeLifecycle()

	case pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		tenantEvent.RoutingEvent = mapSignalmanRoutingEvent(event)

	case pb.EventType_EVENT_TYPE_VOD_LIFECYCLE:
		// Pass proto VodLifecycleData directly (bound via gqlgen.yml)
		tenantEvent.VodLifecycle = event.Data.GetVodLifecycle()

	case pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE:
		tenantEvent.StorageEvent = mapSignalmanStorageEvent(event)

	case pb.EventType_EVENT_TYPE_PROCESS_BILLING:
		tenantEvent.ProcessingEvent = mapSignalmanProcessingEvent(event)
	case pb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT:
		tenantEvent.StorageSnapshot = event.Data.GetStorageSnapshot()

	case pb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION:
		tenantEvent.SkipperInvestigation = &model.SkipperInvestigationEvent{
			ReportID:     "",
			ResourceType: "skipper_investigation",
		}
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
		pb.EventType_EVENT_TYPE_PUSH_REWRITE,
		pb.EventType_EVENT_TYPE_STREAM_SOURCE,
		pb.EventType_EVENT_TYPE_PLAY_REWRITE,
		pb.EventType_EVENT_TYPE_VOD_LIFECYCLE:
		return "STREAMS"

	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		return "SYSTEM"
	case pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE,
		pb.EventType_EVENT_TYPE_PROCESS_BILLING,
		pb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT:
		return "ANALYTICS"
	case pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE:
		return "MESSAGING"
	case pb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION:
		return "AI"

	default:
		return "ANALYTICS"
	}
}

func channelToTenantChannel(channel pb.Channel) string {
	switch channel {
	case pb.Channel_CHANNEL_STREAMS:
		return "STREAMS"
	case pb.Channel_CHANNEL_ANALYTICS:
		return "ANALYTICS"
	case pb.Channel_CHANNEL_SYSTEM:
		return "SYSTEM"
	case pb.Channel_CHANNEL_ALL:
		return "ALL"
	case pb.Channel_CHANNEL_MESSAGING:
		return "MESSAGING"
	case pb.Channel_CHANNEL_AI:
		return "AI"
	default:
		return "ANALYTICS"
	}
}

func tenantMismatch(tenantID string, event *pb.SignalmanEvent) bool {
	if tenantID == "" || event == nil {
		return false
	}
	// Some system/infrastructure broadcasts are emitted without a tenant id.
	// Keep delivering those to tenant-scoped subscribers.
	if event.TenantId == nil {
		return false
	}
	return *event.TenantId != tenantID
}

// processStreamMessages processes stream messages from Signalman gRPC
// Maps proto payloads into canonical StreamEvent for live subscriptions
func (sm *SubscriptionManager) processStreamMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.StreamEvent, streamID *string, tenantID string) {
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

			// Filter by event type - handle lifecycle, start, end, buffer, track list
			if event.EventType != pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_END &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_BUFFER &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST &&
				event.EventType != pb.EventType_EVENT_TYPE_PUSH_REWRITE &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_SOURCE &&
				event.EventType != pb.EventType_EVENT_TYPE_PLAY_REWRITE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Filter by stream ID if specified
			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			update := mapSignalmanStreamEvent(event)
			if update == nil {
				continue
			}

			select {
			case output <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processAnalyticsMessages processes analytics messages from Signalman gRPC
// Passes proto.ClientLifecycleUpdate directly without conversion
func (sm *SubscriptionManager) processAnalyticsMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ClientLifecycleUpdate, streamID *string, tenantID string) {
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
			if event.EventType != pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Extract ClientLifecycleUpdate directly from proto
			if event.Data != nil {
				if cl := event.Data.GetClientLifecycle(); cl != nil {
					if streamID != nil {
						msgStreamID := getStreamIDFromProtoEvent(event)
						if msgStreamID == "" || msgStreamID != *streamID {
							continue
						}
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

// processConnectionMessages processes viewer connect/disconnect messages from Signalman gRPC
// Maps proto viewer events into ConnectionEvent for GraphQL consumption
func (sm *SubscriptionManager) processConnectionMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ConnectionEvent, streamID *string, tenantID string) {
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

			if event.EventType != pb.EventType_EVENT_TYPE_VIEWER_CONNECT &&
				event.EventType != pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			ce := mapSignalmanConnectionEvent(event)
			if ce == nil {
				continue
			}

			select {
			case output <- ce:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processStorageMessages processes storage lifecycle messages from Signalman gRPC
// Maps proto storage lifecycle data into StorageEvent for GraphQL consumption
func (sm *SubscriptionManager) processStorageMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.StorageEvent, streamID *string, tenantID string) {
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

			if event.EventType != pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			update := mapSignalmanStorageEvent(event)
			if update == nil {
				continue
			}

			select {
			case output <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processProcessingMessages processes process billing messages from Signalman gRPC
// Maps proto process billing data into ProcessingUsageRecord for GraphQL consumption
func (sm *SubscriptionManager) processProcessingMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ProcessingUsageRecord, streamID *string, tenantID string) {
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

			if event.EventType != pb.EventType_EVENT_TYPE_PROCESS_BILLING {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			update := mapSignalmanProcessingEvent(event)
			if update == nil {
				continue
			}

			select {
			case output <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processSystemMessages processes system messages from Signalman gRPC
// Passes proto.NodeLifecycleUpdate directly without conversion
func (sm *SubscriptionManager) processSystemMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.NodeLifecycleUpdate, tenantID string) {
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

			if tenantMismatch(tenantID, event) {
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
func (sm *SubscriptionManager) processTrackListMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.StreamTrackListTrigger, streamID string, tenantID string) {
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

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Filter by stream ID if specified
			if streamID != "" {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != streamID {
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
func (sm *SubscriptionManager) processLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ClipLifecycleData, streamID string, tenantID string) {
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

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Extract ClipLifecycleData directly from proto
			if event.Data != nil {
				if cl := event.Data.GetClipLifecycle(); cl != nil {
					// Filter by stream ID if specified
					if streamID != "" && cl.GetStreamId() != streamID {
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

func (sm *SubscriptionManager) processDVRLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.DVRLifecycleData, streamID string, tenantID string) {
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

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Extract DVRLifecycleData directly from proto
			if event.Data != nil {
				if dvr := event.Data.GetDvrLifecycle(); dvr != nil {
					// Filter by stream ID if specified
					if streamID != "" && dvr.GetStreamId() != streamID {
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

// processVodLifecycleMessages processes VOD lifecycle messages from Signalman gRPC
// Passes proto.VodLifecycleData directly (bound via gqlgen.yml, no conversion needed)
func (sm *SubscriptionManager) processVodLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.VodLifecycleData, tenantID string) {
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

			// Filter for VOD lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_VOD_LIFECYCLE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Pass proto directly - no conversion needed (bound via gqlgen.yml)
			if event.Data != nil {
				if vod := event.Data.GetVodLifecycle(); vod != nil {
					select {
					case output <- vod:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processMessageMessages processes messaging events from Signalman gRPC
// Maps proto.MessageLifecycleData to model.Message for GraphQL consumption
func (sm *SubscriptionManager) processMessageMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.Message, conversationID string, tenantID string) {
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

			// Filter for message lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE {
				continue
			}

			if event.Data == nil {
				continue
			}

			ml := event.Data.GetMessageLifecycle()
			if ml == nil {
				continue
			}

			// Enforce tenant isolation when tenant_id is present
			if tenantID != "" {
				if ml.TenantId == nil || *ml.TenantId != tenantID {
					continue
				}
			}

			// Filter by conversation ID
			if conversationID != "" && ml.GetConversationId() != conversationID {
				continue
			}

			// Only forward message_created events (new messages)
			if ml.EventType != pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED {
				continue
			}

			// Map to GraphQL Message type
			msg := mapMessageLifecycleToMessage(ml)
			if msg == nil {
				continue
			}

			select {
			case output <- msg:
			case <-ctx.Done():
				return
			}
		}
	}
}

// mapMessageLifecycleToMessage converts proto MessageLifecycleData to GraphQL Message
func mapMessageLifecycleToMessage(ml *pb.MessageLifecycleData) *model.Message {
	if ml == nil {
		return nil
	}

	rawConversationID := ml.GetConversationId()
	if rawConversationID == "" {
		return nil
	}

	// Parse sender - default to AGENT if unknown
	sender := pb.MessageSender_MESSAGE_SENDER_AGENT
	if ml.Sender != nil {
		switch *ml.Sender {
		case "USER":
			sender = pb.MessageSender_MESSAGE_SENDER_USER
		case "AGENT":
			sender = pb.MessageSender_MESSAGE_SENDER_AGENT
		}
	}

	// Get message ID (use conversation ID as fallback)
	msgID := rawConversationID
	if ml.MessageId != nil && *ml.MessageId != "" {
		msgID = *ml.MessageId
	}

	// Get content
	content := ""
	if ml.Content != nil {
		content = *ml.Content
	}

	return &model.Message{
		ID:             globalid.EncodeComposite(globalid.TypeMessage, rawConversationID, msgID),
		ConversationID: globalid.Encode(globalid.TypeConversation, rawConversationID),
		Content:        content,
		Sender:         sender,
		CreatedAt:      time.Unix(ml.Timestamp, 0),
	}
}

// processConversationMessages processes conversation lifecycle events from Signalman gRPC
func (sm *SubscriptionManager) processConversationMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.Conversation, conversationID string, tenantID string) {
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

			// Filter for message lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE {
				continue
			}

			if event.Data == nil {
				continue
			}

			ml := event.Data.GetMessageLifecycle()
			if ml == nil {
				continue
			}

			// Enforce tenant isolation when tenant_id is present
			if tenantID != "" {
				if ml.TenantId == nil || *ml.TenantId != tenantID {
					continue
				}
			}

			// Filter by conversation ID
			if conversationID != "" && ml.GetConversationId() != conversationID {
				continue
			}

			// Conversation lifecycle events and message updates that affect conversation summary
			switch ml.EventType {
			case pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_CREATED,
				pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_UPDATED,
				pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED,
				pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED:
			default:
				continue
			}

			conv := mapMessageLifecycleToConversation(ml)
			if conv == nil {
				continue
			}

			select {
			case output <- conv:
			case <-ctx.Done():
				return
			}
		}
	}
}

func mapMessageLifecycleToConversation(ml *pb.MessageLifecycleData) *model.Conversation {
	if ml == nil {
		return nil
	}

	rawConversationID := ml.GetConversationId()
	if rawConversationID == "" {
		return nil
	}

	status := parseConversationStatus(ml.Status)
	subject := (*string)(nil)
	if ml.Subject != nil && *ml.Subject != "" {
		subject = ml.Subject
	}

	timestamp := ml.Timestamp
	var updatedAt time.Time
	if timestamp > 0 {
		updatedAt = time.Unix(timestamp, 0)
	} else {
		updatedAt = time.Now()
	}

	var lastMessage *model.Message
	switch ml.EventType {
	case pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED,
		pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED:
		lastMessage = mapMessageLifecycleToMessage(ml)
	}

	return &model.Conversation{
		ID:          globalid.Encode(globalid.TypeConversation, rawConversationID),
		Subject:     subject,
		Status:      status,
		LastMessage: lastMessage,
		UnreadCount: 0,
		CreatedAt:   updatedAt,
		UpdatedAt:   updatedAt,
	}
}

func parseConversationStatus(status *string) pb.ConversationStatus {
	if status == nil || *status == "" {
		return pb.ConversationStatus_CONVERSATION_STATUS_OPEN
	}

	switch strings.ToUpper(*status) {
	case "OPEN":
		return pb.ConversationStatus_CONVERSATION_STATUS_OPEN
	case "RESOLVED":
		return pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED
	case "PENDING":
		return pb.ConversationStatus_CONVERSATION_STATUS_PENDING
	default:
		return pb.ConversationStatus_CONVERSATION_STATUS_OPEN
	}
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
	raw := ""
	if cl := event.Data.GetClientLifecycle(); cl != nil {
		raw = cl.GetStreamId()
	} else if tl := event.Data.GetTrackList(); tl != nil {
		raw = tl.GetStreamId()
	} else if cl := event.Data.GetClipLifecycle(); cl != nil {
		raw = cl.GetStreamId()
	} else if dl := event.Data.GetDvrLifecycle(); dl != nil {
		raw = dl.GetStreamId()
	} else if lb := event.Data.GetLoadBalancing(); lb != nil {
		raw = lb.GetStreamId()
	} else if pr := event.Data.GetPushRewrite(); pr != nil {
		raw = pr.GetStreamId()
	} else if pr := event.Data.GetPlayRewrite(); pr != nil {
		raw = pr.GetStreamId()
	} else if ss := event.Data.GetStreamSource(); ss != nil {
		raw = ss.GetStreamId()
	} else if pos := event.Data.GetPushOutStart(); pos != nil {
		raw = pos.GetStreamId()
	} else if pe := event.Data.GetPushEnd(); pe != nil {
		raw = pe.GetStreamId()
	} else if vc := event.Data.GetViewerConnect(); vc != nil {
		raw = vc.GetStreamId()
	} else if vd := event.Data.GetViewerDisconnect(); vd != nil {
		raw = vd.GetStreamId()
	} else if se := event.Data.GetStreamEnd(); se != nil {
		raw = se.GetStreamId()
	} else if event.Data.GetRecording() != nil {
		raw = ""
	} else if buf := event.Data.GetStreamBuffer(); buf != nil {
		raw = buf.GetStreamId()
	} else if sl := event.Data.GetStreamLifecycle(); sl != nil {
		raw = sl.GetStreamId()
	} else if st := event.Data.GetStorageLifecycle(); st != nil {
		raw = st.GetStreamId()
	} else if pbill := event.Data.GetProcessBilling(); pbill != nil {
		raw = pbill.GetStreamId()
	}

	if raw == "" {
		return ""
	}
	return raw
}
