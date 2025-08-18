package quartermaster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"frameworks/pkg/api/quartermaster"
	"frameworks/pkg/clients"
	"frameworks/pkg/logging"
)

// Client represents a Quartermaster API client
type Client struct {
	baseURL      string
	httpClient   *http.Client
	serviceToken string
	logger       logging.Logger
	retryConfig  clients.RetryConfig
}

// Config represents the configuration for the Quartermaster client
type Config struct {
	BaseURL              string
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
}

// NewClient creates a new Quartermaster API client
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

// ValidateTenant validates a tenant and user combination
func (c *Client) ValidateTenant(ctx context.Context, req *quartermaster.ValidateTenantRequest) (*quartermaster.ValidateTenantResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/api/tenants/validate"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// Note: ValidateTenant is typically a public endpoint, no auth required

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	var validation quartermaster.ValidateTenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&validation); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &validation, nil
}

// ResolveTenant resolves a tenant by domain or subdomain
func (c *Client) ResolveTenant(ctx context.Context, req *quartermaster.ResolveTenantRequest) (*quartermaster.ResolveTenantResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/tenant/resolve"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if c.logger != nil {
			c.logger.WithFields(logging.Fields{
				"status_code": resp.StatusCode,
				"response":    string(body),
			}).Error("Quartermaster tenant resolution failed")
		}
		return &quartermaster.ResolveTenantResponse{
			Error: fmt.Sprintf("tenant resolution failed with status %d", resp.StatusCode),
		}, nil
	}

	var resolution quartermaster.ResolveTenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolution); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &resolution, nil
}

// GetTenantRouting gets routing information for a tenant and stream
func (c *Client) GetTenantRouting(ctx context.Context, req *quartermaster.TenantRoutingRequest) (*quartermaster.TenantRoutingResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/tenant/routing"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster routing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("routing request failed: %s", string(body))
	}

	var routing quartermaster.TenantRoutingResponse
	if err := json.NewDecoder(resp.Body).Decode(&routing); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &routing, nil
}

// GetTenant retrieves tenant information by ID
func (c *Client) GetTenant(ctx context.Context, tenantID string) (*quartermaster.GetTenantResponse, error) {
	url := fmt.Sprintf("%s/api/tenants/%s", c.baseURL, url.PathEscape(tenantID))

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.serviceToken != "" {
		httpReq.Header.Set("X-Service-Token", c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if c.logger != nil {
			c.logger.WithFields(logging.Fields{
				"status_code": resp.StatusCode,
				"response":    string(body),
			}).Error("Quartermaster get tenant failed")
		}
		return &quartermaster.GetTenantResponse{
			Error: fmt.Sprintf("get tenant failed with status %d", resp.StatusCode),
		}, nil
	}

	var response quartermaster.GetTenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// CreateTenant creates a new tenant
func (c *Client) CreateTenant(ctx context.Context, req *quartermaster.CreateTenantRequest) (*quartermaster.CreateTenantResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/tenants"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.serviceToken != "" {
		httpReq.Header.Set("X-Service-Token", c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		if c.logger != nil {
			c.logger.WithFields(logging.Fields{
				"status_code": resp.StatusCode,
				"response":    string(body),
			}).Error("Quartermaster tenant creation failed")
		}
		return &quartermaster.CreateTenantResponse{
			Error: fmt.Sprintf("tenant creation failed with status %d: %s", resp.StatusCode, string(body)),
		}, nil
	}

	var createResp quartermaster.CreateTenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &createResp, nil
}
