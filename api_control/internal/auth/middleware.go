package auth

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// APIAuthMiddleware validates developer API tokens (Commodore-specific)
func APIAuthMiddleware(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
			c.Abort()
			return
		}

		// Extract Bearer token
		parts := strings.Split(auth, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header"})
			c.Abort()
			return
		}

		// Validate API token
		token, err := ValidateAPIToken(db, parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			c.Abort()
			return
		}

		// Set token info in context
		c.Set("user_id", token.UserID)
		c.Set("tenant_id", token.TenantID)
		c.Set("token_permissions", token.Permissions)

		c.Next()
	}
}
