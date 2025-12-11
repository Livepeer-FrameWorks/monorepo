package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPChecker checks HTTP health endpoints
type HTTPChecker struct {
	Path    string        // Health endpoint path (default: /health)
	Timeout time.Duration // Request timeout (default: 5s)
	TLS     bool          // Use HTTPS
}

// Check performs a health check on an HTTP endpoint
func (c *HTTPChecker) Check(address string, port int) *CheckResult {
	result := &CheckResult{
		Name:      "http",
		CheckedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	path := c.Path
	if path == "" {
		path = "/health"
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	scheme := "http"
	if c.TLS {
		scheme = "https"
	}

	url := fmt.Sprintf("%s://%s:%d%s", scheme, address, port, path)
	result.Metadata["url"] = url

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		return result
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	result.Latency = time.Since(start)

	if err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("request failed: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.Metadata["status_code"] = fmt.Sprintf("%d", resp.StatusCode)

	// Read response body (limited to 1KB)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err == nil && len(body) > 0 {
		result.Metadata["response"] = string(body)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.OK = true
		result.Status = "healthy"
		result.Message = fmt.Sprintf("HTTP %d (latency: %v)", resp.StatusCode, result.Latency)
	} else if resp.StatusCode >= 500 {
		result.OK = false
		result.Status = "unhealthy"
		result.Message = fmt.Sprintf("HTTP %d (server error)", resp.StatusCode)
	} else {
		result.OK = false
		result.Status = "degraded"
		result.Message = fmt.Sprintf("HTTP %d (unexpected status)", resp.StatusCode)
	}

	return result
}
