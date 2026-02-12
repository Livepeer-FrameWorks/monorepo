package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// PublicGraphQLAllowlist allows only specific read-only GraphQL operations
// on the unauthenticated /graphql/public endpoint.
// It rejects mutations and any queries not explicitly allowlisted.
func PublicGraphQLAllowlist() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read body
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			c.Abort()
			return
		}
		// Restore body for downstream handler
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		var req struct {
			Query         string `json:"query"`
			OperationName string `json:"operationName"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
			c.Abort()
			return
		}

		q := strings.ToLower(req.Query)
		if strings.Contains(q, "mutation") {
			c.JSON(http.StatusForbidden, gin.H{"error": "mutations not allowed"})
			c.Abort()
			return
		}

		op := strings.ToLower(req.OperationName)
		allowed := false
		for _, a := range allowlistedOperations {
			if op == a {
				allowed = true
				break
			}
		}
		if !allowed {
			for _, a := range allowlistedOperations {
				if strings.Contains(q, a) {
					allowed = true
					break
				}
			}
		}
		if allowed {
			c.Next()
			return
		}

		c.JSON(http.StatusForbidden, gin.H{"error": "operation not allowed"})
		c.Abort()
	}
}
