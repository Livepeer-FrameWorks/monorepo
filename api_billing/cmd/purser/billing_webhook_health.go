package main

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// billingWebhookHealth is separate from liveness so a provider outage does not restart Purser.
func billingWebhookHealth(check func(context.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if check == nil {
			c.JSON(http.StatusOK, gin.H{"status": "not_configured", "read_only": true})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
		defer cancel()
		if err := check(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unhealthy", "read_only": true,
				"message": "Cannot verify Stripe billing webhook subscriptions. Check provider access and enable all required events, including invoice.finalized and credit_note.created/updated/voided.",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "healthy", "read_only": true})
	}
}
