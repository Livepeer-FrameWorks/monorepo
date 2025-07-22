package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ServiceAuthMiddleware validates service-to-service auth tokens
func ServiceAuthMiddleware(expectedToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No authorization header"})
			c.Abort()
			return
		}

		parts := strings.Split(auth, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header"})
			c.Abort()
			return
		}

		if parts[1] != expectedToken {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid service token"})
			c.Abort()
			return
		}

		c.Next()
	}
}
