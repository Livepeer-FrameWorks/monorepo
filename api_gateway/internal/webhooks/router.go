// Package webhooks provides a generic webhook routing layer for the Gateway.
// External webhooks (Stripe, Mollie, etc.) are received here and forwarded
// to internal services via gRPC, keeping those services unexposed.
//
// Architecture:
//
//	Payment Provider    Gateway (public)       Internal Service (mesh)
//	     |                   |                       |
//	     | POST /webhooks/   |                       |
//	     | billing/stripe    |                       |
//	     |------------------>|                       |
//	     |                   | gRPC: ProcessWebhook  |
//	     |                   |---------------------->|
//	     |                   |                       | Verify signature
//	     |                   |                       | Process webhook
//	     |                   |<----------------------|
//	     |<------------------|                       |
//
// Signature verification happens in the target service (secrets stay there).
package webhooks

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
)

// ServiceHandler defines the interface for services that can receive webhooks.
// Services implement this by exposing a WebhookService gRPC endpoint.
type ServiceHandler interface {
	ProcessWebhook(ctx context.Context, req *pb.WebhookRequest) (*pb.WebhookResponse, error)
}

// Router routes incoming webhooks to the appropriate internal service via gRPC.
type Router struct {
	handlers map[string]ServiceHandler // service name -> handler
	logger   logging.Logger
	limiter  *WebhookRateLimiter
}

const maxWebhookBodyBytes int64 = 1 << 20

var allowedProviders = map[string][]string{
	"billing": {"stripe", "mollie"},
}

// NewRouter creates a new webhook router with the given service handlers.
// Service names should match the URL path: /webhooks/{service}/{provider}
func NewRouter(logger logging.Logger) *Router {
	rateLimitPerMin := config.GetEnvInt("WEBHOOK_RATE_LIMIT_PER_MIN", 300)
	var limiter *WebhookRateLimiter
	if rateLimitPerMin > 0 {
		limiter = NewWebhookRateLimiter(rateLimitPerMin, time.Minute, 10*time.Minute)
	}
	return &Router{
		handlers: make(map[string]ServiceHandler),
		logger:   logger,
		limiter:  limiter,
	}
}

// RegisterService registers a service handler for webhook routing.
// The service name is used in the URL path: /webhooks/{serviceName}/{provider}
func (r *Router) RegisterService(name string, handler ServiceHandler) {
	r.handlers[name] = handler
	r.logger.WithField("service", name).Info("Registered webhook handler")
}

// Handle is the Gin handler for incoming webhooks.
// Route: POST /webhooks/:service/:provider
func (r *Router) Handle(c *gin.Context) {
	service := c.Param("service")
	provider := c.Param("provider")

	if !isProviderAllowed(service, provider) {
		r.logger.WithFields(logging.Fields{
			"service":  service,
			"provider": provider,
		}).Warn("Webhook received for invalid provider")
		c.JSON(http.StatusNotFound, gin.H{"error": "invalid endpoint"})
		return
	}

	if r.limiter != nil {
		if !r.limiter.Allow(c.ClientIP()) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
	}

	// Find handler for this service
	handler, ok := r.handlers[service]
	if !ok {
		r.logger.WithFields(logging.Fields{
			"service":  service,
			"provider": provider,
		}).Warn("Webhook received for unknown service")
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown service"})
		return
	}

	if c.Request.ContentLength > maxWebhookBodyBytes {
		r.logger.WithFields(logging.Fields{
			"service":  service,
			"provider": provider,
			"size":     c.Request.ContentLength,
		}).Warn("Webhook payload too large")
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "payload too large"})
		return
	}

	// Read raw body
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxWebhookBodyBytes+1))
	if err != nil {
		r.logger.WithError(err).Error("Failed to read webhook body")
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	if int64(len(body)) > maxWebhookBodyBytes {
		r.logger.WithFields(logging.Fields{
			"service":  service,
			"provider": provider,
			"size":     len(body),
		}).Warn("Webhook payload too large")
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "payload too large"})
		return
	}

	// Collect all headers (includes signature headers like Stripe-Signature)
	headers := make(map[string]string)
	for key, values := range c.Request.Header {
		// Join multiple values with comma (standard HTTP semantics)
		headers[key] = strings.Join(values, ",")
	}

	// Build gRPC request
	req := &pb.WebhookRequest{
		Provider:   provider,
		Body:       body,
		Headers:    headers,
		SourceIp:   c.ClientIP(),
		ReceivedAt: time.Now().Unix(),
	}

	// Log webhook receipt (no sensitive data)
	r.logger.WithFields(logging.Fields{
		"service":   service,
		"provider":  provider,
		"body_size": len(body),
		"source_ip": c.ClientIP(),
	}).Debug("Routing webhook to service")

	// Forward to service via gRPC
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	resp, err := handler.ProcessWebhook(ctx, req)
	if err != nil {
		r.logger.WithError(err).WithFields(logging.Fields{
			"service":  service,
			"provider": provider,
		}).Error("Webhook processing failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "service unavailable"})
		return
	}

	// Return appropriate HTTP status
	statusCode := http.StatusOK
	if resp.StatusCode > 0 {
		statusCode = int(resp.StatusCode)
	}

	if resp.Success {
		c.JSON(statusCode, gin.H{"status": "ok"})
	} else {
		r.logger.WithFields(logging.Fields{
			"service":  service,
			"provider": provider,
			"error":    resp.Error,
		}).Warn("Webhook rejected by service")
		c.JSON(statusCode, gin.H{"error": resp.Error})
	}
}

func isProviderAllowed(service, provider string) bool {
	allowed, ok := allowedProviders[service]
	if !ok {
		return false
	}
	return slices.Contains(allowed, provider)
}

// HandleHealth returns a simple health check for the webhook endpoint.
// Payment providers often check this before sending webhooks.
func (r *Router) HandleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"services": len(r.handlers),
	})
}
