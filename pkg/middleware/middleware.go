package middleware

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"frameworks/pkg/logging"
)

// LoggingMiddleware provides structured request logging
func LoggingMiddleware(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Start timer
		start := time.Now()

		// Process request
		c.Next()

		// Log request details
		logger.WithFields(logging.Fields{
			"status":     c.Writer.Status(),
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"latency":    time.Since(start),
			"client_ip":  c.ClientIP(),
			"user_agent": c.Request.UserAgent(),
			"tenant_id":  c.GetString("tenant_id"),
			"user_id":    c.GetString("user_id"),
		}).Info("HTTP request")
	}
}

// CORSMiddleware handles CORS headers
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Vary for caches/proxies
		c.Header("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")

		// Allow the requesting origin (or * if none specified)
		origin := c.GetHeader("Origin")
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
		} else {
			c.Header("Access-Control-Allow-Origin", "*")
		}

		// Methods: reflect requested method or provide sane defaults
		if m := c.GetHeader("Access-Control-Request-Method"); m != "" {
			c.Header("Access-Control-Allow-Methods", m)
		} else {
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}

		// Headers: reflect requested headers to avoid blocking custom ones (e.g., X-Tenant-Id)
		if h := c.GetHeader("Access-Control-Request-Headers"); h != "" {
			c.Header("Access-Control-Allow-Headers", h)
		} else {
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Tenant-Id, X-Request-Id")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// RecoveryMiddleware provides panic recovery with logging
func RecoveryMiddleware(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				logger.WithFields(logging.Fields{
					"error":     err,
					"client_ip": c.ClientIP(),
					"method":    c.Request.Method,
					"path":      c.Request.URL.Path,
				}).Error("Request handler panic")

				c.AbortWithStatus(500)
			}
		}()

		c.Next()
	}
}

// RequestIDMiddleware adds a unique request ID to each request
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = GenerateRequestID()
		}

		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// timeoutWriter is a custom ResponseWriter that buffers output and handles timeouts
type timeoutWriter struct {
	gin.ResponseWriter
	body         *bytes.Buffer
	headers      http.Header
	mu           sync.Mutex
	timeout      bool
	wroteHeaders bool
	code         int
	size         int
}

// newTimeoutWriter creates a new timeout writer
func newTimeoutWriter(w gin.ResponseWriter, buf *bytes.Buffer) *timeoutWriter {
	return &timeoutWriter{
		ResponseWriter: w,
		body:           buf,
		headers:        make(http.Header),
		code:           http.StatusOK,
	}
}

// Header returns the header map that will be sent by WriteHeader
func (tw *timeoutWriter) Header() http.Header {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	return tw.headers
}

// Write writes data to the connection as part of an HTTP reply
func (tw *timeoutWriter) Write(data []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.timeout {
		return 0, nil
	}

	if !tw.wroteHeaders {
		tw.WriteHeader(tw.code)
	}

	n, err := tw.body.Write(data)
	tw.size += n
	return n, err
}

// WriteString writes a string to the connection
func (tw *timeoutWriter) WriteString(s string) (int, error) {
	return tw.Write([]byte(s))
}

// WriteHeader sends an HTTP response header with the provided status code
func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.timeout || tw.wroteHeaders {
		return
	}

	tw.code = code
	tw.wroteHeaders = true
}

// Size returns the current size of the response
func (tw *timeoutWriter) Size() int {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	return tw.size
}

// Status returns the HTTP status code
func (tw *timeoutWriter) Status() int {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	return tw.code
}

// copyHeaders copies headers from timeout writer to response writer
func (tw *timeoutWriter) copyHeaders() {
	for key, values := range tw.headers {
		for _, value := range values {
			tw.ResponseWriter.Header().Add(key, value)
		}
	}
}

// TimeoutMiddleware adds a timeout context to requests
// Note: This sets a timeout context but doesn't interrupt handlers.
// Handlers must check ctx.Done() themselves for true timeout behavior.
func TimeoutMiddleware(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Create a timeout context
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()

		// Set the timeout context on the request
		c.Request = c.Request.WithContext(ctx)

		// Process request normally - handlers should check ctx.Done()
		c.Next()
	}
}

// GenerateRequestID generates a unique request ID
func GenerateRequestID() string {
	return uuid.New().String()
}
