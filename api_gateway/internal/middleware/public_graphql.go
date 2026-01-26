package middleware

import (
	"bytes"
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

		s := strings.ToLower(string(body))

		// No mutations allowed
		if strings.Contains(s, "mutation") {
			c.JSON(http.StatusForbidden, gin.H{"error": "mutations not allowed"})
			c.Abort()
			return
		}

		// Allowlist minimal safe operations for public use
		// - serviceInstancesHealth: for public status page
		// - resolveViewerEndpoint: for player endpoint discovery by playback id/content id
		// - resolveIngestEndpoint: for StreamCrafter ingest endpoint discovery by stream key
		if strings.Contains(s, "serviceinstanceshealth") || strings.Contains(s, "resolveviewerendpoint") || strings.Contains(s, "resolveingestendpoint") {
			c.Next()
			return
		}

		c.JSON(http.StatusForbidden, gin.H{"error": "operation not allowed"})
		c.Abort()
	}
}
