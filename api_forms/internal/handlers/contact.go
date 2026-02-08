package handlers

import (
	"context"
	"fmt"
	"frameworks/api_forms/internal/validation"
	"frameworks/pkg/logging"
	"frameworks/pkg/turnstile"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type ContactHandler struct {
	emailSender        EmailSender
	turnstileValidator TurnstileVerifier
	toEmail            string
	turnstileEnabled   bool
	logger             logging.Logger
}

type EmailSender interface {
	SendMail(ctx context.Context, to, subject, htmlBody string) error
}

type TurnstileVerifier interface {
	Verify(ctx context.Context, token, remoteIP string) (*turnstile.VerifyResponse, error)
}

func NewContactHandler(
	emailSender EmailSender,
	turnstileValidator TurnstileVerifier,
	toEmail string,
	turnstileEnabled bool,
	logger logging.Logger,
) *ContactHandler {
	return &ContactHandler{
		emailSender:        emailSender,
		turnstileValidator: turnstileValidator,
		toEmail:            toEmail,
		turnstileEnabled:   turnstileEnabled,
		logger:             logger,
	}
}

func (h *ContactHandler) Handle(c *gin.Context) {
	var req validation.ContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Invalid request format",
		})
		return
	}

	remoteIP := getRemoteIP(c)

	if h.turnstileEnabled {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()

		verification, err := h.turnstileValidator.Verify(ctx, req.TurnstileToken, remoteIP)
		if err != nil {
			h.logger.WithFields(logging.Fields{
				"error": err.Error(),
				"ip":    remoteIP,
			}).Error("Turnstile verification error")

			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error":   "Verification service error",
			})
			return
		}

		if !verification.Success {
			h.logger.WithFields(logging.Fields{
				"error_codes": verification.ErrorCodes,
				"ip":          remoteIP,
			}).Warn("Turnstile verification failed")

			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "Turnstile verification failed",
				"details": verification.ErrorCodes,
			})
			return
		}
	}

	validationErrors := validation.ValidateSubmission(&req, h.turnstileEnabled)

	if len(validationErrors) > 0 {
		h.logger.WithFields(logging.Fields{
			"ip":     remoteIP,
			"errors": validationErrors,
			"name":   req.Name,
			"email":  req.Email,
		}).Warn("Blocked submission")

		response := gin.H{
			"success": false,
			"error":   "Submission failed validation",
		}

		if gin.Mode() == gin.DebugMode {
			response["details"] = validationErrors
		}

		c.JSON(http.StatusBadRequest, response)
		return
	}

	emailSubject := fmt.Sprintf("FrameWorks Contact Form: %s", req.Name)
	emailBody := buildEmailHTML(req.Name, req.Email, req.Company, req.Message, remoteIP)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	if err := h.emailSender.SendMail(ctx, h.toEmail, emailSubject, emailBody); err != nil {
		h.logger.WithFields(logging.Fields{
			"error": err.Error(),
			"name":  req.Name,
			"email": req.Email,
		}).Error("Failed to send email")

		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "Failed to send email",
		})
		return
	}

	h.logger.WithFields(logging.Fields{
		"name":    req.Name,
		"email":   req.Email,
		"company": req.Company,
	}).Info("Email sent successfully")

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Thank you for your message! We'll get back to you soon.",
	})
}

func getRemoteIP(c *gin.Context) string {
	if cfIP := c.GetHeader("CF-Connecting-IP"); cfIP != "" {
		return cfIP
	}

	if forwarded := c.GetHeader("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}

	return c.ClientIP()
}

func buildEmailHTML(name, email, company, message, ip string) string {
	companyText := "Not provided"
	if company != "" {
		companyText = company
	}

	message = strings.ReplaceAll(message, "\n", "<br>")

	return fmt.Sprintf(`
		<h2>New Contact Form Submission</h2>
		<p><strong>Name:</strong> %s</p>
		<p><strong>Email:</strong> %s</p>
		<p><strong>Company:</strong> %s</p>
		<p><strong>Message:</strong></p>
		<div style="background: #f5f5f5; padding: 15px; border-radius: 5px; margin: 10px 0;">
			%s
		</div>
		<hr>
		<p><small>Submitted at: %s</small></p>
		<p><small>IP: %s</small></p>
	`, name, email, companyText, message, time.Now().UTC().Format(time.RFC3339), ip)
}
