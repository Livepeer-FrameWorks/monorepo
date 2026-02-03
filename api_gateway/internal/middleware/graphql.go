package middleware

import (
	"context"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/pkg/auth"
	"frameworks/pkg/ctxkeys"

	"github.com/gin-gonic/gin"
)

// UserContext represents authenticated user information for GraphQL resolvers
type UserContext struct {
	UserID   string
	TenantID string
	Email    string
	Role     string
	TokenID  string
}

// GraphQLContextMiddleware transfers user info from Gin context to request context
// for GraphQL resolvers. Auth is already handled by PublicOrJWTAuth middleware.
func GraphQLContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Inject Gin context for resolvers that need direct access (e.g. for ClientIP)
		ctx = context.WithValue(ctx, ctxkeys.KeyGinContext, c)

		// Check for service token in Authorization header
		if ctxkeys.GetServiceToken(ctx) == "" {
			authHeader := c.GetHeader("Authorization")
			if authHeader != "" {
				parts := strings.Split(authHeader, " ")
				if len(parts) == 2 && parts[0] == "Service" {
					ctx = context.WithValue(ctx, ctxkeys.KeyServiceToken, parts[1])
				}
			}
		}

		// Get user info from Gin context (set by PublicOrJWTAuth)
		// or from request context (for WebSocket connections)
		var userIDStr, tenantIDStr, emailStr, roleStr string
		var authenticated bool

		// Try Gin context first (HTTP requests)
		if userIDVal, exists := c.Get("user_id"); exists {
			if userIDStr, authenticated = userIDVal.(string); authenticated {
				if v, ok := c.Get("tenant_id"); ok {
					tenantIDStr, _ = v.(string)
				}
				if v, ok := c.Get("email"); ok {
					emailStr, _ = v.(string)
				}
				if v, ok := c.Get("role"); ok {
					roleStr, _ = v.(string)
				}
			}
		}

		// Fall back to request context (WebSocket connections)
		if !authenticated {
			if userIDVal := ctx.Value(ctxkeys.KeyUserID); userIDVal != nil {
				if userIDStr, authenticated = userIDVal.(string); authenticated {
					tenantIDStr = ctxkeys.GetTenantID(ctx)
					emailStr = ctxkeys.GetEmail(ctx)
					roleStr = ctxkeys.GetRole(ctx)
				}
			}
		}

		// Build user context for GraphQL resolvers
		if authenticated && userIDStr != "" && tenantIDStr != "" {
			user := &UserContext{
				UserID:   userIDStr,
				TenantID: tenantIDStr,
				Email:    emailStr,
				Role:     roleStr,
			}
			ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userIDStr)
			ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantIDStr)
			ctx = context.WithValue(ctx, ctxkeys.KeyEmail, emailStr)
			ctx = context.WithValue(ctx, ctxkeys.KeyRole, roleStr)
		}

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// GraphQLAttachLoaders attaches per-request dataloaders to the context
func GraphQLAttachLoaders(sc *clients.ServiceClients) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		lds := loaders.New(sc)
		ctx = loaders.ContextWithLoaders(ctx, lds)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// GetUserFromContext extracts user information from GraphQL resolver context
func GetUserFromContext(ctx context.Context) *UserContext {
	if v := ctx.Value(ctxkeys.KeyUser); v != nil {
		if user, ok := v.(*UserContext); ok {
			return user
		}
	}
	return nil
}

// RequireAuth checks if user is authenticated and returns error if not
func RequireAuth(ctx context.Context) (*UserContext, error) {
	user := GetUserFromContext(ctx)
	if user == nil {
		return nil, auth.ErrUnauthenticated
	}
	return user, nil
}

// HasServiceToken checks if the current context has a service token
func HasServiceToken(ctx context.Context) bool {
	return ctxkeys.GetServiceToken(ctx) != ""
}
