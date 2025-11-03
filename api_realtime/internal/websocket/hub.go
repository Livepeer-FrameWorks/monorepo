package websocket

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"frameworks/api_realtime/internal/metrics"
	"frameworks/pkg/api/signalman"
	"frameworks/pkg/auth"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/gorilla/websocket"
)

// Hub maintains the set of active clients and broadcasts messages to the clients
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	logger     logging.Logger
	mutex      sync.RWMutex
	metrics    *metrics.Metrics
}

// Client represents a WebSocket client connection
type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	channels []string // Subscribed channels (streams, analytics, system)
	userID   *string  // Optional user ID for user-specific events
	tenantID *string  // Tenant ID for tenant isolation
	logger   logging.Logger
}

// Message and SubscriptionMessage are now imported from pkg/api/signalman

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// NewHub creates a new WebSocket hub
func NewHub(logger logging.Logger, m *metrics.Metrics) *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		logger:     logger,
		metrics:    m,
	}
}

// Run starts the hub's main loop
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()

			// Record metrics for each channel the client subscribes to
			if h.metrics != nil {
				for _, channel := range client.channels {
					h.metrics.HubConnections.WithLabelValues(channel).Inc()
				}
			}

			h.logger.WithFields(logging.Fields{
				"client_count": len(h.clients),
				"channels":     client.channels,
				"user_id":      client.userID,
			}).Info("Client connected")

		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)

				// Record metrics for each channel the client was subscribed to
				if h.metrics != nil {
					for _, channel := range client.channels {
						h.metrics.HubConnections.WithLabelValues(channel).Dec()
					}
				}
			}
			h.mutex.Unlock()
			h.logger.WithFields(logging.Fields{
				"client_count": len(h.clients),
			}).Info("Client disconnected")

		case message := <-h.broadcast:
			h.broadcastMessage(message)
		}
	}
}

// broadcastMessage sends a message to all relevant clients
func (h *Hub) broadcastMessage(message []byte) {
	var msg signalman.Message
	if err := json.Unmarshal(message, &msg); err != nil {
		h.logger.WithError(err).Error("Failed to unmarshal broadcast message")
		return
	}

	h.mutex.RLock()
	defer h.mutex.RUnlock()

	messagesSent := 0
	for client := range h.clients {
		// Check if client is subscribed to this channel
		subscribed := false
		for _, channel := range client.channels {
			if channel == msg.Channel {
				subscribed = true
				break
			}
		}
		if !subscribed {
			continue
		}

		// Apply tenant isolation for tenant-scoped messages
		if msg.TenantID != nil {
			// Message has tenant context - only send to clients of same tenant
			if client.tenantID == nil || *client.tenantID != *msg.TenantID {
				continue
			}
		} else {
			// Infrastructure message (no tenant) - send to all subscribed clients
			// But only if they're subscribed to "system" channel
			if msg.Channel != "system" {
				continue
			}
		}

		// Send message to client
		select {
		case client.send <- message:
			messagesSent++
			// Track message delivery lag
			if h.metrics != nil {
				deliveryLag := time.Since(msg.Timestamp).Seconds()
				h.metrics.MessageDeliveryLag.WithLabelValues(msg.Channel, msg.Type).Observe(deliveryLag)
			}
		default:
			close(client.send)
			delete(h.clients, client)
		}
	}

	// Track hub message metrics
	if h.metrics != nil && messagesSent > 0 {
		h.metrics.HubMessages.WithLabelValues(msg.Channel, msg.Type).Add(float64(messagesSent))
	}
}

// shouldReceiveMessage determines if a client should receive a message
func (h *Hub) shouldReceiveMessage(client *Client, msg *signalman.Message) bool {
	// Check if client is subscribed to the channel
	for _, channel := range client.channels {
		if channel == msg.Channel || channel == "all" {
			// For user-specific messages, check user ID from typed event data
			if userID := h.extractUserIDFromEvent(msg); userID != "" {
				if client.userID != nil && *client.userID == userID {
					return true
				}
				// Skip user-specific messages for non-matching users
				return false
			}
			return true
		}
	}
	return false
}

// extractUserIDFromEvent extracts user ID from event data
func (h *Hub) extractUserIDFromEvent(msg *signalman.Message) string {
	// Extract user ID/session ID from MistTrigger envelope for viewer connect/disconnect
	switch msg.Type {
	case "viewer_resolve":
		return "" // not user-specific
	case "viewer_connect", "viewer_disconnect":
		// RawData holds a MistTrigger; try to unmarshal and inspect
		b, err := json.Marshal(msg.RawData)
		if err != nil {
			return ""
		}
		var mt pb.MistTrigger
		if err := json.Unmarshal(b, &mt); err != nil {
			return ""
		}
		switch p := mt.GetTriggerPayload().(type) {
		case *pb.MistTrigger_ViewerConnect:
			if p.ViewerConnect.GetSessionId() != "" {
				return p.ViewerConnect.GetSessionId()
			}
		case *pb.MistTrigger_ViewerDisconnect:
			if p.ViewerDisconnect.GetSessionId() != "" {
				return p.ViewerDisconnect.GetSessionId()
			}
		}
	}
	return ""
}

// unregisterClient safely unregisters a client
func (h *Hub) unregisterClient(client *Client) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		close(client.send)
		client.conn.Close()
	}
}

// BroadcastTypedToTenant sends a typed message to all clients of a specific tenant
func (h *Hub) BroadcastTypedToTenant(tenantID string, msgType, channel string, data map[string]interface{}) {
	message := signalman.Message{
		Type:      msgType,
		Channel:   channel,
		Data:      h.convertToTypedEventData(msgType, data),
		RawData:   data, // Keep raw data for backwards compatibility
		Timestamp: time.Now(),
		TenantID:  &tenantID,
	}

	messageJSON, err := json.Marshal(message)
	if err != nil {
		h.logger.WithError(err).Error("Failed to marshal typed tenant message")
		return
	}

	h.broadcast <- messageJSON
}

// BroadcastTypedInfrastructure sends typed infrastructure messages to all clients subscribed to system channel
func (h *Hub) BroadcastTypedInfrastructure(msgType string, data map[string]interface{}) {
	message := signalman.Message{
		Type:      msgType,
		Channel:   "system",
		Data:      h.convertToTypedEventData(msgType, data),
		RawData:   data, // Keep raw data for backwards compatibility
		Timestamp: time.Now(),
	}

	messageJSON, err := json.Marshal(message)
	if err != nil {
		h.logger.WithError(err).Error("Failed to marshal typed infrastructure message")
		return
	}

	h.broadcast <- messageJSON
}

// GetStats returns hub statistics
func (h *Hub) GetStats() *signalman.HubStats {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	channelStats := make(map[string]int)
	for client := range h.clients {
		for _, channel := range client.channels {
			channelStats[channel]++
		}
	}

	return &signalman.HubStats{
		Connections:          len(h.clients),
		TotalClients:         len(h.clients),
		ChannelSubscriptions: channelStats,
	}
}

// ServeWS handles WebSocket requests from clients
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	jwtSecret := config.RequireEnv("JWT_SECRET")

	// Check for JWT token in Authorization header
	var userID, tenantID string
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && parts[0] == "Bearer" {
			claims, err := auth.ValidateJWT(parts[1], []byte(jwtSecret))
			if err != nil {
				h.logger.WithError(err).Warn("Invalid JWT token for WebSocket connection")
				http.Error(w, "Invalid authentication", http.StatusUnauthorized)
				return
			}
			userID = claims.UserID
			tenantID = claims.TenantID
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.WithError(err).Error("Failed to upgrade WebSocket connection")
		return
	}

	// Create client with optional authentication
	var userIDPtr, tenantIDPtr *string
	if userID != "" {
		userIDPtr = &userID
	}
	if tenantID != "" {
		tenantIDPtr = &tenantID
	}

	client := &Client{
		hub:      h,
		conn:     conn,
		send:     make(chan []byte, 256),
		channels: []string{},  // No subscriptions initially
		userID:   userIDPtr,   // Set authenticated user ID (nil if not authenticated)
		tenantID: tenantIDPtr, // Set authenticated tenant ID (nil if not authenticated)
		logger:   h.logger,
	}

	client.hub.register <- client

	// Start goroutines for reading and writing
	go client.writePump()
	go client.readPump()
}

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer
	maxMessageSize = 512
)

// readPump pumps messages from the WebSocket connection to the hub
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.WithError(err).Error("WebSocket connection error")
			}
			break
		}

		// Handle subscription messages
		var subMsg signalman.SubscriptionMessage
		if err := json.Unmarshal(message, &subMsg); err != nil {
			c.logger.WithError(err).Warn("Invalid subscription message")
			continue
		}

		c.handleSubscription(&subMsg)
	}
}

// writePump pumps messages from the hub to the WebSocket connection
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued chat messages to the current WebSocket message
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleSubscription processes subscription/unsubscription requests
func (c *Client) handleSubscription(msg *signalman.SubscriptionMessage) {
	switch msg.Action {
	case "subscribe":
		c.channels = append(c.channels, msg.Channels...)
		if msg.UserID != nil {
			c.userID = msg.UserID
		}
		if msg.TenantID != nil {
			c.tenantID = msg.TenantID
		}
		c.logger.WithFields(logging.Fields{
			"channels":  msg.Channels,
			"user_id":   msg.UserID,
			"tenant_id": msg.TenantID,
		}).Info("Client subscribed to channels")

		// Send confirmation
		response := signalman.SubscriptionConfirmation{
			Type:     signalman.TypeSubscriptionConfirmed,
			Channels: c.channels,
		}
		c.sendTypedMessage(response)

	case "unsubscribe":
		// Remove channels from subscription
		for _, channel := range msg.Channels {
			for i, existing := range c.channels {
				if existing == channel {
					c.channels = append(c.channels[:i], c.channels[i+1:]...)
					break
				}
			}
		}

		c.logger.WithFields(logging.Fields{
			"unsubscribed": msg.Channels,
			"remaining":    c.channels,
		}).Info("Client unsubscribed from channels")

		// Send confirmation
		response := signalman.SubscriptionConfirmation{
			Type:     signalman.TypeUnsubscriptionConfirmed,
			Channels: c.channels,
		}
		c.sendTypedMessage(response)
	}
}

// sendMessage functionality replaced by sendTypedMessage for type safety

// sendTypedMessage sends a typed message to the client
func (c *Client) sendTypedMessage(data interface{}) {
	message, err := json.Marshal(data)
	if err != nil {
		c.logger.WithError(err).Error("Failed to marshal typed client message")
		return
	}

	select {
	case c.send <- message:
	default:
		// Channel full, disconnect client
		close(c.send)
	}
}

// convertToTypedEventData converts raw protobuf data to typed EventData
func (h *Hub) convertToTypedEventData(eventType string, data map[string]interface{}) signalman.EventData {
	eventData := signalman.EventData{}
	// Data is a MistTrigger envelope; unmarshal once and extract
	b, err := json.Marshal(data)
	if err != nil {
		return eventData
	}
	var mt pb.MistTrigger
	if err := json.Unmarshal(b, &mt); err != nil {
		return eventData
	}

	switch p := mt.GetTriggerPayload().(type) {
	case *pb.MistTrigger_ClientLifecycleUpdate:
		eventData.ClientLifecycle = p.ClientLifecycleUpdate
	case *pb.MistTrigger_NodeLifecycleUpdate:
		eventData.NodeLifecycle = p.NodeLifecycleUpdate
	case *pb.MistTrigger_TrackList:
		eventData.TrackList = p.TrackList
	case *pb.MistTrigger_StreamBuffer:
		// Stream buffer not represented in EventData; no-op here (Stream events handled separately)
	case *pb.MistTrigger_ClipLifecycleData:
		eventData.ClipLifecycle = p.ClipLifecycleData
	case *pb.MistTrigger_DvrLifecycleData:
		eventData.DVRLifecycle = p.DvrLifecycleData
	case *pb.MistTrigger_LoadBalancingData:
		eventData.LoadBalancing = p.LoadBalancingData
	}
	return eventData
}
