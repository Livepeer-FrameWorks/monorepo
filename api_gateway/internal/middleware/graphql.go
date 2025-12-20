package middleware

import (
	"context"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/pkg/auth"

	"github.com/gin-gonic/gin"
)

// UserContext represents authenticated user information for GraphQL resolvers
type UserContext struct {
	UserID   string
	TenantID string
	Email    string
	Role     string
}

// GraphQLContextMiddleware transfers user info from Gin context to request context
// for GraphQL resolvers. Auth is already handled by PublicOrJWTAuth middleware.
func GraphQLContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Inject Gin context for resolvers that need direct access (e.g. for ClientIP)
		ctx = context.WithValue(ctx, "GinContext", c)

		// Check for service token in Authorization header
		if existingServiceToken := ctx.Value("service_token"); existingServiceToken == nil {
			authHeader := c.GetHeader("Authorization")
			if authHeader != "" {
				parts := strings.Split(authHeader, " ")
				if len(parts) == 2 && parts[0] == "Service" {
					ctx = context.WithValue(ctx, "service_token", parts[1])
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
			if userIDVal := ctx.Value("user_id"); userIDVal != nil {
				if userIDStr, authenticated = userIDVal.(string); authenticated {
					tenantIDStr, _ = ctx.Value("tenant_id").(string)
					emailStr, _ = ctx.Value("email").(string)
					roleStr, _ = ctx.Value("role").(string)
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
			ctx = context.WithValue(ctx, "user", user)
			ctx = context.WithValue(ctx, "user_id", userIDStr)
			ctx = context.WithValue(ctx, "tenant_id", tenantIDStr)
			ctx = context.WithValue(ctx, "email", emailStr)
			ctx = context.WithValue(ctx, "role", roleStr)
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
	if v := ctx.Value("user"); v != nil {
		if user, ok := v.(*UserContext); ok {
			return user
		}
	}
	return nil
}

// IsAuthenticated checks if the current context has an authenticated user
func IsAuthenticated(ctx context.Context) bool {
	return GetUserFromContext(ctx) != nil
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
	var token string
	var ok bool
	if v := ctx.Value("service_token"); v != nil {
		token, ok = v.(string)
	}
	return ok && token != ""
}

// RequireServiceToken checks if service token is present and returns error if not
func RequireServiceToken(ctx context.Context) error {
	if !HasServiceToken(ctx) {
		return auth.ErrUnauthenticated
	}
	return nil
}
