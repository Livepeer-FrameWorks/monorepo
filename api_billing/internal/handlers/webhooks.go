package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/models"
)

// Mollie webhook payload structure
type MollieWebhookPayload struct {
	ID     string `json:"id"`
	Status string `json:"status"` // open, cancelled, pending, authorized, expired, failed, paid
}

// Stripe webhook payload structure
type StripeWebhookPayload struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Metadata struct {
				InvoiceID string `json:"invoice_id"`
				TenantID  string `json:"tenant_id"`
			} `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

// HandleStripeWebhook handles webhook notifications from Stripe payment processor
func HandleStripeWebhook(c middleware.Context) {
	// Verify webhook signature
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err.Error(),
		}).Warn("Failed to read webhook body")
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Invalid request body"})
		return
	}

	// Verify Stripe signature
	signature := c.GetHeader("Stripe-Signature")
	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")

	if webhookSecret != "" && !verifyStripeSignature(body, signature, webhookSecret) {
		logger.WithFields(logging.Fields{
			"signature": signature,
		}).Warn("Invalid Stripe webhook signature")
		c.JSON(http.StatusUnauthorized, middleware.H{"error": "Invalid signature"})
		return
	}

	var payload StripeWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.WithFields(logging.Fields{
			"error": err.Error(),
		}).Warn("Invalid Stripe webhook payload")
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Invalid payload"})
		return
	}

	logger.WithFields(logging.Fields{
		"event_id":   payload.ID,
		"event_type": payload.Type,
		"object_id":  payload.Data.Object.ID,
	}).Info("Received Stripe webhook")

	// Handle payment intent events
	if payload.Type == "payment_intent.succeeded" || payload.Type == "payment_intent.payment_failed" {
		err := handleStripePaymentIntent(payload)
		if err != nil {
			logger.WithError(err).Error("Failed to process Stripe payment intent")
			c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to process webhook"})
			return
		}
	}

	c.JSON(http.StatusOK, middleware.H{"status": "received"})
}

// handleStripePaymentIntent processes Stripe payment intent webhooks
func handleStripePaymentIntent(payload StripeWebhookPayload) error {
	paymentIntentID := payload.Data.Object.ID
	status := payload.Data.Object.Status
	invoiceID := payload.Data.Object.Metadata.InvoiceID

	// Find payment by payment intent ID
	var paymentID string
	var currentStatus string
	err := db.QueryRow(`
		SELECT id, status 
		FROM billing_payments 
		WHERE tx_id = $1 AND method = 'stripe'
	`, paymentIntentID).Scan(&paymentID, &currentStatus)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error":             err.Error(),
			"payment_intent_id": paymentIntentID,
			"provider":          "stripe",
		}).Error("Payment not found for Stripe webhook")
		return err
	}

	// Update payment status based on Stripe status
	var newStatus string
	var confirmedAt *time.Time
	now := time.Now()

	switch status {
	case "succeeded":
		newStatus = "confirmed"
		confirmedAt = &now
	case "payment_failed", "canceled":
		newStatus = "failed"
	case "processing", "requires_payment_method", "requires_confirmation", "requires_action":
		newStatus = "pending"
	default:
		logger.WithFields(logging.Fields{
			"stripe_status": status,
			"payment_id":    paymentID,
		}).Warn("Unknown Stripe payment status")
		return nil
	}

	// Update payment status
	_, err = db.Exec(`
		UPDATE billing_payments 
		SET status = $1, confirmed_at = $2, updated_at = NOW()
		WHERE id = $3
	`, newStatus, confirmedAt, paymentID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error":      err.Error(),
			"payment_id": paymentID,
			"new_status": newStatus,
		}).Error("Failed to update Stripe payment status")
		return err
	}

	// If payment is confirmed, mark invoice as paid
	if newStatus == "confirmed" && invoiceID != "" {
		_, err = db.Exec(`
			UPDATE billing_invoices 
			SET status = 'paid', paid_at = $1, updated_at = NOW()
			WHERE id = $2
		`, now, invoiceID)

		if err != nil {
			logger.WithFields(logging.Fields{
				"error":      err.Error(),
				"invoice_id": invoiceID,
			}).Error("Failed to update invoice status from Stripe webhook")
		} else {
			logger.WithFields(logging.Fields{
				"payment_id": paymentID,
				"invoice_id": invoiceID,
				"provider":   "stripe",
			}).Info("Stripe payment confirmed and invoice marked as paid")

			// Send payment success email
			sendPaymentStatusEmail(invoiceID, paymentID, "stripe", "confirmed")
		}
	}

	// Send payment failed email for failed payments
	if newStatus == "failed" && invoiceID != "" {
		sendPaymentStatusEmail(invoiceID, paymentID, "stripe", "failed")
	}

	return nil
}

// verifyStripeSignature verifies the Stripe webhook signature using HMAC-SHA256
func verifyStripeSignature(payload []byte, signature, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}

	// Parse Stripe signature header format: t=timestamp,v1=signature,v1=signature
	elements := strings.Split(signature, ",")
	var timestamp string
	var signatures []string

	for _, element := range elements {
		parts := strings.SplitN(element, "=", 2)
		if len(parts) != 2 {
			continue
		}

		switch parts[0] {
		case "t":
			timestamp = parts[1]
		case "v1":
			signatures = append(signatures, parts[1])
		}
	}

	if timestamp == "" || len(signatures) == 0 {
		logger.Error("Invalid Stripe signature format: missing timestamp or signatures")
		return false
	}

	// Verify timestamp is within tolerance (5 minutes)
	timestampInt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		logger.WithFields(logging.Fields{
			"timestamp": timestamp,
			"error":     err,
		}).Error("Failed to parse Stripe webhook timestamp")
		return false
	}

	now := time.Now().Unix()
	if now-timestampInt > 300 { // 5 minutes tolerance
		logger.WithFields(logging.Fields{
			"timestamp":   timestampInt,
			"current":     now,
			"age_seconds": now - timestampInt,
		}).Warn("Stripe webhook timestamp too old")
		return false
	}

	// Create signed payload: timestamp + "." + payload
	signedPayload := timestamp + "." + string(payload)

	// Calculate expected signature using HMAC-SHA256
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expectedSignature := hex.EncodeToString(mac.Sum(nil))

	// Compare with provided signatures using constant-time comparison
	for _, providedSig := range signatures {
		if hmac.Equal([]byte(expectedSignature), []byte(providedSig)) {
			return true
		}
	}

	logger.WithFields(logging.Fields{
		"expected":    expectedSignature,
		"provided":    signatures,
		"timestamp":   timestamp,
		"payload_len": len(payload),
	}).Warn("Stripe signature verification failed")

	return false
}

// sendPaymentStatusEmail sends email notification for payment status changes
func sendPaymentStatusEmail(invoiceID, paymentID, provider, status string) {
	// Get invoice and tenant subscription info (billing email is in subscription)
	var tenantID string
	var amount float64
	var currency, billingEmail, tenantName string
	err := db.QueryRow(`
		SELECT bi.tenant_id, bi.amount, bi.currency, ts.billing_email
		FROM billing_invoices bi
		JOIN tenant_subscriptions ts ON bi.tenant_id = ts.tenant_id
		WHERE bi.id = $1
	`, invoiceID).Scan(&tenantID, &amount, &currency, &billingEmail)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error":      err.Error(),
			"invoice_id": invoiceID,
		}).Error("Failed to get invoice and subscription info for payment email notification")
		return
	}

	// Get tenant name from Quartermaster
	tenantInfo, err := getTenantInfo(tenantID)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":      err.Error(),
			"invoice_id": invoiceID,
			"tenant_id":  tenantID,
		}).Error("Failed to get tenant info for payment email notification")
		return
	}
	tenantName = tenantInfo.Name

	if billingEmail == "" {
		logger.WithField("invoice_id", invoiceID).Warn("No tenant email found for payment notification")
		return
	}

	// Send appropriate email based on status
	if status == "confirmed" {
		err = emailService.SendPaymentSuccessEmail(billingEmail, tenantName, invoiceID, amount, currency, provider)
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceID,
				"provider":     provider,
			}).Error("Failed to send payment success email")
		}
	} else if status == "failed" {
		err = emailService.SendPaymentFailedEmail(billingEmail, tenantName, invoiceID, amount, currency, provider)
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceID,
				"provider":     provider,
			}).Error("Failed to send payment failed email")
		}
	}
}

// HandleMollieWebhook handles webhook notifications from Mollie payment processor
func HandleMollieWebhook(c middleware.Context) {
	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.WithFields(logging.Fields{
			"error": err.Error(),
		}).Warn("Invalid webhook payload")
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Invalid payload"})
		return
	}

	logger.WithFields(logging.Fields{
		"payload": payload,
	}).Info("Received Mollie webhook")

	// Find payment by transaction ID
	var paymentID, invoiceID string
	var currentStatus string
	err := db.QueryRow(`
		SELECT id, invoice_id, status 
		FROM billing_payments 
		WHERE tx_id = $1 AND method = 'mollie'
	`, payload["id"]).Scan(&paymentID, &invoiceID, &currentStatus)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error":    err.Error(),
			"tx_id":    payload["id"],
			"provider": "mollie",
		}).Error("Payment not found for webhook")
		c.JSON(http.StatusNotFound, middleware.H{"error": "Payment not found"})
		return
	}

	// Update payment status based on Mollie status
	var newStatus string
	var confirmedAt *time.Time
	now := time.Now()

	switch payload["status"].(string) {
	case "paid":
		newStatus = "confirmed"
		confirmedAt = &now
	case "failed", "cancelled", "expired":
		newStatus = "failed"
	case "pending", "open":
		newStatus = "pending"
	default:
		logger.WithFields(logging.Fields{
			"mollie_status": payload["status"],
			"payment_id":    paymentID,
		}).Warn("Unknown Mollie payment status")
		c.JSON(http.StatusOK, middleware.H{"status": "received"})
		return
	}

	// Update payment status
	_, err = db.Exec(`
		UPDATE billing_payments 
		SET status = $1, confirmed_at = $2, updated_at = NOW()
		WHERE id = $3
	`, newStatus, confirmedAt, paymentID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error":      err.Error(),
			"payment_id": paymentID,
			"new_status": newStatus,
		}).Error("Failed to update payment status")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to update payment"})
		return
	}

	// If payment is confirmed, mark invoice as paid
	if newStatus == "confirmed" {
		_, err = db.Exec(`
			UPDATE billing_invoices 
			SET status = 'paid', paid_at = $1, updated_at = NOW()
			WHERE id = $2
		`, now, invoiceID)

		if err != nil {
			logger.WithFields(logging.Fields{
				"error":      err.Error(),
				"invoice_id": invoiceID,
			}).Error("Failed to update invoice status")
		} else {
			logger.WithFields(logging.Fields{
				"payment_id": paymentID,
				"invoice_id": invoiceID,
				"provider":   "mollie",
			}).Info("Payment confirmed and invoice marked as paid")

			// Send payment success email
			sendPaymentStatusEmail(invoiceID, paymentID, "mollie", "confirmed")
		}
	}

	// Send payment failed email for failed payments
	if newStatus == "failed" {
		sendPaymentStatusEmail(invoiceID, paymentID, "mollie", "failed")
	}

	c.JSON(http.StatusOK, middleware.H{"status": "received"})
}

// getTenantInfo calls Quartermaster to get tenant information using shared client
func getTenantInfo(tenantID string) (*models.Tenant, error) {
	quartermasterURL := config.GetEnv("QUARTERMASTER_URL", "http://localhost:18002")
	serviceToken := config.GetEnv("SERVICE_TOKEN", "")

	client := qmclient.NewClient(qmclient.Config{
		BaseURL:      quartermasterURL,
		ServiceToken: serviceToken,
		Timeout:      10 * time.Second,
		Logger:       logger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, err := client.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenant from Quartermaster: %w", err)
	}

	if response.Error != "" {
		return nil, fmt.Errorf("Quartermaster error: %s", response.Error)
	}

	if response.Tenant == nil {
		return nil, fmt.Errorf("tenant not found")
	}

	// Convert TenantInfo to models.Tenant
	tenant := &models.Tenant{
		ID:   response.Tenant.ID,
		Name: response.Tenant.Name,
	}

	return tenant, nil
}
