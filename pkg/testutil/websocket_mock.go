package testutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	"github.com/gorilla/websocket"
)

// MockWebSocketServer provides a mock WebSocket server for testing
type MockWebSocketServer struct {
	server       *httptest.Server
	upgrader     websocket.Upgrader
	logger       logging.Logger
	jwtHelper    *JWTTestHelper
	connections  map[string]*MockConnection
	connMutex    sync.RWMutex
	messagesChan chan MockMessage

	// Callbacks for test customization
	OnConnect    func(conn *MockConnection, userID, tenantID string)
	OnMessage    func(conn *MockConnection, message map[string]interface{})
	OnDisconnect func(conn *MockConnection, userID, tenantID string)
	AuthRequired bool
}

// MockConnection represents a mock WebSocket connection
type MockConnection struct {
	conn     *websocket.Conn
	userID   string
	tenantID string
	email    string
	role     string
	messages chan map[string]interface{}
	closed   bool
	mutex    sync.RWMutex
}

// MockMessage represents a message sent through the mock server
type MockMessage struct {
	UserID   string
	TenantID string
	Type     string
	Data     map[string]interface{}
}

// NewMockWebSocketServer creates a new mock WebSocket server
func NewMockWebSocketServer() *MockWebSocketServer {
	logger := logging.NewLogger()

	mock := &MockWebSocketServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		logger:       logger,
		jwtHelper:    NewJWTTestHelper(),
		connections:  make(map[string]*MockConnection),
		messagesChan: make(chan MockMessage, 100),
		AuthRequired: true,
	}

	handler := http.HandlerFunc(mock.handleWebSocket)
	mock.server = httptest.NewServer(handler)

	return mock
}

// NewMockWebSocketServerWithAuth creates a mock server with custom JWT helper
func NewMockWebSocketServerWithAuth(jwtHelper *JWTTestHelper) *MockWebSocketServer {
	server := NewMockWebSocketServer()
	server.jwtHelper = jwtHelper
	return server
}

// URL returns the WebSocket URL of the mock server
func (m *MockWebSocketServer) URL() string {
	return strings.Replace(m.server.URL, "http://", "ws://", 1)
}

// Close shuts down the mock server
func (m *MockWebSocketServer) Close() {
	m.connMutex.Lock()
	defer m.connMutex.Unlock()

	// Close all connections
	for _, conn := range m.connections {
		conn.Close()
	}

	m.server.Close()
	close(m.messagesChan)
}

// GetConnection returns a specific connection by user and tenant ID
func (m *MockWebSocketServer) GetConnection(userID, tenantID string) *MockConnection {
	m.connMutex.RLock()
	defer m.connMutex.RUnlock()

	key := userID + ":" + tenantID
	return m.connections[key]
}

// GetConnections returns all active connections
func (m *MockWebSocketServer) GetConnections() map[string]*MockConnection {
	m.connMutex.RLock()
	defer m.connMutex.RUnlock()

	connections := make(map[string]*MockConnection)
	for k, v := range m.connections {
		connections[k] = v
	}
	return connections
}

// BroadcastMessage broadcasts a message to all connections
func (m *MockWebSocketServer) BroadcastMessage(msgType string, data map[string]interface{}) {
	m.connMutex.RLock()
	defer m.connMutex.RUnlock()

	message := map[string]interface{}{
		"type": msgType,
		"data": data,
	}

	for _, conn := range m.connections {
		conn.SendMessage(message)
	}
}

// SendMessageToUser sends a message to a specific user
func (m *MockWebSocketServer) SendMessageToUser(userID, tenantID, msgType string, data map[string]interface{}) {
	conn := m.GetConnection(userID, tenantID)
	if conn != nil {
		message := map[string]interface{}{
			"type": msgType,
			"data": data,
		}
		conn.SendMessage(message)
	}
}

// GetMessages returns the messages channel for testing
func (m *MockWebSocketServer) GetMessages() <-chan MockMessage {
	return m.messagesChan
}

func (m *MockWebSocketServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	var userID, tenantID, email, role string

	if m.AuthRequired {
		// Validate JWT authentication
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		// Validate JWT
		claims, err := m.jwtHelper.ValidateJWT(parts[1])
		if err != nil {
			m.logger.WithError(err).Warn("Invalid JWT token for WebSocket connection")
			http.Error(w, "Invalid authentication", http.StatusUnauthorized)
			return
		}

		userID = claims.UserID
		tenantID = claims.TenantID
		email = claims.Email
		role = claims.Role
	} else {
		// For testing without auth
		userID = "test-user"
		tenantID = "test-tenant"
		email = "test@example.com"
		role = "user"
	}

	// Upgrade to WebSocket
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		m.logger.WithError(err).Error("Failed to upgrade WebSocket connection")
		return
	}

	// Create mock connection
	mockConn := &MockConnection{
		conn:     conn,
		userID:   userID,
		tenantID: tenantID,
		email:    email,
		role:     role,
		messages: make(chan map[string]interface{}, 10),
		closed:   false,
	}

	// Register connection
	key := userID + ":" + tenantID
	m.connMutex.Lock()
	m.connections[key] = mockConn
	m.connMutex.Unlock()

	// Send authentication success message
	authResponse := map[string]interface{}{
		"type": "auth_success",
		"data": map[string]interface{}{
			"user_id":   userID,
			"tenant_id": tenantID,
		},
	}
	mockConn.SendMessage(authResponse)

	// Call connect callback
	if m.OnConnect != nil {
		m.OnConnect(mockConn, userID, tenantID)
	}

	// Start message handlers
	go mockConn.readPump(m)
	go mockConn.writePump(m)
}

// MockConnection methods

// SendMessage sends a message to the connection
func (c *MockConnection) SendMessage(message map[string]interface{}) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if !c.closed {
		select {
		case c.messages <- message:
		default:
			// Channel full, drop message
		}
	}
}

// Close closes the connection
func (c *MockConnection) Close() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.closed {
		c.closed = true
		close(c.messages)
		c.conn.Close()
	}
}

// IsConnected returns whether the connection is active
func (c *MockConnection) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return !c.closed
}

// GetUserID returns the user ID for the connection
func (c *MockConnection) GetUserID() string {
	return c.userID
}

// GetTenantID returns the tenant ID for the connection
func (c *MockConnection) GetTenantID() string {
	return c.tenantID
}

func (c *MockConnection) readPump(server *MockWebSocketServer) {
	defer func() {
		// Cleanup connection
		key := c.userID + ":" + c.tenantID
		server.connMutex.Lock()
		delete(server.connections, key)
		server.connMutex.Unlock()

		if server.OnDisconnect != nil {
			server.OnDisconnect(c, c.userID, c.tenantID)
		}

		c.Close()
	}()

	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck // test utility
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck // test utility
		return nil
	})

	for {
		var message map[string]interface{}
		if err := c.conn.ReadJSON(&message); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				server.logger.WithError(err).Error("Error reading WebSocket message")
			}
			break
		}

		// Send message to server's message channel for testing
		if msgType, ok := message["type"].(string); ok {
			mockMsg := MockMessage{
				UserID:   c.userID,
				TenantID: c.tenantID,
				Type:     msgType,
				Data:     message,
			}

			select {
			case server.messagesChan <- mockMsg:
			default:
				// Channel full, drop message
			}
		}

		// Call message callback
		if server.OnMessage != nil {
			server.OnMessage(c, message)
		}

		// Handle specific message types
		c.handleMessage(message)
	}
}

func (c *MockConnection) writePump(server *MockWebSocketServer) {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.messages:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck // test utility
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{}) //nolint:errcheck // test utility
				return
			}

			if err := c.conn.WriteJSON(message); err != nil {
				server.logger.WithError(err).Error("Error writing WebSocket message")
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck // test utility
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *MockConnection) handleMessage(message map[string]interface{}) {
	msgType, ok := message["type"].(string)
	if !ok {
		return
	}

	// Mock subscription confirmations
	switch msgType {
	case "subscribe_streams":
		response := map[string]interface{}{
			"type":    "subscription_confirmed",
			"channel": "streams",
		}
		c.SendMessage(response)

	case "subscribe_analytics":
		response := map[string]interface{}{
			"type":    "subscription_confirmed",
			"channel": "analytics",
		}
		c.SendMessage(response)

	case "subscribe_system":
		response := map[string]interface{}{
			"type":    "subscription_confirmed",
			"channel": "system",
		}
		c.SendMessage(response)

	case "unsubscribe":
		if channel, ok := message["channel"].(string); ok {
			response := map[string]interface{}{
				"type":    "unsubscription_confirmed",
				"channel": channel,
			}
			c.SendMessage(response)
		}
	}
}

// WebSocketTestClient provides a test client for WebSocket connections
type WebSocketTestClient struct {
	conn     *websocket.Conn
	messages chan map[string]interface{}
	errors   chan error
	closed   bool
	mutex    sync.RWMutex
	logger   logging.Logger
}

// NewWebSocketTestClient creates a new test client and connects to the server
func NewWebSocketTestClient(serverURL string, jwt string) (*WebSocketTestClient, error) {
	logger := logging.NewLogger()

	headers := http.Header{}
	if jwt != "" {
		headers.Set("Authorization", "Bearer "+jwt)
	}

	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(serverURL, headers)
	if resp != nil {
		defer func() {
			_ = resp.Body.Close()
		}()
	}
	if err != nil {
		return nil, err
	}

	client := &WebSocketTestClient{
		conn:     conn,
		messages: make(chan map[string]interface{}, 10),
		errors:   make(chan error, 1),
		logger:   logger,
	}

	// Start read pump
	go client.readPump()

	return client, nil
}

// SendMessage sends a message to the server
func (c *WebSocketTestClient) SendMessage(message map[string]interface{}) error {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if c.closed {
		return websocket.ErrCloseSent
	}

	return c.conn.WriteJSON(message)
}

// ReadMessage reads a message from the server (blocking)
func (c *WebSocketTestClient) ReadMessage() (map[string]interface{}, error) {
	select {
	case msg := <-c.messages:
		return msg, nil
	case err := <-c.errors:
		return nil, err
	}
}

// ReadMessageTimeout reads a message with timeout
func (c *WebSocketTestClient) ReadMessageTimeout(timeout time.Duration) (map[string]interface{}, error) {
	select {
	case msg := <-c.messages:
		return msg, nil
	case err := <-c.errors:
		return nil, err
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	}
}

// Close closes the client connection
func (c *WebSocketTestClient) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.closed {
		c.closed = true
		close(c.messages)
		close(c.errors)
		return c.conn.Close()
	}

	return nil
}

func (c *WebSocketTestClient) readPump() {
	defer c.Close()

	for {
		var message map[string]interface{}
		if err := c.conn.ReadJSON(&message); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				select {
				case c.errors <- err:
				default:
				}
			}
			break
		}

		select {
		case c.messages <- message:
		default:
			// Channel full, drop message
		}
	}
}
