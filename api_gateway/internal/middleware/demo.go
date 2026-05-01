package middleware

import (
	"context"
	"strings"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	"github.com/gin-gonic/gin"
)

// DemoModePostAuth checks for demo mode after authentication has run.
func DemoModePostAuth(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		isDemoMode := c.GetHeader("X-Demo-Mode") == "true" ||
			c.Query("demo") == "true" ||
			strings.Contains(c.GetHeader("User-Agent"), "API-Explorer-Demo")

		if !isDemoMode {
			c.Next()
			return
		}

		logger.WithFields(logging.Fields{
			"path":   c.Request.URL.Path,
			"method": c.Request.Method,
		}).Debug("Demo mode request detected")

		// Set demo mode flag - resolvers check IsDemoMode() and return demo data
		ctx := context.WithValue(c.Request.Context(), ctxkeys.KeyDemoMode, true)

		// Set demo identifiers for resolvers that need them
		ctx = context.WithValue(ctx, ctxkeys.KeyDemoTenantID, "5eed517e-ba5e-da7a-517e-ba5eda7a0001")
		ctx = context.WithValue(ctx, ctxkeys.KeyDemoUserID, "5eedface-5e1f-da7a-face-5e1fda7a0001")

		// SECURITY: Do NOT set KeyTenantID or KeyUserID here.
		// Injecting fake credentials would bypass rate limiting, which checks for empty/public tenant_id.
		// Resolvers should check IsDemoMode() and use KeyDemoTenantID/KeyDemoUserID if needed.

		// Mark as read-only for safety
		ctx = context.WithValue(ctx, ctxkeys.KeyReadOnly, true)

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// IsDemoMode checks if the current request is in demo mode
func IsDemoMode(ctx context.Context) bool {
	return ctxkeys.IsDemoMode(ctx)
}
