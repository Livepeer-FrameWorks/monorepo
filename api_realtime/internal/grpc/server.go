package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"frameworks/api_realtime/internal/metrics"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SignalmanServer implements the gRPC SignalmanService
type SignalmanServer struct {
	pb.UnimplementedSignalmanServiceServer
	hub     *Hub
	logger  logging.Logger
	metrics *metrics.Metrics
}

// Hub manages all connected gRPC streaming clients
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan *pb.SignalmanEvent
	register   chan *Client
	unregister chan *Client
	logger     logging.Logger
	metrics    *metrics.Metrics
	mutex      sync.RWMutex
}

// Client represents a connected gRPC streaming client
type Client struct {
	stream   pb.SignalmanService_SubscribeServer
	channels []pb.Channel
	userID   string
	tenantID string
	send     chan *pb.ServerMessage
	done     chan struct{}
	logger   logging.Logger
	mutex    sync.RWMutex
}

// NewSignalmanServer creates a new gRPC server for Signalman
func NewSignalmanServer(logger logging.Logger, m *metrics.Metrics) *SignalmanServer {
	hub := &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *pb.SignalmanEvent, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		logger:     logger,
		metrics:    m,
	}

	server := &SignalmanServer{
		hub:     hub,
		logger:  logger,
		metrics: m,
	}

	// Start hub event loop
	go hub.run()

	return server
}

// GetHub returns the hub for external event broadcasting (e.g., from Kafka consumer)
func (s *SignalmanServer) GetHub() *Hub {
	return s.hub
}

// run is the main event loop for the hub
func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()

			// Record metrics
			if h.metrics != nil {
				for _, ch := range client.channels {
					h.metrics.HubConnections.WithLabelValues(channelToString(ch)).Inc()
				}
			}

			h.logger.WithFields(logging.Fields{
				"client_count": len(h.clients),
				"channels":     client.channels,
				"user_id":      client.userID,
				"tenant_id":    client.tenantID,
			}).Info("gRPC client connected")

		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)

				// Record metrics
				if h.metrics != nil {
					for _, ch := range client.channels {
						h.metrics.HubConnections.WithLabelValues(channelToString(ch)).Dec()
					}
				}
			}
			h.mutex.Unlock()

			h.logger.WithFields(logging.Fields{
				"client_count": len(h.clients),
			}).Info("gRPC client disconnected")

		case event := <-h.broadcast:
			h.broadcastEvent(event)
		}
	}
}

// redactSensitiveData removes sensitive fields from event data before broadcast.
// Only redacts client-facing events (viewer connect/disconnect, client lifecycle, messaging content).
// Infrastructure IPs (nodes, load balancing) are NOT redacted.
func redactSensitiveData(event *pb.SignalmanEvent) {
	if event == nil || event.Data == nil {
		return
	}

	switch p := event.Data.Payload.(type) {
	case *pb.EventData_ViewerConnect:
		if p.ViewerConnect != nil {
			p.ViewerConnect.Host = "" // Redact client IP
		}
	case *pb.EventData_ViewerDisconnect:
		if p.ViewerDisconnect != nil {
			p.ViewerDisconnect.Host = "" // Redact client IP
		}
	case *pb.EventData_ClientLifecycle:
		if p.ClientLifecycle != nil {
			p.ClientLifecycle.Host = "" // Redact client IP
		}
		// LoadBalancingData, NodeLifecycleUpdate - do NOT redact (infrastructure IPs)
	case *pb.EventData_MessageLifecycle:
		if p.MessageLifecycle != nil {
			p.MessageLifecycle.Content = nil
			p.MessageLifecycle.Subject = nil
		}
	}
}

// broadcastEvent sends an event to all relevant clients
func (h *Hub) broadcastEvent(event *pb.SignalmanEvent) {
	// Redact sensitive fields before broadcasting
	redactSensitiveData(event)

	h.mutex.RLock()
	defer h.mutex.RUnlock()

	messagesSent := 0
	for client := range h.clients {
		if !client.shouldReceive(event) {
			continue
		}

		msg := &pb.ServerMessage{
			Message: &pb.ServerMessage_Event{
				Event: event,
			},
		}

		select {
		case client.send <- msg:
			messagesSent++
			// Track message delivery lag
			if h.metrics != nil {
				deliveryLag := time.Since(event.Timestamp.AsTime()).Seconds()
				h.metrics.MessageDeliveryLag.WithLabelValues(
					channelToString(event.Channel),
					eventTypeToString(event.EventType),
				).Observe(deliveryLag)
			}
		default:
			// Client buffer full, mark for disconnect
			h.logger.Warn("Client send buffer full, dropping message")
		}
	}

	// Track hub message metrics
	if h.metrics != nil && messagesSent > 0 {
		h.metrics.HubMessages.WithLabelValues(
			channelToString(event.Channel),
			eventTypeToString(event.EventType),
		).Add(float64(messagesSent))
	}
}

// BroadcastEvent allows external callers (e.g., Kafka consumer) to broadcast events
func (h *Hub) BroadcastEvent(event *pb.SignalmanEvent) {
	h.broadcast <- event
}

// BroadcastToTenant broadcasts an event to clients of a specific tenant
func (h *Hub) BroadcastToTenant(tenantID string, eventType pb.EventType, channel pb.Channel, data *pb.EventData) {
	event := &pb.SignalmanEvent{
		EventType: eventType,
		Channel:   channel,
		Data:      data,
		Timestamp: timestamppb.Now(),
		TenantId:  &tenantID,
	}
	h.broadcast <- event
}

// BroadcastInfrastructure broadcasts infrastructure events (no tenant scope)
func (h *Hub) BroadcastInfrastructure(eventType pb.EventType, data *pb.EventData) {
	event := &pb.SignalmanEvent{
		EventType: eventType,
		Channel:   pb.Channel_CHANNEL_SYSTEM,
		Data:      data,
		Timestamp: timestamppb.Now(),
	}
	h.broadcast <- event
}

// shouldReceive determines if a client should receive an event
func (c *Client) shouldReceive(event *pb.SignalmanEvent) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	// Check channel subscription
	subscribed := false
	for _, ch := range c.channels {
		if ch == event.Channel || ch == pb.Channel_CHANNEL_ALL {
			subscribed = true
			break
		}
	}
	if !subscribed {
		return false
	}

	// Apply tenant isolation
	if event.TenantId != nil && *event.TenantId != "" {
		// Tenant-scoped event - only send to matching tenant
		if c.tenantID == "" || c.tenantID != *event.TenantId {
			return false
		}
	} else {
		// Infrastructure event - only send to system channel subscribers
		if event.Channel != pb.Channel_CHANNEL_SYSTEM {
			return false
		}
	}

	return true
}

// Subscribe implements bidirectional streaming for realtime events
func (s *SignalmanServer) Subscribe(stream pb.SignalmanService_SubscribeServer) error {
	ctx := stream.Context()
	client := &Client{
		stream:   stream,
		channels: []pb.Channel{},
		userID:   ctxkeys.GetUserID(ctx),
		tenantID: ctxkeys.GetTenantID(ctx),
		send:     make(chan *pb.ServerMessage, 256),
		done:     make(chan struct{}),
		logger:   s.logger,
	}

	// Start sender goroutine
	go client.sendLoop()

	// Register client with hub
	s.hub.register <- client

	// Ensure cleanup on exit
	defer func() {
		close(client.done)
		s.hub.unregister <- client
	}()

	// Read client messages
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return status.Errorf(codes.Internal, "receive error: %v", err)
		}

		switch m := msg.Message.(type) {
		case *pb.ClientMessage_Subscribe:
			client.handleSubscribe(m.Subscribe)
		case *pb.ClientMessage_Unsubscribe:
			client.handleUnsubscribe(m.Unsubscribe)
		case *pb.ClientMessage_Ping:
			client.handlePing(m.Ping)
		}
	}
}

// sendLoop sends messages to the client stream
func (c *Client) sendLoop() {
	for {
		select {
		case <-c.done:
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			if msg == nil {
				c.logger.Warn("Skipping nil server message")
				continue
			}
			if err := c.stream.Send(msg); err != nil {
				c.logger.WithError(err).Error("Failed to send message to client")
				return
			}
		}
	}
}

// handleSubscribe processes a subscribe request
func (c *Client) handleSubscribe(req *pb.SubscribeRequest) {
	c.mutex.Lock()
	c.channels = append(c.channels, req.Channels...)
	currentChannels := make([]pb.Channel, len(c.channels))
	copy(currentChannels, c.channels)
	c.mutex.Unlock()

	c.logger.WithFields(logging.Fields{
		"channels":  req.Channels,
		"user_id":   c.userID,
		"tenant_id": c.tenantID,
	}).Info("Client subscribed to channels")

	// Send confirmation
	confirmation := &pb.ServerMessage{
		Message: &pb.ServerMessage_SubscriptionConfirmed{
			SubscriptionConfirmed: &pb.SubscriptionConfirmation{
				SubscribedChannels: currentChannels,
			},
		},
	}

	select {
	case c.send <- confirmation:
	default:
		c.logger.Warn("Failed to send subscription confirmation - buffer full")
	}
}

// handleUnsubscribe processes an unsubscribe request
func (c *Client) handleUnsubscribe(req *pb.UnsubscribeRequest) {
	c.mutex.Lock()
	// Remove channels
	for _, toRemove := range req.Channels {
		for i, ch := range c.channels {
			if ch == toRemove {
				c.channels = append(c.channels[:i], c.channels[i+1:]...)
				break
			}
		}
	}
	currentChannels := make([]pb.Channel, len(c.channels))
	copy(currentChannels, c.channels)
	c.mutex.Unlock()

	c.logger.WithFields(logging.Fields{
		"unsubscribed": req.Channels,
		"remaining":    currentChannels,
	}).Info("Client unsubscribed from channels")

	// Send confirmation
	confirmation := &pb.ServerMessage{
		Message: &pb.ServerMessage_SubscriptionConfirmed{
			SubscriptionConfirmed: &pb.SubscriptionConfirmation{
				SubscribedChannels: currentChannels,
			},
		},
	}

	select {
	case c.send <- confirmation:
	default:
		c.logger.Warn("Failed to send unsubscription confirmation - buffer full")
	}
}

// handlePing processes a ping request
func (c *Client) handlePing(ping *pb.Ping) {
	pong := &pb.ServerMessage{
		Message: &pb.ServerMessage_Pong{
			Pong: &pb.Pong{
				TimestampMs: ping.TimestampMs,
			},
		},
	}

	select {
	case c.send <- pong:
	default:
		c.logger.Warn("Failed to send pong - buffer full")
	}
}

// GetHubStats returns hub statistics
func (s *SignalmanServer) GetHubStats(ctx context.Context, req *pb.GetHubStatsRequest) (*pb.HubStats, error) {
	s.hub.mutex.RLock()
	defer s.hub.mutex.RUnlock()

	channelStats := make(map[string]int32)
	for client := range s.hub.clients {
		client.mutex.RLock()
		for _, ch := range client.channels {
			channelStats[channelToString(ch)]++
		}
		client.mutex.RUnlock()
	}

	return &pb.HubStats{
		TotalConnections:     int32(len(s.hub.clients)),
		TotalClients:         int32(len(s.hub.clients)),
		ChannelSubscriptions: channelStats,
	}, nil
}

// Helper functions

func channelToString(ch pb.Channel) string {
	switch ch {
	case pb.Channel_CHANNEL_STREAMS:
		return "streams"
	case pb.Channel_CHANNEL_ANALYTICS:
		return "analytics"
	case pb.Channel_CHANNEL_SYSTEM:
		return "system"
	case pb.Channel_CHANNEL_ALL:
		return "all"
	case pb.Channel_CHANNEL_MESSAGING:
		return "messaging"
	case pb.Channel_CHANNEL_AI:
		return "ai"
	default:
		return "unknown"
	}
}

func eventTypeToString(et pb.EventType) string {
	switch et {
	// Stream events
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
	// System events
	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE:
		return "node_lifecycle_update"
	case pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		return "load_balancing"
	// Analytics events
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
	case pb.EventType_EVENT_TYPE_RECORDING_COMPLETE:
		return "recording_complete"
	case pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE:
		return "storage_lifecycle"
	case pb.EventType_EVENT_TYPE_PROCESS_BILLING:
		return "process_billing"
	case pb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT:
		return "storage_snapshot"
	case pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE:
		return "message_lifecycle"
	case pb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION:
		return "skipper_investigation"
	default:
		return "unknown"
	}
}
