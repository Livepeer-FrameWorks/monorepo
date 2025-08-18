package purser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"frameworks/pkg/api/purser"
	"frameworks/pkg/clients"
	"frameworks/pkg/logging"
)

// Client represents a Purser API client
type Client struct {
	baseURL      string
	httpClient   *http.Client
	serviceToken string
	logger       logging.Logger
	retryConfig  clients.RetryConfig
}

// Config represents the configuration for the Purser client
type Config struct {
	BaseURL              string
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
}

// NewClient creates a new Purser API client
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

// GetTenantTierInfo retrieves tier information for a tenant
func (c *Client) GetTenantTierInfo(ctx context.Context, tenantID string) (*purser.TenantTierInfoResponse, error) {
	url := fmt.Sprintf("%s/api/tenants/%s/tier-info", c.baseURL, url.PathEscape(tenantID))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Service-Token", c.serviceToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Purser error (%d): %s", resp.StatusCode, string(body))
	}

	var tierInfo purser.TenantTierInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&tierInfo); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &tierInfo, nil
}

// CheckUserLimit checks if a tenant can add a new user
func (c *Client) CheckUserLimit(ctx context.Context, req *purser.CheckUserLimitRequest) (*purser.CheckUserLimitResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/tenants/%s/check-user-limit", c.baseURL, url.PathEscape(req.TenantID))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Service-Token", c.serviceToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	var checkResp purser.CheckUserLimitResponse
	if resp.StatusCode == http.StatusForbidden {
		checkResp.Allowed = false
		// Try to parse error message if available
		if body, err := io.ReadAll(resp.Body); err == nil {
			var errorResp purser.ErrorResponse
			if json.Unmarshal(body, &errorResp) == nil {
				checkResp.Error = errorResp.Error
			} else {
				checkResp.Error = "Tenant user limit reached"
			}
		}
		return &checkResp, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("user limit check failed: %s", string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(&checkResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &checkResp, nil
}

// SubmitBillingData submits billing/usage data to Purser
func (c *Client) SubmitBillingData(ctx context.Context, req *purser.BillingDataRequest) (*purser.BillingDataResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/tenants/%s/billing-data", c.baseURL, url.PathEscape(req.TenantID))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Service-Token", c.serviceToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	var billingResp purser.BillingDataResponse
	if err := json.NewDecoder(resp.Body).Decode(&billingResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &billingResp, nil
}

// GetTenantUsage retrieves usage data for a tenant
func (c *Client) GetTenantUsage(ctx context.Context, req *purser.TenantUsageRequest) (*purser.TenantUsageResponse, error) {
	url := fmt.Sprintf("%s/api/tenants/%s/usage?start_date=%s&end_date=%s",
		c.baseURL,
		url.PathEscape(req.TenantID),
		url.QueryEscape(req.StartDate),
		url.QueryEscape(req.EndDate))

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("X-Service-Token", c.serviceToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Purser error (%d): %s", resp.StatusCode, string(body))
	}

	var usageResp purser.TenantUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usageResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &usageResp, nil
}

// GetSubscription retrieves subscription information for a tenant
func (c *Client) GetSubscription(ctx context.Context, tenantID string) (*purser.GetSubscriptionResponse, error) {
	url := fmt.Sprintf("%s/api/tenants/%s/subscription", c.baseURL, url.PathEscape(tenantID))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Service-Token", c.serviceToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	var subscriptionResp purser.GetSubscriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&subscriptionResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &subscriptionResp, nil
}

// IngestUsage submits usage summaries to Purser
func (c *Client) IngestUsage(ctx context.Context, req *purser.UsageIngestRequest) (*purser.UsageIngestResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/usage/ingest", c.baseURL)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Service-Token", c.serviceToken)

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Purser error (%d): %s", resp.StatusCode, string(body))
	}

	var ingestResp purser.UsageIngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&ingestResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &ingestResp, nil
}
