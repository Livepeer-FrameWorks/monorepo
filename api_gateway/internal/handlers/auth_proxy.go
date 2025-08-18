package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"

	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

// AuthProxy handles proxying authentication requests to Commodore
type AuthProxy struct {
	commodoreURL string
	logger       logging.Logger
}

// NewAuthProxy creates a new auth proxy instance
func NewAuthProxy(commodoreURL string, logger logging.Logger) *AuthProxy {
	return &AuthProxy{
		commodoreURL: commodoreURL,
		logger:       logger,
	}
}

// ProxyToCommodore creates a gin handler that proxies requests to Commodore
func (ap *AuthProxy) ProxyToCommodore(path string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Construct target URL
		targetURL, err := url.Parse(ap.commodoreURL + path)
		if err != nil {
			ap.logger.WithError(err).Error("Failed to parse Commodore URL")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
		}

		// Read request body
		var body []byte
		if c.Request.Body != nil {
			body, err = io.ReadAll(c.Request.Body)
			if err != nil {
				ap.logger.WithError(err).Error("Failed to read request body")
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
				return
			}
		}

		// Create proxy request
		proxyReq, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL.String(), bytes.NewBuffer(body))
		if err != nil {
			ap.logger.WithError(err).Error("Failed to create proxy request")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
		}

		// Copy headers (excluding hop-by-hop headers)
		for key, values := range c.Request.Header {
			// Skip hop-by-hop headers
			if isHopByHopHeader(key) {
				continue
			}
			for _, value := range values {
				proxyReq.Header.Add(key, value)
			}
		}

		// Set the real IP
		proxyReq.Header.Set("X-Real-IP", c.ClientIP())
		proxyReq.Header.Set("X-Forwarded-For", c.Request.Header.Get("X-Forwarded-For"))
		if proxyReq.Header.Get("X-Forwarded-For") == "" {
			proxyReq.Header.Set("X-Forwarded-For", c.ClientIP())
		}

		// Make the request
		client := &http.Client{}
		resp, err := client.Do(proxyReq)
		if err != nil {
			ap.logger.WithError(err).Error("Failed to make request to Commodore")
			c.JSON(http.StatusBadGateway, gin.H{"error": "Service temporarily unavailable"})
			return
		}
		defer resp.Body.Close()

		// Copy response headers
		for key, values := range resp.Header {
			// Skip hop-by-hop headers
			if isHopByHopHeader(key) {
				continue
			}
			for _, value := range values {
				c.Header(key, value)
			}
		}

		// Copy response
		c.Status(resp.StatusCode)

		// Copy response body
		if resp.Body != nil {
			_, err = io.Copy(c.Writer, resp.Body)
			if err != nil {
				ap.logger.WithError(err).Error("Failed to copy response body")
				return
			}
		}

		// Log the proxy operation
		ap.logger.WithFields(logging.Fields{
			"method":    c.Request.Method,
			"path":      path,
			"status":    resp.StatusCode,
			"client_ip": c.ClientIP(),
		}).Info("Proxied auth request to Commodore")
	}
}

// isHopByHopHeader checks if a header is a hop-by-hop header that shouldn't be proxied
func isHopByHopHeader(header string) bool {
	hopByHopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}

	header = strings.ToLower(header)
	for _, hopHeader := range hopByHopHeaders {
		if strings.ToLower(hopHeader) == header {
			return true
		}
	}
	return false
}
