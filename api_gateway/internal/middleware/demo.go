package middleware

import (
	"context"
	"strings"

	"frameworks/pkg/logging"
	"github.com/gin-gonic/gin"
)

// DemoModePostAuth checks for demo mode after authentication has run.
func DemoModePostAuth(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Allow demo mode even for unauthenticated requests.
		// PublicOrJWTAuth intentionally skips auth for some allowlisted queries, so tenant_id may be unset.
		var tenantIDStr string
		if tenantID, ok := c.Get("tenant_id"); ok {
			if s, ok2 := tenantID.(string); ok2 {
				tenantIDStr = s
			}
		}

		isDemoMode := c.GetHeader("X-Demo-Mode") == "true" ||
			c.Query("demo") == "true" ||
			strings.Contains(c.GetHeader("User-Agent"), "API-Explorer-Demo")

		if isDemoMode {
			logger.WithFields(logging.Fields{
				"path":   c.Request.URL.Path,
				"method": c.Request.Method,
			}).Debug("Demo mode request detected")

			// Set demo mode context
			ctx := context.WithValue(c.Request.Context(), "demo_mode", true)

			// Use consistent demo tenant and user for predictable responses
			ctx = context.WithValue(ctx, "demo_tenant_id", "demo_tenant_frameworks")
			ctx = context.WithValue(ctx, "demo_user_id", "demo_user_developer")

			// For unauthenticated / public requests, also set tenant_id/user_id so downstream resolvers that
			// still expect tenant context don't fail.
			if tenantIDStr == "" || strings.HasPrefix(tenantIDStr, "public:") {
				ctx = context.WithValue(ctx, "tenant_id", "demo_tenant_frameworks")
				ctx = context.WithValue(ctx, "user_id", "demo_user_developer")
			}

			// Mark as read-only for safety
			ctx = context.WithValue(ctx, "read_only", true)

			c.Request = c.Request.WithContext(ctx)
		}

		c.Next()
	}
}

// IsDemoMode checks if the current request is in demo mode
func IsDemoMode(ctx context.Context) bool {
	demoMode, ok := ctx.Value("demo_mode").(bool)
	return ok && demoMode
}
