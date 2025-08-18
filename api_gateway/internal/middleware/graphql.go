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
		// Extract user information set by pkg/auth middleware
		userID, _ := c.Get("user_id")
		tenantID, _ := c.Get("tenant_id")
		email, _ := c.Get("email")
		role, _ := c.Get("role")

		ctx := c.Request.Context()

		// Extract JWT token from Authorization header for WebSocket subscriptions
		auth := c.GetHeader("Authorization")
		if auth != "" {
			parts := strings.Split(auth, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				ctx = context.WithValue(ctx, "jwt_token", parts[1])
			}
		}

		// If user is authenticated, add user context for GraphQL resolvers
		if userID != nil {
			user := &UserContext{
				UserID:   userID.(string),
				TenantID: tenantID.(string),
				Email:    email.(string),
				Role:     role.(string),
			}

			// Add user to request context for GraphQL resolvers
			ctx = context.WithValue(ctx, "user", user)
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
