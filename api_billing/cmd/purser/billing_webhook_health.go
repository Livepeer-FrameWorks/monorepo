package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
)

// billingWebhookHealth is separate from liveness so a provider outage does not restart Purser.
func billingWebhookHealth(check func(context.Context) error, logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if check == nil {
			c.JSON(http.StatusOK, gin.H{"status": "not_configured", "read_only": true})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
		defer cancel()
		err := check(ctx)
		if err == nil {
			c.JSON(http.StatusOK, gin.H{"status": "healthy", "read_only": true})
			return
		}
		body := gin.H{"status": "unhealthy", "read_only": true}
		fields := logging.Fields{}
		// Only the typed reason is secret-free; any other error text stays out of the body and log.
		var reason *stripe.BillingWebhookError
		if errors.As(err, &reason) {
			body["reason"], body["message"] = string(reason.Reason), reason.Message
			fields["reason"], fields["detail"] = string(reason.Reason), reason.Message
			if reason.ExpectedURL != "" {
				body["expected_url"], fields["expected_url"] = reason.ExpectedURL, reason.ExpectedURL
			}
			if len(reason.MissingEvents) != 0 {
				body["missing_events"], fields["missing_events"] = reason.MissingEvents, reason.MissingEvents
			}
			if reason.StripeCode != "" {
				body["stripe_code"], fields["stripe_code"] = reason.StripeCode, reason.StripeCode
			}
		} else {
			body["reason"], body["message"] = "unknown", "Cannot verify Stripe billing webhook subscriptions."
			fields["reason"] = "unknown"
		}
		if logger != nil {
			logger.WithFields(fields).Warn("Stripe billing webhook configuration check failed")
		}
		c.JSON(http.StatusServiceUnavailable, body)
	}
}
