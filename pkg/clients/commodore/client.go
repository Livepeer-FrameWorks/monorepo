package commodore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"frameworks/pkg/api/commodore"
	"frameworks/pkg/clients"
	"frameworks/pkg/logging"
)

// Client represents a Commodore API client
type Client struct {
	baseURL      string
	httpClient   *http.Client
	serviceToken string
	logger       logging.Logger
	retryConfig  clients.RetryConfig
}

// Config represents the configuration for the Commodore client
type Config struct {
	BaseURL              string
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
}

// NewClient creates a new Commodore API client
func NewClient(config Config) *Client {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	retryConfig := clients.DefaultRetryConfig()
	if config.RetryConfig != nil {
		retryConfig = *config.RetryConfig
	}

	// Add circuit breaker if configured
	if config.CircuitBreakerConfig != nil {
		retryConfig.CircuitBreaker = clients.NewCircuitBreaker(*config.CircuitBreakerConfig)
	}

	httpClient := &http.Client{
		Timeout: config.Timeout,
	}

	return &Client{
		baseURL:      config.BaseURL,
		httpClient:   httpClient,
		serviceToken: config.ServiceToken,
		logger:       config.Logger,
		retryConfig:  retryConfig,
	}
}

// ValidateStreamKey validates a stream key against Commodore
func (c *Client) ValidateStreamKey(ctx context.Context, streamKey string) (*commodore.ValidateStreamKeyResponse, error) {
	endpoint := fmt.Sprintf("/api/validate-stream-key/%s", url.PathEscape(streamKey))
	url := c.baseURL + endpoint

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var validation commodore.ValidateStreamKeyResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &validation); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		validation.Valid = true
	} else {
		// Parse error response
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			// Fallback to raw error message
			validation.Error = string(body)
		} else {
			validation.Error = errorResp.Error
		}
		validation.Valid = false
	}

	return &validation, nil
}

// ResolvePlaybackID resolves a playback ID to an internal stream name
func (c *Client) ResolvePlaybackID(ctx context.Context, playbackID string) (*commodore.ResolvePlaybackIDResponse, error) {
	endpoint := fmt.Sprintf("/api/resolve-playback-id/%s", url.PathEscape(playbackID))
	url := c.baseURL + endpoint

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var response commodore.ResolvePlaybackIDResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// ForwardStreamEvent forwards a stream event to Commodore
func (c *Client) ForwardStreamEvent(ctx context.Context, endpoint string, eventData *commodore.StreamEventRequest) error {
	jsonData, err := json.Marshal(eventData)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	url := fmt.Sprintf("%s/api/%s", c.baseURL, endpoint)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to forward event to Commodore: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		if c.logger != nil {
			c.logger.WithFields(logging.Fields{
				"status_code": resp.StatusCode,
				"response":    string(body),
				"endpoint":    endpoint,
			}).Error("Commodore returned error")
		}
		return fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ForwardStreamStart is a convenience method for forwarding stream start events
func (c *Client) ForwardStreamStart(ctx context.Context, req *commodore.StreamEventRequest) error {
	req.EventType = "push_rewrite_success"
	return c.ForwardStreamEvent(ctx, "stream-start", req)
}
