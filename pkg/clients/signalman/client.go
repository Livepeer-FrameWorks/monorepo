package signalman

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"frameworks/pkg/api/signalman"
	"frameworks/pkg/logging"

	"github.com/gorilla/websocket"
)

// Client represents a WebSocket client for Signalman
type Client struct {
	baseURL        string
	conn           *websocket.Conn
	logger         logging.Logger
	messageChan    chan signalman.Message
	subscriptions  []string
	userID         *string
	tenantID       *string
	mutex          sync.RWMutex
	reconnectDelay time.Duration
	maxReconnects  int
	connected      bool
	stopChan       chan struct{}
	doneChan       chan struct{}
}

// Config represents the configuration for the Signalman client
type Config struct {
	BaseURL        string
	Logger         logging.Logger
	UserID         *string
	TenantID       *string
	ReconnectDelay time.Duration
	MaxReconnects  int
}

// MessageHandler represents a function that handles incoming messages
type MessageHandler func(msg signalman.Message) error

// NewClient creates a new Signalman WebSocket client
func NewClient(config Config) *Client {
	if config.ReconnectDelay == 0 {
		config.ReconnectDelay = 5 * time.Second
	}
	if config.MaxReconnects == 0 {
		config.MaxReconnects = 5
	}

	return &Client{
		baseURL:        config.BaseURL,
		logger:         config.Logger,
		userID:         config.UserID,
		tenantID:       config.TenantID,
		reconnectDelay: config.ReconnectDelay,
		maxReconnects:  config.MaxReconnects,
		messageChan:    make(chan signalman.Message, 100),
		subscriptions:  make([]string, 0),
		stopChan:       make(chan struct{}),
		doneChan:       make(chan struct{}),
	}
}

// Connect establishes a WebSocket connection to Signalman
func (c *Client) Connect(ctx context.Context, endpoint string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.connected {
		return fmt.Errorf("client is already connected")
	}

	wsURL := c.buildWebSocketURL(endpoint)

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 30 * time.Second

	conn, resp, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("failed to connect to WebSocket (status: %d): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	c.conn = conn
	c.connected = true

	// Start read/write pumps
	go c.readPump()
	go c.writePump()

	c.logger.Info("Connected to Signalman WebSocket")
	return nil
}

// ConnectWithAuth establishes a WebSocket connection to Signalman with JWT authentication
func (c *Client) ConnectWithAuth(ctx context.Context, endpoint string, jwtToken string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.connected {
		return fmt.Errorf("client is already connected")
	}

	wsURL := c.buildWebSocketURL(endpoint)

	// Set up headers with JWT authentication
	headers := make(http.Header)
	if jwtToken != "" {
		headers.Set("Authorization", "Bearer "+jwtToken)
	}

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 30 * time.Second

	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("failed to connect to WebSocket (status: %d): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	c.conn = conn
	c.connected = true

	// Start read/write pumps
	go c.readPump()
	go c.writePump()

	c.logger.WithFields(logging.Fields{
		"user_id":   c.userID,
		"tenant_id": c.tenantID,
	}).Info("Connected to Signalman WebSocket with authentication")
	return nil
}

// ConnectStreams connects to the streams WebSocket endpoint
func (c *Client) ConnectStreams(ctx context.Context) error {
	return c.Connect(ctx, "/ws/streams")
}

// ConnectAnalytics connects to the analytics WebSocket endpoint
func (c *Client) ConnectAnalytics(ctx context.Context) error {
	return c.Connect(ctx, "/ws/analytics")
}

// ConnectSystem connects to the system WebSocket endpoint
func (c *Client) ConnectSystem(ctx context.Context) error {
	return c.Connect(ctx, "/ws/system")
}

// ConnectAll connects to the all-events WebSocket endpoint
func (c *Client) ConnectAll(ctx context.Context) error {
	return c.Connect(ctx, "/ws")
}

// buildWebSocketURL constructs the WebSocket URL
func (c *Client) buildWebSocketURL(endpoint string) string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		// Fallback to direct construction
		return fmt.Sprintf("ws://%s%s", c.baseURL, endpoint)
	}

	// Convert HTTP/HTTPS to WS/WSS
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}

	wsURL := &url.URL{
		Scheme: scheme,
		Host:   u.Host,
		Path:   endpoint,
	}

	return wsURL.String()
}

// Subscribe subscribes to specific channels
func (c *Client) Subscribe(channels []string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.connected {
		return fmt.Errorf("client is not connected")
	}

	subMsg := signalman.SubscriptionMessage{
		Action:   signalman.ActionSubscribe,
		Channels: channels,
		UserID:   c.userID,
		TenantID: c.tenantID,
	}

	// Set write deadline for subscription message
	c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := c.conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("failed to send subscription: %w", err)
	}

	// Update local subscriptions
	c.subscriptions = append(c.subscriptions, channels...)

	c.logger.WithFields(logging.Fields{
		"channels":  channels,
		"user_id":   c.userID,
		"tenant_id": c.tenantID,
	}).Info("Subscribed to channels")

	return nil
}

// Unsubscribe unsubscribes from specific channels
func (c *Client) Unsubscribe(channels []string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.connected {
		return fmt.Errorf("client is not connected")
	}

	subMsg := signalman.SubscriptionMessage{
		Action:   signalman.ActionUnsubscribe,
		Channels: channels,
		UserID:   c.userID,
		TenantID: c.tenantID,
	}

	// Set write deadline for unsubscription message
	c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := c.conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("failed to send unsubscription: %w", err)
	}

	// Update local subscriptions
	for _, channel := range channels {
		for i, existing := range c.subscriptions {
			if existing == channel {
				c.subscriptions = append(c.subscriptions[:i], c.subscriptions[i+1:]...)
				break
			}
		}
	}

	c.logger.WithFields(logging.Fields{
		"channels":  channels,
		"remaining": c.subscriptions,
	}).Info("Unsubscribed from channels")

	return nil
}

// GetMessages returns the channel for receiving messages
func (c *Client) GetMessages() <-chan signalman.Message {
	return c.messageChan
}

// IsConnected returns whether the client is connected
func (c *Client) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.connected
}

// GetSubscriptions returns the current subscriptions
func (c *Client) GetSubscriptions() []string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return append([]string(nil), c.subscriptions...) // Return a copy
}

// TenantID returns the tenant ID associated with the client
func (c *Client) TenantID() string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.tenantID == nil {
		return ""
	}
	return *c.tenantID
}

// Close closes the WebSocket connection
func (c *Client) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.connected {
		return nil
	}

	close(c.stopChan)

	if c.conn != nil {
		c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.conn.Close()
	}

	c.connected = false
	close(c.messageChan)

	// Wait for pumps to finish
	<-c.doneChan

	c.logger.Info("Disconnected from Signalman WebSocket")
	return nil
}

// readPump handles reading messages from the WebSocket
func (c *Client) readPump() {
	defer func() {
		c.mutex.Lock()
		c.connected = false
		c.mutex.Unlock()

		if c.conn != nil {
			c.conn.Close()
		}

		select {
		case c.doneChan <- struct{}{}:
		default:
		}
	}()

	c.conn.SetReadLimit(512 * 1024) // 512KB max message size
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		select {
		case <-c.stopChan:
			return
		default:
		}

		var message signalman.Message
		err := c.conn.ReadJSON(&message)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.WithError(err).Error("WebSocket read error")
			}
			return
		}

		// Send message to channel (non-blocking)
		select {
		case c.messageChan <- message:
		default:
			c.logger.Warn("Message channel full, dropping message")
		}
	}
}

// writePump handles writing messages to the WebSocket (primarily ping messages)
func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second) // Send ping every 54 seconds
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.logger.WithError(err).Error("Failed to send ping")
				return
			}
		}
	}
}

// StartMessageHandler starts a message handler in a goroutine
func (c *Client) StartMessageHandler(handler MessageHandler) {
	go func() {
		for msg := range c.GetMessages() {
			if err := handler(msg); err != nil {
				c.logger.WithError(err).WithFields(logging.Fields{
					"message_type": msg.Type,
					"channel":      msg.Channel,
				}).Error("Message handler error")
			}
		}
	}()
}

// SubscribeToStreams is a convenience method to subscribe to streams channel
func (c *Client) SubscribeToStreams() error {
	return c.Subscribe([]string{signalman.ChannelStreams})
}

// SubscribeToAnalytics is a convenience method to subscribe to analytics channel
func (c *Client) SubscribeToAnalytics() error {
	return c.Subscribe([]string{signalman.ChannelAnalytics})
}

// SubscribeToSystem is a convenience method to subscribe to system channel
func (c *Client) SubscribeToSystem() error {
	return c.Subscribe([]string{signalman.ChannelSystem})
}

// SubscribeToAll is a convenience method to subscribe to all channels
func (c *Client) SubscribeToAll() error {
	return c.Subscribe([]string{signalman.ChannelAll})
}
