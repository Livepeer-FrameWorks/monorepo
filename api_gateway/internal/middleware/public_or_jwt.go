package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	pkgauth "frameworks/pkg/auth"

	"github.com/gin-gonic/gin"
)

// isAllowlistedQuery returns true if the GraphQL request body contains one of the
// read-only operations we allow without authentication.
func isAllowlistedQuery(body []byte) bool {
	s := strings.ToLower(string(body))
	// Disallow mutations entirely on unauthenticated requests
	if strings.Contains(s, "mutation") {
		return false
	}
	// Minimal allowlist for public access
	if strings.Contains(s, "serviceinstanceshealth") || strings.Contains(s, "resolveviewerendpoint") {
		return true
	}
	return false
}

// PublicOrJWTAuth allows unauthenticated access for a small allowlist of read-only
// GraphQL queries; otherwise requires a valid JWT or service token. WebSocket upgrades
// pass through here and are authenticated in the GraphQL InitFunc.
func PublicOrJWTAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Allow WebSocket upgrades; auth handled by WS init
		if c.GetHeader("Upgrade") == "websocket" && strings.Contains(c.GetHeader("Connection"), "Upgrade") {
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			// Defer to standard JWT/service-token middleware
			pkgauth.JWTAuthMiddleware(secret)(c)
			return
		}

		// No Authorization header: allow only specific read-only queries
		// Only handle POST with JSON body for now
		if c.Request.Method != http.MethodPost {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
			c.Abort()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			c.Abort()
			return
		}
		// Restore body for downstream handler
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		if isAllowlistedQuery(body) {
			c.Next()
			return
		}

		c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
		c.Abort()
	}
}
