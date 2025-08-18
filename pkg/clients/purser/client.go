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
	"frameworks/pkg/models"
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
	url := fmt.Sprintf("%s/billing/status", c.baseURL)

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
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Purser error (%d): %s", resp.StatusCode, string(body))
	}

	// Parse billing status response and extract tier info
	var billingStatus models.BillingStatus
	if err := json.NewDecoder(resp.Body).Decode(&billingStatus); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert to TenantTierInfoResponse format
	tierInfo := &models.TenantTierInfo{
		Tenant: models.Tenant{
			ID:   billingStatus.TenantID,
			Name: billingStatus.TenantID, // Use ID as name for now
			// Other fields not available in billing status
		},
		Subscription:  &billingStatus.Subscription,
		Tier:          &billingStatus.Tier,
		ClusterAccess: []models.TenantClusterAccess{}, // Not available in billing status
	}

	return tierInfo, nil
}

// CheckUserLimit checks if a tenant can add a new user
// User limits are now checked via the billing/status endpoint
func (c *Client) CheckUserLimit(ctx context.Context, req *purser.CheckUserLimitRequest) (*purser.CheckUserLimitResponse, error) {
	url := fmt.Sprintf("%s/billing/status", c.baseURL)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

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
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("billing status check failed: %s", string(body))
	}

	// Parse billing status to extract user limits
	var billingStatus models.BillingStatus
	if err := json.NewDecoder(resp.Body).Decode(&billingStatus); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Extract user limits from tier features
	var maxUsers int = 10 // Default limit
	if billingStatus.Tier.Features != nil {
		if userLimit, exists := billingStatus.Tier.Features["max_users"]; exists {
			if limit, ok := userLimit.(float64); ok {
				maxUsers = int(limit)
			}
		}
	}

	// TODO: Get current user count from a different endpoint or cache
	// For now, assume we're within limits
	checkResp := &purser.CheckUserLimitResponse{
		Allowed:      true,
		CurrentUsers: 0, // Would need to query actual user count
		MaxUsers:     maxUsers,
	}

	return checkResp, nil
}

// SubmitBillingData submits billing/usage data to Purser
func (c *Client) SubmitBillingData(ctx context.Context, req *purser.BillingDataRequest) (*purser.BillingDataResponse, error) {
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
	// This is a service-to-service call, use service token
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

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
	url := fmt.Sprintf("%s/usage/records?tenant_id=%s&start_date=%s&end_date=%s",
		c.baseURL,
		url.QueryEscape(req.TenantID),
		url.QueryEscape(req.StartDate),
		url.QueryEscape(req.EndDate))

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// This is a service-to-service call, use service token
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

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
	url := fmt.Sprintf("%s/billing/status", c.baseURL)

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
		return nil, fmt.Errorf("failed to call Purser: %w", err)
	}
	defer resp.Body.Close()

	// Parse billing status to extract subscription info
	var billingStatus models.BillingStatus
	if err := json.NewDecoder(resp.Body).Decode(&billingStatus); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert to GetSubscriptionResponse format
	subscriptionResp := &purser.GetSubscriptionResponse{
		Subscription: &purser.SubscriptionInfo{
			ID:            billingStatus.Subscription.ID,
			TenantID:      billingStatus.Subscription.TenantID,
			TierID:        billingStatus.Subscription.TierID,
			Status:        billingStatus.Subscription.Status,
			BillingPeriod: billingStatus.Tier.BillingPeriod,
			StartDate:     billingStatus.Subscription.StartedAt.Format("2006-01-02"),
			BasePrice:     billingStatus.Tier.BasePrice,
			Currency:      billingStatus.Tier.Currency,
		},
	}

	return subscriptionResp, nil
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
	// This is a service-to-service call, use service token
	if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

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
