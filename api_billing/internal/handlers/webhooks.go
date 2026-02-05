package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"
	pb "frameworks/pkg/proto"
)

var errMollieResourceNotFound = errors.New("mollie resource not found")

// Stripe webhook payload structure
// Flexible struct to handle multiple event types (payment_intent, subscription, invoice, checkout)
type StripeWebhookPayload struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"` // Parsed per event type
	} `json:"data"`
}

// StripePaymentIntentObject for payment_intent events
type StripePaymentIntentObject struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Metadata struct {
		InvoiceID string `json:"invoice_id"`
		TenantID  string `json:"tenant_id"`
	} `json:"metadata"`
}

// StripeCheckoutSessionObject for checkout.session.completed events
type StripeCheckoutSessionObject struct {
	ID           string `json:"id"`
	CustomerID   string `json:"customer"`
	Subscription string `json:"subscription"`
	Mode         string `json:"mode"` // "subscription" or "payment"
	Metadata     struct {
		Purpose     string `json:"purpose"`
		ReferenceID string `json:"reference_id"`
		TenantID    string `json:"tenant_id"`
		TierID      string `json:"tier_id"`
		ClusterID   string `json:"cluster_id"`
	} `json:"metadata"`
}

// StripeSubscriptionObject for customer.subscription.* events
type StripeSubscriptionObject struct {
	ID                string `json:"id"`
	CustomerID        string `json:"customer"`
	Status            string `json:"status"` // active, past_due, canceled, trialing, etc.
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
	Items             struct {
		Data []struct {
			ID               string `json:"id"`
			CurrentPeriodEnd int64  `json:"current_period_end"`
		} `json:"data"`
	} `json:"items"`
	Metadata struct {
		Purpose     string `json:"purpose"`
		ReferenceID string `json:"reference_id"`
		TenantID    string `json:"tenant_id"`
		TierID      string `json:"tier_id"`
		ClusterID   string `json:"cluster_id"`
	} `json:"metadata"`
}

// StripeInvoiceObject for invoice.* events
type StripeInvoiceObject struct {
	ID             string `json:"id"`
	CustomerID     string `json:"customer"`
	SubscriptionID string `json:"subscription"`
	Status         string `json:"status"` // paid, open, draft, uncollectible, void
	AmountDue      int64  `json:"amount_due"`
	AmountPaid     int64  `json:"amount_paid"`
	Currency       string `json:"currency"`
	AttemptCount   int    `json:"attempt_count"`
	Metadata       struct {
		TenantID string `json:"tenant_id"`
	} `json:"metadata"`
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
func sendPaymentStatusEmail(invoiceID, provider, status string) {
	// Get invoice and tenant subscription info (billing email is in subscription)
	var tenantID string
	var amount float64
	var currency, billingEmail, tenantName string
	err := db.QueryRow(`
		SELECT bi.tenant_id, bi.amount, bi.currency, ts.billing_email
		FROM purser.billing_invoices bi
		JOIN purser.tenant_subscriptions ts ON bi.tenant_id = ts.tenant_id
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
	switch status {
	case "confirmed":
		err = emailService.SendPaymentSuccessEmail(billingEmail, tenantName, invoiceID, amount, currency, provider)
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceID,
				"provider":     provider,
			}).Error("Failed to send payment success email")
		}
	case "failed":
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

func sendTenantPaymentStatusEmail(tenantID, invoiceRef, provider, status string, amount float64, currency string) {
	if tenantID == "" {
		return
	}

	var billingEmail string
	err := db.QueryRow(`
		SELECT billing_email FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
	`, tenantID).Scan(&billingEmail)
	if err != nil {
		logger.WithFields(logging.Fields{
			"error":     err.Error(),
			"tenant_id": tenantID,
		}).Error("Failed to get billing email for tenant payment notification")
		return
	}
	if billingEmail == "" {
		logger.WithField("tenant_id", tenantID).Warn("No tenant email found for payment notification")
		return
	}

	tenantName := ""
	if tenantInfo, err := getTenantInfo(tenantID); err == nil && tenantInfo != nil {
		tenantName = tenantInfo.Name
	}

	currency = strings.ToUpper(currency)
	switch status {
	case "confirmed":
		err = emailService.SendPaymentSuccessEmail(billingEmail, tenantName, invoiceRef, amount, currency, provider)
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceRef,
				"provider":     provider,
			}).Error("Failed to send payment success email")
		}
	case "failed":
		err = emailService.SendPaymentFailedEmail(billingEmail, tenantName, invoiceRef, amount, currency, provider)
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceRef,
				"provider":     provider,
			}).Error("Failed to send payment failed email")
		}
	}
}

// ============================================================================
// GRPC WEBHOOK PROCESSING
// These functions are called by the gRPC server (ProcessWebhook) instead of
// the HTTP handlers. They receive raw body and headers from the Gateway.
// ============================================================================

// ProcessStripeWebhookGRPC processes a Stripe webhook received via gRPC from the Gateway.
// Returns (success, error_message, http_status_code).
func ProcessStripeWebhookGRPC(body []byte, headers map[string]string) (bool, string, int) {
	// Verify Stripe signature
	signature := headerValue(headers, "Stripe-Signature")
	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")

	if webhookSecret == "" {
		logger.Error("STRIPE_WEBHOOK_SECRET not configured; rejecting webhook")
		return false, "Webhook verification not configured", 503
	} else if !verifyStripeSignature(body, signature, webhookSecret) {
		logger.WithFields(logging.Fields{
			"signature": signature,
		}).Warn("Invalid Stripe webhook signature")
		recordWebhookSignatureFailure("stripe")
		return false, "Invalid signature", 401
	}

	var payload StripeWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.WithFields(logging.Fields{
			"error": err.Error(),
		}).Warn("Invalid Stripe webhook payload")
		return false, "Invalid payload", 400
	}

	logger.WithFields(logging.Fields{
		"event_id":   payload.ID,
		"event_type": payload.Type,
	}).Info("Received Stripe webhook via gRPC")

	// Check idempotency - skip if already processed
	if isWebhookAlreadyProcessed("stripe", payload.ID) {
		logger.WithField("event_id", payload.ID).Debug("Stripe webhook already processed, skipping")
		return true, "", 200
	}

	var err error
	switch {
	case payload.Type == "payment_intent.succeeded" || payload.Type == "payment_intent.payment_failed":
		err = handleStripePaymentIntentGRPC(payload)
	case payload.Type == "checkout.session.completed":
		err = DispatchStripeCheckoutCompleted(payload.Data.Object)
	case strings.HasPrefix(payload.Type, "customer.subscription."):
		err = handleStripeSubscriptionEvent(payload)
	case payload.Type == "invoice.paid":
		err = handleStripeInvoicePaid(payload)
	case payload.Type == "invoice.payment_failed":
		err = handleStripeInvoiceFailed(payload)
	default:
		logger.WithField("event_type", payload.Type).Debug("Ignoring unhandled Stripe event type")
	}

	if err != nil {
		logger.WithError(err).WithField("event_type", payload.Type).Error("Failed to process Stripe webhook")
		return false, "Failed to process webhook", 500
	}

	// Mark as processed
	markWebhookProcessed("stripe", payload.ID, payload.Type)
	return true, "", 200
}

// isWebhookAlreadyProcessed checks if a webhook event was already processed
func isWebhookAlreadyProcessed(provider, eventID string) bool {
	if db == nil {
		return false
	}
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM purser.webhook_events WHERE provider = $1 AND event_id = $2)
	`, provider, eventID).Scan(&exists)
	return err == nil && exists
}

// markWebhookProcessed marks a webhook event as processed
func markWebhookProcessed(provider, eventID, eventType string) {
	if db == nil {
		return
	}
	_, err := db.Exec(`
		INSERT INTO purser.webhook_events (provider, event_id, event_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (provider, event_id) DO NOTHING
	`, provider, eventID, eventType)
	if err != nil {
		logger.WithError(err).Warn("Failed to mark webhook as processed")
	}
}

// handleStripePaymentIntentGRPC handles payment_intent events
func handleStripePaymentIntentGRPC(payload StripeWebhookPayload) error {
	var obj StripePaymentIntentObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse payment intent: %w", err)
	}

	invoiceID := obj.Metadata.InvoiceID
	if invoiceID == "" {
		logger.WithField("payment_intent_id", obj.ID).Debug("No invoice_id in payment intent metadata, skipping")
		return nil
	}

	status := "confirmed"
	if payload.Type == "payment_intent.payment_failed" {
		status = "failed"
	}

	_, err := db.Exec(`
		UPDATE purser.billing_payments
		SET status = $1, updated_at = NOW(), confirmed_at = CASE WHEN $1 = 'confirmed' THEN NOW() ELSE confirmed_at END
		WHERE invoice_id = $2 AND method = 'stripe'
	`, status, invoiceID)
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}

	logger.WithFields(logging.Fields{
		"payment_intent_id": obj.ID,
		"invoice_id":        invoiceID,
		"status":            status,
	}).Info("Updated payment status from Stripe webhook")

	go sendPaymentStatusEmail(invoiceID, "stripe", status)

	var paymentID, tenantID, currency string
	var amount float64
	if err := db.QueryRow(`
		SELECT p.id, i.tenant_id, p.amount, p.currency
		FROM purser.billing_payments p
		JOIN purser.billing_invoices i ON p.invoice_id = i.id
		WHERE p.invoice_id = $1 AND p.method = 'stripe'
		ORDER BY p.created_at DESC
		LIMIT 1
	`, invoiceID).Scan(&paymentID, &tenantID, &amount, &currency); err == nil && tenantID != "" {
		eventType := eventPaymentSucceeded
		if status == "failed" {
			eventType = eventPaymentFailed
		}
		emitBillingEvent(eventType, tenantID, "payment", paymentID, &pb.BillingEvent{
			PaymentId: paymentID,
			InvoiceId: invoiceID,
			Amount:    amount,
			Currency:  currency,
			Provider:  "stripe",
			Status:    status,
		})
	}

	return nil
}

// handleStripeCheckoutCompleted is now handled by DispatchStripeCheckoutCompleted in checkout.go
// The new dispatcher routes based on metadata.purpose (subscription, invoice, prepaid)

// handleStripeSubscriptionEvent handles customer.subscription.* events
func handleStripeSubscriptionEvent(payload StripeWebhookPayload) error {
	var obj StripeSubscriptionObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse subscription: %w", err)
	}

	ourStatus := MapStripeSubscriptionStatus(obj.Status, obj.CancelAtPeriodEnd)

	// Get period end from subscription items
	var periodEnd *time.Time
	if len(obj.Items.Data) > 0 && obj.Items.Data[0].CurrentPeriodEnd > 0 {
		t := time.Unix(obj.Items.Data[0].CurrentPeriodEnd, 0)
		periodEnd = &t
	}

	if obj.Metadata.ClusterID != "" || obj.Metadata.Purpose == "cluster_subscription" {
		if err := updateClusterSubscriptionFromStripe(obj, ourStatus, periodEnd); err != nil {
			return err
		}
		return nil
	}

	// Find tenant by Stripe subscription ID
	var tenantID string
	err := db.QueryRow(`
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_subscription_id = $1
	`, obj.ID).Scan(&tenantID)
	if err != nil {
		// Try to find by customer ID if subscription ID not found
		err = db.QueryRow(`
			SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id = $1
		`, obj.CustomerID).Scan(&tenantID)
		if err != nil {
			// Try metadata fallback
			if obj.Metadata.TenantID != "" {
				tenantID = obj.Metadata.TenantID
			} else {
				logger.WithField("subscription_id", obj.ID).Warn("No tenant found for Stripe subscription")
				return nil
			}
		}
	}

	_, err = db.Exec(`
		UPDATE purser.tenant_subscriptions
		SET stripe_subscription_status = $1,
		    status = $2,
		    stripe_current_period_end = $3,
		    updated_at = NOW()
		WHERE tenant_id = $4
	`, obj.Status, ourStatus, periodEnd, tenantID)
	if err != nil {
		return fmt.Errorf("failed to update subscription status: %w", err)
	}

	logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": obj.ID,
		"stripe_status":   obj.Status,
		"our_status":      ourStatus,
	}).Info("Updated subscription status from Stripe webhook")

	subscriptionID := ""
	_ = db.QueryRow(`SELECT id FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&subscriptionID)
	if subscriptionID == "" {
		subscriptionID = obj.ID
	}
	eventType := eventSubscriptionUpdated
	if ourStatus == "cancelled" {
		eventType = eventSubscriptionCanceled
	}
	emitBillingEvent(eventType, tenantID, "subscription", subscriptionID, &pb.BillingEvent{
		SubscriptionId: subscriptionID,
		Provider:       "stripe",
		Status:         ourStatus,
	})

	return nil
}

// handleStripeInvoicePaid handles invoice.paid events
func handleStripeInvoicePaid(payload StripeWebhookPayload) error {
	var obj StripeInvoiceObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse invoice: %w", err)
	}

	// Find tenant by Stripe customer ID
	var tenantID string
	err := db.QueryRow(`
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id = $1
	`, obj.CustomerID).Scan(&tenantID)
	if err != nil {
		if obj.Metadata.TenantID != "" {
			tenantID = obj.Metadata.TenantID
		} else {
			logger.WithField("customer_id", obj.CustomerID).Debug("No tenant found for Stripe customer, skipping invoice.paid")
			return nil
		}
	}

	// Reset dunning attempts on successful payment
	_, err = db.Exec(`
		UPDATE purser.tenant_subscriptions
		SET dunning_attempts = 0, updated_at = NOW()
		WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		logger.WithError(err).Warn("Failed to reset dunning attempts")
	}

	logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"invoice_id":  obj.ID,
		"amount_paid": obj.AmountPaid,
	}).Info("Processed successful Stripe invoice payment")

	emitBillingEvent(eventInvoicePaid, tenantID, "invoice", obj.ID, &pb.BillingEvent{
		InvoiceId: obj.ID,
		Amount:    float64(obj.AmountPaid) / 100.0,
		Currency:  obj.Currency,
		Provider:  "stripe",
		Status:    "paid",
	})

	return nil
}

// handleStripeInvoiceFailed handles invoice.payment_failed events
func handleStripeInvoiceFailed(payload StripeWebhookPayload) error {
	var obj StripeInvoiceObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse invoice: %w", err)
	}

	// Find tenant by Stripe customer ID
	var tenantID string
	err := db.QueryRow(`
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id = $1
	`, obj.CustomerID).Scan(&tenantID)
	if err != nil {
		if obj.Metadata.TenantID != "" {
			tenantID = obj.Metadata.TenantID
		} else {
			logger.WithField("customer_id", obj.CustomerID).Debug("No tenant found for Stripe customer, skipping invoice.payment_failed")
			return nil
		}
	}

	// Increment dunning attempts
	_, err = db.Exec(`
		UPDATE purser.tenant_subscriptions
		SET dunning_attempts = dunning_attempts + 1, updated_at = NOW()
		WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		logger.WithError(err).Warn("Failed to increment dunning attempts")
	}

	logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"invoice_id":    obj.ID,
		"attempt_count": obj.AttemptCount,
	}).Warn("Stripe invoice payment failed")

	go sendTenantPaymentStatusEmail(tenantID, obj.ID, "stripe", "failed", float64(obj.AmountDue)/100, obj.Currency)

	emitBillingEvent(eventInvoicePaymentFailed, tenantID, "invoice", obj.ID, &pb.BillingEvent{
		InvoiceId: obj.ID,
		Amount:    float64(obj.AmountDue) / 100.0,
		Currency:  obj.Currency,
		Provider:  "stripe",
		Status:    "failed",
	})

	return nil
}

func MapStripeSubscriptionStatus(status string, cancelAtPeriodEnd bool) string {
	switch status {
	case "active", "trialing":
		if cancelAtPeriodEnd {
			return "pending_cancellation"
		}
		return "active"
	case "past_due":
		return "past_due"
	case "canceled", "unpaid", "incomplete_expired":
		return "cancelled"
	case "incomplete", "paused":
		return "pending"
	default:
		return status
	}
}

func updateClusterSubscriptionFromStripe(obj StripeSubscriptionObject, ourStatus string, periodEnd *time.Time) error {
	res, err := db.Exec(`
		UPDATE purser.cluster_subscriptions
		SET stripe_subscription_status = $1,
		    status = $2,
		    stripe_current_period_end = $3,
		    updated_at = NOW()
		WHERE stripe_subscription_id = $4
	`, obj.Status, ourStatus, periodEnd, obj.ID)
	if err != nil {
		return fmt.Errorf("failed to update cluster subscription status: %w", err)
	}

	updated, _ := res.RowsAffected()
	if updated == 0 && obj.Metadata.TenantID != "" && obj.Metadata.ClusterID != "" {
		_, err = db.Exec(`
			UPDATE purser.cluster_subscriptions
			SET stripe_subscription_id = $1,
			    stripe_subscription_status = $2,
			    status = $3,
			    stripe_current_period_end = $4,
			    updated_at = NOW()
			WHERE tenant_id = $5 AND cluster_id = $6
		`, obj.ID, obj.Status, ourStatus, periodEnd, obj.Metadata.TenantID, obj.Metadata.ClusterID)
		if err != nil {
			return fmt.Errorf("failed to update cluster subscription by tenant/cluster: %w", err)
		}
	}

	if ourStatus == "cancelled" && qmClient != nil {
		var tenantID, clusterID string
		err = db.QueryRow(`
			SELECT tenant_id, cluster_id FROM purser.cluster_subscriptions
			WHERE stripe_subscription_id = $1
		`, obj.ID).Scan(&tenantID, &clusterID)
		if err == nil && tenantID != "" && clusterID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := qmClient.UnsubscribeFromCluster(ctx, &pb.UnsubscribeFromClusterRequest{
				TenantId:  tenantID,
				ClusterId: clusterID,
			}); err != nil {
				return fmt.Errorf("failed to revoke cluster access: %w", err)
			}
		}
	}

	logger.WithFields(logging.Fields{
		"subscription_id": obj.ID,
		"cluster_id":      obj.Metadata.ClusterID,
		"stripe_status":   obj.Status,
		"our_status":      ourStatus,
	}).Info("Updated cluster subscription status from Stripe webhook")

	return nil
}

// ProcessMollieWebhookGRPC processes a Mollie webhook received via gRPC from the Gateway.
// Returns (success, error_message, http_status_code).
func ProcessMollieWebhookGRPC(body []byte, headers map[string]string) (bool, string, int) {
	if mollieClient == nil {
		logger.Warn("Mollie client not configured; rejecting webhook")
		return false, "Mollie not configured", 503
	}

	if mollieClient.HasWebhookSecret() {
		signature := headerValue(headers, "X-Mollie-Signature")
		if signature == "" {
			logger.Warn("Mollie webhook signature missing")
			recordWebhookSignatureFailure("mollie")
			return false, "Invalid signature", 401
		}
		if !mollieClient.VerifyWebhook(body, signature) {
			logger.Warn("Mollie webhook signature verification failed")
			recordWebhookSignatureFailure("mollie")
			return false, "Invalid signature", 401
		}
	} else {
		logger.Debug("Mollie webhook secret not configured; using API fetch verification")
	}

	var payload MollieWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.WithFields(logging.Fields{
			"error": err.Error(),
		}).Warn("Invalid Mollie webhook payload")
		return false, "Invalid payload", 400
	}

	logger.WithFields(logging.Fields{
		"payload": payload,
	}).Info("Received Mollie webhook via gRPC")

	if payload.ID == "" {
		logger.Warn("Mollie webhook payload missing id")
		return false, "Invalid payload", 400
	}

	resource := strings.ToLower(payload.Resource)
	var eventID string
	var err error

	switch resource {
	case "subscription":
		eventID, err = handleMollieSubscriptionWebhook(payload.ID)
	case "", "payment":
		eventID, err = handleMolliePaymentWebhook(payload.ID)
		if errors.Is(err, errMollieResourceNotFound) {
			eventID, err = handleMollieSubscriptionWebhook(payload.ID)
		}
	default:
		eventID, err = handleMolliePaymentWebhook(payload.ID)
		if errors.Is(err, errMollieResourceNotFound) {
			eventID, err = handleMollieSubscriptionWebhook(payload.ID)
		}
	}

	if err != nil {
		logger.WithError(err).Error("Failed to process Mollie webhook")
		return false, "Failed to process webhook", 500
	}

	if eventID != "" {
		markWebhookProcessed("mollie", eventID, resource)
	}

	return true, "", 200
}

func headerValue(headers map[string]string, key string) string {
	for headerKey, value := range headers {
		if strings.EqualFold(headerKey, key) {
			return value
		}
	}
	return ""
}

func recordWebhookSignatureFailure(provider string) {
	if metrics == nil || metrics.WebhookSignatureFailures == nil {
		return
	}
	metrics.WebhookSignatureFailures.WithLabelValues(provider).Inc()
}

func handleMolliePaymentWebhook(paymentID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	payment, err := mollieClient.GetPayment(ctx, paymentID)
	if err != nil {
		return "", errMollieResourceNotFound
	}

	status := strings.ToLower(payment.Status)
	if status == "" {
		return "", fmt.Errorf("missing Mollie payment status")
	}

	eventID := mollieEventID("payment", payment.ID, status)
	if isWebhookAlreadyProcessed("mollie", eventID) {
		return eventID, nil
	}

	newStatus, ok := mapMolliePaymentStatus(status)
	if !ok {
		logger.WithFields(logging.Fields{
			"mollie_status": status,
			"payment_id":    payment.ID,
		}).Warn("Unknown Mollie payment status")
		return eventID, nil
	}

	tenantID := mollieMetadataString(payment.Metadata, "tenant_id")
	purpose := mollieMetadataString(payment.Metadata, "purpose")
	paymentType := mollieMetadataString(payment.Metadata, "payment_type")
	referenceID := mollieMetadataString(payment.Metadata, "reference_id")
	invoiceID := mollieMetadataString(payment.Metadata, "invoice_id")
	topupID := mollieMetadataString(payment.Metadata, "topup_id")
	if topupID == "" {
		topupID = referenceID
	}

	if paymentType == "first_payment" || string(payment.SequenceType) == "first" {
		if newStatus != "confirmed" {
			return eventID, nil
		}
		if tenantID == "" {
			return "", fmt.Errorf("missing tenant_id for Mollie first payment")
		}
		if payment.CustomerID == "" || payment.MandateID == "" {
			return "", fmt.Errorf("missing Mollie customer or mandate ID")
		}

		_, _ = db.Exec(`
			INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id)
			VALUES ($1, $2)
			ON CONFLICT (tenant_id) DO UPDATE SET mollie_customer_id = $2
		`, tenantID, payment.CustomerID)

		mandate, err := mollieClient.GetMandate(ctx, payment.CustomerID, payment.MandateID)
		if err != nil {
			return "", fmt.Errorf("failed to fetch Mollie mandate: %w", err)
		}
		info := mollieClient.ExtractMandateInfo(mandate, payment.CustomerID)
		if err := upsertMollieMandate(tenantID, info); err != nil {
			return "", err
		}
		return eventID, nil
	}

	if purpose == "prepaid" {
		if newStatus != "confirmed" {
			return eventID, nil
		}
		if tenantID == "" || topupID == "" {
			return "", fmt.Errorf("missing tenant_id or topup_id for Mollie prepaid payment")
		}
		if payment.Amount == nil {
			return "", fmt.Errorf("missing Mollie payment amount")
		}
		amountCents, currency, err := mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
		if err != nil {
			return "", err
		}
		if err := handlePrepaidCheckoutCompleted(payment.ID, tenantID, topupID, amountCents, currency, ProviderMollie); err != nil {
			return "", err
		}
		return eventID, nil
	}

	if invoiceID == "" {
		invoiceID = referenceID
	}
	if err := updateInvoicePaymentStatus("mollie", payment.ID, invoiceID, newStatus); err != nil {
		return "", err
	}

	if newStatus == "confirmed" || newStatus == "failed" {
		if tenantID == "" && invoiceID != "" {
			_ = db.QueryRow(`SELECT tenant_id FROM purser.billing_invoices WHERE id = $1`, invoiceID).Scan(&tenantID)
		}
		if tenantID != "" {
			amountCents, currency, err := mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
			if err == nil {
				eventType := eventPaymentSucceeded
				if newStatus == "failed" {
					eventType = eventPaymentFailed
				}
				emitBillingEvent(eventType, tenantID, "payment", payment.ID, &pb.BillingEvent{
					PaymentId: payment.ID,
					InvoiceId: invoiceID,
					Amount:    float64(amountCents) / 100.0,
					Currency:  currency,
					Provider:  "mollie",
					Status:    newStatus,
				})
			}
		}
	}

	return eventID, nil
}

func handleMollieSubscriptionWebhook(subscriptionID string) (string, error) {
	if subscriptionID == "" {
		return "", fmt.Errorf("missing Mollie subscription ID")
	}

	var tenantID string
	err := db.QueryRow(`
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE mollie_subscription_id = $1
	`, subscriptionID).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errMollieResourceNotFound
	}
	if err != nil {
		return "", fmt.Errorf("failed to lookup tenant for Mollie subscription: %w", err)
	}

	var mollieCustomerID string
	err = db.QueryRow(`
		SELECT mollie_customer_id FROM purser.mollie_customers WHERE tenant_id = $1
	`, tenantID).Scan(&mollieCustomerID)
	if err != nil {
		return "", fmt.Errorf("failed to lookup Mollie customer: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sub, err := mollieClient.GetSubscription(ctx, mollieCustomerID, subscriptionID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Mollie subscription: %w", err)
	}

	info := mollieClient.ExtractSubscriptionInfo(sub, mollieCustomerID)
	eventID := mollieEventID("subscription", subscriptionID, info.Status)
	if isWebhookAlreadyProcessed("mollie", eventID) {
		return eventID, nil
	}

	ourStatus := mapMollieSubscriptionStatus(info.Status)
	_, err = db.Exec(`
		UPDATE purser.tenant_subscriptions
		SET mollie_subscription_id = $1,
		    status = $2,
		    payment_method = 'mollie',
		    cancelled_at = CASE WHEN $2 = 'cancelled' THEN NOW() ELSE cancelled_at END,
		    updated_at = NOW()
		WHERE tenant_id = $3
	`, subscriptionID, ourStatus, tenantID)
	if err != nil {
		return "", fmt.Errorf("failed to update subscription from Mollie: %w", err)
	}

	logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": subscriptionID,
		"mollie_status":   info.Status,
		"our_status":      ourStatus,
	}).Info("Updated subscription status from Mollie webhook")

	eventType := eventSubscriptionUpdated
	if ourStatus == "cancelled" {
		eventType = eventSubscriptionCanceled
	}
	emitBillingEvent(eventType, tenantID, "subscription", subscriptionID, &pb.BillingEvent{
		SubscriptionId: subscriptionID,
		Provider:       "mollie",
		Status:         ourStatus,
	})

	return eventID, nil
}

func mollieEventID(resource, id, status string) string {
	return fmt.Sprintf("%s:%s:%s", resource, id, status)
}

func mollieMetadataString(meta any, key string) string {
	switch m := meta.(type) {
	case map[string]interface{}:
		if val, ok := m[key]; ok {
			return fmt.Sprint(val)
		}
	case map[string]string:
		if val, ok := m[key]; ok {
			return val
		}
	case string:
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(m), &parsed); err == nil {
			if val, ok := parsed[key]; ok {
				return fmt.Sprint(val)
			}
		}
	}
	return ""
}

func mollieAmountToCents(value, currency string) (int64, string, error) {
	if value == "" || currency == "" {
		return 0, "", fmt.Errorf("missing Mollie amount")
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, "", fmt.Errorf("invalid Mollie amount: %w", err)
	}
	return int64(math.Round(parsed * 100)), currency, nil
}

func mapMolliePaymentStatus(status string) (string, bool) {
	switch status {
	case "paid":
		return "confirmed", true
	case "failed", "cancelled", "expired":
		return "failed", true
	case "pending", "open":
		return "pending", true
	default:
		return "", false
	}
}

func mapMollieSubscriptionStatus(status string) string {
	switch strings.ToLower(status) {
	case "active":
		return "active"
	case "pending":
		return "pending"
	case "suspended":
		return "suspended"
	case "canceled", "cancelled", "completed":
		return "cancelled"
	default:
		return status
	}
}

func updateInvoicePaymentStatus(provider, txID, invoiceID, newStatus string) error {
	var paymentID string
	var foundInvoiceID string
	err := db.QueryRow(`
		SELECT id, invoice_id FROM purser.billing_payments
		WHERE tx_id = $1 AND method = $2
	`, txID, provider).Scan(&paymentID, &foundInvoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to lookup payment: %w", err)
	}
	if invoiceID == "" {
		invoiceID = foundInvoiceID
	}

	var confirmedAt *time.Time
	now := time.Now()
	if newStatus == "confirmed" {
		confirmedAt = &now
	}

	_, err = db.Exec(`
		UPDATE purser.billing_payments
		SET status = $1, confirmed_at = $2, updated_at = NOW()
		WHERE id = $3
	`, newStatus, confirmedAt, paymentID)
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}

	if invoiceID == "" {
		return nil
	}

	if newStatus == "confirmed" {
		_, err = db.Exec(`
			UPDATE purser.billing_invoices
			SET status = 'paid', paid_at = $1, updated_at = NOW()
			WHERE id = $2 AND status IN ('pending', 'overdue')
		`, now, invoiceID)
		if err != nil {
			logger.WithFields(logging.Fields{
				"error":      err.Error(),
				"invoice_id": invoiceID,
			}).Error("Failed to update invoice status")
		} else {
			sendPaymentStatusEmail(invoiceID, provider, "confirmed")
		}
	}

	if newStatus == "failed" {
		sendPaymentStatusEmail(invoiceID, provider, "failed")
	}

	return nil
}

func upsertMollieMandate(tenantID string, info billingmollie.MandateInfo) error {
	if tenantID == "" {
		return fmt.Errorf("missing tenant_id for Mollie mandate")
	}
	details, err := json.Marshal(info.Details)
	if err != nil {
		return fmt.Errorf("failed to serialize Mollie mandate details: %w", err)
	}

	_, err = db.Exec(`
		INSERT INTO purser.mollie_mandates (
			tenant_id, mollie_customer_id, mollie_mandate_id,
			status, method, details, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (mollie_mandate_id) DO UPDATE SET
			status = EXCLUDED.status,
			method = EXCLUDED.method,
			details = EXCLUDED.details,
			updated_at = NOW()
	`, tenantID, info.MollieCustomerID, info.MollieMandateID, info.Status, info.Method, details, info.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to store Mollie mandate: %w", err)
	}

	return nil
}

// getTenantInfo calls Quartermaster to get tenant information using gRPC
func getTenantInfo(tenantID string) (*models.Tenant, error) {
	if qmClient == nil {
		return nil, fmt.Errorf("quartermaster client not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, err := qmClient.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenant from Quartermaster: %w", err)
	}

	if response.GetError() != "" {
		return nil, fmt.Errorf("Quartermaster error: %s", response.GetError())
	}

	pbTenant := response.GetTenant()
	if pbTenant == nil {
		return nil, fmt.Errorf("tenant not found")
	}

	// Convert proto Tenant to models.Tenant
	tenant := &models.Tenant{
		ID:   pbTenant.GetId(),
		Name: pbTenant.GetName(),
	}

	return tenant, nil
}
