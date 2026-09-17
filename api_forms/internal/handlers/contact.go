package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/api_forms/internal/validation"
	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"

	"github.com/gin-gonic/gin"
)

const contactMaxBodyBytes = 128 * 1024

type ContactHandler struct {
	emailSender        EmailSender
	turnstileValidator TurnstileVerifier
	toEmail            string
	emailSubjectPrefix string
	successMessage     string
	turnstileEnabled   bool
	logger             logging.Logger
	metrics            *FormMetrics
	activityEmitter    ActivityEmitter
}

func NewContactHandler(
	emailSender EmailSender,
	turnstileValidator TurnstileVerifier,
	toEmail string,
	emailSubjectPrefix string,
	successMessage string,
	turnstileEnabled bool,
	logger logging.Logger,
	metrics *FormMetrics,
	activityEmitter ActivityEmitter,
) *ContactHandler {
	return &ContactHandler{
		emailSender:        emailSender,
		turnstileValidator: turnstileValidator,
		toEmail:            toEmail,
		emailSubjectPrefix: emailSubjectPrefix,
		successMessage:     successMessage,
		turnstileEnabled:   turnstileEnabled,
		logger:             logger,
		metrics:            metrics,
		activityEmitter:    activityEmitter,
	}
}

func (h *ContactHandler) Handle(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, contactMaxBodyBytes)
	var req validation.ContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.metrics.IncContact("bad_request")
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "Request body too large",
			})
			return
		}
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
			h.metrics.IncContact("turnstile_error")
			h.logger.WithFields(logging.Fields{
				"error": err.Error(),
				"ip":    remoteIP,
			}).Error("Turnstile verification error")

			c.JSON(http.StatusBadGateway, gin.H{
				"success": false,
				"error":   "Verification service error",
			})
			return
		}

		if !verification.Success {
			h.metrics.IncContact("turnstile_failed")
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
		h.metrics.IncContact("validation_failed")
		h.logger.WithFields(logging.Fields{
			"ip":     remoteIP,
			"errors": validationErrors,
			"name":   redactName(req.Name),
			"email":  redactEmail(req.Email),
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

	emailSubject := fmt.Sprintf("%s: %s", h.emailSubjectPrefix, req.Name)
	emailBody, err := renderContactEmail(req.Name, req.Email, req.Company, req.Message, remoteIP)
	if err != nil {
		h.metrics.IncContact("email_error")
		h.logger.WithError(err).Error("Failed to render contact email")
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "Failed to prepare email",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	if err := h.emailSender.SendMail(ctx, h.toEmail, emailSubject, emailBody); err != nil {
		h.metrics.IncContact("email_error")
		h.logger.WithFields(logging.Fields{
			"error": err.Error(),
			"name":  redactName(req.Name),
			"email": redactEmail(req.Email),
		}).Error("Failed to send email")

		c.JSON(http.StatusBadGateway, gin.H{
			"success": false,
			"error":   "Failed to send email",
		})
		return
	}

	h.metrics.IncContact("success")
	h.logger.WithFields(logging.Fields{
		"name":    redactName(req.Name),
		"email":   redactEmail(req.Email),
		"company": req.Company,
	}).Info("Email sent successfully")
	if h.activityEmitter != nil {
		if err := h.activityEmitter.EmitActivity(c.Request.Context(), serviceevents.MarketingContactDelivered); err != nil {
			h.logger.WithError(err).Warn("Failed to emit contact delivery activity")
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": h.successMessage,
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
	body, err := renderContactEmail(name, email, company, message, ip)
	if err != nil {
		return ""
	}
	return body
}

func renderContactEmail(name, email, company, message, ip string) (string, error) {
	companyText := "Not provided"
	if company != "" {
		companyText = company
	}
	data := contactEmailData{
		Name:         name,
		Email:        email,
		Company:      companyText,
		MessageLines: strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n"),
		SubmittedAt:  time.Now().UTC().Format(time.RFC3339),
		IP:           ip,
	}
	return emailpkg.RenderLayout(emailpkg.LayoutData{
		LogoURL:      emailpkg.PublicLogoURL(os.Getenv("EMAIL_LOGO_URL"), os.Getenv("WEBAPP_PUBLIC_URL")),
		Preheader:    "A new website contact request was submitted.",
		Eyebrow:      "Steward · Website",
		Title:        "New contact form submission",
		SupportEmail: contactSupportEmail(),
		Content:      data,
	}, contactEmailTemplate, nil)
}

type contactEmailData struct {
	Name         string
	Email        string
	Company      string
	MessageLines []string
	SubmittedAt  string
	IP           string
}

func contactSupportEmail() string {
	if supportEmail := strings.TrimSpace(os.Getenv("SUPPORT_EMAIL")); supportEmail != "" {
		return supportEmail
	}
	return "support@frameworks.network"
}

const contactEmailTemplate = `<table width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; border-collapse:collapse; margin:0 0 20px; font-size:13px;">
  <tr><th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #e7edf0; background:#f7fafb;">Name</th><td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;">{{.Name}}</td></tr>
  <tr><th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #e7edf0; background:#f7fafb;">Email</th><td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;"><a href="mailto:{{.Email}}" style="color:#0f4b6e;">{{.Email}}</a></td></tr>
  <tr><th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #e7edf0; background:#f7fafb;">Company</th><td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;">{{.Company}}</td></tr>
</table>
<h2 style="color:#24283b; margin:24px 0 10px; font-size:18px; line-height:24px;">Message</h2>
<div style="background:#f7fafb; border-left:3px solid #0f4b6e; padding:15px; margin:10px 0 20px; color:#24283b; font-size:14px; line-height:21px;">{{range $index, $line := .MessageLines}}{{if $index}}<br>{{end}}{{$line}}{{end}}</div>
<p style="color:#667085; font-size:12px; line-height:18px; margin-top:28px;">Submitted at: {{.SubmittedAt}}<br>IP: {{.IP}}</p>`
