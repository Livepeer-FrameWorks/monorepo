package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/auth"
	"frameworks/pkg/ctxkeys"

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
		claims, err := auth.ValidateJWT(token, secret)
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
		c.Set("auth_type", "jwt")

		// Also set in request context for gRPC client interceptor
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, claims.TenantID)
		ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, token)
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
	if strings.Contains(s, "serviceinstanceshealth") || strings.Contains(s, "resolveviewerendpoint") || strings.Contains(s, "resolveingestendpoint") {
		return true
	}
	return false
}

// PublicOrJWTAuth allows unauthenticated access for a small allowlist of read-only
// GraphQL queries; otherwise requires a valid JWT or service token. WebSocket upgrades
// pass through here and are authenticated in the GraphQL InitFunc.
// Supports both Authorization header and httpOnly cookies for JWT.
//
// Flow:
// 1. WebSocket upgrades → pass through (auth in InitFunc)
// 2. POST requests → check allowlist FIRST
//   - If allowlisted → proceed anonymously (ignore any tokens)
//   - If not allowlisted → require auth
//
// 3. Other methods → require auth
func PublicOrJWTAuth(secret []byte, serviceClients *clients.ServiceClients) gin.HandlerFunc {
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
				c.Set("public_allowlisted", true)
				c.Next()
				return
			}
		}

		authResult, err := AuthenticateRequest(c.Request.Context(), c.Request, serviceClients, secret, AuthOptions{
			AllowCookies: true,
			AllowWallet:  true,
			AllowX402:    true,
		}, nil)
		if err != nil && authResult == nil {
			c.Next()
			return
		}
		if authResult == nil {
			c.Next()
			return
		}

		if authResult.AuthType == "x402" && authResult.JWTToken != "" {
			c.Header("X-Access-Token", authResult.JWTToken)
			if authResult.ExpiresAt != nil {
				c.Header("X-Access-Token-Expires-At", authResult.ExpiresAt.Format(time.RFC3339))
			}

			isDev := os.Getenv("ENV") == "development" ||
				os.Getenv("BUILD_ENV") == "development" ||
				os.Getenv("GO_ENV") == "development"
			secure := !isDev
			c.SetSameSite(http.SameSiteLaxMode)
			c.SetCookie("access_token", authResult.JWTToken, 15*60, "/", "", secure, true)
			if authResult.TenantID != "" {
				c.SetCookie("tenant_id", authResult.TenantID, 15*60, "/", "", secure, true)
			}
		}

		c.Set("user_id", authResult.UserID)
		c.Set("tenant_id", authResult.TenantID)
		c.Set("email", authResult.Email)
		c.Set("role", authResult.Role)
		c.Set("auth_type", authResult.AuthType)
		if authResult.AuthType == "x402" {
			c.Set("x402_processed", authResult.X402Processed)
			c.Set("x402_auth_only", authResult.X402AuthOnly)
		}
		if authResult.AuthType == "api_token" {
			tokenID := authResult.TokenID
			if tokenID == "" {
				tokenID = authResult.APIToken
			}
			c.Set("api_token_hash", hashIdentifier(tokenID))
		}

		ctx := ApplyAuthToContext(c.Request.Context(), authResult)

		// Also forward X-PAYMENT header in context for downstream gRPC calls (viewer-pays flows)
		if xPayment := GetX402PaymentHeader(c.Request); xPayment != "" {
			ctx = context.WithValue(ctx, ctxkeys.KeyXPayment, xPayment)
		}

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
