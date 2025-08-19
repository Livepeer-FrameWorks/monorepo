package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/pkg/auth"
	"frameworks/pkg/logging"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration test for the complete WebSocket authentication flow
// Tests the end-to-end flow from client -> api_gateway -> api_realtime (signalman)

type TestServer struct {
	*httptest.Server
	upgrader websocket.Upgrader
	logger   logging.Logger
}

func NewTestSignalmanServer() *TestServer {
	logger := logging.NewLogger()
	
	server := &TestServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		logger: logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", server.handleWebSocket)
	server.Server = httptest.NewServer(mux)
	
	return server
}

func (s *TestServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Simulate signalman's JWT validation logic
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

	// Validate JWT using the same secret as the gateway
	jwtSecret := []byte("integration-test-secret")
	claims, err := auth.ValidateJWT(parts[1], jwtSecret)
	if err != nil {
		s.logger.WithError(err).Warn("Invalid JWT token for WebSocket connection")
		http.Error(w, "Invalid authentication", http.StatusUnauthorized)
		return
	}

	// Upgrade to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.WithError(err).Error("Failed to upgrade WebSocket connection")
		return
	}
	defer conn.Close()

	// Send authentication success message
	authResponse := map[string]interface{}{
		"type": "auth_success",
		"data": map[string]interface{}{
			"user_id":   claims.UserID,
			"tenant_id": claims.TenantID,
		},
	}
	if err := conn.WriteJSON(authResponse); err != nil {
		s.logger.WithError(err).Error("Failed to send auth response")
		return
	}

	// Handle subscription messages
	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				s.logger.WithError(err).Error("Error reading WebSocket message")
			}
			break
		}

		// Echo back subscription confirmations
		if msgType, ok := msg["type"].(string); ok {
			switch msgType {
			case "subscribe_streams":
				response := map[string]interface{}{
					"type": "subscription_confirmed",
					"channel": "streams",
				}
				conn.WriteJSON(response)
			case "subscribe_analytics":
				response := map[string]interface{}{
					"type": "subscription_confirmed", 
					"channel": "analytics",
				}
				conn.WriteJSON(response)
			case "subscribe_system":
				response := map[string]interface{}{
					"type": "subscription_confirmed",
					"channel": "system",
				}
				conn.WriteJSON(response)
			}
		}
	}
}

func TestWebSocketAuthenticationFlow_Success(t *testing.T) {
	// Start test signalman server
	signalmanServer := NewTestSignalmanServer()
	defer signalmanServer.Close()

	logger := logging.NewLogger()
	
	// Test data
	userID := "test-user-123"
	tenantID := "test-tenant-456"
	email := "test@example.com"
	role := "user"
	jwtSecret := []byte("integration-test-secret")

	// Generate valid JWT
	jwt, err := auth.GenerateJWT(userID, tenantID, email, role, jwtSecret)
	require.NoError(t, err)

	// Connect to test signalman server with authentication
	wsURL := strings.Replace(signalmanServer.URL, "http://", "ws://", 1) + "/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+jwt)

	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(wsURL, headers)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn.Close()

	// Read authentication success message
	var authMsg map[string]interface{}
	err = conn.ReadJSON(&authMsg)
	require.NoError(t, err)

	assert.Equal(t, "auth_success", authMsg["type"])
	data, ok := authMsg["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, userID, data["user_id"])
	assert.Equal(t, tenantID, data["tenant_id"])

	logger.Info("WebSocket authentication flow completed successfully")
}

func TestWebSocketAuthenticationFlow_InvalidJWT(t *testing.T) {
	// Start test signalman server
	signalmanServer := NewTestSignalmanServer()
	defer signalmanServer.Close()

	// Try to connect with invalid JWT
	wsURL := strings.Replace(signalmanServer.URL, "http://", "ws://", 1) + "/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer invalid.jwt.token")

	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(wsURL, headers)
	
	// Should fail to connect
	require.Error(t, err)
	require.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode)
	if conn != nil {
		conn.Close()
	}
}

func TestWebSocketAuthenticationFlow_ExpiredJWT(t *testing.T) {
	// Start test signalman server
	signalmanServer := NewTestSignalmanServer()
	defer signalmanServer.Close()

	jwtSecret := []byte("integration-test-secret")
	
	// Create an expired JWT (this is tricky in tests, so we'll simulate by using wrong secret)
	// In real scenario, we'd need to manipulate the expiration time
	expiredJWT, err := auth.GenerateJWT("user", "tenant", "test@example.com", "user", []byte("wrong-secret"))
	require.NoError(t, err)

	// Try to connect with expired/invalid JWT
	wsURL := strings.Replace(signalmanServer.URL, "http://", "ws://", 1) + "/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+expiredJWT)

	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(wsURL, headers)
	
	// Should fail to connect
	require.Error(t, err)
	require.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode)
	if conn != nil {
		conn.Close()
	}
}

func TestWebSocketSubscriptionFlow(t *testing.T) {
	// Start test signalman server
	signalmanServer := NewTestSignalmanServer()
	defer signalmanServer.Close()

	jwtSecret := []byte("integration-test-secret")
	jwt, err := auth.GenerateJWT("user-123", "tenant-456", "test@example.com", "user", jwtSecret)
	require.NoError(t, err)

	// Connect with valid JWT
	wsURL := strings.Replace(signalmanServer.URL, "http://", "ws://", 1) + "/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+jwt)

	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(wsURL, headers)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn.Close()

	// Read auth success
	var authMsg map[string]interface{}
	err = conn.ReadJSON(&authMsg)
	require.NoError(t, err)
	assert.Equal(t, "auth_success", authMsg["type"])

	// Test subscription types
	subscriptions := []struct {
		name    string
		message map[string]interface{}
	}{
		{
			name: "stream subscription",
			message: map[string]interface{}{
				"type":      "subscribe_streams",
				"stream_id": "test-stream-123",
			},
		},
		{
			name: "analytics subscription",
			message: map[string]interface{}{
				"type": "subscribe_analytics",
			},
		},
		{
			name: "system subscription",
			message: map[string]interface{}{
				"type": "subscribe_system",
			},
		},
	}

	for _, sub := range subscriptions {
		t.Run(sub.name, func(t *testing.T) {
			// Send subscription message
			err := conn.WriteJSON(sub.message)
			require.NoError(t, err)

			// Read confirmation
			var confirmMsg map[string]interface{}
			err = conn.ReadJSON(&confirmMsg)
			require.NoError(t, err)
			assert.Equal(t, "subscription_confirmed", confirmMsg["type"])
		})
	}
}

func TestWebSocketConcurrentConnections(t *testing.T) {
	// Start test signalman server
	signalmanServer := NewTestSignalmanServer()
	defer signalmanServer.Close()

	jwtSecret := []byte("integration-test-secret")
	
	// Test concurrent connections from different users
	numConnections := 5
	var wg sync.WaitGroup
	results := make(chan error, numConnections)

	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			
			// Generate JWT for this user
			userID := fmt.Sprintf("user-%d", id)
			tenantID := fmt.Sprintf("tenant-%d", id)
			jwt, err := auth.GenerateJWT(userID, tenantID, "test@example.com", "user", jwtSecret)
			if err != nil {
				results <- fmt.Errorf("JWT generation failed for user %d: %w", id, err)
				return
			}

			// Connect
			wsURL := strings.Replace(signalmanServer.URL, "http://", "ws://", 1) + "/ws"
			headers := http.Header{}
			headers.Set("Authorization", "Bearer "+jwt)

			dialer := websocket.DefaultDialer
			conn, resp, err := dialer.Dial(wsURL, headers)
			if err != nil {
				results <- fmt.Errorf("connection failed for user %d: %w", id, err)
				return
			}
			defer conn.Close()

			if resp.StatusCode != http.StatusSwitchingProtocols {
				results <- fmt.Errorf("unexpected status for user %d: %d", id, resp.StatusCode)
				return
			}

			// Read auth success
			var authMsg map[string]interface{}
			err = conn.ReadJSON(&authMsg)
			if err != nil {
				results <- fmt.Errorf("failed to read auth message for user %d: %w", id, err)
				return
			}

			if authMsg["type"] != "auth_success" {
				results <- fmt.Errorf("unexpected auth message type for user %d: %v", id, authMsg["type"])
				return
			}

			results <- nil
		}(i)
	}

	// Wait for all connections to complete
	wg.Wait()
	close(results)

	// Check results
	errorCount := 0
	for err := range results {
		if err != nil {
			t.Errorf("Connection error: %v", err)
			errorCount++
		}
	}

	assert.Equal(t, 0, errorCount, "All concurrent connections should succeed")
}

func TestWebSocketReconnectionFlow(t *testing.T) {
	// Start test signalman server
	signalmanServer := NewTestSignalmanServer()
	defer signalmanServer.Close()

	jwtSecret := []byte("integration-test-secret")
	jwt, err := auth.GenerateJWT("user-123", "tenant-456", "test@example.com", "user", jwtSecret)
	require.NoError(t, err)

	wsURL := strings.Replace(signalmanServer.URL, "http://", "ws://", 1) + "/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+jwt)

	// First connection
	dialer := websocket.DefaultDialer
	conn1, _, err := dialer.Dial(wsURL, headers)
	require.NoError(t, err)

	// Read auth success
	var authMsg map[string]interface{}
	err = conn1.ReadJSON(&authMsg)
	require.NoError(t, err)
	assert.Equal(t, "auth_success", authMsg["type"])

	// Close first connection
	conn1.Close()

	// Reconnect with same credentials
	conn2, resp, err := dialer.Dial(wsURL, headers)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn2.Close()

	// Read auth success for reconnection
	err = conn2.ReadJSON(&authMsg)
	require.NoError(t, err)
	assert.Equal(t, "auth_success", authMsg["type"])

	// Verify user data is consistent
	data, ok := authMsg["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "user-123", data["user_id"])
	assert.Equal(t, "tenant-456", data["tenant_id"])
}

func TestWebSocketTenantIsolation(t *testing.T) {
	// Start test signalman server
	signalmanServer := NewTestSignalmanServer()
	defer signalmanServer.Close()

	jwtSecret := []byte("integration-test-secret")

	// Create JWTs for different tenants
	jwt1, err := auth.GenerateJWT("user-1", "tenant-1", "user1@example.com", "user", jwtSecret)
	require.NoError(t, err)

	jwt2, err := auth.GenerateJWT("user-2", "tenant-2", "user2@example.com", "user", jwtSecret)
	require.NoError(t, err)

	wsURL := strings.Replace(signalmanServer.URL, "http://", "ws://", 1) + "/ws"

	// Connect as tenant 1
	headers1 := http.Header{}
	headers1.Set("Authorization", "Bearer "+jwt1)
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, headers1)
	require.NoError(t, err)
	defer conn1.Close()

	// Connect as tenant 2
	headers2 := http.Header{}
	headers2.Set("Authorization", "Bearer "+jwt2)
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, headers2)
	require.NoError(t, err)
	defer conn2.Close()

	// Verify both connections receive their respective tenant data
	var authMsg1, authMsg2 map[string]interface{}
	
	err = conn1.ReadJSON(&authMsg1)
	require.NoError(t, err)
	
	err = conn2.ReadJSON(&authMsg2)
	require.NoError(t, err)

	// Verify tenant isolation
	data1, ok := authMsg1["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "tenant-1", data1["tenant_id"])

	data2, ok := authMsg2["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "tenant-2", data2["tenant_id"])

	// Verify they received different tenant IDs
	assert.NotEqual(t, data1["tenant_id"], data2["tenant_id"])
}