package handlers

import (
	"context"
	"frameworks/api_forms/internal/validation"
	"frameworks/pkg/clients/listmonk"
	"frameworks/pkg/logging"
	"frameworks/pkg/turnstile"
	"net/http"
	"net/mail"
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
	listmonkClient     *listmonk.Client
	turnstileValidator *turnstile.Validator
	defaultListID      int
	turnstileEnabled   bool
	logger             logging.Logger
}

func NewSubscribeHandler(
	client *listmonk.Client,
	validator *turnstile.Validator,
	defaultListID int,
	turnstileEnabled bool,
	logger logging.Logger,
) *SubscribeHandler {
	return &SubscribeHandler{
		listmonkClient:     client,
		turnstileValidator: validator,
		defaultListID:      defaultListID,
		turnstileEnabled:   turnstileEnabled,
		logger:             logger,
	}
}

func (h *SubscribeHandler) Handle(c *gin.Context) {
	var req SubscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// 1. Validate Bot
	remoteIP := c.ClientIP()
	if h.turnstileEnabled {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		ver, err := h.turnstileValidator.Verify(ctx, req.TurnstileToken, remoteIP)
		if err != nil || !ver.Success {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid email"})
		return
	}

	// 3. Call Listmonk
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	err := h.listmonkClient.Subscribe(ctx, req.Email, req.Name, h.defaultListID, false)
	if err != nil {
		h.logger.WithError(err).Error("Listmonk subscribe failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Subscription failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
