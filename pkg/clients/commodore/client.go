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
	"frameworks/pkg/models"
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
	endpoint := fmt.Sprintf("/validate-stream-key/%s", url.PathEscape(streamKey))
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
	endpoint := fmt.Sprintf("/resolve-playback-id/%s", url.PathEscape(playbackID))
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

	url := fmt.Sprintf("%s/%s", c.baseURL, endpoint)
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

// Authentication Methods

// Login authenticates a user with email and password
func (c *Client) Login(ctx context.Context, email, password string) (*commodore.AuthResponse, error) {
	loginReq := commodore.LoginRequest{
		Email:    email,
		Password: password,
	}

	jsonData, err := json.Marshal(loginReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal login request: %w", err)
	}

	url := c.baseURL + "/login"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to login: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("login failed with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("login failed: %s", errorResp.Error)
	}

	var authResponse commodore.AuthResponse
	if err := json.Unmarshal(body, &authResponse); err != nil {
		return nil, fmt.Errorf("failed to parse auth response: %w", err)
	}

	return &authResponse, nil
}

// Register creates a new user account
func (c *Client) Register(ctx context.Context, email, password, firstName, lastName string) (*commodore.AuthResponse, error) {
	registerReq := commodore.RegisterRequest{
		Email:     email,
		Password:  password,
		FirstName: firstName,
		LastName:  lastName,
	}

	jsonData, err := json.Marshal(registerReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal register request: %w", err)
	}

	url := c.baseURL + "/register"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to register: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("registration failed with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("registration failed: %s", errorResp.Error)
	}

	var authResponse commodore.AuthResponse
	if err := json.Unmarshal(body, &authResponse); err != nil {
		return nil, fmt.Errorf("failed to parse auth response: %w", err)
	}

	return &authResponse, nil
}

// GetMe gets the current user profile (requires authentication)
func (c *Client) GetMe(ctx context.Context, userToken string) (*models.User, error) {
	url := c.baseURL + "/me"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to get user profile with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to get user profile: %s", errorResp.Error)
	}

	var user models.User
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("failed to parse user response: %w", err)
	}

	return &user, nil
}

// Stream Management Methods

// GetStreams gets all streams for the authenticated user
func (c *Client) GetStreams(ctx context.Context, userToken string) (*commodore.StreamsResponse, error) {
	url := c.baseURL + "/streams"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to get streams with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to get streams: %s", errorResp.Error)
	}

	var streams commodore.StreamsResponse
	if err := json.Unmarshal(body, &streams); err != nil {
		return nil, fmt.Errorf("failed to parse streams response: %w", err)
	}

	return &streams, nil
}

// CreateStream creates a new stream
func (c *Client) CreateStream(ctx context.Context, userToken string, req *commodore.CreateStreamRequest) (*commodore.StreamResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal stream request: %w", err)
	}

	url := c.baseURL + "/streams"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to create stream with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to create stream: %s", errorResp.Error)
	}

	var stream commodore.StreamResponse
	if err := json.Unmarshal(body, &stream); err != nil {
		return nil, fmt.Errorf("failed to parse stream response: %w", err)
	}

	return &stream, nil
}

// DeleteStream deletes a stream by ID
func (c *Client) DeleteStream(ctx context.Context, userToken, streamID string) error {
	url := fmt.Sprintf("%s/streams/%s", c.baseURL, url.PathEscape(streamID))
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to delete stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return fmt.Errorf("failed to delete stream with status %d: %s", resp.StatusCode, string(body))
		}
		return fmt.Errorf("failed to delete stream: %s", errorResp.Error)
	}

	return nil
}

// Clip Management Methods

// CreateClip creates a new clip from a stream
func (c *Client) CreateClip(ctx context.Context, userToken string, req *commodore.CreateClipRequest) (*commodore.ClipResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal clip request: %w", err)
	}

	url := c.baseURL + "/clips"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clip: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to create clip with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to create clip: %s", errorResp.Error)
	}

	var clip commodore.ClipResponse
	if err := json.Unmarshal(body, &clip); err != nil {
		return nil, fmt.Errorf("failed to parse clip response: %w", err)
	}

	return &clip, nil
}
