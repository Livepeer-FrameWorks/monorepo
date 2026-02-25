package middleware

import (
	"context"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"frameworks/pkg/ctxkeys"
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
			"tenant_id":  c.GetString(string(ctxkeys.KeyTenantID)),
			"user_id":    c.GetString(string(ctxkeys.KeyUserID)),
		}).Info("HTTP request")
	}
}

// CORSMiddleware handles CORS headers with origin validation.
// In dev mode (devMode=true), all origins are allowed.
// In production, only origins in allowedOrigins are reflected.
// Wildcard entries like "*.example.com" match any subdomain.
func CORSMiddleware(allowedOrigins []string, devMode bool) gin.HandlerFunc {
	exactOrigins := make(map[string]bool, len(allowedOrigins))
	var wildcardSuffixes []string
	for _, o := range allowedOrigins {
		trimmed := strings.TrimRight(strings.TrimSpace(o), "/")
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "*.") {
			// "*.example.com" → match origins ending in ".example.com"
			wildcardSuffixes = append(wildcardSuffixes, trimmed[1:]) // store ".example.com"
		} else {
			exactOrigins[trimmed] = true
		}
	}

	isAllowed := func(origin string) bool {
		if devMode {
			return true
		}
		if exactOrigins[origin] {
			return true
		}
		// Extract scheme + host for wildcard matching
		// e.g. "https://app.us.example.com" → check if "://app.us.example.com" has suffix ".example.com"
		for _, suffix := range wildcardSuffixes {
			if idx := strings.Index(origin, "://"); idx >= 0 {
				host := origin[idx+3:]
				if strings.HasSuffix(host, suffix) {
					return true
				}
			}
		}
		return false
	}

	return func(c *gin.Context) {
		c.Header("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")

		origin := c.GetHeader("Origin")
		allowed := origin != "" && isAllowed(origin)

		if allowed {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")

			if m := c.GetHeader("Access-Control-Request-Method"); m != "" {
				c.Header("Access-Control-Allow-Methods", m)
			} else {
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			}

			if h := c.GetHeader("Access-Control-Request-Headers"); h != "" {
				c.Header("Access-Control-Allow-Headers", h)
			} else {
				c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Tenant-Id, X-Request-Id")
			}
		}

		if c.Request.Method == http.MethodOptions {
			if allowed {
				c.AbortWithStatus(http.StatusNoContent)
			} else {
				c.AbortWithStatus(http.StatusForbidden)
			}
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
					"error":      err,
					"stacktrace": string(debug.Stack()),
					"client_ip":  c.ClientIP(),
					"method":     c.Request.Method,
					"path":       c.Request.URL.Path,
				}).Error("Request handler panic")

				if c.Writer.Written() {
					c.Abort()
					return
				}
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
