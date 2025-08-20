package middleware

import (
	"context"
	"net/http"
	"strings"

	"frameworks/pkg/logging"
	"github.com/gin-gonic/gin"
)

// DemoMode middleware detects demo mode requests and sets appropriate context
func DemoMode(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check for demo mode indicators
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

			// Override JWT context values for demo mode
			ctx = context.WithValue(ctx, "tenant_id", "demo_tenant_frameworks")
			ctx = context.WithValue(ctx, "user_id", "demo_user_developer")

			// Mark as read-only for safety
			ctx = context.WithValue(ctx, "read_only", true)

			c.Request = c.Request.WithContext(ctx)
		}

		c.Next()
	}
}

// DemoModeHTTP provides the standard HTTP middleware version
func DemoModeHTTP(logger logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Check for demo mode indicators
			isDemoMode := r.Header.Get("X-Demo-Mode") == "true" ||
				r.URL.Query().Get("demo") == "true" ||
				strings.Contains(r.Header.Get("User-Agent"), "API-Explorer-Demo")

			if isDemoMode {
				logger.WithFields(logging.Fields{
					"path":   r.URL.Path,
					"method": r.Method,
				}).Debug("Demo mode request detected")

				// Set demo mode context
				ctx = context.WithValue(ctx, "demo_mode", true)

				// Use consistent demo tenant and user for predictable responses
				ctx = context.WithValue(ctx, "demo_tenant_id", "demo_tenant_frameworks")
				ctx = context.WithValue(ctx, "demo_user_id", "demo_user_developer")

				// Override JWT context values for demo mode
				ctx = context.WithValue(ctx, "tenant_id", "demo_tenant_frameworks")
				ctx = context.WithValue(ctx, "user_id", "demo_user_developer")

				// Mark as read-only for safety
				ctx = context.WithValue(ctx, "read_only", true)

				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// IsDemoMode checks if the current request is in demo mode
func IsDemoMode(ctx context.Context) bool {
	demoMode, ok := ctx.Value("demo_mode").(bool)
	return ok && demoMode
}

// GetDemoTenantID returns the demo tenant ID if in demo mode
func GetDemoTenantID(ctx context.Context) string {
	if IsDemoMode(ctx) {
		if tenantID, ok := ctx.Value("demo_tenant_id").(string); ok {
			return tenantID
		}
	}
	return ""
}

// GetDemoUserID returns the demo user ID if in demo mode
func GetDemoUserID(ctx context.Context) string {
	if IsDemoMode(ctx) {
		if userID, ok := ctx.Value("demo_user_id").(string); ok {
			return userID
		}
	}
	return ""
}
