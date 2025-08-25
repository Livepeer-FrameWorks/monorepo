package foghorn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	fapi "frameworks/pkg/api/foghorn"
	"frameworks/pkg/clients"
	"frameworks/pkg/logging"
	"frameworks/pkg/validation"
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

// NodeShutdownRequest represents a node shutdown notification to Foghorn
// Must match exactly what Foghorn expects: node_id, type, timestamp, reason, details
type NodeShutdownRequest struct {
	NodeID    string                          `json:"node_id"`
	Type      string                          `json:"type"`
	Timestamp int64                           `json:"timestamp"`
	Reason    string                          `json:"reason"`
	Details   *validation.FoghornNodeShutdown `json:"details"`
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

// CreateClip sends a typed clip creation request to Foghorn
func (c *Client) CreateClip(ctx context.Context, req *fapi.CreateClipRequest) (*fapi.CreateClipResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal create clip: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/clips/create", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}
	var out fapi.CreateClipResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// GetClips gets a paginated list of clips for a tenant
func (c *Client) GetClips(ctx context.Context, tenantID string, page, limit int) (*fapi.ClipsListResponse, error) {
	url := fmt.Sprintf("%s/clips?tenant_id=%s&page=%d&limit=%d", c.baseURL, tenantID, page, limit)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	var out fapi.ClipsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// GetClip gets a specific clip by hash
func (c *Client) GetClip(ctx context.Context, clipHash, tenantID string) (*fapi.ClipInfo, error) {
	url := fmt.Sprintf("%s/clips/%s?tenant_id=%s", c.baseURL, clipHash, tenantID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	var out fapi.ClipInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// GetClipNode gets node information for clip viewing
func (c *Client) GetClipNode(ctx context.Context, clipHash, tenantID string) (*fapi.ClipNodeInfo, error) {
	url := fmt.Sprintf("%s/clips/%s/node?tenant_id=%s", c.baseURL, clipHash, tenantID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	var out fapi.ClipNodeInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// DeleteClip soft-deletes a clip
func (c *Client) DeleteClip(ctx context.Context, clipHash, tenantID string) error {
	url := fmt.Sprintf("%s/clips/%s?tenant_id=%s", c.baseURL, clipHash, tenantID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// StartDVRRecording sends a DVR recording request to Foghorn
func (c *Client) StartDVRRecording(ctx context.Context, req *fapi.StartDVRRequest) (*fapi.StartDVRResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal DVR start request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/dvr/start", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	var out fapi.StartDVRResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// StopDVRRecording requests stopping an active DVR recording
func (c *Client) StopDVRRecording(ctx context.Context, dvrHash, tenantID string) error {
	if dvrHash == "" || tenantID == "" {
		return fmt.Errorf("dvrHash and tenantID are required")
	}
	endpoint := fmt.Sprintf("%s/dvr/stop/%s?tenant_id=%s", c.baseURL, url.PathEscape(dvrHash), url.QueryEscape(tenantID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// GetDVRStatus fetches DVR status by hash for a tenant
func (c *Client) GetDVRStatus(ctx context.Context, dvrHash, tenantID string) (*fapi.DVRInfo, error) {
	if dvrHash == "" || tenantID == "" {
		return nil, fmt.Errorf("dvrHash and tenantID are required")
	}
	endpoint := fmt.Sprintf("%s/dvr/status/%s?tenant_id=%s", c.baseURL, url.PathEscape(dvrHash), url.QueryEscape(tenantID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}
	var out fapi.DVRInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// ListDVRRecordings lists DVR recordings for a tenant, optionally filtered
func (c *Client) ListDVRRecordings(ctx context.Context, tenantID, internalName, status string, page, limit int) (*fapi.DVRListResponse, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenantID is required")
	}
	v := url.Values{}
	v.Set("tenant_id", tenantID)
	if internalName != "" {
		v.Set("internal_name", internalName)
	}
	if status != "" {
		v.Set("status", status)
	}
	if page > 0 {
		v.Set("page", fmt.Sprintf("%d", page))
	}
	if limit > 0 {
		v.Set("limit", fmt.Sprintf("%d", limit))
	}
	endpoint := fmt.Sprintf("%s/dvr/recordings?%s", c.baseURL, v.Encode())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}
	var out fapi.DVRListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// ResolveViewerEndpoint resolves viewer endpoints through Foghorn load balancing
func (c *Client) ResolveViewerEndpoint(ctx context.Context, req *fapi.ViewerEndpointRequest) (*fapi.ViewerEndpointResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal viewer endpoint request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/viewer/resolve-endpoint", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call foghorn: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Foghorn error (%d): %s", resp.StatusCode, string(body))
	}

	var out fapi.ViewerEndpointResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}
