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
		tenantID, ok := c.Get("tenant_id")
		tenantIDStr, ok := tenantID.(string)
		if !ok || tenantIDStr == "" || strings.HasPrefix(tenantIDStr, "public:") {
			c.Next()
			return
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
