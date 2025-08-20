package resolvers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_gateway/graph/model"
	signalmanapi "frameworks/pkg/api/signalman"
	signalmanclient "frameworks/pkg/clients/signalman"
	"frameworks/pkg/logging"
)

// WebSocketManager manages WebSocket connections to Signalman for GraphQL subscriptions
type WebSocketManager struct {
	clients      map[string]*signalmanclient.Client // Key: userID:tenantID
	logger       logging.Logger
	mutex        sync.RWMutex
	signalmanURL string
	cleanup      chan string // Channel for cleanup signals
	stopChan     chan struct{}
}

// ConnectionConfig represents configuration for a WebSocket connection
type ConnectionConfig struct {
	UserID   string
	TenantID string
	JWT      string
}

// NewWebSocketManager creates a new WebSocket connection manager
func NewWebSocketManager(signalmanURL string, logger logging.Logger) *WebSocketManager {
	wm := &WebSocketManager{
		clients:      make(map[string]*signalmanclient.Client),
		logger:       logger,
		signalmanURL: signalmanURL,
		cleanup:      make(chan string, 10),
		stopChan:     make(chan struct{}),
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
		return nil, fmt.Errorf("failed to connect to Signalman: %w", err)
	}

	wm.clients[key] = client

	wm.logger.WithFields(logging.Fields{
		"user_id":   config.UserID,
		"tenant_id": config.TenantID,
		"key":       key,
	}).Info("Created new WebSocket connection to Signalman")

	return client, nil
}

// SubscribeToStreams subscribes to stream events and returns a channel of updates
func (wm *WebSocketManager) SubscribeToStreams(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *model.StreamEvent, error) {
	client, err := wm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	// Subscribe to streams channel
	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to streams: %w", err)
	}

	// Create output channel
	updates := make(chan *model.StreamEvent, 10)

	// Start message processing goroutine
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
func (wm *WebSocketManager) SubscribeToTrackList(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *model.TrackListEvent, error) {
	client, err := wm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	// Subscribe to streams channel (track list events come through streams)
	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to track list updates: %w", err)
	}

	// Create output channel
	updates := make(chan *model.TrackListEvent, 10)

	// Start message processing goroutine
	go wm.processTrackListMessages(ctx, client, updates, streamID)

	return updates, nil
}

// SubscribeToTenantEvents subscribes to tenant events and returns a channel of updates
func (wm *WebSocketManager) SubscribeToTenantEvents(ctx context.Context, config ConnectionConfig, tenantID string) (<-chan model.TenantEvent, error) {
	client, err := wm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	// Subscribe to all channels to get various tenant events
	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to tenant events (streams): %w", err)
	}
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to tenant events (analytics): %w", err)
	}
	if err := client.SubscribeToSystem(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to tenant events (system): %w", err)
	}

	// Create output channel
	updates := make(chan model.TenantEvent, 10)

	// Start message processing goroutine
	go wm.processTenantEventMessages(ctx, client, updates, tenantID)

	return updates, nil
}

// processStreamMessages processes stream messages from Signalman and converts them to GraphQL types
func (wm *WebSocketManager) processStreamMessages(ctx context.Context, client *signalmanclient.Client, output chan<- *model.StreamEvent, streamID *string) {
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

			// Filter by stream ID if specified
			if streamID != nil {
				if msgStreamID, exists := msg.Data["stream_id"].(string); exists && msgStreamID != *streamID {
					continue
				}
			}

			// Convert to GraphQL model
			if update := wm.convertToStreamEvent(msg); update != nil {
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

// convertToStreamEvent converts a Signalman message to a GraphQL StreamEvent
func (wm *WebSocketManager) convertToStreamEvent(msg signalmanapi.Message) *model.StreamEvent {
	streamID, _ := msg.Data["stream_id"].(string)
	var detailsPtr *string

	switch msg.Type {
	case signalmanapi.TypeStreamStart:
		return &model.StreamEvent{Type: model.StreamEventTypeStreamStart, Stream: streamID, Status: model.StreamStatusLive, Timestamp: msg.Timestamp, Details: detailsPtr}
	case signalmanapi.TypeStreamEnd:
		return &model.StreamEvent{Type: model.StreamEventTypeStreamEnd, Stream: streamID, Status: model.StreamStatusEnded, Timestamp: msg.Timestamp, Details: detailsPtr}
	case signalmanapi.TypeStreamError:
		return &model.StreamEvent{Type: model.StreamEventTypeStreamError, Stream: streamID, Status: model.StreamStatusOffline, Timestamp: msg.Timestamp, Details: detailsPtr}
	case signalmanapi.TypeStreamBuffer:
		return &model.StreamEvent{Type: model.StreamEventTypeBufferUpdate, Stream: streamID, Status: model.StreamStatusLive, Timestamp: msg.Timestamp, Details: detailsPtr}
	case signalmanapi.TypeTrackList:
		return &model.StreamEvent{Type: model.StreamEventTypeTrackListUpdate, Stream: streamID, Status: model.StreamStatusLive, Timestamp: msg.Timestamp, Details: detailsPtr}
	default:
		return &model.StreamEvent{Type: model.StreamEventTypeStreamStart, Stream: streamID, Status: model.StreamStatusLive, Timestamp: msg.Timestamp, Details: detailsPtr}
	}
}

// convertToViewerMetrics converts a Signalman message to a GraphQL ViewerMetrics
func (wm *WebSocketManager) convertToViewerMetrics(msg signalmanapi.Message) *model.ViewerMetrics {
	if msg.Type != signalmanapi.TypeViewerMetrics {
		return nil
	}
	streamID, _ := msg.Data["stream_id"].(string)
	currentViewers := 0
	if v, ok := msg.Data["viewer_count"].(float64); ok {
		currentViewers = int(v)
	}
	peakViewers := 0
	if pv, ok := msg.Data["peak_viewers"].(float64); ok {
		peakViewers = int(pv)
	}
	bandwidth := 0.0
	if b, ok := msg.Data["bandwidth"].(float64); ok {
		bandwidth = b
	}
	var connectionQuality *float64
	if cq, ok := msg.Data["connection_quality"].(float64); ok {
		connectionQuality = &cq
	}
	var bufferHealth *float64
	if bh, ok := msg.Data["buffer_health"].(float64); ok {
		bufferHealth = &bh
	}
	return &model.ViewerMetrics{
		Stream:            streamID,
		CurrentViewers:    currentViewers,
		PeakViewers:       peakViewers,
		Bandwidth:         bandwidth,
		ConnectionQuality: connectionQuality,
		BufferHealth:      bufferHealth,
		Timestamp:         msg.Timestamp,
	}
}

// convertToSystemHealthEvent converts a Signalman message to a GraphQL SystemHealthEvent
func (wm *WebSocketManager) convertToSystemHealthEvent(msg signalmanapi.Message) *model.SystemHealthEvent {
	nodeID, _ := msg.Data["node_id"].(string)
	clusterID, _ := msg.Data["cluster_id"].(string)
	cpu := 0.0
	if v, ok := msg.Data["cpu_usage"].(float64); ok {
		cpu = v
	}
	mem := 0.0
	if v, ok := msg.Data["memory_usage"].(float64); ok {
		mem = v
	}
	disk := 0.0
	if v, ok := msg.Data["disk_usage"].(float64); ok {
		disk = v
	}
	health := 0.0
	if v, ok := msg.Data["health_score"].(float64); ok {
		health = v
	}
	status := model.NodeStatusHealthy
	if s, ok := msg.Data["status"].(string); ok {
		switch s {
		case "HEALTHY", "healthy":
			status = model.NodeStatusHealthy
		case "DEGRADED", "degraded":
			status = model.NodeStatusDegraded
		case "UNHEALTHY", "unhealthy":
			status = model.NodeStatusUnhealthy
		}
	}
	return &model.SystemHealthEvent{
		Node:        nodeID,
		Cluster:     clusterID,
		Status:      status,
		CPUUsage:    cpu,
		MemoryUsage: mem,
		DiskUsage:   disk,
		HealthScore: health,
		Timestamp:   msg.Timestamp,
	}
}

// processTrackListMessages processes track list messages from Signalman
func (wm *WebSocketManager) processTrackListMessages(ctx context.Context, client *signalmanclient.Client, output chan<- *model.TrackListEvent, streamID string) {
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

			// Only process track list events
			if msg.Type != signalmanapi.TypeTrackList {
				continue
			}

			// Filter by stream ID
			if msgStreamID, exists := msg.Data["stream_id"].(string); exists && msgStreamID != streamID {
				continue
			}

			// Convert to GraphQL model
			if update := wm.convertToTrackListEvent(msg); update != nil {
				select {
				case output <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// processTenantEventMessages processes tenant event messages from Signalman
func (wm *WebSocketManager) processTenantEventMessages(ctx context.Context, client *signalmanclient.Client, output chan<- model.TenantEvent, tenantID string) {
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

			// Only process messages for the requested tenant
			if msg.TenantID == nil || *msg.TenantID != tenantID {
				continue
			}

			// Convert to GraphQL model based on message type
			var tenantEvent model.TenantEvent
			switch msg.Type {
			case signalmanapi.TypeStreamStart, signalmanapi.TypeStreamEnd, signalmanapi.TypeStreamError, signalmanapi.TypeStreamBuffer, signalmanapi.TypeTrackList:
				tenantEvent = wm.convertToStreamEvent(msg)
			case signalmanapi.TypeViewerMetrics:
				tenantEvent = wm.convertToViewerMetrics(msg)
				// Note: SystemHealthEvent doesn't implement TenantEvent interface,
				// so we don't include it in tenant events for now
			}

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

// convertToTrackListEvent converts a Signalman message to a GraphQL TrackListEvent
func (wm *WebSocketManager) convertToTrackListEvent(msg signalmanapi.Message) *model.TrackListEvent {
	if msg.Type != signalmanapi.TypeTrackList {
		return nil
	}

	streamID, _ := msg.Data["stream_id"].(string)

	trackList := ""
	if tl, ok := msg.Data["track_list"].(string); ok {
		trackList = tl
	}

	trackCount := 0
	if tc, ok := msg.Data["track_count"].(float64); ok {
		trackCount = int(tc)
	}

	return &model.TrackListEvent{
		Stream:     streamID,
		TrackList:  trackList,
		TrackCount: trackCount,
		Timestamp:  msg.Timestamp,
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
