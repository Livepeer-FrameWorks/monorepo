package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"frameworks/pkg/logging"
)

// Context represents an HTTP request context
type Context = *gin.Context

// HandlerFunc represents an HTTP handler function
type HandlerFunc = gin.HandlerFunc

// H is a shortcut for map[string]interface{}
type H = gin.H

// Engine represents a gin engine
type Engine = *gin.Engine

// LoggingMiddleware provides structured request logging
func LoggingMiddleware(logger logging.Logger) HandlerFunc {
	return func(c Context) {
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
func CORSMiddleware() HandlerFunc {
	return func(c Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// RecoveryMiddleware provides panic recovery with logging
func RecoveryMiddleware(logger logging.Logger) HandlerFunc {
	return func(c Context) {
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
func RequestIDMiddleware() HandlerFunc {
	return func(c Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = GenerateRequestID()
		}

		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// TimeoutMiddleware adds a timeout to request processing
func TimeoutMiddleware(timeout time.Duration) HandlerFunc {
	return func(c Context) {
		// Wrap the request in a timeout context
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()

		// Replace request context
		c.Request = c.Request.WithContext(ctx)

		// Process request
		done := make(chan struct{})
		go func() {
			c.Next()
			close(done)
		}()

		// Wait for completion or timeout
		select {
		case <-done:
			return
		case <-ctx.Done():
			c.AbortWithStatus(http.StatusGatewayTimeout)
			return
		}
	}
}

// GenerateRequestID generates a unique request ID
func GenerateRequestID() string {
	return uuid.New().String()
}
