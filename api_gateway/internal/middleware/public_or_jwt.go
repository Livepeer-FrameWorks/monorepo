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
		c.Set(string(ctxkeys.KeyUserID), claims.UserID)
		c.Set(string(ctxkeys.KeyTenantID), claims.TenantID)
		c.Set(string(ctxkeys.KeyEmail), claims.Email)
		c.Set(string(ctxkeys.KeyRole), claims.Role)
		c.Set(string(ctxkeys.KeyAuthType), "jwt")

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
				c.Set(string(ctxkeys.KeyPublicAllowlisted), true)
				ctx := context.WithValue(c.Request.Context(), ctxkeys.KeyPublicAllowlisted, true)
				c.Request = c.Request.WithContext(ctx)
				c.Next()
				return
			}
		}

		authResult, err := AuthenticateRequest(c.Request.Context(), c.Request, serviceClients, secret, AuthOptions{
			AllowCookies: true,
			AllowWallet:  true,
			AllowX402:    true,
		}, nil)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication failed"})
			c.Abort()
			return
		}
		if authResult == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			c.Abort()
			return
		}

		if authResult.AuthType == "x402" && authResult.JWTToken != "" {
			applyX402Cookies(c, authResult)
		}

		c.Set(string(ctxkeys.KeyUserID), authResult.UserID)
		c.Set(string(ctxkeys.KeyTenantID), authResult.TenantID)
		c.Set(string(ctxkeys.KeyEmail), authResult.Email)
		c.Set(string(ctxkeys.KeyRole), authResult.Role)
		c.Set(string(ctxkeys.KeyAuthType), authResult.AuthType)
		if authResult.AuthType == "x402" {
			c.Set(string(ctxkeys.KeyX402Processed), authResult.X402Processed)
			c.Set(string(ctxkeys.KeyX402AuthOnly), authResult.X402AuthOnly)
		}
		if authResult.AuthType == "api_token" {
			tokenID := authResult.TokenID
			if tokenID == "" {
				tokenID = authResult.APIToken
			}
			c.Set(string(ctxkeys.KeyAPITokenHash), hashIdentifier(tokenID))
			c.Set(string(ctxkeys.KeyPermissions), authResult.Permissions)
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

func applyX402Cookies(c *gin.Context, authResult *AuthResult) {
	if authResult == nil || authResult.JWTToken == "" {
		return
	}
	c.Header("X-Access-Token", authResult.JWTToken)
	if authResult.ExpiresAt != nil {
		c.Header("X-Access-Token-Expires-At", authResult.ExpiresAt.Format(time.RFC3339))
	}

	isDev := os.Getenv("ENV") == "development" ||
		os.Getenv("BUILD_ENV") == "development" ||
		os.Getenv("GO_ENV") == "development"
	secure := !isDev
	cookieDomain := strings.TrimPrefix(os.Getenv("COOKIE_DOMAIN"), ".")
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("access_token", authResult.JWTToken, 15*60, "/", cookieDomain, secure, true)
	if authResult.TenantID != "" {
		c.SetCookie("tenant_id", authResult.TenantID, 15*60, "/", cookieDomain, secure, true)
	}
}
