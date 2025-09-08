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

// GraphQLContextMiddleware extracts user information from Gin context
// and adds it to the GraphQL resolver context
func GraphQLContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Extract JWT token from Authorization header for regular HTTP requests
		// For WebSocket connections, the JWT token is already set in the InitFunc
		if existingToken := ctx.Value("jwt_token"); existingToken == nil {
			auth := c.GetHeader("Authorization")
			if auth != "" {
				parts := strings.Split(auth, " ")
				if len(parts) == 2 && parts[0] == "Bearer" {
					ctx = context.WithValue(ctx, "jwt_token", parts[1])
				}
			}
		}

		// Check for service token in Authorization header
		// Service tokens are used by providers/admins for bootstrap token management
		if existingServiceToken := ctx.Value("service_token"); existingServiceToken == nil {
			auth := c.GetHeader("Authorization")
			if auth != "" {
				parts := strings.Split(auth, " ")
				// Service tokens use "Service" prefix instead of "Bearer"
				if len(parts) == 2 && parts[0] == "Service" {
					ctx = context.WithValue(ctx, "service_token", parts[1])
				}
			}
		}

		// Check for user information from Gin context (regular HTTP requests)
		// or from WebSocket context (WebSocket connections)
		var userIDStr, tenantIDStr, emailStr, roleStr string
		var authenticated bool

		// First try to get from Gin context (for HTTP requests with auth middleware)
		if userIDVal, exists := c.Get("user_id"); exists {
			if userIDStr, authenticated = userIDVal.(string); authenticated {
				if tenantIDVal, exists := c.Get("tenant_id"); exists {
					tenantIDStr, _ = tenantIDVal.(string)
				}
				if emailVal, exists := c.Get("email"); exists {
					emailStr, _ = emailVal.(string)
				}
				if roleVal, exists := c.Get("role"); exists {
					roleStr, _ = roleVal.(string)
				}
			}
		}

		// If not authenticated via Gin, check request context (for WebSocket connections)
		if !authenticated {
			if userIDVal := ctx.Value("user_id"); userIDVal != nil {
				if userIDStr, authenticated = userIDVal.(string); authenticated {
					if tenantIDVal := ctx.Value("tenant_id"); tenantIDVal != nil {
						tenantIDStr, _ = tenantIDVal.(string)
					}
					if emailVal := ctx.Value("email"); emailVal != nil {
						emailStr, _ = emailVal.(string)
					}
					if roleVal := ctx.Value("role"); roleVal != nil {
						roleStr, _ = roleVal.(string)
					}
				}
			}
		}

		// If user is authenticated, add user context for GraphQL resolvers
		if authenticated && userIDStr != "" && tenantIDStr != "" && emailStr != "" && roleStr != "" {
			user := &UserContext{
				UserID:   userIDStr,
				TenantID: tenantIDStr,
				Email:    emailStr,
				Role:     roleStr,
			}

			// Add user to request context for GraphQL resolvers
			ctx = context.WithValue(ctx, "user", user)

			// Also add individual values that resolvers expect
			ctx = context.WithValue(ctx, "user_id", userIDStr)
			ctx = context.WithValue(ctx, "tenant_id", tenantIDStr)
			ctx = context.WithValue(ctx, "email", emailStr)
			ctx = context.WithValue(ctx, "role", roleStr)
		}

		// Update the request context
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// GraphQLAttachLoaders attaches per-request dataloaders to the context
func GraphQLAttachLoaders(sc *clients.ServiceClients) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		lds := loaders.New(sc)
		ctx = context.WithValue(ctx, "loaders", lds)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// GetUserFromContext extracts user information from GraphQL resolver context
func GetUserFromContext(ctx context.Context) *UserContext {
	if user, ok := ctx.Value("user").(*UserContext); ok {
		return user
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
	token, ok := ctx.Value("service_token").(string)
	return ok && token != ""
}

// RequireServiceToken checks if service token is present and returns error if not
func RequireServiceToken(ctx context.Context) error {
	if !HasServiceToken(ctx) {
		return auth.ErrUnauthenticated
	}
	return nil
}
