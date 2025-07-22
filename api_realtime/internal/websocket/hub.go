package websocket

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"frameworks/pkg/logging"

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

// Message represents a real-time message sent to clients
type Message struct {
	Type      string                 `json:"type"`
	Channel   string                 `json:"channel"`
	Data      map[string]interface{} `json:"data"`
	Timestamp time.Time              `json:"timestamp"`
	TenantID  *string                `json:"tenant_id,omitempty"` // For tenant-scoped messages
}

// SubscriptionMessage represents a subscription request from client
type SubscriptionMessage struct {
	Action   string   `json:"action"`   // "subscribe" or "unsubscribe"
	Channels []string `json:"channels"` // ["streams", "analytics", "system"]
	UserID   *string  `json:"user_id,omitempty"`
	TenantID *string  `json:"tenant_id,omitempty"` // Required for tenant-scoped channels
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// NewHub creates a new WebSocket hub
func NewHub(logger logging.Logger) *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		logger:     logger,
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
	var msg Message
	if err := json.Unmarshal(message, &msg); err != nil {
		h.logger.WithError(err).Error("Failed to unmarshal broadcast message")
		return
	}

	h.mutex.RLock()
	defer h.mutex.RUnlock()

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
		default:
			close(client.send)
			delete(h.clients, client)
		}
	}
}

// BroadcastToTenant sends a message to all clients of a specific tenant
func (h *Hub) BroadcastToTenant(tenantID string, msgType, channel string, data map[string]interface{}) {
	message := Message{
		Type:      msgType,
		Channel:   channel,
		Data:      data,
		Timestamp: time.Now(),
		TenantID:  &tenantID,
	}

	messageJSON, err := json.Marshal(message)
	if err != nil {
		h.logger.WithError(err).Error("Failed to marshal tenant message")
		return
	}

	h.broadcast <- messageJSON
}

// BroadcastInfrastructure sends infrastructure messages to all clients subscribed to system channel
func (h *Hub) BroadcastInfrastructure(msgType string, data map[string]interface{}) {
	message := Message{
		Type:      msgType,
		Channel:   "system",
		Data:      data,
		Timestamp: time.Now(),
		TenantID:  nil, // No tenant context for infrastructure
	}

	messageJSON, err := json.Marshal(message)
	if err != nil {
		h.logger.WithError(err).Error("Failed to marshal infrastructure message")
		return
	}

	h.broadcast <- messageJSON
}

// shouldReceiveMessage determines if a client should receive a message
func (h *Hub) shouldReceiveMessage(client *Client, msg *Message) bool {
	// Check if client is subscribed to the channel
	for _, channel := range client.channels {
		if channel == msg.Channel || channel == "all" {
			// For user-specific messages, check user ID
			if userID, exists := msg.Data["user_id"]; exists {
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

// BroadcastEvent sends an event to all subscribed clients
func (h *Hub) BroadcastEvent(eventType, channel string, data map[string]interface{}) {
	message := Message{
		Type:      eventType,
		Channel:   channel,
		Data:      data,
		Timestamp: time.Now().UTC(),
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		h.logger.WithError(err).Error("Failed to marshal broadcast message")
		return
	}

	select {
	case h.broadcast <- messageBytes:
	default:
		h.logger.Warn("Broadcast channel full, dropping message")
	}
}

// GetStats returns hub statistics
func (h *Hub) GetStats() map[string]interface{} {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	channelStats := make(map[string]int)
	for client := range h.clients {
		for _, channel := range client.channels {
			channelStats[channel]++
		}
	}

	return map[string]interface{}{
		"total_clients":         len(h.clients),
		"channel_subscriptions": channelStats,
	}
}

// ServeWS handles WebSocket requests from clients
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.WithError(err).Error("Failed to upgrade WebSocket connection")
		return
	}

	client := &Client{
		hub:      h,
		conn:     conn,
		send:     make(chan []byte, 256),
		channels: []string{}, // No subscriptions initially
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
		var subMsg SubscriptionMessage
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
func (c *Client) handleSubscription(msg *SubscriptionMessage) {
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
		response := map[string]interface{}{
			"type":     "subscription_confirmed",
			"channels": c.channels,
		}
		c.sendMessage(response)

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
		response := map[string]interface{}{
			"type":     "unsubscription_confirmed",
			"channels": c.channels,
		}
		c.sendMessage(response)
	}
}

// sendMessage sends a message to the client
func (c *Client) sendMessage(data map[string]interface{}) {
	message, err := json.Marshal(data)
	if err != nil {
		c.logger.WithError(err).Error("Failed to marshal client message")
		return
	}

	select {
	case c.send <- message:
	default:
		// Channel full, disconnect client
		close(c.send)
	}
}
