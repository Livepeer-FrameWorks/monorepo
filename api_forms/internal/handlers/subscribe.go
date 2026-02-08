package handlers

import (
	"context"
	"errors"
	"frameworks/api_forms/internal/validation"
	"frameworks/pkg/clients/listmonk"
	"frameworks/pkg/logging"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type SubscribeRequest struct {
	Email          string                 `json:"email" binding:"required"`
	Name           string                 `json:"name"`
	TurnstileToken string                 `json:"turnstile_token"`
	PhoneNumber    string                 `json:"phone_number"`
	HumanCheck     string                 `json:"human_check"`
	Behavior       map[string]interface{} `json:"behavior"`
}

type SubscribeHandler struct {
	listmonkClient     ListmonkClient
	turnstileValidator TurnstileVerifier
	defaultListID      int
	turnstileEnabled   bool
	logger             logging.Logger
	metrics            *FormMetrics
}

func NewSubscribeHandler(
	client ListmonkClient,
	validator TurnstileVerifier,
	defaultListID int,
	turnstileEnabled bool,
	logger logging.Logger,
	metrics *FormMetrics,
) *SubscribeHandler {
	return &SubscribeHandler{
		listmonkClient:     client,
		turnstileValidator: validator,
		defaultListID:      defaultListID,
		turnstileEnabled:   turnstileEnabled,
		logger:             logger,
		metrics:            metrics,
	}
}

func (h *SubscribeHandler) Handle(c *gin.Context) {
	var req SubscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.metrics.IncSubscribe("bad_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if c.Request.Header.Get("Idempotency-Key") != "" {
		h.logger.WithFields(logging.Fields{
			"idempotency_key": c.Request.Header.Get("Idempotency-Key"),
			"email":           req.Email,
			"ip":              c.ClientIP(),
		}).Info("Subscribe request received with idempotency key")
	}

	// 1. Validate Bot
	remoteIP := c.ClientIP()
	if h.turnstileEnabled {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		ver, err := h.turnstileValidator.Verify(ctx, req.TurnstileToken, remoteIP)
		if err != nil {
			h.metrics.IncSubscribe("turnstile_error")
			h.logger.WithFields(logging.Fields{
				"error": err.Error(),
				"ip":    remoteIP,
			}).Error("Turnstile verification error on subscribe")
			c.JSON(http.StatusBadGateway, gin.H{"error": "Verification service error"})
			return
		}
		if !ver.Success {
			h.metrics.IncSubscribe("turnstile_failed")
			h.logger.WithFields(logging.Fields{"ip": remoteIP}).Warn("Bot detected on subscribe")
			c.JSON(http.StatusBadRequest, gin.H{"error": "Bot verification failed"})
			return
		}
	} else {
		// Fallback heuristics
		errors := validation.ValidateBot(validation.BotCheckParams{
			PhoneNumber: req.PhoneNumber,
			HumanCheck:  req.HumanCheck,
			Behavior:    req.Behavior,
		})
		if len(errors) > 0 {
			h.metrics.IncSubscribe("validation_failed")
			h.logger.WithFields(logging.Fields{
				"ip":     remoteIP,
				"errors": errors,
			}).Warn("Bot detected on subscribe (heuristics)")
			c.JSON(http.StatusBadRequest, gin.H{"error": "Submission failed validation", "details": errors})
			return
		}
	}

	// 2. Validate Email
	if _, err := mail.ParseAddress(req.Email); err != nil {
		h.metrics.IncSubscribe("invalid_email")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid email"})
		return
	}

	// 3. Call Listmonk
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	normalizedEmail := strings.ToLower(strings.TrimSpace(req.Email))
	if info, exists, err := h.listmonkClient.GetSubscriber(ctx, normalizedEmail); err != nil {
		h.logger.WithError(err).Error("Listmonk lookup failed")
		h.metrics.IncSubscribe("listmonk_error")
		respondListmonkError(c, err)
		return
	} else if exists {
		if info.Status == "blocklisted" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription failed"})
			return
		}

		for _, sub := range info.Lists {
			// Treat only confirmed entries as already subscribed.
			// Unconfirmed double-opt-in members should be allowed to retry so they can receive a new confirmation email.
			if sub.ListID == h.defaultListID && sub.Status == "confirmed" {
				c.JSON(http.StatusOK, gin.H{"success": true})
				return
			}
		}
	}

	err := h.listmonkClient.Subscribe(ctx, normalizedEmail, strings.TrimSpace(req.Name), h.defaultListID, false)
	if err != nil {
		var apiErr *listmonk.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			c.JSON(http.StatusOK, gin.H{"success": true})
			return
		}
		h.metrics.IncSubscribe("listmonk_error")
		h.logger.WithError(err).Error("Listmonk subscribe failed")
		respondListmonkError(c, err)
		return
	}

	h.metrics.IncSubscribe("success")
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func respondListmonkError(c *gin.Context, err error) {
	if isTimeoutError(err) {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "Subscription service timeout"})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "Subscription service unavailable"})
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
