package resolvers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/pkg/api/periscope"
	signalmanapi "frameworks/pkg/api/signalman"
	signalmanclient "frameworks/pkg/clients/signalman"
	"frameworks/pkg/logging"
	"frameworks/pkg/validation"
)

// WebSocketManager manages WebSocket connections to Signalman for GraphQL subscriptions
type WebSocketManager struct {
	clients      map[string]*signalmanclient.Client // Key: userID:tenantID
	logger       logging.Logger
	mutex        sync.RWMutex
	signalmanURL string
	cleanup      chan string // Channel for cleanup signals
	stopChan     chan struct{}
	metrics      *GraphQLMetrics
}

// ConnectionConfig represents configuration for a WebSocket connection
type ConnectionConfig struct {
	UserID   string
	TenantID string
	JWT      string
}

// NewWebSocketManager creates a new WebSocket connection manager
func NewWebSocketManager(signalmanURL string, logger logging.Logger, metrics *GraphQLMetrics) *WebSocketManager {
	wm := &WebSocketManager{
		clients:      make(map[string]*signalmanclient.Client),
		logger:       logger,
		signalmanURL: signalmanURL,
		cleanup:      make(chan string, 10),
		stopChan:     make(chan struct{}),
		metrics:      metrics,
	}

	// Start cleanup goroutine
	go wm.cleanupWorker()

	return wm
}

// GetOrCreateConnection gets an existing connection or creates a new one for a user/tenant
func (wm *WebSocketManager) GetOrCreateConnection(ctx context.Context, config ConnectionConfig) (*signalmanclient.Client, error) {
	key := fmt.Sprintf("%s:%s", config.UserID, config.TenantID)

	wm.mutex.RLock()
	if client, exists := wm.clients[key]; exists && client.IsConnected() {
		wm.mutex.RUnlock()
		return client, nil
	}
	wm.mutex.RUnlock()

	// Need to create a new connection
	wm.mutex.Lock()
	defer wm.mutex.Unlock()

	// Double-check after acquiring write lock
	if client, exists := wm.clients[key]; exists && client.IsConnected() {
		return client, nil
	}

	// Create new client with authentication
	client := signalmanclient.NewClient(signalmanclient.Config{
		BaseURL:        wm.signalmanURL,
		Logger:         wm.logger,
		UserID:         &config.UserID,
		TenantID:       &config.TenantID,
		ReconnectDelay: 5 * time.Second,
		MaxReconnects:  5,
	})

	// Connect with authentication
	if err := client.ConnectWithAuth(ctx, "/ws", config.JWT); err != nil {
		wm.logger.WithError(err).WithFields(logging.Fields{
			"user_id":   config.UserID,
			"tenant_id": config.TenantID,
		}).Error("Failed to connect to Signalman")

		if wm.metrics != nil {
			wm.metrics.WebSocketMessages.WithLabelValues("outbound", "connection_error").Inc()
		}
		return nil, fmt.Errorf("failed to connect to Signalman: %w", err)
	}

	wm.clients[key] = client

	// Record successful connection
	if wm.metrics != nil {
		wm.metrics.WebSocketConnections.WithLabelValues(config.TenantID).Inc()
		wm.metrics.WebSocketMessages.WithLabelValues("outbound", "connection_success").Inc()
	}

	wm.logger.WithFields(logging.Fields{
		"user_id":   config.UserID,
		"tenant_id": config.TenantID,
		"key":       key,
	}).Info("Created new WebSocket connection to Signalman")

	return client, nil
}

// SubscribeToStreams subscribes to stream events and returns a channel of updates
func (wm *WebSocketManager) SubscribeToStreams(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *periscope.StreamEvent, error) {
	client, err := wm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to streams: %w", err)
	}

	updates := make(chan *periscope.StreamEvent, 10)
	go wm.processStreamMessages(ctx, client, updates, streamID)
	return updates, nil
}

// SubscribeToAnalytics subscribes to analytics events and returns a channel of updates
func (wm *WebSocketManager) SubscribeToAnalytics(ctx context.Context, config ConnectionConfig) (<-chan *model.ViewerMetrics, error) {
	client, err := wm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	// Subscribe to analytics channel
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	// Create output channel
	updates := make(chan *model.ViewerMetrics, 10)

	// Start message processing goroutine
	go wm.processAnalyticsMessages(ctx, client, updates)

	return updates, nil
}

// SubscribeToSystem subscribes to system events and returns a channel of updates
func (wm *WebSocketManager) SubscribeToSystem(ctx context.Context, config ConnectionConfig) (<-chan *model.SystemHealthEvent, error) {
	client, err := wm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	// Subscribe to system channel
	if err := client.SubscribeToSystem(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to system: %w", err)
	}

	// Create output channel
	updates := make(chan *model.SystemHealthEvent, 10)

	// Start message processing goroutine
	go wm.processSystemMessages(ctx, client, updates)

	return updates, nil
}

// SubscribeToTrackList subscribes to track list events and returns a channel of updates
func (wm *WebSocketManager) SubscribeToTrackList(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *periscope.AnalyticsTrackListEvent, error) {
	client, err := wm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to track list updates: %w", err)
	}

	updates := make(chan *periscope.AnalyticsTrackListEvent, 10)
	go wm.processTrackListMessages(ctx, client, updates, streamID)
	return updates, nil
}

// processStreamMessages processes stream messages from Signalman and converts them to periscope DTOs
func (wm *WebSocketManager) processStreamMessages(ctx context.Context, client *signalmanclient.Client, output chan<- *periscope.StreamEvent, streamID *string) {
	defer close(output)

	messages := client.GetMessages()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}

			// Filter out WebSocket protocol messages (not actual stream events)
			if wm.isProtocolMessage(msg.Type) {
				continue
			}

			if streamID != nil {
				msgStreamID := getStreamIDFromEventData(msg.Data)
				if msgStreamID != "" && msgStreamID != *streamID {
					continue
				}
			}

			if update := wm.convertToPeriscopeStreamEvent(msg); update != nil {
				select {
				case output <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// processAnalyticsMessages processes analytics messages from Signalman
func (wm *WebSocketManager) processAnalyticsMessages(ctx context.Context, client *signalmanclient.Client, output chan<- *model.ViewerMetrics) {
	defer close(output)

	messages := client.GetMessages()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}

			// Convert to GraphQL model
			if update := wm.convertToViewerMetrics(msg); update != nil {
				select {
				case output <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// processSystemMessages processes system messages from Signalman
func (wm *WebSocketManager) processSystemMessages(ctx context.Context, client *signalmanclient.Client, output chan<- *model.SystemHealthEvent) {
	defer close(output)

	messages := client.GetMessages()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}

			// Convert to GraphQL model
			if update := wm.convertToSystemHealthEvent(msg); update != nil {
				select {
				case output <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// processTrackListMessages processes track list messages to periscope DTOs
func (wm *WebSocketManager) processTrackListMessages(ctx context.Context, client *signalmanclient.Client, output chan<- *periscope.AnalyticsTrackListEvent, streamID string) {
	defer close(output)

	messages := client.GetMessages()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}

			if msg.Type != signalmanapi.TypeTrackList {
				continue
			}

			msgStreamID := getStreamIDFromEventData(msg.Data)
			if msgStreamID != "" && msgStreamID != streamID {
				continue
			}

			if update := wm.convertToPeriscopeTrackListEvent(msg); update != nil {
				select {
				case output <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// convertToViewerMetrics converts a Signalman message to a GraphQL ViewerMetrics
func (wm *WebSocketManager) convertToViewerMetrics(msg signalmanapi.Message) *model.ViewerMetrics {
	if msg.Type != signalmanapi.TypeViewerMetrics {
		return nil
	}

	streamID := getStreamIDFromEventData(msg.Data)
	currentViewers := 0
	bandwidth := 0.0

	// Extract viewer metrics from ClientLifecycle payload
	if msg.Data.ClientLifecycle != nil {
		// Use bandwidth_out as the main bandwidth metric
		bandwidth = msg.Data.ClientLifecycle.BandwidthOut
		// For viewer count, we assume 1 viewer per client lifecycle event
		currentViewers = 1

		// Update streamID if not found by helper (ClientLifecycle has InternalName)
		if streamID == "" {
			streamID = msg.Data.ClientLifecycle.InternalName
		}
	}

	// Ensure timestamp is not zero - GraphQL schema requires non-null timestamp
	timestamp := msg.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
		wm.logger.Warn("Received viewer metrics with zero timestamp, using current time",
			"stream_id", streamID)
	}

	return &model.ViewerMetrics{
		Stream:         streamID,
		CurrentViewers: currentViewers,
		ViewerCount:    currentViewers,
		PeakViewers:    currentViewers, // For single client events, peak equals current
		Bandwidth:      bandwidth,
		Timestamp:      timestamp,
	}
}

// convertToSystemHealthEvent converts a Signalman message to a GraphQL SystemHealthEvent
func (wm *WebSocketManager) convertToSystemHealthEvent(msg signalmanapi.Message) *model.SystemHealthEvent {
	nodeID := ""
	clusterID := ""
	cpu := 0.0
	mem := 0.0
	disk := 0.0
	health := 0.0
	status := model.NodeStatusHealthy

	// Extract from NodeLifecycle payload which HAS all the data
	if msg.Data.NodeLifecycle != nil {
		nodeID = msg.Data.NodeLifecycle.NodeID
		clusterID = msg.Data.NodeLifecycle.NodeID    // Use NodeID as cluster for now
		cpu = msg.Data.NodeLifecycle.CPUUsage / 10.0 // Convert from tenths to percentage

		// Calculate memory usage percentage
		if msg.Data.NodeLifecycle.RAMMax > 0 {
			mem = float64(msg.Data.NodeLifecycle.RAMCurrent) / float64(msg.Data.NodeLifecycle.RAMMax) * 100
		}

		// NodeLifecycle doesn't include disk metrics
		disk = 0.0

		// Health score based on IsHealthy flag
		if msg.Data.NodeLifecycle.IsHealthy {
			health = 100.0
			status = model.NodeStatusHealthy
		} else {
			health = 0.0
			status = model.NodeStatusUnhealthy
		}
	}

	// Ensure timestamp is not zero - GraphQL schema requires non-null timestamp
	timestamp := msg.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
		wm.logger.Warn("Received system health event with zero timestamp, using current time",
			"node_id", nodeID,
			"cluster_id", clusterID)
	}

	return &model.SystemHealthEvent{
		Node:        nodeID,
		Cluster:     clusterID,
		Status:      status,
		CPUUsage:    cpu,
		MemoryUsage: mem,
		DiskUsage:   disk,
		HealthScore: health,
		Timestamp:   timestamp,
	}
}

// convertToTrackListEvent converts a Signalman message to a GraphQL TrackListEvent
func (wm *WebSocketManager) convertToTrackListEvent(msg signalmanapi.Message) *periscope.AnalyticsTrackListEvent {
	if msg.Type != signalmanapi.TypeTrackList {
		return nil
	}

	streamID := getStreamIDFromEventData(msg.Data)
	trackList := ""
	trackCount := 0

	// Extract track list data from TrackList payload
	if msg.Data.TrackList != nil {
		trackList = msg.Data.TrackList.TrackListJSON
		trackCount = msg.Data.TrackList.TrackCount
	}

	// Ensure timestamp is not zero - GraphQL schema requires non-null timestamp
	timestamp := msg.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
		wm.logger.Warn("Received track list event with zero timestamp, using current time",
			"stream_id", streamID)
	}

	return &periscope.AnalyticsTrackListEvent{
		Stream:     streamID,
		TrackList:  trackList,
		TrackCount: trackCount,
		Timestamp:  timestamp,
	}
}

// CleanupConnection removes a connection from the pool
func (wm *WebSocketManager) CleanupConnection(userID, tenantID string) {
	key := fmt.Sprintf("%s:%s", userID, tenantID)

	wm.mutex.Lock()
	defer wm.mutex.Unlock()

	if client, exists := wm.clients[key]; exists {
		client.Close()
		delete(wm.clients, key)

		wm.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
			"key":       key,
		}).Info("Cleaned up WebSocket connection")
	}
}

// cleanupWorker handles cleanup requests
func (wm *WebSocketManager) cleanupWorker() {
	ticker := time.NewTicker(5 * time.Minute) // Periodic cleanup
	defer ticker.Stop()

	for {
		select {
		case <-wm.stopChan:
			return
		case key := <-wm.cleanup:
			// Handle specific cleanup request
			wm.mutex.Lock()
			if client, exists := wm.clients[key]; exists {
				client.Close()
				delete(wm.clients, key)
			}
			wm.mutex.Unlock()
		case <-ticker.C:
			// Periodic cleanup of disconnected clients
			wm.periodicCleanup()
		}
	}
}

// periodicCleanup removes disconnected clients
func (wm *WebSocketManager) periodicCleanup() {
	wm.mutex.Lock()
	defer wm.mutex.Unlock()

	var toRemove []string
	for key, client := range wm.clients {
		if !client.IsConnected() {
			toRemove = append(toRemove, key)
		}
	}

	for _, key := range toRemove {
		if client, exists := wm.clients[key]; exists {
			client.Close()
			delete(wm.clients, key)
		}
	}

	if len(toRemove) > 0 {
		wm.logger.WithFields(logging.Fields{
			"cleaned_connections": len(toRemove),
		}).Info("Periodic cleanup removed disconnected WebSocket connections")
	}
}

// Shutdown gracefully shuts down the WebSocket manager
func (wm *WebSocketManager) Shutdown() error {
	close(wm.stopChan)

	wm.mutex.Lock()
	defer wm.mutex.Unlock()

	// Close all connections
	for key, client := range wm.clients {
		client.Close()
		delete(wm.clients, key)
	}

	wm.logger.Info("WebSocket manager shutdown completed")
	return nil
}

// getStreamIDFromEventData extracts stream ID from typed EventData struct
func getStreamIDFromEventData(data validation.EventData) string {
	if data.StreamIngest != nil && data.StreamIngest.InternalName != "" {
		return data.StreamIngest.InternalName
	}
	if data.StreamLifecycle != nil && data.StreamLifecycle.InternalName != "" {
		return data.StreamLifecycle.InternalName
	}
	if data.LoadBalancing != nil && data.LoadBalancing.StreamID != "" {
		return data.LoadBalancing.StreamID
	}
	if data.ClientLifecycle != nil && data.ClientLifecycle.InternalName != "" {
		return data.ClientLifecycle.InternalName
	}
	if data.TrackList != nil && data.TrackList.InternalName != "" {
		return data.TrackList.InternalName
	}
	return ""
}

// isProtocolMessage checks if the message type is a WebSocket protocol message
func (wm *WebSocketManager) isProtocolMessage(msgType string) bool {
	switch msgType {
	case signalmanapi.TypeSubscriptionConfirmed, signalmanapi.TypeUnsubscriptionConfirmed:
		return true
	default:
		return false
	}
}

func (wm *WebSocketManager) convertToPeriscopeStreamEvent(msg signalmanapi.Message) *periscope.StreamEvent {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	streamID := getStreamIDFromEventData(msg.Data)
	status := ""
	switch msg.Type {
	case signalmanapi.TypeStreamStart:
		status = "LIVE"
	case signalmanapi.TypeStreamEnd:
		status = "ENDED"
	case signalmanapi.TypeStreamError:
		status = "OFFLINE"
	case signalmanapi.TypeStreamBuffer, signalmanapi.TypeTrackList:
		status = "LIVE"
	default:
		// Unknown message type - return nil to filter out
		return nil
	}
	return &periscope.StreamEvent{
		Timestamp:    msg.Timestamp,
		EventID:      "",
		EventType:    string(msg.Type),
		Status:       status,
		NodeID:       "",
		EventData:    "",
		InternalName: streamID,
	}
}

func (wm *WebSocketManager) convertToPeriscopeTrackListEvent(msg signalmanapi.Message) *periscope.AnalyticsTrackListEvent {
	if msg.Type != signalmanapi.TypeTrackList {
		return nil
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	count := 0
	tracks := ""
	if msg.Data.TrackList != nil {
		count = msg.Data.TrackList.TrackCount
		tracks = msg.Data.TrackList.TrackListJSON
	}
	streamID := getStreamIDFromEventData(msg.Data)
	return &periscope.AnalyticsTrackListEvent{
		Timestamp:  msg.Timestamp,
		NodeID:     "",
		TrackList:  tracks,
		TrackCount: count,
		Stream:     streamID,
	}
}
