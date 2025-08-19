package middleware

import (
	"context"
	"strings"

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

		// Check for user information from Gin context (regular HTTP requests)
		// or from WebSocket context (WebSocket connections)
		var userID, tenantID, email, role interface{}

		// First try to get from Gin context (for HTTP requests with auth middleware)
		userID, exists := c.Get("user_id")
		if !exists {
			// If not in Gin context, check request context (for WebSocket connections)
			userID = ctx.Value("user_id")
		}

		tenantID, exists = c.Get("tenant_id")
		if !exists {
			tenantID = ctx.Value("tenant_id")
		}

		email, exists = c.Get("email")
		if !exists {
			email = ctx.Value("email")
		}

		role, exists = c.Get("role")
		if !exists {
			role = ctx.Value("role")
		}

		// If user is authenticated, add user context for GraphQL resolvers
		if userID != nil {
			userIDStr, ok1 := userID.(string)
			tenantIDStr, ok2 := tenantID.(string)
			emailStr, ok3 := email.(string)
			roleStr, ok4 := role.(string)

			if ok1 && ok2 && ok3 && ok4 {
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
		}

		// Update the request context
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
