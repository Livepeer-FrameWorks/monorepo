package cloudflare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.cloudflare.com/client/v4"
	defaultTimeout = 30 * time.Second
)

// Client is a CloudFlare API client
type Client struct {
	apiToken   string
	zoneID     string
	accountID  string
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new CloudFlare API client
func NewClient(apiToken, zoneID, accountID string) *Client {
	return &Client{
		apiToken:  apiToken,
		zoneID:    zoneID,
		accountID: accountID,
		baseURL:   defaultBaseURL,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// SetTimeout sets the HTTP client timeout
func (c *Client) SetTimeout(timeout time.Duration) {
	c.httpClient.Timeout = timeout
}

// doRequest performs an HTTP request with CloudFlare API authentication
func (c *Client) doRequest(method, path string, body interface{}) (*APIResponse, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonData)
	}

	url := c.baseURL + path
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse API response
	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse API response: %w (body: %s)", err, string(respBody))
	}

	// Check for API errors
	if !apiResp.Success {
		if len(apiResp.Errors) > 0 {
			return &apiResp, fmt.Errorf("CloudFlare API error: %s (code: %d)", apiResp.Errors[0].Message, apiResp.Errors[0].Code)
		}
		return &apiResp, fmt.Errorf("CloudFlare API request failed")
	}

	return &apiResp, nil
}

// CreateMonitor creates a health check monitor
func (c *Client) CreateMonitor(monitor Monitor) (*Monitor, error) {
	path := fmt.Sprintf("/accounts/%s/load_balancers/monitors", c.accountID)
	resp, err := c.doRequest("POST", path, monitor)
	if err != nil {
		return nil, err
	}

	// Parse result
	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var created Monitor
	if err := json.Unmarshal(resultJSON, &created); err != nil {
		return nil, fmt.Errorf("failed to parse monitor: %w", err)
	}

	return &created, nil
}

// ListMonitors lists all health check monitors
func (c *Client) ListMonitors() ([]Monitor, error) {
	path := fmt.Sprintf("/accounts/%s/load_balancers/monitors", c.accountID)
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var monitors []Monitor
	if err := json.Unmarshal(resultJSON, &monitors); err != nil {
		return nil, fmt.Errorf("failed to parse monitors: %w", err)
	}

	return monitors, nil
}

// DeleteMonitor deletes a health check monitor
func (c *Client) DeleteMonitor(monitorID string) error {
	path := fmt.Sprintf("/accounts/%s/load_balancers/monitors/%s", c.accountID, monitorID)
	_, err := c.doRequest("DELETE", path, nil)
	return err
}

// CreatePool creates a load balancer pool
func (c *Client) CreatePool(pool Pool) (*Pool, error) {
	path := fmt.Sprintf("/accounts/%s/load_balancers/pools", c.accountID)
	resp, err := c.doRequest("POST", path, pool)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var created Pool
	if err := json.Unmarshal(resultJSON, &created); err != nil {
		return nil, fmt.Errorf("failed to parse pool: %w", err)
	}

	return &created, nil
}

// UpdatePool updates a load balancer pool
func (c *Client) UpdatePool(poolID string, pool Pool) (*Pool, error) {
	path := fmt.Sprintf("/accounts/%s/load_balancers/pools/%s", c.accountID, poolID)
	resp, err := c.doRequest("PUT", path, pool)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var updated Pool
	if err := json.Unmarshal(resultJSON, &updated); err != nil {
		return nil, fmt.Errorf("failed to parse pool: %w", err)
	}

	return &updated, nil
}

// ListPools lists all load balancer pools
func (c *Client) ListPools() ([]Pool, error) {
	basePath := fmt.Sprintf("/accounts/%s/load_balancers/pools", c.accountID)
	var all []Pool
	page := 1
	for {
		path := addQueryParam(addQueryParam(basePath, fmt.Sprintf("page=%d", page)), "per_page=100")
		resp, err := c.doRequest("GET", path, nil)
		if err != nil {
			return nil, err
		}

		resultJSON, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}

		var pools []Pool
		if err := json.Unmarshal(resultJSON, &pools); err != nil {
			return nil, fmt.Errorf("failed to parse pools: %w", err)
		}
		all = append(all, pools...)

		if resp.ResultInfo == nil || resp.ResultInfo.TotalPages <= page {
			break
		}
		page++
	}

	return all, nil
}

// GetPool retrieves a specific pool by ID
func (c *Client) GetPool(poolID string) (*Pool, error) {
	path := fmt.Sprintf("/accounts/%s/load_balancers/pools/%s", c.accountID, poolID)
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var pool Pool
	if err := json.Unmarshal(resultJSON, &pool); err != nil {
		return nil, fmt.Errorf("failed to parse pool: %w", err)
	}

	return &pool, nil
}

// DeletePool deletes a load balancer pool
func (c *Client) DeletePool(poolID string) error {
	path := fmt.Sprintf("/accounts/%s/load_balancers/pools/%s", c.accountID, poolID)
	_, err := c.doRequest("DELETE", path, nil)
	return err
}

// AddOriginToPool adds an origin to an existing pool
func (c *Client) AddOriginToPool(poolID string, origin Origin) (*Pool, error) {
	// Get existing pool
	pool, err := c.GetPool(poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to get pool: %w", err)
	}

	// Add origin
	pool.Origins = append(pool.Origins, origin)

	// Update pool
	return c.UpdatePool(poolID, *pool)
}

// RemoveOriginFromPool removes an origin from a pool by address
func (c *Client) RemoveOriginFromPool(poolID, originAddress string) (*Pool, error) {
	// Get existing pool
	pool, err := c.GetPool(poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to get pool: %w", err)
	}

	// Filter out the origin
	var updatedOrigins []Origin
	for _, origin := range pool.Origins {
		if origin.Address != originAddress {
			updatedOrigins = append(updatedOrigins, origin)
		}
	}

	if len(updatedOrigins) == len(pool.Origins) {
		return nil, fmt.Errorf("origin with address %s not found in pool", originAddress)
	}

	pool.Origins = updatedOrigins

	// Update pool
	return c.UpdatePool(poolID, *pool)
}

// CreateLoadBalancer creates a load balancer with geo-routing
func (c *Client) CreateLoadBalancer(lb LoadBalancer) (*LoadBalancer, error) {
	path := fmt.Sprintf("/zones/%s/load_balancers", c.zoneID)
	resp, err := c.doRequest("POST", path, lb)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var created LoadBalancer
	if err := json.Unmarshal(resultJSON, &created); err != nil {
		return nil, fmt.Errorf("failed to parse load balancer: %w", err)
	}

	return &created, nil
}

// UpdateLoadBalancer updates a load balancer
func (c *Client) UpdateLoadBalancer(lbID string, lb LoadBalancer) (*LoadBalancer, error) {
	path := fmt.Sprintf("/zones/%s/load_balancers/%s", c.zoneID, lbID)
	resp, err := c.doRequest("PUT", path, lb)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var updated LoadBalancer
	if err := json.Unmarshal(resultJSON, &updated); err != nil {
		return nil, fmt.Errorf("failed to parse load balancer: %w", err)
	}

	return &updated, nil
}

// ListLoadBalancers lists all load balancers in the zone
func (c *Client) ListLoadBalancers() ([]LoadBalancer, error) {
	basePath := fmt.Sprintf("/zones/%s/load_balancers", c.zoneID)
	var all []LoadBalancer
	page := 1
	for {
		path := addQueryParam(addQueryParam(basePath, fmt.Sprintf("page=%d", page)), "per_page=100")
		resp, err := c.doRequest("GET", path, nil)
		if err != nil {
			return nil, err
		}

		resultJSON, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}

		var lbs []LoadBalancer
		if err := json.Unmarshal(resultJSON, &lbs); err != nil {
			return nil, fmt.Errorf("failed to parse load balancers: %w", err)
		}
		all = append(all, lbs...)

		if resp.ResultInfo == nil || resp.ResultInfo.TotalPages <= page {
			break
		}
		page++
	}

	return all, nil
}

// DeleteLoadBalancer deletes a load balancer
func (c *Client) DeleteLoadBalancer(lbID string) error {
	path := fmt.Sprintf("/zones/%s/load_balancers/%s", c.zoneID, lbID)
	_, err := c.doRequest("DELETE", path, nil)
	return err
}

// CreateDNSRecord creates a DNS record
func (c *Client) CreateDNSRecord(record DNSRecord) (*DNSRecord, error) {
	path := fmt.Sprintf("/zones/%s/dns_records", c.zoneID)
	resp, err := c.doRequest("POST", path, record)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var created DNSRecord
	if err := json.Unmarshal(resultJSON, &created); err != nil {
		return nil, fmt.Errorf("failed to parse DNS record: %w", err)
	}

	return &created, nil
}

// UpdateDNSRecord updates a DNS record
func (c *Client) UpdateDNSRecord(recordID string, record DNSRecord) (*DNSRecord, error) {
	path := fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, recordID)
	resp, err := c.doRequest("PUT", path, record)
	if err != nil {
		return nil, err
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var updated DNSRecord
	if err := json.Unmarshal(resultJSON, &updated); err != nil {
		return nil, fmt.Errorf("failed to parse DNS record: %w", err)
	}

	return &updated, nil
}

// ListDNSRecords lists DNS records, optionally filtered by type and/or name
func (c *Client) ListDNSRecords(recordType, name string) ([]DNSRecord, error) {
	path := fmt.Sprintf("/zones/%s/dns_records", c.zoneID)

	// Add query parameters if provided
	if recordType != "" || name != "" {
		path += "?"
		if recordType != "" {
			path += "type=" + recordType
		}
		if name != "" {
			if recordType != "" {
				path += "&"
			}
			path += "name=" + name
		}
	}

	var all []DNSRecord
	page := 1
	for {
		pagePath := addQueryParam(addQueryParam(path, fmt.Sprintf("page=%d", page)), "per_page=100")
		resp, err := c.doRequest("GET", pagePath, nil)
		if err != nil {
			return nil, err
		}

		resultJSON, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}

		var records []DNSRecord
		if err := json.Unmarshal(resultJSON, &records); err != nil {
			return nil, fmt.Errorf("failed to parse DNS records: %w", err)
		}
		all = append(all, records...)

		if resp.ResultInfo == nil || resp.ResultInfo.TotalPages <= page {
			break
		}
		page++
	}

	return all, nil
}

func addQueryParam(path, param string) string {
	if strings.Contains(path, "?") {
		return path + "&" + param
	}
	return path + "?" + param
}

// DeleteDNSRecord deletes a DNS record
func (c *Client) DeleteDNSRecord(recordID string) error {
	path := fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, recordID)
	_, err := c.doRequest("DELETE", path, nil)
	return err
}

// FindDNSRecordByName finds a DNS record by name and type
func (c *Client) FindDNSRecordByName(name, recordType string) (*DNSRecord, error) {
	records, err := c.ListDNSRecords(recordType, name)
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("DNS record not found: %s (%s)", name, recordType)
	}

	return &records[0], nil
}

// CreateCNAME is a helper to create a CNAME record
func (c *Client) CreateCNAME(name, target string, proxied bool) (*DNSRecord, error) {
	record := DNSRecord{
		Type:    "CNAME",
		Name:    name,
		Content: target,
		TTL:     1, // Auto
		Proxied: proxied,
	}
	return c.CreateDNSRecord(record)
}

// CreateARecord is a helper to create an A record
func (c *Client) CreateARecord(name, ip string, proxied bool) (*DNSRecord, error) {
	record := DNSRecord{
		Type:    "A",
		Name:    name,
		Content: ip,
		TTL:     1, // Auto
		Proxied: proxied,
	}
	return c.CreateDNSRecord(record)
}
