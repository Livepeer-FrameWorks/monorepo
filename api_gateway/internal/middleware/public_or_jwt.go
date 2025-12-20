package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	pkgauth "frameworks/pkg/auth"

	"github.com/gin-gonic/gin"
)

// RequireJWTAuth requires a valid JWT from cookie or Authorization header.
// Unlike PublicOrJWTAuth, this does not allow unauthenticated access.
func RequireJWTAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Try to get token from Authorization header first, then fall back to cookie
		var token string
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				token = parts[1]
			}
		}

		// Fall back to httpOnly cookie if no Authorization header
		if token == "" {
			if cookieToken, err := c.Cookie("access_token"); err == nil && cookieToken != "" {
				token = cookieToken
			}
		}

		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization token"})
			c.Abort()
			return
		}

		// Validate JWT
		claims, err := pkgauth.ValidateJWT(token, secret)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		// Set user context for downstream handlers
		c.Set("user_id", claims.UserID)
		c.Set("tenant_id", claims.TenantID)
		c.Set("email", claims.Email)
		c.Set("role", claims.Role)

		// Also set in request context for gRPC client interceptor
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, "user_id", claims.UserID)
		ctx = context.WithValue(ctx, "tenant_id", claims.TenantID)
		ctx = context.WithValue(ctx, "jwt_token", token)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

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

type TokenValidator func(token string) (*UserContext, error)

// PublicOrJWTAuth allows unauthenticated access for a small allowlist of read-only
// GraphQL queries; otherwise requires a valid JWT or service token. WebSocket upgrades
// pass through here and are authenticated in the GraphQL InitFunc.
// Supports both Authorization header and httpOnly cookies for JWT.
//
// Flow:
// 1. WebSocket upgrades → pass through (auth in InitFunc)
// 2. POST requests → check allowlist FIRST
//    - If allowlisted → proceed anonymously (ignore any tokens)
//    - If not allowlisted → require auth
// 3. Other methods → require auth
func PublicOrJWTAuth(secret []byte, validator TokenValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Allow WebSocket upgrades; auth handled by WS init
		if c.GetHeader("Upgrade") == "websocket" && strings.Contains(c.GetHeader("Connection"), "Upgrade") {
			c.Next()
			return
		}

		// For POST requests, check allowlist FIRST (before looking at tokens)
		// This ensures public endpoints work even if client sends stale/invalid tokens
		if c.Request.Method == http.MethodPost {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
				c.Abort()
				return
			}
			// Restore body for downstream handler
			c.Request.Body = io.NopCloser(bytes.NewReader(body))

			// If allowlisted, proceed anonymously (ignore any tokens sent)
			if isAllowlistedQuery(body) {
				c.Next()
				return
			}
		}

		// Not allowlisted - require authentication
		// Try to get token from Authorization header first, then fall back to cookie
		var token string
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				token = parts[1]
			}
		}

		// Fall back to httpOnly cookie if no Authorization header
		if token == "" {
			if cookieToken, err := c.Cookie("access_token"); err == nil && cookieToken != "" {
				token = cookieToken
			}
		}

		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
			c.Abort()
			return
		}

		// 1. Try JWT
		claims, err := pkgauth.ValidateJWT(token, secret)
		if err == nil {
			c.Set("user_id", claims.UserID)
			c.Set("tenant_id", claims.TenantID)
			c.Set("email", claims.Email)
			c.Set("role", claims.Role)
			c.Next()
			return
		}

		// 2. Try API Token (via callback)
		if validator != nil {
			user, err := validator(token)
			if err == nil && user != nil {
				c.Set("user_id", user.UserID)
				c.Set("tenant_id", user.TenantID)
				c.Set("email", user.Email)
				c.Set("role", user.Role)
				c.Next()
				return
			}
		}

		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		c.Abort()
	}
}
