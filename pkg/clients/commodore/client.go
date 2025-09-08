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
	"frameworks/pkg/cache"
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
	cache        *cache.Cache
}

// Config represents the configuration for the Commodore client
type Config struct {
	BaseURL              string
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
	Cache                *cache.Cache
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
		cache:        config.Cache,
	}
}

// ValidateStreamKey validates a stream key against Commodore
func (c *Client) ValidateStreamKey(ctx context.Context, streamKey string) (*commodore.ValidateStreamKeyResponse, error) {
	endpoint := fmt.Sprintf("/validate-stream-key/%s", url.PathEscape(streamKey))
	url := c.baseURL + endpoint

	// Cache key
	if c.cache != nil {
		if v, ok, _ := c.cache.Get(ctx, "commodore:validate:"+streamKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.validateStreamKeyNoCache(ctx, url)
			if err != nil || !resp.Valid {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*commodore.ValidateStreamKeyResponse), nil
		}
	}
	return c.validateStreamKeyNoCache(ctx, url)
}

func (c *Client) validateStreamKeyNoCache(ctx context.Context, url string) (*commodore.ValidateStreamKeyResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Use user's JWT from context if available, otherwise fall back to service token
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			req.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
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

	if c.cache != nil {
		if v, ok, _ := c.cache.Get(ctx, "commodore:resolve:"+playbackID, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.resolvePlaybackIDNoCache(ctx, url)
			if err != nil {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*commodore.ResolvePlaybackIDResponse), nil
		}
	}
	return c.resolvePlaybackIDNoCache(ctx, url)
}

func (c *Client) resolvePlaybackIDNoCache(ctx context.Context, url string) (*commodore.ResolvePlaybackIDResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Use user's JWT from context if available, otherwise fall back to service token
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			req.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
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
	// Use user's JWT from context if available, otherwise fall back to service token
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			req.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
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

// UpdateStream updates an existing stream
func (c *Client) UpdateStream(ctx context.Context, userToken, streamID string, req *commodore.UpdateStreamRequest) (*models.Stream, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update stream request: %w", err)
	}

	url := fmt.Sprintf("%s/streams/%s", c.baseURL, url.PathEscape(streamID))
	httpReq, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to update stream: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to update stream with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to update stream: %s", errorResp.Error)
	}

	var stream models.Stream
	if err := json.Unmarshal(body, &stream); err != nil {
		return nil, fmt.Errorf("failed to parse stream response: %w", err)
	}

	return &stream, nil
}

// RefreshStreamKey generates a new stream key for an existing stream
func (c *Client) RefreshStreamKey(ctx context.Context, userToken, streamID string) (*models.Stream, error) {
	url := fmt.Sprintf("%s/streams/%s/refresh-key", c.baseURL, url.PathEscape(streamID))
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh stream key: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to refresh stream key with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to refresh stream key: %s", errorResp.Error)
	}

	var stream models.Stream
	if err := json.Unmarshal(body, &stream); err != nil {
		return nil, fmt.Errorf("failed to parse stream response: %w", err)
	}

	return &stream, nil
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

// GetClips retrieves all clips for the authenticated user
func (c *Client) GetClips(ctx context.Context, userToken string, streamID *string) (*commodore.ClipsListResponse, error) {
	urlBuilder := c.baseURL + "/clips"

	// Add optional stream_id filter
	if streamID != nil && *streamID != "" {
		params := url.Values{}
		params.Add("stream_id", *streamID)
		urlBuilder += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", urlBuilder, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to get clips with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to get clips: %s", errorResp.Error)
	}

	var clipsResp commodore.ClipsListResponse
	if err := json.Unmarshal(body, &clipsResp); err != nil {
		return nil, fmt.Errorf("failed to parse clips response: %w", err)
	}

	return &clipsResp, nil
}

// GetClip retrieves a specific clip by ID
func (c *Client) GetClip(ctx context.Context, userToken string, clipID string) (*commodore.ClipFullResponse, error) {
	url := fmt.Sprintf("%s/clips/%s", c.baseURL, url.PathEscape(clipID))
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to get clip with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to get clip: %s", errorResp.Error)
	}

	var clip commodore.ClipFullResponse
	if err := json.Unmarshal(body, &clip); err != nil {
		return nil, fmt.Errorf("failed to parse clip response: %w", err)
	}

	return &clip, nil
}

// GetClipURLs retrieves viewing URLs for a specific clip
func (c *Client) GetClipURLs(ctx context.Context, userToken string, clipID string) (*commodore.ClipViewingURLs, error) {
	url := fmt.Sprintf("%s/clips/%s/urls", c.baseURL, url.PathEscape(clipID))
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to get clip URLs with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to get clip URLs: %s", errorResp.Error)
	}

	var clipURLs commodore.ClipViewingURLs
	if err := json.Unmarshal(body, &clipURLs); err != nil {
		return nil, fmt.Errorf("failed to parse clip URLs response: %w", err)
	}

	return &clipURLs, nil
}

// DeleteClip deletes a clip by ID
func (c *Client) DeleteClip(ctx context.Context, userToken string, clipID string) error {
	url := fmt.Sprintf("%s/clips/%s", c.baseURL, url.PathEscape(clipID))
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return fmt.Errorf("failed to delete clip with status %d: %s", resp.StatusCode, string(body))
		}
		return fmt.Errorf("failed to delete clip: %s", errorResp.Error)
	}

	return nil
}

// === DVR MANAGEMENT ===

// StartDVR starts a DVR recording via Commodore -> Foghorn proxy
func (c *Client) StartDVR(ctx context.Context, userToken string, req *commodore.StartDVRRequest) (*commodore.StartDVRResponse, error) {
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal DVR start request: %w", err)
	}
	url := c.baseURL + "/dvr/start"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}
	var out commodore.StartDVRResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &out, nil
}

// StopDVR stops an active DVR recording via Commodore
func (c *Client) StopDVR(ctx context.Context, userToken, dvrHash string) error {
	url := c.baseURL + "/dvr/stop"
	body := map[string]string{"dvr_hash": dvrHash}
	b, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(b))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}
	return nil
}

// GetRecordingConfig fetches a stream's recording configuration
func (c *Client) GetRecordingConfig(ctx context.Context, userToken, internalName string) (*commodore.RecordingConfig, error) {
	url := fmt.Sprintf("%s/streams/%s/recording-config", c.baseURL, url.PathEscape(internalName))
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}
	var cfg commodore.RecordingConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &cfg, nil
}

// SetRecordingConfig updates a stream's recording configuration
func (c *Client) SetRecordingConfig(ctx context.Context, userToken, internalName string, cfg commodore.RecordingConfig) (*commodore.RecordingConfig, error) {
	url := fmt.Sprintf("%s/streams/%s/recording-config", c.baseURL, url.PathEscape(internalName))
	body, _ := json.Marshal(cfg)
	httpReq, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(respBody, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(respBody))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}
	var out commodore.RecordingConfig
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &out, nil
}

// ListDVRRequests lists DVR recordings via Commodore proxy
func (c *Client) ListDVRRequests(ctx context.Context, userToken string, internalName *string, status *string, page, limit *int) (*commodore.DVRListResponse, error) {
	v := url.Values{}
	if internalName != nil && *internalName != "" {
		v.Set("internal_name", *internalName)
	}
	if status != nil && *status != "" {
		v.Set("status", *status)
	}
	if page != nil {
		v.Set("page", fmt.Sprintf("%d", *page))
	}
	if limit != nil {
		v.Set("limit", fmt.Sprintf("%d", *limit))
	}
	endpoint := c.baseURL + "/dvr/requests"
	if len(v) > 0 {
		endpoint += "?" + v.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}
	var out commodore.DVRListResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &out, nil
}

// GetDVRStatus fetches DVR status details for a given hash via Commodore
func (c *Client) GetDVRStatus(ctx context.Context, userToken, dvrHash string) (*commodore.DVRInfo, error) {
	endpoint := fmt.Sprintf("%s/dvr/status/%s", c.baseURL, url.PathEscape(dvrHash))
	httpReq, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}
	var out commodore.DVRInfo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &out, nil
}

// CreateAPIToken creates a new API token for the authenticated user
func (c *Client) CreateAPIToken(ctx context.Context, userToken string, req *models.CreateAPITokenRequest) (*commodore.CreateAPITokenResponse, error) {
	url := c.baseURL + "/developer/tokens"

	// Prepare request body
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}

	var tokenResp commodore.CreateAPITokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &tokenResp, nil
}

// GetAPITokens retrieves all API tokens for the authenticated user
func (c *Client) GetAPITokens(ctx context.Context, userToken string) (*commodore.APITokenListResponse, error) {
	url := c.baseURL + "/developer/tokens"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}

	var tokensResp commodore.APITokenListResponse
	if err := json.Unmarshal(body, &tokensResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &tokensResp, nil
}

// RevokeAPIToken revokes an API token by ID
func (c *Client) RevokeAPIToken(ctx context.Context, userToken, tokenID string) (*commodore.RevokeAPITokenResponse, error) {
	url := c.baseURL + "/developer/tokens/" + url.PathEscape(tokenID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Commodore returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Commodore returned error: %s", errorResp.Error)
	}

	var revokeResp commodore.RevokeAPITokenResponse
	if err := json.Unmarshal(body, &revokeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &revokeResp, nil
}

// === STREAM KEYS MANAGEMENT ===

// GetStreamKeys retrieves all stream keys for a specific stream
func (c *Client) GetStreamKeys(ctx context.Context, userToken, streamID string) (*commodore.StreamKeysResponse, error) {
	url := fmt.Sprintf("%s/streams/%s/keys", c.baseURL, url.PathEscape(streamID))
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to get stream keys with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to get stream keys: %s", errorResp.Error)
	}

	var keysResp commodore.StreamKeysResponse
	if err := json.Unmarshal(body, &keysResp); err != nil {
		return nil, fmt.Errorf("failed to parse stream keys response: %w", err)
	}

	return &keysResp, nil
}

// CreateStreamKey creates a new stream key for a specific stream
func (c *Client) CreateStreamKey(ctx context.Context, userToken, streamID string, req *commodore.CreateStreamKeyRequest) (*commodore.StreamKeyResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/streams/%s/keys", c.baseURL, url.PathEscape(streamID))
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to create stream key with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to create stream key: %s", errorResp.Error)
	}

	var keyResp commodore.StreamKeyResponse
	if err := json.Unmarshal(body, &keyResp); err != nil {
		return nil, fmt.Errorf("failed to parse stream key response: %w", err)
	}

	return &keyResp, nil
}

// DeactivateStreamKey deactivates a specific stream key
func (c *Client) DeactivateStreamKey(ctx context.Context, userToken, streamID, keyID string) (*commodore.SuccessResponse, error) {
	url := fmt.Sprintf("%s/streams/%s/keys/%s", c.baseURL, url.PathEscape(streamID), url.PathEscape(keyID))
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to deactivate stream key with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to deactivate stream key: %s", errorResp.Error)
	}

	var successResp commodore.SuccessResponse
	if err := json.Unmarshal(body, &successResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &successResp, nil
}

// === RECORDINGS MANAGEMENT ===

// GetRecordings retrieves all recordings for the authenticated user
func (c *Client) GetRecordings(ctx context.Context, userToken string, streamID *string) (*commodore.RecordingsResponse, error) {
	urlBuilder := c.baseURL + "/recordings"

	// Add optional stream_id filter
	if streamID != nil && *streamID != "" {
		params := url.Values{}
		params.Add("stream_id", *streamID)
		urlBuilder += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", urlBuilder, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to get recordings with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to get recordings: %s", errorResp.Error)
	}

	var recordingsResp commodore.RecordingsResponse
	if err := json.Unmarshal(body, &recordingsResp); err != nil {
		return nil, fmt.Errorf("failed to parse recordings response: %w", err)
	}

	return &recordingsResp, nil
}

// === VIEWER ENDPOINT RESOLUTION ===

// ResolveViewerEndpoint calls Commodore to resolve viewer endpoints (which then calls Foghorn)
func (c *Client) ResolveViewerEndpoint(ctx context.Context, contentType, contentID string, viewerIP *string) ([]commodore.ViewerEndpoint, error) {
	req := commodore.ViewerEndpointRequest{
		ContentType: contentType,
		ContentID:   contentID,
	}

	if viewerIP != nil {
		req.ViewerIP = *viewerIP
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/viewer/resolve-endpoint"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// Use service token for viewer endpoint resolution
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Commodore: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp commodore.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("failed to resolve viewer endpoints with status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("failed to resolve viewer endpoints: %s", errorResp.Error)
	}

	var viewerResp commodore.ViewerEndpointResponse
	if err := json.Unmarshal(body, &viewerResp); err != nil {
		return nil, fmt.Errorf("failed to parse viewer endpoint response: %w", err)
	}

	// Combine primary and fallbacks into a single slice
	endpoints := []commodore.ViewerEndpoint{viewerResp.Primary}
	endpoints = append(endpoints, viewerResp.Fallbacks...)

	return endpoints, nil
}

// GetStreamMeta fetches Mist JSON meta via Commodore proxy with optional target params
func (c *Client) GetStreamMeta(ctx context.Context, streamKey string, includeRaw bool, targetBaseURL, targetNodeID *string) (*commodore.StreamMetaResponse, error) {
	v := url.Values{}
	if targetBaseURL != nil && *targetBaseURL != "" {
		v.Set("target_base_url", *targetBaseURL)
	}
	if targetNodeID != nil && *targetNodeID != "" {
		v.Set("target_node_id", *targetNodeID)
	}
	if includeRaw {
		v.Set("include_raw", "1")
	}
	endpoint := fmt.Sprintf("%s/streams/%s/meta", c.baseURL, url.PathEscape(streamKey))
	if len(v) > 0 {
		endpoint += "?" + v.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
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
	var out commodore.StreamMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// ResolveInternalName resolves an internal_name to tenant_id and user_id for enrichment
func (c *Client) ResolveInternalName(ctx context.Context, internalName string) (*commodore.InternalNameResponse, error) {
	endpoint := fmt.Sprintf("/resolve-internal-name/%s", url.PathEscape(internalName))
	url := c.baseURL + endpoint

	if c.cache != nil {
		if v, ok, _ := c.cache.Get(ctx, "commodore:internal:"+internalName, func(ctx context.Context, _ string) (interface{}, bool, error) {
			resp, err := c.resolveInternalNameNoCache(ctx, url)
			if err != nil {
				return nil, false, err
			}
			return resp, true, nil
		}); ok {
			return v.(*commodore.InternalNameResponse), nil
		}
	}
	return c.resolveInternalNameNoCache(ctx, url)
}

func (c *Client) resolveInternalNameNoCache(ctx context.Context, url string) (*commodore.InternalNameResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Use service token for internal name resolution
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

	var response commodore.InternalNameResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}
