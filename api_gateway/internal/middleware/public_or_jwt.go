package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/auth"
	"frameworks/pkg/config"
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

// PublicOrJWTAuth allows unauthenticated access for a small allowlist of read-only
// GraphQL queries; otherwise requires a valid JWT or service token. WebSocket upgrades
// pass through here and are authenticated in the GraphQL InitFunc.
// Supports both Authorization header and httpOnly cookies for JWT.
//
// Flow:
// 1. WebSocket upgrades → pass through (auth in InitFunc)
// 2. POST requests → check allowlist FIRST
//   - If allowlisted → proceed anonymously (ignore any tokens)
//   - If not allowlisted and no auth is provided → return a 402 x402 challenge
//   - If not allowlisted and auth is provided → require valid auth
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

		if !hasAuthCredentials(c.Request) {
			opName, variables := extractGraphQLRequest(c)
			resourcePath := c.Request.URL.Path
			if opName != "" {
				if resource := graphqlResourcePath(opName, variables); resource != "" {
					resourcePath = resource
				} else {
					resourcePath = "graphql://" + opName
				}
			}
			var x402Provider X402Provider
			if serviceClients != nil && serviceClients.Purser != nil {
				x402Provider = serviceClients.Purser
			}
			c.JSON(http.StatusPaymentRequired, build402Response(c.Request.Context(), "", opName, resourcePath, x402Provider, nil))
			c.Abort()
			return
		}

		authResult, err := AuthenticateRequest(c.Request.Context(), c.Request, serviceClients, secret, AuthOptions{
			AllowCookies: true,
			AllowWallet:  true,
			AllowX402:    true,
		}, nil)
		if err != nil {
			if GetX402PaymentHeader(c.Request) != "" {
				c.JSON(http.StatusPaymentRequired, gin.H{
					"error":     "payment_failed",
					"message":   err.Error(),
					"code":      "X402_PAYMENT_FAILED",
					"topup_url": "/account/billing",
				})
				c.Abort()
				return
			}
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
			applyX402SessionHeaders(c, authResult)
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

func hasAuthCredentials(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return true
	}
	if strings.TrimSpace(GetX402PaymentHeader(r)) != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("X-Wallet-Address")) != "" {
		return true
	}
	if _, err := r.Cookie("access_token"); err == nil {
		return true
	}
	return false
}

func applyX402SessionHeaders(c *gin.Context, authResult *AuthResult) {
	if authResult == nil || authResult.JWTToken == "" {
		return
	}
	c.Header("X-Access-Token", authResult.JWTToken)
	if authResult.ExpiresAt != nil {
		c.Header("X-Access-Token-Expires-At", authResult.ExpiresAt.Format(time.RFC3339))
	}
	if !x402CookiesAllowed(c) {
		return
	}

	isDev := config.IsDevelopment()
	secure := !isDev
	cookieDomain := config.GetCookieDomain()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("access_token", authResult.JWTToken, 15*60, "/", cookieDomain, secure, true)
	if authResult.TenantID != "" {
		c.SetCookie("tenant_id", authResult.TenantID, 15*60, "/", cookieDomain, secure, true)
	}
}

func x402CookiesAllowed(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if c.Request.Header.Get("Origin") == "" {
		return true
	}
	return c.Writer.Header().Get("Access-Control-Allow-Credentials") == "true"
}
