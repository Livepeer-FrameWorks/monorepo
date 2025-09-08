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
	"frameworks/pkg/cache"
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
	cache        *cache.Cache
}

// Config represents the configuration for the Quartermaster client
type Config struct {
	BaseURL              string
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
	Cache                *cache.Cache
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
		cache:        config.Cache,
	}
}

// getWithCache runs loader via cache when available
func (c *Client) getWithCache(ctx context.Context, key string, loader func() (interface{}, bool, error)) (interface{}, bool, error) {
	if c.cache == nil {
		return loader()
	}
	return c.cache.Get(ctx, key, func(_ context.Context, _ string) (interface{}, bool, error) {
		return loader()
	})
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
	key := "ResolveTenant:" + req.Domain + "|" + req.Subdomain
	val, ok, err := c.getWithCache(ctx, key, func() (interface{}, bool, error) {
		jsonBody, err := json.Marshal(req)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal request: %w", err)
		}

		u := c.baseURL + "/tenant/resolve"
		httpReq, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewBuffer(jsonBody))
		if err != nil {
			return nil, false, fmt.Errorf("failed to create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
			if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
				httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
			}
		} else if c.serviceToken != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
		}

		resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
		if err != nil {
			return nil, false, fmt.Errorf("failed to call Quartermaster: %w", err)
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
			return &quartermaster.ResolveTenantResponse{Error: fmt.Sprintf("tenant resolution failed with status %d", resp.StatusCode)}, true, nil
		}

		var resolution quartermaster.ResolveTenantResponse
		if err := json.NewDecoder(resp.Body).Decode(&resolution); err != nil {
			return nil, false, fmt.Errorf("failed to decode response: %w", err)
		}
		return &resolution, true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return val.(*quartermaster.ResolveTenantResponse), nil
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
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
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

// === INFRASTRUCTURE METHODS ===

// GetClusters retrieves all infrastructure clusters
func (c *Client) GetClusters(ctx context.Context) (*quartermaster.ClustersResponse, error) {
	url := c.baseURL + "/clusters"

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get clusters failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response quartermaster.ClustersResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// GetCluster retrieves a specific infrastructure cluster
func (c *Client) GetCluster(ctx context.Context, clusterID string) (*quartermaster.ClusterResponse, error) {
	url := fmt.Sprintf("%s/clusters/%s", c.baseURL, url.PathEscape(clusterID))

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get cluster failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response quartermaster.ClusterResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// GetNodes retrieves all infrastructure nodes with optional filtering
func (c *Client) GetNodes(ctx context.Context, filters map[string]string) (*quartermaster.NodesResponse, error) {
	urlBuilder := c.baseURL + "/nodes"

	// Add query parameters for filtering
	params := url.Values{}
	for key, value := range filters {
		if value != "" {
			params.Add(key, value)
		}
	}
	if len(params) > 0 {
		urlBuilder += "?" + params.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", urlBuilder, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get nodes failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response quartermaster.NodesResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// GetNode retrieves a specific infrastructure node
func (c *Client) GetNode(ctx context.Context, nodeID string) (*quartermaster.NodeResponse, error) {
	url := fmt.Sprintf("%s/nodes/%s", c.baseURL, url.PathEscape(nodeID))

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get node failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response quartermaster.NodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// GetServices retrieves all services from the service catalog
func (c *Client) GetServices(ctx context.Context) (*quartermaster.ServicesResponse, error) {
	url := c.baseURL + "/services"

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get services failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response quartermaster.ServicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// GetClusterServices retrieves services assigned to a specific cluster
func (c *Client) GetClusterServices(ctx context.Context, clusterID string) (*quartermaster.ClusterServicesResponse, error) {
	url := fmt.Sprintf("%s/clusters/%s/services", c.baseURL, url.PathEscape(clusterID))

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get cluster services failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response quartermaster.ClusterServicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// GetServiceInstances retrieves running service instances with optional filtering
func (c *Client) GetServiceInstances(ctx context.Context, filters map[string]string) (*quartermaster.ServiceInstancesResponse, error) {
	urlBuilder := c.baseURL + "/service-instances"

	// Add query parameters for filtering
	params := url.Values{}
	for key, value := range filters {
		if value != "" {
			params.Add(key, value)
		}
	}
	if len(params) > 0 {
		urlBuilder += "?" + params.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", urlBuilder, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}

	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get service instances failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response quartermaster.ServiceInstancesResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

// GetServicesHealth returns health for all instances
func (c *Client) GetServicesHealth(ctx context.Context) (*quartermaster.ServicesHealthResponse, error) {
	u := c.baseURL + "/services/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			req.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("services health failed: %s", string(body))
	}
	var out quartermaster.ServicesHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// GetServiceHealth returns health for instances of a specific service
func (c *Client) GetServiceHealth(ctx context.Context, serviceID string) (*quartermaster.ServicesHealthResponse, error) {
	u := fmt.Sprintf("%s/services/%s/health", c.baseURL, url.PathEscape(serviceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			req.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("service health failed: %s", string(body))
	}
	var out quartermaster.ServicesHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// BootstrapEdgeNode registers an edge node via Foghorn using an enrollment token
func (c *Client) BootstrapEdgeNode(ctx context.Context, req *quartermaster.BootstrapEdgeNodeRequest) (*quartermaster.BootstrapEdgeNodeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	u := c.baseURL + "/bootstrap/edge-node"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bootstrap edge failed: %s", string(b))
	}
	var out quartermaster.BootstrapEdgeNodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// BootstrapService registers a service instance in Quartermaster
func (c *Client) BootstrapService(ctx context.Context, req *quartermaster.BootstrapServiceRequest) (*quartermaster.BootstrapServiceResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	u := c.baseURL + "/bootstrap/service"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bootstrap service failed: %s", string(b))
	}
	var out quartermaster.BootstrapServiceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// ServiceDiscovery returns discovered service instances of a given type and optional cluster
func (c *Client) ServiceDiscovery(ctx context.Context, serviceType string, clusterID *string) (*quartermaster.ServiceDiscoveryResponse, error) {
	// Use cache when available
	cacheKey := "ServiceDiscovery:" + serviceType
	if clusterID != nil {
		cacheKey += ":" + *clusterID
	}
	val, ok, err := c.getWithCache(ctx, cacheKey, func() (interface{}, bool, error) {
		u := c.baseURL + "/service-discovery"
		params := url.Values{}
		params.Set("type", serviceType)
		if clusterID != nil && *clusterID != "" {
			params.Set("cluster_id", *clusterID)
		}
		if enc := params.Encode(); enc != "" {
			u += "?" + enc
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create request: %w", err)
		}
		if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
			if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
				req.Header.Set("Authorization", "Bearer "+tokenStr)
			}
		} else if c.serviceToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.serviceToken)
		}
		resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
		if err != nil {
			return nil, false, fmt.Errorf("failed to call Quartermaster: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, false, fmt.Errorf("service discovery failed with status %d: %s", resp.StatusCode, string(body))
		}
		var out quartermaster.ServiceDiscoveryResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, false, fmt.Errorf("failed to decode response: %w", err)
		}
		return &out, true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return val.(*quartermaster.ServiceDiscoveryResponse), nil
}

// === BOOTSTRAP TOKEN MANAGEMENT ===

// CreateBootstrapToken creates a one-time bootstrap token for edge/service
func (c *Client) CreateBootstrapToken(ctx context.Context, req *quartermaster.CreateBootstrapTokenRequest) (*quartermaster.CreateBootstrapTokenResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	u := c.baseURL + "/admin/bootstrap-tokens"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create bootstrap token failed: %s", string(b))
	}
	var out quartermaster.CreateBootstrapTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// ListBootstrapTokens lists bootstrap tokens (optionally filterable via query params in future)
func (c *Client) ListBootstrapTokens(ctx context.Context) (*quartermaster.BootstrapTokensResponse, error) {
	u := c.baseURL + "/admin/bootstrap-tokens"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list bootstrap tokens failed: %s", string(b))
	}
	var out quartermaster.BootstrapTokensResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// RevokeBootstrapToken deletes a bootstrap token by ID
func (c *Client) RevokeBootstrapToken(ctx context.Context, id string) error {
	u := c.baseURL + "/admin/bootstrap-tokens/" + url.PathEscape(id)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
		if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tokenStr)
		}
	} else if c.serviceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
	resp, err := clients.DoWithRetry(ctx, c.httpClient, httpReq, c.retryConfig)
	if err != nil {
		return fmt.Errorf("failed to call Quartermaster: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revoke bootstrap token failed: %s", string(b))
	}
	return nil
}

// GetClustersAccess returns clusters the tenant can currently use
func (c *Client) GetClustersAccess(ctx context.Context) (*quartermaster.ClustersAccessResponse, error) {
	// Cache by tenant if available in context
	tenantID, _ := ctx.Value("tenant_id").(string)
	cacheKey := "ClustersAccess:" + tenantID
	val, ok, err := c.getWithCache(ctx, cacheKey, func() (interface{}, bool, error) {
		u := c.baseURL + "/clusters/access"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create request: %w", err)
		}
		if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
			if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
				req.Header.Set("Authorization", "Bearer "+tokenStr)
			}
		} else if c.serviceToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.serviceToken)
		}
		resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
		if err != nil {
			return nil, false, fmt.Errorf("failed to call Quartermaster: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, false, fmt.Errorf("get clusters access failed with status %d: %s", resp.StatusCode, string(body))
		}
		var out quartermaster.ClustersAccessResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, false, fmt.Errorf("failed to decode response: %w", err)
		}
		return &out, true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return val.(*quartermaster.ClustersAccessResponse), nil
}

// GetClustersAvailable returns clusters available for onboarding
func (c *Client) GetClustersAvailable(ctx context.Context) (*quartermaster.ClustersAvailableResponse, error) {
	// Use a simple cache key; availability is global
	cacheKey := "ClustersAvailable"
	val, ok, err := c.getWithCache(ctx, cacheKey, func() (interface{}, bool, error) {
		u := c.baseURL + "/clusters/available"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create request: %w", err)
		}
		if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
			if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
				req.Header.Set("Authorization", "Bearer "+tokenStr)
			}
		} else if c.serviceToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.serviceToken)
		}
		resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
		if err != nil {
			return nil, false, fmt.Errorf("failed to call Quartermaster: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, false, fmt.Errorf("get clusters available failed with status %d: %s", resp.StatusCode, string(body))
		}
		var out quartermaster.ClustersAvailableResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, false, fmt.Errorf("failed to decode response: %w", err)
		}
		return &out, true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return val.(*quartermaster.ClustersAvailableResponse), nil
}

// GetNodeOwner resolves the owner of a node (tenant enrichment), with caching
func (c *Client) GetNodeOwner(ctx context.Context, nodeID string) (*quartermaster.NodeOwnerResponse, error) {
	cacheKey := "NodeOwner:" + nodeID
	val, ok, err := c.getWithCache(ctx, cacheKey, func() (interface{}, bool, error) {
		u := fmt.Sprintf("%s/nodes/%s/owner", c.baseURL, url.PathEscape(nodeID))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create request: %w", err)
		}
		if jwtToken := ctx.Value("jwt_token"); jwtToken != nil {
			if tokenStr, ok := jwtToken.(string); ok && tokenStr != "" {
				req.Header.Set("Authorization", "Bearer "+tokenStr)
			}
		} else if c.serviceToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.serviceToken)
		}
		resp, err := clients.DoWithRetry(ctx, c.httpClient, req, c.retryConfig)
		if err != nil {
			return nil, false, fmt.Errorf("failed to call Quartermaster: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, false, fmt.Errorf("get node owner failed with status %d: %s", resp.StatusCode, string(body))
		}
		var out quartermaster.NodeOwnerResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, false, fmt.Errorf("failed to decode response: %w", err)
		}
		return &out, true, nil
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return val.(*quartermaster.NodeOwnerResponse), nil
}

// ResolveNodeFingerprint resolves a node to a tenant and canonical node_id using fingerprint and server-observed IP/Geo
func (c *Client) ResolveNodeFingerprint(ctx context.Context, req *quartermaster.ResolveNodeFingerprintRequest) (*quartermaster.ResolveNodeFingerprintResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	u := c.baseURL + "/nodes/resolve-fingerprint"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewBuffer(body))
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
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("resolve fingerprint failed: %s", string(b))
	}
	var out quartermaster.ResolveNodeFingerprintResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}
