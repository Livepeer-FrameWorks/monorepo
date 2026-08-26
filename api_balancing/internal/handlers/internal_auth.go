package handlers

import (
	"context"
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/gin-gonic/gin"
)

// RequireInternalRead accepts the provider service credential or a platform-
// operator JWT. It is for private diagnostics and compatibility reads only.
func RequireInternalRead() gin.HandlerFunc {
	return requireInternalIdentity(false)
}

// RequireInternalMutation requires a platform-operator JWT. The shared service
// credential is intentionally insufficient for operator mutations.
func RequireInternalMutation() gin.HandlerFunc {
	return requireInternalIdentity(true)
}

// RequireInternalCompatibility preserves read-only Mist diagnostics for the
// service credential while ensuring the legacy root weights query always takes
// the stronger operator-JWT path.
func RequireInternalCompatibility() gin.HandlerFunc {
	read := requireInternalIdentity(false)
	mutation := requireInternalIdentity(true)
	return func(c *gin.Context) {
		if c.Query("weights") != "" {
			mutation(c)
			return
		}
		read(c)
	}
}

func requireInternalIdentity(mutation bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bearer authorization required"})
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if !mutation {
			serviceToken := os.Getenv("SERVICE_TOKEN")
			if serviceToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(serviceToken)) == 1 {
				ctx := context.WithValue(c.Request.Context(), ctxkeys.KeyAuthType, "service")
				c.Request = c.Request.WithContext(ctx)
				c.Next()
				return
			}
		}

		claims, err := auth.ValidateJWT(token, []byte(os.Getenv("JWT_SECRET")))
		if err != nil || !claims.HasRole(auth.RolePlatformOperator) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "platform operator authorization required"})
			return
		}
		ctx := context.WithValue(c.Request.Context(), ctxkeys.KeyAuthType, "jwt")
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, claims.TenantID)
		ctx = context.WithValue(ctx, ctxkeys.KeyRole, claims.Role)
		ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
