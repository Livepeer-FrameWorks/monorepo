package periscope

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"frameworks/pkg/api/periscope"
	"frameworks/pkg/clients"
	"frameworks/pkg/logging"
)

// Client represents a Periscope Query API client
type Client struct {
	baseURL      string
	httpClient   *http.Client
	serviceToken string
	logger       logging.Logger
	retryConfig  clients.RetryConfig
}

// Config represents the configuration for the Periscope client
type Config struct {
	BaseURL              string
	ServiceToken         string
	Timeout              time.Duration
	Logger               logging.Logger
	RetryConfig          *clients.RetryConfig
	CircuitBreakerConfig *clients.CircuitBreakerConfig
}

// NewClient creates a new Periscope Query API client
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

// makeRequest is a helper function to make authenticated requests
func (c *Client) makeRequest(ctx context.Context, method, endpoint string, params url.Values) ([]byte, error) {
	requestURL := c.baseURL + endpoint
	if params != nil {
		requestURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
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
		return nil, fmt.Errorf("failed to call Periscope Query: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		if len(body) == 0 {
			return nil, fmt.Errorf("Periscope Query returned error status %d with empty body", resp.StatusCode)
		}
		var errorResp periscope.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("Periscope Query returned error status %d: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("Periscope Query returned error: %s", errorResp.Error)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("failed to parse response: empty body")
	}

	return body, nil
}

// GetStreamAnalytics returns analytics for all streams with recent activity
func (c *Client) GetStreamAnalytics(ctx context.Context, tenantID, streamID, startTime, endTime string) (*periscope.StreamAnalyticsResponse, error) {
	params := url.Values{}
	if streamID != "" {
		params.Set("stream_id", streamID)
	}
	if startTime != "" {
		params.Set("start_time", startTime)
	}
	if endTime != "" {
		params.Set("end_time", endTime)
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/streams", params)
	if err != nil {
		return nil, err
	}

	var response periscope.StreamAnalyticsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetStreamDetails returns detailed analytics for a specific stream
func (c *Client) GetStreamDetails(ctx context.Context, internalName string) (*periscope.StreamDetailsResponse, error) {
	endpoint := fmt.Sprintf("/analytics/streams/%s", url.PathEscape(internalName))
	body, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var response periscope.StreamDetailsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetStreamEvents returns events for a specific stream
func (c *Client) GetStreamEvents(ctx context.Context, internalName string) (*periscope.StreamEventsResponse, error) {
	endpoint := fmt.Sprintf("/analytics/streams/%s/events", url.PathEscape(internalName))
	body, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var response periscope.StreamEventsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetViewerStats returns viewer statistics for a specific stream
func (c *Client) GetViewerStats(ctx context.Context, internalName string) (*periscope.ViewerStatsResponse, error) {
	endpoint := fmt.Sprintf("/analytics/streams/%s/viewers", url.PathEscape(internalName))
	body, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var response periscope.ViewerStatsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetTrackListEvents returns track list updates for a specific stream
func (c *Client) GetTrackListEvents(ctx context.Context, internalName string, startTime, endTime *time.Time) (*periscope.TrackListEventsResponse, error) {
	endpoint := fmt.Sprintf("/analytics/streams/%s/track-list", url.PathEscape(internalName))

	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", endpoint, params)
	if err != nil {
		return nil, err
	}

	var response periscope.TrackListEventsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetStreamBufferEvents returns buffer events for a specific stream
func (c *Client) GetStreamBufferEvents(ctx context.Context, internalName string, startTime, endTime *time.Time) (*periscope.BufferEventsResponse, error) {
	endpoint := fmt.Sprintf("/analytics/streams/%s/buffer", url.PathEscape(internalName))

	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", endpoint, params)
	if err != nil {
		return nil, err
	}

	var response periscope.BufferEventsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetStreamEndEvents returns end events for a specific stream
func (c *Client) GetStreamEndEvents(ctx context.Context, internalName string, startTime, endTime *time.Time) (*periscope.EndEventsResponse, error) {
	endpoint := fmt.Sprintf("/analytics/streams/%s/end", url.PathEscape(internalName))

	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", endpoint, params)
	if err != nil {
		return nil, err
	}

	var response periscope.EndEventsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetViewerMetrics returns viewer metrics from ClickHouse
func (c *Client) GetViewerMetrics(ctx context.Context, tenantID, streamID string, startTime, endTime *time.Time) (*periscope.ViewerMetricsResponse, error) {
	params := url.Values{}
	if streamID != "" {
		params.Set("stream_id", streamID)
	}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/viewer-metrics", params)
	if err != nil {
		return nil, err
	}

	var response periscope.ViewerMetricsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetConnectionEvents returns connection events from ClickHouse
func (c *Client) GetConnectionEvents(ctx context.Context, startTime, endTime *time.Time) (*periscope.ConnectionEventsResponse, error) {
	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/connection-events", params)
	if err != nil {
		return nil, err
	}

	var response periscope.ConnectionEventsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetNodeMetrics returns node metrics from ClickHouse
func (c *Client) GetNodeMetrics(ctx context.Context, startTime, endTime *time.Time) (*periscope.NodeMetricsResponse, error) {
	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/node-metrics", params)
	if err != nil {
		return nil, err
	}

	var response periscope.NodeMetricsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetRoutingEvents returns routing events from ClickHouse
func (c *Client) GetRoutingEvents(ctx context.Context, startTime, endTime *time.Time) (*periscope.RoutingEventsResponse, error) {
	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/routing-events", params)
	if err != nil {
		return nil, err
	}

	var response periscope.RoutingEventsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetStreamHealthMetrics returns stream health metrics from ClickHouse
func (c *Client) GetStreamHealthMetrics(ctx context.Context, startTime, endTime *time.Time) (*periscope.StreamHealthMetricsResponse, error) {
	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/stream-health", params)
	if err != nil {
		return nil, err
	}

	var response periscope.StreamHealthMetricsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetViewerMetrics5m returns aggregated viewer metrics from ClickHouse materialized view
func (c *Client) GetViewerMetrics5m(ctx context.Context, startTime, endTime *time.Time) (*periscope.ViewerMetrics5mResponse, error) {
	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/viewer-metrics/5m", params)
	if err != nil {
		return nil, err
	}

	var response periscope.ViewerMetrics5mResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetNodeMetrics1h returns hourly aggregated node metrics from ClickHouse materialized view
func (c *Client) GetNodeMetrics1h(ctx context.Context, startTime, endTime *time.Time) (*periscope.NodeMetrics1hResponse, error) {
	params := url.Values{}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/node-metrics/1h", params)
	if err != nil {
		return nil, err
	}

	var response periscope.NodeMetrics1hResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetPlatformOverview returns high-level platform metrics
func (c *Client) GetPlatformOverview(ctx context.Context, tenantID, startTime, endTime string) (*periscope.PlatformOverviewResponse, error) {
	params := url.Values{}
	if startTime != "" {
		params.Set("start_time", startTime)
	}
	if endTime != "" {
		params.Set("end_time", endTime)
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/platform/overview", params)
	if err != nil {
		return nil, err
	}

	var response periscope.PlatformOverviewResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetRealtimeStreams returns current live streams with analytics
func (c *Client) GetRealtimeStreams(ctx context.Context) (*periscope.RealtimeStreamsResponse, error) {
	body, err := c.makeRequest(ctx, "GET", "/analytics/realtime/streams", nil)
	if err != nil {
		return nil, err
	}

	var response periscope.RealtimeStreamsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetRealtimeViewers returns current viewer counts across all streams
func (c *Client) GetRealtimeViewers(ctx context.Context) (*periscope.RealtimeViewersResponse, error) {
	body, err := c.makeRequest(ctx, "GET", "/analytics/realtime/viewers", nil)
	if err != nil {
		return nil, err
	}

	var response periscope.RealtimeViewersResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetRealtimeEvents returns recent events across all streams
func (c *Client) GetRealtimeEvents(ctx context.Context) (*periscope.RealtimeEventsResponse, error) {
	body, err := c.makeRequest(ctx, "GET", "/analytics/realtime/events", nil)
	if err != nil {
		return nil, err
	}

	var response periscope.RealtimeEventsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}

// GetClipEvents returns clip lifecycle events with optional filters
func (c *Client) GetClipEvents(ctx context.Context, internalName *string, stage *string, startTime, endTime *time.Time, offset, limit *int) (*periscope.ClipEventsResponse, error) {
	params := url.Values{}
	if internalName != nil && *internalName != "" {
		params.Set("internal_name", *internalName)
	}
	if stage != nil && *stage != "" {
		params.Set("stage", *stage)
	}
	if startTime != nil {
		params.Set("start_time", startTime.Format(time.RFC3339))
	}
	if endTime != nil {
		params.Set("end_time", endTime.Format(time.RFC3339))
	}
	if offset != nil {
		params.Set("offset", fmt.Sprintf("%d", *offset))
	}
	if limit != nil {
		params.Set("limit", fmt.Sprintf("%d", *limit))
	}

	body, err := c.makeRequest(ctx, "GET", "/analytics/clip-events", params)
	if err != nil {
		return nil, err
	}
	var resp periscope.ClipEventsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &resp, nil
}
