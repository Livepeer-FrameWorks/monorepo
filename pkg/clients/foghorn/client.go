package foghorn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"frameworks/pkg/clients"
	"frameworks/pkg/logging"
)

// Client represents a Foghorn API client
type Client struct {
	baseURL      string
	httpClient   *http.Client
	serviceToken string
	logger       logging.Logger
	retryConfig  clients.RetryConfig
}

// Config represents the configuration for the Foghorn client
type Config struct {
	BaseURL              string
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
}

// NewClient creates a new Foghorn client
func NewClient(config Config) *Client {
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	retryConfig := clients.DefaultRetryConfig()
	if config.RetryConfig != nil {
		retryConfig = *config.RetryConfig
	}

	// Add circuit breaker if configured
	if config.CircuitBreakerConfig != nil {
		retryConfig.CircuitBreaker = clients.NewCircuitBreaker(*config.CircuitBreakerConfig)
	}

	return &Client{
		baseURL: config.BaseURL,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		serviceToken: config.ServiceToken,
		logger:       config.Logger,
		retryConfig:  retryConfig,
	}
}

// NodeUpdateRequest represents a node update request to Foghorn
type NodeUpdateRequest struct {
	NodeID    string                 `json:"node_id"`
	BaseURL   string                 `json:"base_url"`
	IsHealthy bool                   `json:"is_healthy"`
	Latitude  *float64               `json:"latitude,omitempty"`
	Longitude *float64               `json:"longitude,omitempty"`
	Location  string                 `json:"location,omitempty"`
	EventType string                 `json:"event_type"`
	Timestamp int64                  `json:"timestamp"`
	Metrics   map[string]interface{} `json:"metrics,omitempty"`
}

// NodeShutdownRequest represents a node shutdown notification to Foghorn
// Must match exactly what Foghorn expects: node_id, type, timestamp, reason, details
type NodeShutdownRequest struct {
	NodeID    string                 `json:"node_id"`
	Type      string                 `json:"type"`
	Timestamp int64                  `json:"timestamp"`
	Reason    string                 `json:"reason"`
	Details   map[string]interface{} `json:"details"`
}

// StreamHealthRequest represents a stream health update to Foghorn
type StreamHealthRequest struct {
	NodeID       string                 `json:"node_id"`
	StreamName   string                 `json:"stream_name"`
	InternalName string                 `json:"internal_name"`
	IsHealthy    bool                   `json:"is_healthy"`
	Timestamp    int64                  `json:"timestamp"`
	Details      map[string]interface{} `json:"details,omitempty"`
}

// UpdateNode sends a node update to Foghorn
func (c *Client) UpdateNode(ctx context.Context, req *NodeUpdateRequest) error {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal node update: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/node/update", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// Use user's JWT from context if available, otherwise fall back to service token
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to send node update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// NotifyShutdown sends a shutdown notification to Foghorn
func (c *Client) NotifyShutdown(ctx context.Context, req *NodeShutdownRequest) error {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal shutdown notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/node/shutdown", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// Use user's JWT from context if available, otherwise fall back to service token
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to send shutdown notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.logger.WithFields(logging.Fields{
			"status": resp.StatusCode,
			"body":   string(body),
		}).Warn("Foghorn shutdown notification failed")
	}

	return nil
}

// UpdateStreamHealth sends a stream health update to Foghorn
func (c *Client) UpdateStreamHealth(ctx context.Context, req *StreamHealthRequest) error {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal stream health update: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/stream/health", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// Use user's JWT from context if available, otherwise fall back to service token
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to send stream health update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}
