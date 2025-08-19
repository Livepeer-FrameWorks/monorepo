package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ServiceAuthMiddleware validates service-to-service auth tokens
func ServiceAuthMiddleware(expectedToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
			c.Abort()
			return
		}

		// Extract Bearer token
		parts := strings.Split(auth, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header"})
			c.Abort()
			return
		}

		// Validate token
		if err := ValidateServiceToken(parts[1], expectedToken); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			c.Abort()
			return
		}

		c.Next()
	}
}

// JWTAuthMiddleware validates JWT tokens for web sessions and service tokens for service-to-service calls
// It supports WebSocket upgrade requests by allowing them through for later authentication
func JWTAuthMiddleware(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check if this is a WebSocket upgrade request
		if c.GetHeader("Upgrade") == "websocket" && 
		   strings.Contains(c.GetHeader("Connection"), "Upgrade") {
			// Allow WebSocket upgrade requests through - auth will be handled by the WebSocket handler
			c.Next()
			return
		}

		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
			c.Abort()
			return
		}

		// Extract Bearer token
		parts := strings.Split(auth, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header"})
			c.Abort()
			return
		}

		token := parts[1]

		// Try JWT validation first
		claims, err := ValidateJWT(token, secret)
		if err == nil {
			// JWT is valid - set user claims in context
			c.Set("user_id", claims.UserID)
			c.Set("tenant_id", claims.TenantID)
			c.Set("email", claims.Email)
			c.Set("role", claims.Role)
			c.Next()
			return
		}

		// If JWT validation fails, try service token validation
		serviceToken := GetServiceToken()
		if serviceToken != "" && ValidateServiceToken(token, serviceToken) == nil {
			// Service token is valid - set service account claims in context
			c.Set("user_id", "00000000-0000-0000-0000-000000000000")   // Service account UUID
			c.Set("tenant_id", "00000000-0000-0000-0000-000000000001") // System tenant
			c.Set("email", "service@internal")
			c.Set("role", "service")
			c.Next()
			return
		}

		// Both validations failed
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid JWT token"})
		c.Abort()
	}
}
