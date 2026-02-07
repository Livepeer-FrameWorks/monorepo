package auth

import (
	"net/http"
	"strings"

	"frameworks/pkg/ctxkeys"
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

// APIKeyIdentity holds the claims injected when a request authenticates via API key.
type APIKeyIdentity struct {
	TenantID string
	UserID   string
	Role     string
}

type jwtMiddlewareConfig struct {
	apiKeys map[string]APIKeyIdentity
}

// JWTOption configures optional behaviour for JWTAuthMiddleware.
type JWTOption func(*jwtMiddlewareConfig)

// WithAPIKeys registers static API keys that are accepted as Bearer tokens.
// When a request's bearer token matches a key, the associated identity is
// injected into the Gin context and JWT validation is skipped.
func WithAPIKeys(keys map[string]APIKeyIdentity) JWTOption {
	return func(cfg *jwtMiddlewareConfig) {
		cfg.apiKeys = keys
	}
}

// JWTAuthMiddleware validates JWT tokens for web sessions and service tokens for service-to-service calls.
// It supports WebSocket upgrade requests by allowing them through for later authentication.
func JWTAuthMiddleware(secret []byte, opts ...JWTOption) gin.HandlerFunc {
	var cfg jwtMiddlewareConfig
	for _, o := range opts {
		o(&cfg)
	}

	return func(c *gin.Context) {
		// Check if this is a WebSocket upgrade request
		if c.GetHeader("Upgrade") == "websocket" &&
			strings.Contains(c.GetHeader("Connection"), "Upgrade") {
			c.Next()
			return
		}

		auth := c.GetHeader("Authorization")
		if auth == "" {
			// Browser clients typically use httpOnly cookies for auth.
			if cookieToken, err := c.Cookie("access_token"); err == nil && cookieToken != "" {
				auth = "Bearer " + cookieToken
			} else {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
				c.Abort()
				return
			}
		}

		// Extract Bearer token
		parts := strings.Split(auth, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header"})
			c.Abort()
			return
		}

		token := parts[1]

		// Try static API key match first (cheapest check)
		if identity, ok := cfg.apiKeys[token]; ok && token != "" {
			c.Set(string(ctxkeys.KeyUserID), identity.UserID)
			c.Set(string(ctxkeys.KeyTenantID), identity.TenantID)
			c.Set(string(ctxkeys.KeyRole), identity.Role)
			c.Set(string(ctxkeys.KeyAuthType), "api_key")
			c.Next()
			return
		}

		// Try JWT validation
		claims, err := ValidateJWT(token, secret)
		if err == nil {
			c.Set(string(ctxkeys.KeyUserID), claims.UserID)
			c.Set(string(ctxkeys.KeyTenantID), claims.TenantID)
			c.Set(string(ctxkeys.KeyEmail), claims.Email)
			c.Set(string(ctxkeys.KeyRole), claims.Role)
			c.Set(string(ctxkeys.KeyAuthType), "jwt")
			c.Set(string(ctxkeys.KeyJWTToken), token)
			c.Next()
			return
		}

		// If JWT validation fails, try service token validation
		serviceToken := GetServiceToken()
		if serviceToken != "" && ValidateServiceToken(token, serviceToken) == nil {
			c.Set(string(ctxkeys.KeyUserID), "00000000-0000-0000-0000-000000000000")
			c.Set(string(ctxkeys.KeyTenantID), "00000000-0000-0000-0000-000000000001")
			c.Set(string(ctxkeys.KeyEmail), "service@internal")
			c.Set(string(ctxkeys.KeyRole), "service")
			c.Set(string(ctxkeys.KeyAuthType), "service")
			c.Next()
			return
		}

		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid JWT token"})
		c.Abort()
	}
}
