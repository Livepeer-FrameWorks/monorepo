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
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/operator"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

// errMollieUnknownPayment is returned when Mollie reports the payment id does
// not exist. Treat it as a bad webhook id rather than a transient processing
// failure.
var errMollieUnknownPayment = errors.New("mollie payment not found")

// errWebhookMissingLocalReference signals that the provider event references
// a local row (invoice, payment, top-up) that does not exist yet. The caller
// translates this into a 'blocked' webhook_events row so the provider's retry
// drives reconciliation once the local row appears, instead of silently
// no-oping and marking the event processed.
var errWebhookMissingLocalReference = errors.New("webhook references local row that does not exist yet")

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
	ID           string `json:"id"`
	Status       string `json:"status"`
	LatestCharge string `json:"latest_charge"`
	Metadata     struct {
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
			ID                 string `json:"id"`
			CurrentPeriodStart int64  `json:"current_period_start"`
			CurrentPeriodEnd   int64  `json:"current_period_end"`
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
	ID         string `json:"id"`
	CustomerID string `json:"customer"`
	// SubscriptionID is the legacy top-level linkage. The dahlia invoice
	// payload carries the subscription id under
	// parent.subscription_details.subscription instead; resolveSubscriptionID
	// reads that with this field as the fallback.
	SubscriptionID string `json:"subscription"`
	Status         string `json:"status"` // paid, open, draft, uncollectible, void
	AmountDue      int64  `json:"amount_due"`
	AmountPaid     int64  `json:"amount_paid"`
	Currency       string `json:"currency"`
	AttemptCount   int    `json:"attempt_count"`
	// Subscription invoices carry the billing period directly. Used by
	// the operator credit ledger writer to record the period the payment
	// covered.
	PeriodStart      int64  `json:"period_start"`
	PeriodEnd        int64  `json:"period_end"`
	HostedInvoiceURL string `json:"hosted_invoice_url"`
	Metadata         struct {
		TenantID string `json:"tenant_id"`
	} `json:"metadata"`
	Parent struct {
		SubscriptionDetails struct {
			Subscription string `json:"subscription"`
		} `json:"subscription_details"`
	} `json:"parent"`
}

// resolveSubscriptionID returns the Stripe subscription id that generated this
// invoice, preferring the dahlia location
// (parent.subscription_details.subscription) and falling back to the top-level
// subscription field used by older API versions.
func (o *StripeInvoiceObject) resolveSubscriptionID() string {
	if o.Parent.SubscriptionDetails.Subscription != "" {
		return o.Parent.SubscriptionDetails.Subscription
	}
	return o.SubscriptionID
}

// verifyStripeSignature verifies the Stripe webhook signature using HMAC-SHA256
func (s *Service) verifyStripeSignature(payload []byte, signature, secret string) bool {
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
		s.logger.Error("Invalid Stripe signature format: missing timestamp or signatures")
		return false
	}

	// Verify timestamp is within tolerance (5 minutes)
	timestampInt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"timestamp": timestamp,
			"error":     err,
		}).Error("Failed to parse Stripe webhook timestamp")
		return false
	}

	now := time.Now().Unix()
	const toleranceSeconds int64 = 300 // 5 minutes tolerance
	drift := now - timestampInt
	if drift < 0 {
		drift = -drift
	}
	if drift > toleranceSeconds {
		s.logger.WithFields(logging.Fields{
			"timestamp":  timestampInt,
			"current":    now,
			"drift_secs": drift,
		}).Warn("Stripe webhook timestamp outside tolerance window")
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

	s.logger.WithFields(logging.Fields{
		"timestamp":   timestamp,
		"payload_len": len(payload),
	}).Warn("Stripe signature verification failed")

	return false
}

// sendPaymentStatusEmail sends email notification for payment status changes
func (s *Service) sendPaymentStatusEmail(invoiceID, provider, status string) {
	ctx := context.Background()
	// Get invoice and tenant subscription info (billing email is in subscription)
	var tenantID string
	var amount float64
	var currency, billingEmail, tenantName string
	err := s.db.QueryRowContext(ctx, `
		SELECT bi.tenant_id, bi.amount, bi.currency, ts.billing_email
		FROM purser.billing_invoices bi
		JOIN purser.tenant_subscriptions ts ON bi.tenant_id = ts.tenant_id
		WHERE bi.id = $1
	`, invoiceID).Scan(&tenantID, &amount, &currency, &billingEmail)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error":      err.Error(),
			"invoice_id": invoiceID,
		}).Error("Failed to get invoice and subscription info for payment email notification")
		return
	}

	// Get tenant name from Quartermaster
	tenantInfo, err := s.getTenantInfo(tenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error":      err.Error(),
			"invoice_id": invoiceID,
			"tenant_id":  tenantID,
		}).Error("Failed to get tenant info for payment email notification")
		return
	}
	tenantName = tenantInfo.Name

	if billingEmail == "" {
		s.logger.WithField("invoice_id", invoiceID).Warn("No tenant email found for payment notification")
		return
	}

	// Send appropriate email based on status
	switch status {
	case "confirmed":
		err = s.emailService.SendPaymentSuccessEmail(billingEmail, tenantName, invoiceID, amount, currency, provider)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceID,
				"provider":     provider,
			}).Error("Failed to send payment success email")
		}
	case "failed":
		err = s.emailService.SendPaymentFailedEmail(billingEmail, tenantName, invoiceID, amount, currency, provider)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceID,
				"provider":     provider,
			}).Error("Failed to send payment failed email")
		}
	}
}

// sendTenantActionRequiredEmail notifies the tenant that a payment needs their
// authentication and links the relevant hosted or in-app resolution page.
func (s *Service) sendTenantActionRequiredEmail(tenantID, invoiceRef string, amount float64, currency, actionURL string) {
	if tenantID == "" {
		return
	}
	var billingEmail string
	if err := s.db.QueryRowContext(context.Background(), `
		SELECT billing_email FROM purser.tenant_subscriptions WHERE tenant_id = $1
	`, tenantID).Scan(&billingEmail); err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get billing email for SCA notification")
		return
	}
	if billingEmail == "" {
		s.logger.WithField("tenant_id", tenantID).Warn("No tenant email found for SCA notification")
		return
	}
	tenantName := ""
	if info, infoErr := s.getTenantInfo(tenantID); infoErr == nil && info != nil {
		tenantName = info.Name
	}
	if err := s.emailService.SendPaymentActionRequiredEmail(billingEmail, tenantName, invoiceRef, amount, strings.ToUpper(currency), actionURL); err != nil {
		s.logger.WithError(err).WithField("invoice_id", invoiceRef).Error("Failed to send payment action-required email")
	}
}

func (s *Service) sendTenantPaymentStatusEmail(tenantID, invoiceRef, provider, status string, amount float64, currency string) {
	if tenantID == "" {
		return
	}

	var billingEmail string
	err := s.db.QueryRowContext(context.Background(), `
		SELECT billing_email FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
	`, tenantID).Scan(&billingEmail)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error":     err.Error(),
			"tenant_id": tenantID,
		}).Error("Failed to get billing email for tenant payment notification")
		return
	}
	if billingEmail == "" {
		s.logger.WithField("tenant_id", tenantID).Warn("No tenant email found for payment notification")
		return
	}

	tenantName := ""
	tenantInfo, tenantErr := s.getTenantInfo(tenantID)
	if tenantErr == nil && tenantInfo != nil {
		tenantName = tenantInfo.Name
	}

	currency = strings.ToUpper(currency)
	switch status {
	case "confirmed":
		err = s.emailService.SendPaymentSuccessEmail(billingEmail, tenantName, invoiceRef, amount, currency, provider)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail,
				"invoice_id":   invoiceRef,
				"provider":     provider,
			}).Error("Failed to send payment success email")
		}
	case "failed":
		err = s.emailService.SendPaymentFailedEmail(billingEmail, tenantName, invoiceRef, amount, currency, provider)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
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
func (s *Service) ProcessStripeWebhookGRPC(body []byte, headers map[string]string) (bool, string, int) {
	// Verify Stripe signature
	signature := headerValue(headers, "Stripe-Signature")
	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")

	if webhookSecret == "" {
		s.logger.Error("STRIPE_WEBHOOK_SECRET not configured; rejecting webhook")
		return false, "Webhook verification not configured", 503
	} else if !s.verifyStripeSignature(body, signature, webhookSecret) {
		s.logger.Warn("Invalid Stripe webhook signature")
		s.recordWebhookSignatureFailure("stripe")
		return false, "Invalid signature", 401
	}

	var payload StripeWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.logger.WithFields(logging.Fields{
			"error": err.Error(),
		}).Warn("Invalid Stripe webhook payload")
		return false, "Invalid payload", 400
	}

	s.logger.WithFields(logging.Fields{
		"event_id":   payload.ID,
		"event_type": payload.Type,
	}).Info("Received Stripe webhook via gRPC")

	ctx := context.Background()
	claim, claimErr := s.claimWebhookEvent(ctx, "stripe", payload.ID, payload.Type, signature, body)
	if claimErr != nil {
		s.logger.WithError(claimErr).Error("Failed to claim Stripe webhook event")
		return false, "Failed to claim webhook", 500
	}
	if !claim.claimed {
		s.logger.WithFields(logging.Fields{
			"event_id": payload.ID,
			"status":   claim.previous,
		}).Debug("Stripe webhook already claimed or terminal, skipping")
		return true, "", 200
	}

	var err error
	switch {
	case payload.Type == "payment_intent.succeeded" || payload.Type == "payment_intent.payment_failed":
		err = s.handleStripePaymentIntentGRPC(payload)
	case payload.Type == "checkout.session.completed" || payload.Type == "checkout.session.async_payment_succeeded":
		// Both deliver a Checkout Session; async_payment_succeeded carries
		// payment_status=paid so the dispatcher settles what completed staged.
		err = s.DispatchStripeCheckoutCompleted(ctx, payload.Data.Object)
	case payload.Type == "checkout.session.async_payment_failed":
		err = s.handleStripeCheckoutAsyncPaymentFailed(payload)
	case payload.Type == "checkout.session.expired":
		err = s.handleStripeCheckoutExpired(payload)
	case strings.HasPrefix(payload.Type, "customer.subscription."):
		err = s.handleStripeSubscriptionEvent(payload)
	case payload.Type == "invoice.paid":
		err = s.handleStripeInvoicePaid(payload)
	case payload.Type == "invoice.payment_failed":
		err = s.handleStripeInvoiceFailed(payload)
	case payload.Type == "invoice.payment_action_required":
		err = s.handleStripeInvoicePaymentActionRequired(payload)
	case payload.Type == "charge.refunded":
		err = s.handleStripeChargeRefunded(payload)
	case strings.HasPrefix(payload.Type, "charge.dispute."):
		err = s.handleStripeChargeDispute(payload)
	default:
		s.logger.WithField("event_type", payload.Type).Debug("Ignoring unhandled Stripe event type")
	}

	if err != nil {
		blocked := errors.Is(err, errWebhookMissingLocalReference)
		if markErr := s.markWebhookFailed(ctx, "stripe", payload.ID, err.Error(), blocked, false); markErr != nil {
			s.logger.WithError(markErr).Warn("Failed to mark Stripe webhook failed")
		}
		s.logger.WithError(err).WithField("event_type", payload.Type).Error("Failed to process Stripe webhook")
		return false, "Failed to process webhook", 500
	}

	if markErr := s.markWebhookSucceeded(ctx, "stripe", payload.ID, ""); markErr != nil {
		s.logger.WithError(markErr).Error("Failed to mark Stripe webhook processed")
		return false, "Failed to record webhook completion", 500
	}
	return true, "", 200
}

// webhookClaim is the outcome of attempting to claim a provider event for
// processing. claimed=true means this caller owns the work; on commit it must
// call markWebhookSucceeded or markWebhookFailed. claimed=false means the
// event row exists in a terminal state and the caller must not reprocess.
// The blocked state covers events that were durably accepted but cannot
// reconcile yet (out-of-order: provider sent us a payment-succeeded event
// before the matching invoice was created locally) and must be retried.
type webhookClaim struct {
	claimed  bool
	terminal bool // already processed/failed_terminal
	blocked  bool // failed_retryable or blocked, requires retry
	previous string
}

const webhookClaimLease = 2 * time.Minute

// claimWebhookEvent inserts a 'claimed' row for (provider, event_id), or
// atomically reclaims a previous retryable/blocked row. Fresh claimed rows are
// treated as in-flight so duplicate deliveries cannot run reconciliation
// concurrently; stale claimed rows are reclaimed after webhookClaimLease so a
// claim-then-crash does not suppress provider retries forever.
func (s *Service) claimWebhookEvent(ctx context.Context, provider, eventID, eventType, signatureHeader string, rawPayload []byte) (*webhookClaim, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	if eventID == "" {
		return nil, fmt.Errorf("missing event_id for %s webhook", provider)
	}
	var (
		status   string
		acquired bool
	)
	err := s.db.QueryRowContext(ctx, `
		WITH claimed AS (
			INSERT INTO purser.webhook_events
				(provider, event_id, event_type, status, signature_header, raw_payload, received_at)
			VALUES ($1, $2, $3, 'claimed', NULLIF($4, ''), $5, NOW())
			ON CONFLICT (provider, event_id) DO UPDATE
				SET status = 'claimed',
				    retry_count = purser.webhook_events.retry_count + 1,
				    received_at = NOW(),
				    event_type = COALESCE(NULLIF(EXCLUDED.event_type, ''), purser.webhook_events.event_type),
				    signature_header = COALESCE(EXCLUDED.signature_header, purser.webhook_events.signature_header),
				    raw_payload = COALESCE(EXCLUDED.raw_payload, purser.webhook_events.raw_payload),
				    last_error = NULL
				WHERE purser.webhook_events.status IN ('failed_retryable', 'blocked')
				   OR (purser.webhook_events.status = 'claimed'
				       AND purser.webhook_events.received_at < NOW() - ($6::int * INTERVAL '1 second'))
			RETURNING status
		)
		SELECT status, TRUE FROM claimed
		UNION ALL
		SELECT status, FALSE
		FROM purser.webhook_events
		WHERE provider = $1 AND event_id = $2
		  AND NOT EXISTS (SELECT 1 FROM claimed)
		LIMIT 1
	`, provider, eventID, eventType, signatureHeader, rawPayload, int(webhookClaimLease/time.Second)).Scan(&status, &acquired)
	if err != nil {
		return nil, fmt.Errorf("claim webhook event: %w", err)
	}
	if acquired && status == "claimed" {
		return &webhookClaim{claimed: true, previous: status}, nil
	}
	switch status {
	case "processed", "failed_terminal":
		return &webhookClaim{terminal: true, previous: status}, nil
	case "claimed":
		return &webhookClaim{previous: status}, nil
	case "failed_retryable", "blocked":
		return &webhookClaim{blocked: status == "blocked", previous: status}, nil
	default:
		return &webhookClaim{previous: status}, nil
	}
}

// markWebhookSucceeded advances a claimed webhook event to 'processed'.
// Errors are returned so the gRPC handler can surface them to the Gateway;
// the previous silent log-and-swallow behavior allowed the same event to
// reprocess indefinitely without the operator noticing.
func (s *Service) markWebhookSucceeded(ctx context.Context, provider, eventID, providerObjectID string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE purser.webhook_events
		SET status = 'processed',
		    processed_at = NOW(),
		    last_error = NULL,
		    provider_object_id = COALESCE(provider_object_id, NULLIF($3, ''))
		WHERE provider = $1 AND event_id = $2
	`, provider, eventID, providerObjectID)
	if err != nil {
		return fmt.Errorf("mark webhook processed: %w", err)
	}
	return nil
}

// markWebhookFailed records a processing failure. blocked=true means the
// failure is a missing local reference that should clear on a future retry
// once the local invoice/payment row exists; blocked=false means a generic
// transient failure (DB error, downstream call timeout). terminal=true
// retires the event from further retries (signature mismatch caught after
// claim, malformed body that survived initial parse, etc.).
func (s *Service) markWebhookFailed(ctx context.Context, provider, eventID, errMsg string, blocked, terminal bool) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	target := "failed_retryable"
	if terminal {
		target = "failed_terminal"
	} else if blocked {
		target = "blocked"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE purser.webhook_events
		SET status = $3::varchar,
		    last_error = $4,
		    processed_at = CASE WHEN $5::boolean THEN NOW() ELSE processed_at END
		WHERE provider = $1 AND event_id = $2
	`, provider, eventID, target, errMsg, terminal)
	if err != nil {
		return fmt.Errorf("mark webhook failed: %w", err)
	}
	return nil
}

// handleStripePaymentIntentGRPC handles payment_intent events. A missing
// metadata.invoice_id is logged at debug rather than treated as failure
// because Stripe-initiated PaymentIntents (subscription base) do not flow
// through this code path. A successful PaymentIntent whose local
// billing_payments row is missing is surfaced as a blocked-retry instead of
// a silent no-op, so the next provider retry drives reconciliation once the
// local row exists. Settlement runs through the shared partial-payment-aware
// helper, never a direct invoice UPDATE.
func (s *Service) handleStripePaymentIntentGRPC(payload StripeWebhookPayload) error {
	var obj StripePaymentIntentObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse payment intent: %w", err)
	}

	invoiceID := obj.Metadata.InvoiceID
	if invoiceID == "" {
		s.logger.WithField("payment_intent_id", obj.ID).Debug("No invoice_id in payment intent metadata, skipping")
		return nil
	}

	ctx := context.Background()
	status := "confirmed"
	if payload.Type == "payment_intent.payment_failed" {
		status = "failed"
	}

	updated, err := s.updateInvoicePaymentStatus("stripe", obj.ID, invoiceID, status)
	if err != nil {
		return err
	}
	if !updated {
		s.logger.WithFields(logging.Fields{
			"payment_intent_id": obj.ID,
			"invoice_id":        invoiceID,
			"status":            status,
		}).Warn("Stripe webhook did not match a local invoice payment; blocking for retry")
		return fmt.Errorf("invoice %s has no pending card payment for %s: %w", invoiceID, obj.ID, errWebhookMissingLocalReference)
	}

	s.logger.WithFields(logging.Fields{
		"payment_intent_id": obj.ID,
		"invoice_id":        invoiceID,
		"status":            status,
	}).Info("Updated payment status from Stripe webhook")

	var paymentID, tenantID, currency string
	var amountCents int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT p.id, i.tenant_id, (p.amount * 100)::bigint, p.currency
		FROM purser.billing_payments p
		JOIN purser.billing_invoices i ON p.invoice_id = i.id
		WHERE p.invoice_id = $1 AND p.method = 'card' AND p.tx_id = $2
		ORDER BY p.created_at DESC
		LIMIT 1
	`, invoiceID, obj.ID).Scan(&paymentID, &tenantID, &amountCents, &currency); err == nil && tenantID != "" {
		if mapErr := s.upsertProviderPaymentObject(ctx, providerPaymentObjectInput{
			provider:         "stripe",
			objectType:       "payment_intent",
			providerObjectID: obj.ID,
			tenantID:         tenantID,
			localRefType:     "payment",
			localRefID:       paymentID,
			metadata: map[string]any{
				"invoice_id": invoiceID,
			},
		}); mapErr != nil {
			s.logger.WithError(mapErr).WithField("payment_intent_id", obj.ID).Warn("Failed to record Stripe payment_intent mapping")
		}
		if obj.LatestCharge != "" {
			if mapErr := s.upsertProviderPaymentObject(ctx, providerPaymentObjectInput{
				provider:         "stripe",
				objectType:       "charge",
				providerObjectID: obj.LatestCharge,
				tenantID:         tenantID,
				localRefType:     "payment",
				localRefID:       paymentID,
				metadata: map[string]any{
					"invoice_id":        invoiceID,
					"payment_intent_id": obj.ID,
				},
			}); mapErr != nil {
				s.logger.WithError(mapErr).WithField("charge_id", obj.LatestCharge).Warn("Failed to record Stripe charge mapping")
			}
		}
		eventType := eventPaymentSucceeded
		if status == "failed" {
			eventType = eventPaymentFailed
		}
		emitBillingEvent(s.db, s.logger, eventType, tenantID, "payment", paymentID, &ipcpb.BillingEvent{
			PaymentId: paymentID,
			InvoiceId: invoiceID,
			Amount:    float64(amountCents) / float64(intPow10(currencyMinorUnitExponent(currency))),
			Currency:  currency,
			Provider:  "stripe",
			Status:    status,
		})
	}

	return nil
}

// intPow10 returns 10^n for small n. Used to derive the integer divisor
// when rendering integer minor units into the BillingEvent presentation
// amount (proto-defined float64). The conversion lives at the wire boundary
// only; ledger math is integer cents throughout.
func intPow10(n int) int64 {
	out := int64(1)
	for range n {
		out *= 10
	}
	return out
}

// handleStripeSubscriptionEvent handles customer.subscription.* events
func (s *Service) handleStripeSubscriptionEvent(payload StripeWebhookPayload) error {
	var obj StripeSubscriptionObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse subscription: %w", err)
	}

	ctx := context.Background()
	ourStatus := MapStripeSubscriptionStatus(obj.Status, obj.CancelAtPeriodEnd)

	// Get period end from subscription items
	var periodStart *time.Time
	var periodEnd *time.Time
	if len(obj.Items.Data) > 0 {
		if obj.Items.Data[0].CurrentPeriodStart > 0 {
			t := time.Unix(obj.Items.Data[0].CurrentPeriodStart, 0)
			periodStart = &t
		}
		if obj.Items.Data[0].CurrentPeriodEnd > 0 {
			t := time.Unix(obj.Items.Data[0].CurrentPeriodEnd, 0)
			periodEnd = &t
		}
	}

	if obj.Metadata.ClusterID != "" || obj.Metadata.Purpose == "cluster_subscription" {
		if ourStatus == "active" {
			// Activation authority for an async cluster subscription: grant
			// access once Stripe collects the first payment.
			return s.activateClusterSubscriptionFromStripe(ctx, obj.Metadata.TenantID, obj.Metadata.ClusterID, obj.CustomerID, obj.ID, "")
		}
		if err := s.updateClusterSubscriptionFromStripe(obj, ourStatus, periodEnd); err != nil {
			return err
		}
		return nil
	}

	// Find tenant by Stripe subscription ID
	var tenantID string
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_subscription_id = $1
	`, obj.ID).Scan(&tenantID)
	if err != nil {
		// Try to find by customer ID if subscription ID not found
		err = s.db.QueryRowContext(ctx, `
			SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id = $1
		`, obj.CustomerID).Scan(&tenantID)
		if err != nil {
			// Stripe subscription metadata carries tenant_id for checkout-created
			// subscriptions before the local customer index has been populated.
			if obj.Metadata.TenantID != "" {
				tenantID = obj.Metadata.TenantID
			} else {
				s.logger.WithField("subscription_id", obj.ID).Warn("No tenant found for Stripe subscription")
				return nil
			}
		}
	}

	if ourStatus == "active" {
		// Activation authority for an async tenant subscription: apply the
		// purchased tier and clear staged checkout state once funds settle.
		if _, actErr := s.activateTenantSubscriptionFromStripe(ctx, tenantID, obj.CustomerID, obj.ID, obj.Metadata.TierID, periodStart, periodEnd); actErr != nil {
			return actErr
		}
		if _, intentErr := s.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET provider_subscription_id = COALESCE(provider_subscription_id, NULLIF($1, '')),
			    status = 'succeeded',
			    succeeded_at = COALESCE(succeeded_at, NOW()),
			    updated_at = NOW()
			WHERE provider = 'stripe' AND provider_subscription_id = $1
		`, obj.ID); intentErr != nil {
			return fmt.Errorf("failed to mark subscription intent succeeded: %w", intentErr)
		}
	} else {
		if _, err = s.db.ExecContext(ctx, `
			UPDATE purser.tenant_subscriptions
			SET stripe_subscription_status = $1,
			    status = $2,
			    stripe_current_period_end = $3,
			    billing_period_start = COALESCE($5, billing_period_start),
			    billing_period_end = COALESCE($3, billing_period_end),
			    next_billing_date = COALESCE($3, next_billing_date),
			    updated_at = NOW()
			WHERE tenant_id = $4
		`, obj.Status, ourStatus, periodEnd, tenantID, periodStart); err != nil {
			return fmt.Errorf("failed to update subscription status: %w", err)
		}
		// A subscription that reached a terminal failure (incomplete_expired /
		// unpaid / canceled all map to "cancelled") without ever activating
		// leaves staged stripe_checkout state behind; clear it so a failed async
		// first payment does not strand a pending tier.
		if ourStatus == "cancelled" {
			if clearErr := s.clearStagedStripeCheckout(ctx, tenantID, obj.ID); clearErr != nil {
				return clearErr
			}
		}
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": obj.ID,
		"stripe_status":   obj.Status,
		"our_status":      ourStatus,
	}).Info("Updated subscription status from Stripe webhook")

	subscriptionID := ""
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&subscriptionID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to look up internal subscription ID, falling back to Stripe ID")
	}
	if subscriptionID == "" {
		subscriptionID = obj.ID
	}
	eventType := eventSubscriptionUpdated
	if ourStatus == "cancelled" {
		eventType = eventSubscriptionCanceled
	}
	emitBillingEvent(s.db, s.logger, eventType, tenantID, "subscription", subscriptionID, &ipcpb.BillingEvent{
		SubscriptionId: subscriptionID,
		Provider:       "stripe",
		Status:         ourStatus,
	})

	return nil
}

// handleStripeInvoicePaid handles invoice.paid events
func (s *Service) handleStripeInvoicePaid(payload StripeWebhookPayload) error {
	var obj StripeInvoiceObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse invoice: %w", err)
	}

	ctx := context.Background()
	// Find tenant by Stripe customer ID
	var tenantID string
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id = $1
	`, obj.CustomerID).Scan(&tenantID)
	if err != nil {
		if obj.Metadata.TenantID != "" {
			tenantID = obj.Metadata.TenantID
		} else {
			s.logger.WithField("customer_id", obj.CustomerID).Debug("No tenant found for Stripe customer, skipping invoice.paid")
			return nil
		}
	}

	// Reset dunning attempts on successful payment
	_, err = s.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET dunning_attempts = 0, updated_at = NOW()
		WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to reset dunning attempts")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"invoice_id":  obj.ID,
		"amount_paid": obj.AmountPaid,
	}).Info("Processed successful Stripe invoice payment")

	// If this invoice corresponds to a monthly cluster_subscription, write
	// the operator credit ledger row so marketplace revenue is tracked from
	// day one. Pre-launch with marketplace disabled the lookup returns no
	// rows and this is a no-op.
	if err := s.recordMonthlyClusterCredit(ctx, &obj); err != nil {
		return fmt.Errorf("record monthly cluster credit: %w", err)
	}

	// Activation authority for an async cluster subscription: a settled invoice
	// is proof of payment, so grant cluster access. Idempotent and a no-op when
	// the subscription is not a cluster subscription; converges with the
	// customer.subscription.updated path regardless of which lands first.
	if subID := obj.resolveSubscriptionID(); subID != "" {
		if err := s.activateClusterSubscriptionFromStripe(ctx, "", "", "", subID, ""); err != nil {
			return fmt.Errorf("activate cluster subscription from invoice.paid: %w", err)
		}
	}

	// Tenant-subscription invariant: provider-managed tenant_subscriptions
	// produce Purser invoices with base_amount = 0 (the base is represented
	// as an included_subscription line because the provider's recurring
	// charge owns it). So there is nothing for invoice.paid to reconcile on
	// the base; metered overage collection lives elsewhere.

	emitBillingEvent(s.db, s.logger, eventInvoicePaid, tenantID, "invoice", obj.ID, &ipcpb.BillingEvent{
		InvoiceId: obj.ID,
		Amount:    float64(obj.AmountPaid) / 100.0,
		Currency:  obj.Currency,
		Provider:  "stripe",
		Status:    "paid",
	})

	return nil
}

// recordMonthlyClusterCredit looks up whether the given Stripe invoice is
// for a cluster_subscription and, if so, writes an operator_credit_ledger
// accrual row. Marketplace launch reads this ledger to compute payouts.
func (s *Service) recordMonthlyClusterCredit(ctx context.Context, obj *StripeInvoiceObject) error {
	subscriptionID := obj.resolveSubscriptionID()
	if subscriptionID == "" || obj.AmountPaid <= 0 {
		return nil
	}
	// Resolve the cluster_subscription + owner from our books.
	var (
		clusterID         string
		consumingTenantID string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT cluster_id, tenant_id
		FROM purser.cluster_subscriptions
		WHERE stripe_subscription_id = $1
	`, subscriptionID).Scan(&clusterID, &consumingTenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not a cluster subscription
	}
	if err != nil {
		return fmt.Errorf("lookup cluster_subscription by stripe_subscription_id: %w", err)
	}
	// Resolve the owner via Quartermaster (cluster_owner_tenant_id lives there).
	if s.qmClient == nil {
		return errors.New("quartermaster client not configured")
	}
	resp, err := s.qmClient.GetCluster(ctx, clusterID)
	if err != nil || resp == nil || resp.GetCluster() == nil {
		return fmt.Errorf("get cluster %s: %w", clusterID, err)
	}
	ownerStr := resp.GetCluster().GetOwnerTenantId()
	if ownerStr == "" || ownerStr == consumingTenantID {
		// platform-owned or self-hosted (consumer == owner): no operator
		// credit. Self-payment doesn't make sense as a payable.
		return nil
	}
	ownerUUID, err := uuid.Parse(ownerStr)
	if err != nil {
		return fmt.Errorf("parse cluster owner_tenant_id %q: %w", ownerStr, err)
	}

	periodStart := time.Unix(obj.PeriodStart, 0).UTC()
	periodEnd := time.Unix(obj.PeriodEnd, 0).UTC()
	if obj.PeriodStart == 0 || obj.PeriodEnd == 0 || !periodEnd.After(periodStart) {
		// Stripe normally sends these on subscription invoices. When the
		// payload omits them, receipt time keeps the row queryable by a
		// deterministic period.
		periodEnd = time.Now().UTC()
		periodStart = periodEnd.AddDate(0, -1, 0)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if persistErr := operator.PersistStripeSubscriptionCredit(ctx, tx,
		obj.ID, ownerUUID, clusterID, strings.ToUpper(obj.Currency), obj.AmountPaid,
		periodStart, periodEnd, "cluster_monthly"); persistErr != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("rollback failed (%w) after credit error: %w", rbErr, persistErr)
		}
		return persistErr
	}
	return tx.Commit()
}

// handleStripeInvoiceFailed handles invoice.payment_failed events
func (s *Service) handleStripeInvoiceFailed(payload StripeWebhookPayload) error {
	var obj StripeInvoiceObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse invoice: %w", err)
	}

	ctx := context.Background()
	// Find tenant by Stripe customer ID
	var tenantID string
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id = $1
	`, obj.CustomerID).Scan(&tenantID)
	if err != nil {
		if obj.Metadata.TenantID != "" {
			tenantID = obj.Metadata.TenantID
		} else {
			s.logger.WithField("customer_id", obj.CustomerID).Debug("No tenant found for Stripe customer, skipping invoice.payment_failed")
			return nil
		}
	}

	// Increment dunning attempts
	_, err = s.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET dunning_attempts = dunning_attempts + 1, updated_at = NOW()
		WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to increment dunning attempts")
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"invoice_id":    obj.ID,
		"attempt_count": obj.AttemptCount,
	}).Warn("Stripe invoice payment failed")

	go s.sendTenantPaymentStatusEmail(tenantID, obj.ID, "stripe", "failed", float64(obj.AmountDue)/100, obj.Currency)

	emitBillingEvent(s.db, s.logger, eventInvoicePaymentFailed, tenantID, "invoice", obj.ID, &ipcpb.BillingEvent{
		InvoiceId: obj.ID,
		Amount:    float64(obj.AmountDue) / 100.0,
		Currency:  obj.Currency,
		Provider:  "stripe",
		Status:    "failed",
	})

	return nil
}

// stripeCheckoutSessionEvent is the slice of a Checkout Session that the
// async-failed and expired handlers need to route by purpose.
type stripeCheckoutSessionEvent struct {
	ID            string `json:"id"`
	PaymentIntent string `json:"payment_intent"`
	Subscription  string `json:"subscription"`
	Metadata      struct {
		Purpose     string `json:"purpose"`
		TenantID    string `json:"tenant_id"`
		ReferenceID string `json:"reference_id"`
		ClusterID   string `json:"cluster_id"`
	} `json:"metadata"`
}

// handleStripeCheckoutAsyncPaymentFailed records the failure of a delayed
// Checkout payment (SEPA/iDEAL/Bancontact) that was ultimately declined. No
// value was granted — the completed handler gated on payment_status — so this
// only moves the staged one-time payment to a terminal state. Subscription
// checkouts are reconciled via the customer.subscription.* terminal path.
func (s *Service) handleStripeCheckoutAsyncPaymentFailed(payload StripeWebhookPayload) error {
	var sess stripeCheckoutSessionEvent
	if err := json.Unmarshal(payload.Data.Object, &sess); err != nil {
		return fmt.Errorf("failed to parse checkout session: %w", err)
	}
	ctx := context.Background()
	switch CheckoutPurpose(sess.Metadata.Purpose) {
	case PurposeInvoice:
		if sess.Metadata.ReferenceID == "" {
			return nil
		}
		txID := sess.PaymentIntent
		if txID == "" {
			txID = sess.ID
		}
		if _, err := s.updateInvoicePaymentStatus("stripe", txID, sess.Metadata.ReferenceID, "failed"); err != nil {
			return err
		}
		s.logger.WithFields(logging.Fields{
			"session_id": sess.ID,
			"invoice_id": sess.Metadata.ReferenceID,
		}).Warn("Stripe async invoice payment failed")
		return nil
	case PurposePrepaid:
		return s.markPendingTopupTerminal(ctx, sess.Metadata.ReferenceID, "failed")
	default:
		s.logger.WithFields(logging.Fields{
			"session_id": sess.ID,
			"purpose":    sess.Metadata.Purpose,
		}).Info("Async payment failed for subscription checkout; awaiting subscription terminal event")
		return nil
	}
}

// handleStripeCheckoutExpired cleans up the staged state for a Checkout Session
// that expired without payment. One-time top-ups are marked expired; staged
// subscription/cluster checkout state is cleared so an abandoned upgrade does
// not strand a pending tier. Unpaid invoices are left payable (a new checkout
// can be created), so only the open intent is expired.
func (s *Service) handleStripeCheckoutExpired(payload StripeWebhookPayload) error {
	var sess stripeCheckoutSessionEvent
	if err := json.Unmarshal(payload.Data.Object, &sess); err != nil {
		return fmt.Errorf("failed to parse checkout session: %w", err)
	}
	ctx := context.Background()
	if err := s.expireStripeCheckoutIntent(ctx, sess.ID); err != nil {
		return err
	}
	switch CheckoutPurpose(sess.Metadata.Purpose) {
	case PurposePrepaid:
		return s.markPendingTopupTerminal(ctx, sess.Metadata.ReferenceID, "expired")
	case PurposeSubscription:
		return s.clearStagedStripeCheckout(ctx, sess.Metadata.TenantID, sess.Subscription)
	case PurposeClusterSubscription:
		return s.clearStagedClusterSubscription(ctx, sess.ID, sess.Subscription)
	default:
		s.logger.WithField("session_id", sess.ID).Debug("Checkout session expired; intent expired")
		return nil
	}
}

// handleStripeInvoicePaymentActionRequired notifies the customer that a
// recurring charge needs their authentication (SCA) and emails the hosted
// invoice page where they complete it. It never marks the invoice failed.
func (s *Service) handleStripeInvoicePaymentActionRequired(payload StripeWebhookPayload) error {
	var obj StripeInvoiceObject
	if err := json.Unmarshal(payload.Data.Object, &obj); err != nil {
		return fmt.Errorf("failed to parse invoice: %w", err)
	}
	ctx := context.Background()
	var tenantID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id FROM purser.tenant_subscriptions WHERE stripe_customer_id = $1
	`, obj.CustomerID).Scan(&tenantID); err != nil {
		if obj.Metadata.TenantID != "" {
			tenantID = obj.Metadata.TenantID
		}
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id":          tenantID,
		"invoice_id":         obj.ID,
		"hosted_invoice_url": obj.HostedInvoiceURL,
	}).Warn("Stripe invoice requires customer authentication (SCA); notifying customer")
	go s.sendTenantActionRequiredEmail(tenantID, obj.ID, float64(obj.AmountDue)/100, obj.Currency, obj.HostedInvoiceURL)
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

func (s *Service) updateClusterSubscriptionFromStripe(obj StripeSubscriptionObject, ourStatus string, periodEnd *time.Time) error {
	ctx := context.Background()
	res, err := s.db.ExecContext(ctx, `
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
		_, err = s.db.ExecContext(ctx, `
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

	if ourStatus == "cancelled" && s.qmClient != nil {
		var tenantID, clusterID string
		err = s.db.QueryRowContext(ctx, `
			SELECT tenant_id, cluster_id FROM purser.cluster_subscriptions
			WHERE stripe_subscription_id = $1
		`, obj.ID).Scan(&tenantID, &clusterID)
		if err == nil && tenantID != "" && clusterID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := s.qmClient.UnsubscribeFromCluster(ctx, &quartermasterpb.UnsubscribeFromClusterRequest{
				TenantId:  tenantID,
				ClusterId: clusterID,
			}); err != nil {
				return fmt.Errorf("failed to revoke cluster access: %w", err)
			}
		}
	}

	s.logger.WithFields(logging.Fields{
		"subscription_id": obj.ID,
		"cluster_id":      obj.Metadata.ClusterID,
		"stripe_status":   obj.Status,
		"our_status":      ourStatus,
	}).Info("Updated cluster subscription status from Stripe webhook")

	return nil
}

// ProcessMollieWebhookGRPC processes a Mollie webhook received via gRPC from the Gateway.
// Returns (success, error_message, http_status_code).
//
// Mollie webhooks are application/x-www-form-urlencoded with a single `id`
// parameter; the integrator fetches details via the API. JSON is accepted only
// when the caller explicitly sends application/json.
func (s *Service) ProcessMollieWebhookGRPC(body []byte, headers map[string]string) (bool, string, int) {
	if s.mollieClient == nil {
		s.logger.Warn("Mollie client not configured; rejecting webhook")
		return false, "Mollie not configured", 503
	}

	paymentID, err := parseMollieWebhookID(body, headerValue(headers, "Content-Type"))
	if err != nil {
		s.logger.WithError(err).Warn("Invalid Mollie webhook payload")
		return false, "Invalid payload", 400
	}
	if paymentID == "" {
		s.logger.Warn("Mollie webhook payload missing id")
		return false, "Invalid payload", 400
	}

	s.logger.WithField("payment_id", paymentID).Info("Received Mollie webhook via gRPC")

	ctx := context.Background()
	// Mollie does not sign its webhook bodies, so the only safe pattern is
	// to fetch the payment authoritatively from the Mollie API and
	// reconcile on (mollie_payment_id, status). The synthesized event id
	// claim/lock pattern collapses concurrent deliveries for the same
	// payment-state transition; subsequent transitions get distinct event
	// ids and are processed in order.
	eventID, err := s.handleMolliePaymentWebhook(ctx, paymentID, body)
	if errors.Is(err, errMollieUnknownPayment) {
		s.logger.WithField("payment_id", paymentID).Warn("Mollie webhook references unknown payment id")
		return false, "Payment not found", 404
	}
	if err != nil {
		// eventID may be empty when the failure occurred before we could
		// derive a status (and therefore an event id); in that case the
		// next provider retry re-runs the lookup.
		if eventID != "" {
			blocked := errors.Is(err, errWebhookMissingLocalReference)
			if markErr := s.markWebhookFailed(ctx, "mollie", eventID, err.Error(), blocked, false); markErr != nil {
				s.logger.WithError(markErr).Warn("Failed to mark Mollie webhook failed")
			}
		}
		s.logger.WithError(err).Error("Failed to process Mollie webhook")
		return false, "Failed to process webhook", 500
	}

	if eventID != "" {
		if markErr := s.markWebhookSucceeded(ctx, "mollie", eventID, paymentID); markErr != nil {
			s.logger.WithError(markErr).Error("Failed to mark Mollie webhook processed")
			return false, "Failed to record webhook completion", 500
		}
	}

	return true, "", 200
}

// parseMollieWebhookID extracts the `id` parameter from a Mollie webhook body.
// Real Mollie webhooks are application/x-www-form-urlencoded; JSON is only
// parsed when the content type says the body is JSON.
func parseMollieWebhookID(body []byte, contentType string) (string, error) {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if mediaType == "application/json" {
		var payload MollieWebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", fmt.Errorf("invalid json: %w", err)
		}
		return payload.ID, nil
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return "", fmt.Errorf("invalid form body: %w", err)
	}
	return values.Get("id"), nil
}

func headerValue(headers map[string]string, key string) string {
	for headerKey, value := range headers {
		if strings.EqualFold(headerKey, key) {
			return value
		}
	}
	return ""
}

func (s *Service) recordWebhookSignatureFailure(provider string) {
	if s.metrics == nil || s.metrics.WebhookSignatureFailures == nil {
		return
	}
	s.metrics.WebhookSignatureFailures.WithLabelValues(provider).Inc()
}

func (s *Service) handleMolliePaymentWebhook(parentCtx context.Context, paymentID string, rawBody []byte) (string, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()

	payment, err := s.mollieClient.GetPayment(ctx, paymentID)
	if err != nil {
		return "", errMollieUnknownPayment
	}

	status := strings.ToLower(payment.Status)
	if status == "" {
		return "", fmt.Errorf("missing Mollie payment status")
	}

	eventID := mollieEventIDForPayment(payment, status)
	claim, claimErr := s.claimWebhookEvent(ctx, "mollie", eventID, "payment", "", rawBody)
	if claimErr != nil {
		return eventID, claimErr
	}
	if !claim.claimed {
		return eventID, nil
	}

	// Mollie reports refund/chargeback movement on the original payment
	// rather than firing a separate event. Apply the reversal ledger
	// movement before mapping the status, then still reconcile the payment
	// state in case this is the first local observation of the payment.
	if _, refundErr := s.applyMolliePaymentReversalsIfAny(ctx, payment); refundErr != nil {
		return eventID, refundErr
	}

	newStatus, ok := mapMolliePaymentStatus(status)
	if !ok {
		s.logger.WithFields(logging.Fields{
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
	billingPaymentID := mollieMetadataString(payment.Metadata, "billing_payment_id")
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

		if _, execErr := s.db.ExecContext(ctx, `
			INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id)
			VALUES ($1, $2)
			ON CONFLICT (tenant_id) DO UPDATE SET mollie_customer_id = $2
		`, tenantID, payment.CustomerID); execErr != nil {
			return "", fmt.Errorf("failed to upsert Mollie customer mapping: %w", execErr)
		}

		mandate, mandateErr := s.mollieClient.GetMandate(ctx, payment.CustomerID, payment.MandateID)
		if mandateErr != nil {
			return "", fmt.Errorf("failed to fetch Mollie mandate: %w", mandateErr)
		}
		info := s.mollieClient.ExtractMandateInfo(mandate, payment.CustomerID)
		if upsertErr := s.upsertMollieMandate(tenantID, info); upsertErr != nil {
			return "", upsertErr
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
		// Mollie reconciliation fetches authoritative payment status before
		// reaching this branch, so the funds have settled.
		if err := s.handlePrepaidCheckoutCompleted(ctx, payment.ID, payment.ID, tenantID, topupID, amountCents, currency, ProviderMollie, true); err != nil {
			return "", err
		}
		return eventID, nil
	}

	// Subscription installments: Mollie auto-creates a payment per period and
	// fires this webhook with payment.SubscriptionID set. We reconcile by
	// locating the local tenant_subscription, finding the matching invoice
	// for the period that contains payment.CreatedAt, inserting a pending
	// billing_payments row keyed by the Mollie payment id, then falling
	// through to updateInvoicePaymentStatus which will confirm it and flip
	// the invoice paid. metadata.invoice_id is set when the on-demand charge
	// helper (overage collection) creates the payment; in that case we skip
	// the subscription-period lookup.
	if payment.SubscriptionID != "" && invoiceID == "" {
		if tenantID == "" {
			if scanErr := s.db.QueryRowContext(ctx, `
				SELECT tenant_id FROM purser.tenant_subscriptions WHERE mollie_subscription_id = $1
			`, payment.SubscriptionID).Scan(&tenantID); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
				s.logger.WithError(scanErr).WithField("mollie_subscription_id", payment.SubscriptionID).Warn("Failed to resolve tenant_id from subscription")
			}
		}
		resolvedInvoiceID, resolveErr := s.resolveMollieSubscriptionInvoice(ctx, payment.SubscriptionID, payment)
		if resolveErr != nil {
			return eventID, resolveErr
		}
		if resolvedInvoiceID == "" {
			// Out-of-order: Mollie fired the subscription-installment webhook
			// before the local invoice for the period was finalized. Persist
			// the observation so invoice finalization drains it; do not
			// silently no-op, do not return an error that retries forever.
			if obsErr := s.upsertMolliePaymentObservation(ctx, tenantID, payment, rawBody); obsErr != nil {
				return eventID, fmt.Errorf("persist mollie observation: %w", obsErr)
			}
			s.logger.WithFields(logging.Fields{
				"mollie_payment_id":      payment.ID,
				"mollie_subscription_id": payment.SubscriptionID,
				"tenant_id":              tenantID,
			}).Info("Mollie subscription payment observed before local invoice; awaiting finalize drain")
			return eventID, nil
		}
		invoiceID = resolvedInvoiceID
		if invoiceID != "" && payment.Amount != nil {
			amountCents, _, amtErr := mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
			if amtErr == nil {
				amountStr := centsToDecimalString(amountCents, payment.Amount.Currency)
				if _, insertErr := s.db.ExecContext(ctx, `
					INSERT INTO purser.billing_payments (invoice_id, method, amount, currency, tx_id, status, created_at, updated_at)
					VALUES ($1, 'card', $2::numeric, $3, $4, 'pending', NOW(), NOW())
					ON CONFLICT DO NOTHING
				`, invoiceID, amountStr, payment.Amount.Currency, payment.ID); insertErr != nil {
					s.logger.WithError(insertErr).WithField("mollie_payment_id", payment.ID).Warn("Failed to insert subscription-installment billing_payment")
				}
			}
		}
		if sub, subErr := s.mollieClient.GetSubscription(ctx, payment.CustomerID, payment.SubscriptionID); subErr == nil && sub.NextPaymentDate != nil {
			if _, persistErr := s.db.ExecContext(ctx, `
				UPDATE purser.tenant_subscriptions
				SET mollie_next_payment_date = $1, updated_at = NOW()
				WHERE mollie_subscription_id = $2
			`, sub.NextPaymentDate.String(), payment.SubscriptionID); persistErr != nil {
				s.logger.WithError(persistErr).WithField("mollie_subscription_id", payment.SubscriptionID).Warn("Failed to persist next_payment_date")
			}
		}
	}

	if invoiceID == "" {
		invoiceID = referenceID
	}
	if billingPaymentID != "" {
		if _, attachErr := s.db.ExecContext(ctx, `
			UPDATE purser.billing_payments
			SET tx_id = $1, updated_at = NOW()
			WHERE id = $2
			  AND status = 'pending'
			  AND (tx_id IS NULL OR tx_id = $1 OR tx_id LIKE 'mollie-overage-intent:%')
		`, payment.ID, billingPaymentID); attachErr != nil {
			return "", fmt.Errorf("attach Mollie payment id to billing payment: %w", attachErr)
		}
	}
	paymentUpdated, err := s.updateInvoicePaymentStatus("mollie", payment.ID, invoiceID, newStatus)
	if err != nil {
		return "", err
	}
	if !paymentUpdated {
		return eventID, nil
	}

	if newStatus == "confirmed" || newStatus == "failed" {
		if tenantID == "" && invoiceID != "" {
			if err := s.db.QueryRowContext(ctx, `SELECT tenant_id FROM purser.billing_invoices WHERE id = $1`, invoiceID).Scan(&tenantID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				s.logger.WithError(err).WithField("invoice_id", invoiceID).Warn("Failed to resolve tenant from invoice, billing event will be skipped")
			}
		}
		if tenantID != "" && payment.Amount != nil {
			amountCents, currency, err := mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
			if err == nil {
				eventType := eventPaymentSucceeded
				if newStatus == "failed" {
					eventType = eventPaymentFailed
				}
				emitBillingEvent(s.db, s.logger, eventType, tenantID, "payment", payment.ID, &ipcpb.BillingEvent{
					PaymentId: payment.ID,
					InvoiceId: invoiceID,
					Amount:    float64(amountCents) / float64(intPow10(currencyMinorUnitExponent(currency))),
					Currency:  currency,
					Provider:  "mollie",
					Status:    newStatus,
				})
			}
		}
	}

	return eventID, nil
}

func mollieEventID(resource, id, status string) string {
	return fmt.Sprintf("%s:%s:%s", resource, id, status)
}

func mollieEventIDForPayment(payment *mollie.Payment, status string) string {
	if payment == nil {
		return mollieEventID("payment", "", status)
	}
	parts := []string{"payment", payment.ID, status}
	if payment.AmountRefunded != nil && payment.AmountRefunded.Value != "" {
		parts = append(parts, "refunded", payment.AmountRefunded.Value, strings.ToUpper(payment.AmountRefunded.Currency))
	}
	if payment.AmountChargedBack != nil && payment.AmountChargedBack.Value != "" {
		parts = append(parts, "charged_back", payment.AmountChargedBack.Value, strings.ToUpper(payment.AmountChargedBack.Currency))
	}
	return strings.Join(parts, ":")
}

// upsertMolliePaymentObservation records an out-of-order Mollie subscription
// payment webhook when the local invoice has not been finalized yet. The
// drain at invoice finalization time looks rows up by (tenant_id,
// mollie_subscription_id) and attaches them to the new invoice. The unique
// index on mollie_payment_id collapses concurrent webhook retries to a
// single observation row.
// StripeChargeObject minimally describes a Stripe charge as it appears on
// charge.refunded and charge.dispute.* events. We only consume what the
// reversal pipeline needs.
type StripeChargeObject struct {
	ID             string `json:"id"`
	PaymentIntent  string `json:"payment_intent"`
	Amount         int64  `json:"amount"`
	AmountRefunded int64  `json:"amount_refunded"`
	AmountCaptured int64  `json:"amount_captured"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	Refunded       bool   `json:"refunded"`
	DisputeID      string `json:"dispute"`
	BalanceTxn     string `json:"balance_transaction"`
	Refunds        struct {
		Data []struct {
			ID       string `json:"id"`
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
			Reason   string `json:"reason"`
			Status   string `json:"status"`
		} `json:"data"`
	} `json:"refunds"`
}

// StripeDisputeObject is the slim shape we consume from charge.dispute.*
// events. Funds-withdrawn / funds-reinstated transitions tweak the same
// payment_reversals row keyed on the dispute id.
type StripeDisputeObject struct {
	ID       string `json:"id"`
	Charge   string `json:"charge"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Reason   string `json:"reason"`
	Status   string `json:"status"`
}

// handleStripeChargeRefunded processes a charge.refunded webhook by writing
// payment_reversals rows for each new refund and applying their effect to
// billing_payments + invoice net-paid state. Idempotent on provider refund
// ids: replays do not double-credit.
func (s *Service) handleStripeChargeRefunded(payload StripeWebhookPayload) error {
	var charge StripeChargeObject
	if err := json.Unmarshal(payload.Data.Object, &charge); err != nil {
		return fmt.Errorf("failed to parse charge: %w", err)
	}
	if charge.PaymentIntent == "" {
		// Refund on a charge that was not created through a PaymentIntent.
		// All FrameWorks-side flows use PaymentIntents, so the absence
		// means this is not our charge; do not error.
		s.logger.WithField("charge_id", charge.ID).Debug("Ignoring Stripe charge.refunded without payment_intent")
		return nil
	}
	ctx := context.Background()
	if charge.PaymentIntent != "" {
		var tenantID, paymentID sql.NullString
		if scanErr := s.db.QueryRowContext(ctx, `
			SELECT i.tenant_id, p.id
			FROM purser.billing_payments p
			JOIN purser.billing_invoices i ON i.id = p.invoice_id
			WHERE p.tx_id = $1 AND p.method = 'card'
			ORDER BY p.created_at DESC
			LIMIT 1
		`, charge.PaymentIntent).Scan(&tenantID, &paymentID); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			s.logger.WithError(scanErr).WithField("payment_intent_id", charge.PaymentIntent).Debug("Stripe charge mapping payment lookup failed")
		}
		if tenantID.Valid && paymentID.Valid {
			if mapErr := s.upsertProviderPaymentObject(ctx, providerPaymentObjectInput{
				provider:         "stripe",
				objectType:       "charge",
				providerObjectID: charge.ID,
				tenantID:         tenantID.String,
				localRefType:     "payment",
				localRefID:       paymentID.String,
				metadata: map[string]any{
					"payment_intent_id": charge.PaymentIntent,
				},
			}); mapErr != nil {
				s.logger.WithError(mapErr).WithField("charge_id", charge.ID).Warn("Failed to record Stripe charge mapping")
			}
		}
	}
	for _, r := range charge.Refunds.Data {
		if r.ID == "" || r.Amount <= 0 {
			continue
		}
		if r.Status != "succeeded" {
			// Pending/failed refunds are not money movement yet; skip.
			continue
		}
		applied, applyErr := s.applyProviderReversal(ctx, providerReversalInput{
			provider:           "stripe",
			reversalType:       "refund",
			providerReversalID: r.ID,
			providerChargeID:   charge.ID,
			providerPaymentID:  charge.PaymentIntent,
			amountCents:        r.Amount,
			currency:           strings.ToUpper(r.Currency),
			reason:             r.Reason,
		})
		if applyErr != nil {
			return applyErr
		}
		if !applied {
			s.logger.WithFields(logging.Fields{
				"refund_id":   r.ID,
				"payment_int": charge.PaymentIntent,
			}).Debug("Stripe refund already applied; webhook replay")
		}
	}
	return nil
}

// handleStripeChargeDispute applies dispute money movement to the reversal
// ledger. charge.dispute.funds_withdrawn is the cash-out event; we treat the
// creation event as informational, the funds_withdrawn as the reversal,
// and funds_reinstated as a reversal of the reversal (status=needs_review
// so ops decide whether to clean up automatically or by hand).
func (s *Service) handleStripeChargeDispute(payload StripeWebhookPayload) error {
	var dispute StripeDisputeObject
	if err := json.Unmarshal(payload.Data.Object, &dispute); err != nil {
		return fmt.Errorf("failed to parse dispute: %w", err)
	}
	if dispute.Charge == "" {
		return nil
	}
	ctx := context.Background()
	// Look up the original Stripe charge to find the payment_intent (and
	// thus our local billing_payments row). The dispute event itself does
	// not always carry payment_intent directly; provider_payment_objects
	// would be used if populated, otherwise we fall back to the charge id.
	var providerPaymentID sql.NullString
	if scanErr := s.db.QueryRowContext(ctx, `
				SELECT MAX(metadata->>'payment_intent_id')
				FROM purser.provider_payment_objects
				WHERE provider = 'stripe' AND object_type = 'charge' AND provider_object_id = $1
			`, dispute.Charge).Scan(&providerPaymentID); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		s.logger.WithError(scanErr).WithField("charge_id", dispute.Charge).Debug("provider_payment_objects lookup failed for dispute")
	}
	if !providerPaymentID.Valid || providerPaymentID.String == "" {
		return fmt.Errorf("dispute %s references unmapped charge %s: %w", dispute.ID, dispute.Charge, errWebhookMissingLocalReference)
	}

	switch payload.Type {
	case "charge.dispute.created":
		// Informational: persist a pending reversal row but do not move
		// money until funds_withdrawn.
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO purser.payment_reversals (
				tenant_id, payment_id, provider, reversal_type,
				provider_reversal_id, provider_charge_id,
				amount_cents, currency, status, reason
			)
			SELECT i.tenant_id, p.id, 'stripe', 'dispute',
			       $1, $2, $3, $4, 'pending', $5
			FROM purser.billing_payments p
				JOIN purser.billing_invoices i ON p.invoice_id = i.id
				WHERE p.tx_id = $6
				  AND p.method = 'card'
			ORDER BY p.created_at DESC
			LIMIT 1
			ON CONFLICT (provider, provider_reversal_id) DO NOTHING
		`, dispute.ID, dispute.Charge, dispute.Amount, strings.ToUpper(dispute.Currency), dispute.Reason, providerPaymentID.String)
		if err != nil {
			return fmt.Errorf("record dispute creation: %w", err)
		}
		return nil
	case "charge.dispute.funds_withdrawn", "charge.dispute.closed":
		applied, applyErr := s.applyProviderReversal(ctx, providerReversalInput{
			provider:           "stripe",
			reversalType:       "dispute",
			providerReversalID: dispute.ID,
			providerChargeID:   dispute.Charge,
			providerPaymentID:  providerPaymentID.String,
			amountCents:        dispute.Amount,
			currency:           strings.ToUpper(dispute.Currency),
			reason:             dispute.Reason,
		})
		if applyErr != nil {
			return applyErr
		}
		_ = applied
		return nil
	case "charge.dispute.funds_reinstated":
		// Reversed dispute: flag for operator review rather than silently
		// reversing automatically; the negative balance / clawback may have
		// already paid out.
		_, err := s.db.ExecContext(ctx, `
			UPDATE purser.payment_reversals
			SET status = 'needs_review', operator_review_required = TRUE, updated_at = NOW()
			WHERE provider = 'stripe' AND provider_reversal_id = $1
		`, dispute.ID)
		if err != nil {
			return fmt.Errorf("flag dispute reinstatement: %w", err)
		}
		return nil
	default:
		return nil
	}
}

// providerReversalInput is the normalized input the central reversal helper
// accepts. Stripe refund, Stripe dispute funds_withdrawn, Mollie refund, and
// Mollie chargeback all map onto this shape.
type providerReversalInput struct {
	provider           string
	reversalType       string
	providerReversalID string
	providerChargeID   string
	providerPaymentID  string // Stripe PaymentIntent id or Mollie payment id
	amountCents        int64
	currency           string
	reason             string
}

// applyProviderReversal writes the reversal ledger row, credits the
// originating billing_payments.reversed_amount_cents, and reopens the
// invoice if net confirmed payments are now below the invoice amount.
// Returns (applied, error) — applied=false means we found an existing
// terminal reversal row (replay).
func (s *Service) applyProviderReversal(parentCtx context.Context, in providerReversalInput) (bool, error) {
	if in.providerReversalID == "" || in.amountCents <= 0 {
		return false, fmt.Errorf("invalid provider reversal input")
	}
	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin reversal tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				s.logger.WithError(rbErr).Warn("Failed to roll back reversal tx")
			}
		}
	}()

	// Locate the originating billing_payments row by tx_id. For Stripe
	// we match on the PaymentIntent id; for Mollie on the payment id.
	var paymentID, invoiceID, tenantID, paymentCurrency string
	var pendingTopupID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT p.id, p.invoice_id, i.tenant_id, p.currency
		FROM purser.billing_payments p
		JOIN purser.billing_invoices i ON p.invoice_id = i.id
		WHERE p.method = 'card'
		  AND p.tx_id = $1
		ORDER BY p.created_at DESC
		LIMIT 1
	`, in.providerPaymentID).Scan(&paymentID, &invoiceID, &tenantID, &paymentCurrency)
	if errors.Is(err, sql.ErrNoRows) {
		// Maybe it was a prepaid top-up rather than an invoice payment.
		err = tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, currency
			FROM purser.pending_topups
			WHERE (provider_payment_id = $1 OR checkout_id = $1)
			ORDER BY created_at DESC
			LIMIT 1
		`, in.providerPaymentID).Scan(&pendingTopupID, &tenantID, &paymentCurrency)
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("reversal %s references unknown provider payment %s: %w",
				in.providerReversalID, in.providerPaymentID, errWebhookMissingLocalReference)
		}
		if err != nil {
			return false, fmt.Errorf("lookup topup for reversal: %w", err)
		}
	} else if err != nil {
		return false, fmt.Errorf("lookup payment for reversal: %w", err)
	}

	// Sanity: provider may report the reversal in a different currency
	// than the original payment. Refuse to reconcile rather than mixing.
	if paymentCurrency != "" && in.currency != "" && paymentCurrency != in.currency {
		return false, fmt.Errorf("reversal currency %s != payment currency %s", in.currency, paymentCurrency)
	}

	// Idempotent reversal-ledger insert. A pending dispute observation may
	// transition to succeeded when the money-moving provider event arrives;
	// already-succeeded rows return no id and are treated as replays.
	var reversalID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO purser.payment_reversals (
			tenant_id, payment_id, pending_topup_id, invoice_id,
			provider, reversal_type, provider_reversal_id, provider_charge_id,
			amount_cents, currency, status, reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'succeeded', $11)
		ON CONFLICT (provider, provider_reversal_id) DO UPDATE SET
			payment_id = COALESCE(purser.payment_reversals.payment_id, EXCLUDED.payment_id),
			pending_topup_id = COALESCE(purser.payment_reversals.pending_topup_id, EXCLUDED.pending_topup_id),
			invoice_id = COALESCE(purser.payment_reversals.invoice_id, EXCLUDED.invoice_id),
			provider_charge_id = COALESCE(purser.payment_reversals.provider_charge_id, EXCLUDED.provider_charge_id),
			status = 'succeeded',
			updated_at = NOW()
			WHERE purser.payment_reversals.status = 'pending'
		RETURNING id
	`, tenantID, nullableString(paymentID), pendingTopupID, nullableString(invoiceID),
		in.provider, in.reversalType, in.providerReversalID, nullableString(in.providerChargeID),
		in.amountCents, in.currency, nullableString(in.reason)).Scan(&reversalID)
	if errors.Is(err, sql.ErrNoRows) {
		// Replay: row already existed, nothing more to do.
		if commitErr := tx.Commit(); commitErr != nil {
			return false, commitErr
		}
		committed = true
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert reversal: %w", err)
	}

	// Apply money movement based on which side the reversal hits.
	if paymentID != "" && invoiceID != "" {
		if err := applyInvoicePaymentReversalTx(ctx, tx, paymentID, invoiceID, in.amountCents, in.currency); err != nil {
			return false, err
		}
		// Operator credit clawback: marketplace cluster lines on this
		// invoice need a reverses_ledger_id row pointing at the original
		// accrual. The clawback runs in the same transaction as the
		// invoice-side reversal so the ledger never disagrees with the
		// invoice state.
		if err := applyOperatorCreditClawbackTx(ctx, tx, invoiceID, reversalID, in.amountCents); err != nil {
			return false, err
		}
	}
	if pendingTopupID.Valid && tenantID != "" {
		if err := s.applyPrepaidTopupReversalTx(ctx, tx, tenantID, pendingTopupID.String, reversalID, in.amountCents, in.currency, in.reason); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit reversal tx: %w", err)
	}
	committed = true
	return true, nil
}

// applyInvoicePaymentReversalTx credits reversed_amount_cents on the
// originating billing_payments row, denormalizes the invoice's
// reversed_paid_cents, and reopens the invoice (status pending,
// reopened_at = NOW(), paid_at preserved) if net confirmed payments now
// fall below the invoice amount.
func applyInvoicePaymentReversalTx(ctx context.Context, tx *sql.Tx, paymentID, invoiceID string, amountCents int64, currency string) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.billing_payments
		SET reversed_amount_cents = reversed_amount_cents + $1, updated_at = NOW()
		WHERE id = $2
	`, amountCents, paymentID); err != nil {
		return fmt.Errorf("credit reversed_amount_cents: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.billing_invoices
		SET reversed_paid_cents = reversed_paid_cents + $1, updated_at = NOW()
		WHERE id = $2
	`, amountCents, invoiceID); err != nil {
		return fmt.Errorf("credit invoice reversed_paid_cents: %w", err)
	}
	// Reopen if net confirmed payments now fall below the invoice amount.
	// paid_at is preserved as the first-paid timestamp; reopened_at records
	// the most recent transition out of paid.
	_, err := tx.ExecContext(ctx, `
		UPDATE purser.billing_invoices i
		SET status = 'pending',
		    reopened_at = NOW(),
		    updated_at = NOW()
		WHERE i.id = $1
		  AND i.status = 'paid'
		  AND i.currency = $2
		  AND (
		      SELECT COALESCE(SUM(p.amount - COALESCE(p.reversed_amount_cents, 0)::numeric / 100), 0)
		      FROM purser.billing_payments p
		      WHERE p.invoice_id = i.id
		        AND p.status = 'confirmed'
		        AND p.currency = i.currency
		  ) < i.amount
	`, invoiceID, currency)
	if err != nil {
		return fmt.Errorf("reopen invoice on reversal: %w", err)
	}
	return nil
}

// applyOperatorCreditClawbackTx writes one clawback per reversal/accrual pair,
// prorated by the reversed amount over the invoice total. The link table makes
// replay idempotent while preserving every ledger row that affects payout
// reporting.
func applyOperatorCreditClawbackTx(ctx context.Context, tx *sql.Tx, invoiceID, reversalID string, reversedCents int64) error {
	if invoiceID == "" || reversedCents <= 0 {
		return nil
	}
	// Read invoice total in cents (NUMERIC(10,2) → bigint via × 100).
	var invoiceCents int64
	if err := tx.QueryRowContext(ctx, `
		SELECT (amount * 100)::bigint FROM purser.billing_invoices WHERE id = $1
	`, invoiceID).Scan(&invoiceCents); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read invoice amount for clawback: %w", err)
	}
	if invoiceCents <= 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, cluster_owner_tenant_id, cluster_id, currency, gross_cents, platform_fee_cents, payable_cents, period_start, period_end
		FROM purser.operator_credit_ledger
		WHERE invoice_id = $1
		  AND entry_type = 'accrual'
	`, invoiceID)
	if err != nil {
		return fmt.Errorf("list operator accruals: %w", err)
	}
	defer rows.Close()
	type accrual struct {
		id, ownerTenant, clusterID, currency string
		gross, fee, payable                  int64
		periodStart, periodEnd               time.Time
	}
	var todo []accrual
	for rows.Next() {
		var a accrual
		if scanErr := rows.Scan(&a.id, &a.ownerTenant, &a.clusterID, &a.currency, &a.gross, &a.fee, &a.payable, &a.periodStart, &a.periodEnd); scanErr != nil {
			return fmt.Errorf("scan operator accrual: %w", scanErr)
		}
		todo = append(todo, a)
	}
	if rErr := rows.Err(); rErr != nil {
		return fmt.Errorf("iterate operator accruals: %w", rErr)
	}
	if len(todo) == 0 {
		return nil
	}
	// Proration factor: reversedCents / invoiceCents. We compute each
	// clawback in cents by (accrual.x * reversedCents / invoiceCents)
	// using integer math so totals stay exact for typical refunds.
	var linkedClawbackID string
	for _, a := range todo {
		clawGross := (a.gross * reversedCents) / invoiceCents
		clawFee := (a.fee * reversedCents) / invoiceCents
		clawPayable := (a.payable * reversedCents) / invoiceCents
		if clawGross == 0 && clawFee == 0 && clawPayable == 0 {
			continue
		}
		var clawbackID string
		if err := tx.QueryRowContext(ctx, `
			WITH existing AS (
				SELECT operator_credit_ledger_id AS id
				FROM purser.operator_credit_clawback_reversals
				WHERE payment_reversal_id = $5::uuid
				  AND accrual_ledger_id = $1::uuid
			),
			inserted AS (
				INSERT INTO purser.operator_credit_ledger (
					source_type, invoice_line_item_id, storage_provider_usage_record_id,
					usage_adjustment_id, stripe_invoice_id,
					entry_type, reverses_ledger_id,
					cluster_owner_tenant_id, cluster_id, invoice_id, period_start, period_end,
					currency, gross_cents, platform_fee_cents, payable_cents, status, notes
				)
			SELECT ol.source_type, ol.invoice_line_item_id, ol.storage_provider_usage_record_id,
			       ol.usage_adjustment_id, ol.stripe_invoice_id,
			       'clawback', ol.id,
			       ol.cluster_owner_tenant_id, ol.cluster_id, ol.invoice_id,
			       ol.period_start, ol.period_end, ol.currency,
				       -$2, -$3, -$4, 'clawed_back',
				       jsonb_build_object('payment_reversal_id', $5::text)
				FROM purser.operator_credit_ledger ol
				WHERE ol.id = $1
				  AND NOT EXISTS (SELECT 1 FROM existing)
				RETURNING id
			),
			chosen AS (
				SELECT id FROM existing
				UNION ALL
				SELECT id FROM inserted
				LIMIT 1
			),
			mapped AS (
				INSERT INTO purser.operator_credit_clawback_reversals (
					payment_reversal_id, operator_credit_ledger_id, accrual_ledger_id
				)
				SELECT $5::uuid, id, $1::uuid FROM chosen
				ON CONFLICT (payment_reversal_id, accrual_ledger_id) DO UPDATE SET
					operator_credit_ledger_id = EXCLUDED.operator_credit_ledger_id
				RETURNING operator_credit_ledger_id
			)
			SELECT operator_credit_ledger_id FROM mapped
		`, a.id, clawGross, clawFee, clawPayable, reversalID).Scan(&clawbackID); err != nil {
			return fmt.Errorf("insert clawback for accrual %s: %w", a.id, err)
		}
		if linkedClawbackID == "" {
			linkedClawbackID = clawbackID
		}
		// Mark the original accrual clawed_back if the signed clawback fully
		// covers the signed payable amount; otherwise leave at its current state.
		if absCents(clawPayable) >= absCents(a.payable) {
			if _, err := tx.ExecContext(ctx, `
				UPDATE purser.operator_credit_ledger
				SET status = 'clawed_back', updated_at = NOW()
				WHERE id = $1 AND status IN ('held', 'accruing', 'eligible')
			`, a.id); err != nil {
				return fmt.Errorf("mark accrual clawed_back: %w", err)
			}
		}
	}
	if linkedClawbackID != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE purser.payment_reversals
			SET operator_credit_ledger_id = $1, updated_at = NOW()
			WHERE id = $2
		`, linkedClawbackID, reversalID); err != nil {
			return fmt.Errorf("link reversal to clawback ledger row: %w", err)
		}
	}
	return nil
}

func absCents(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// applyPrepaidTopupReversalTx writes the negative balance_transactions row
// for a refunded prepaid top-up. If the refund would drop the prepaid
// balance below zero, operator_review_required is flipped TRUE on the
// reversal row so ops can decide whether to recollect or write off.
func (s *Service) applyPrepaidTopupReversalTx(ctx context.Context, tx *sql.Tx, tenantID, topupID, reversalID string, amountCents int64, currency, reason string) error {
	// Increment the refunded marker on pending_topups.
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.pending_topups
		SET refunded_amount_cents = refunded_amount_cents + $1, updated_at = NOW()
		WHERE id = $2
	`, amountCents, topupID); err != nil {
		return fmt.Errorf("credit pending_topups refunded_amount_cents: %w", err)
	}

	// Look at the current balance before debiting so we can flag negative.
	var currentBalance int64
	if err := tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency).Scan(&currentBalance); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read prepaid balance: %w", err)
	}
	willGoNegative := currentBalance < amountCents

	// Negative balance transaction. Idempotent on (tenant_id, reference_type,
	// reference_id) where reference_id is the reversal row id.
	reversalUUID, err := uuid.Parse(reversalID)
	if err != nil {
		return fmt.Errorf("parse reversal id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			tenant_id, amount_cents, balance_after_cents, transaction_type,
			description, reference_id, reference_type, actor_kind, reason
		)
		SELECT $1,
		       -$2,
		       COALESCE((SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = $3), 0) - $2,
		       'refund', $4, $5, 'payment_reversal', 'webhook', $6
		ON CONFLICT (tenant_id, reference_type, reference_id) DO NOTHING
	`, tenantID, amountCents, currency, fmt.Sprintf("Refund/chargeback %s", reason), reversalUUID, reason); err != nil {
		return fmt.Errorf("insert reversal balance_transaction: %w", err)
	}

	// Apply to the live balance.
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = balance_cents - $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, amountCents, tenantID, currency); err != nil {
		return fmt.Errorf("debit prepaid balance: %w", err)
	}

	if willGoNegative {
		if _, err := tx.ExecContext(ctx, `
			UPDATE purser.payment_reversals
			SET operator_review_required = TRUE, updated_at = NOW()
			WHERE id = $1
		`, reversalUUID); err != nil {
			return fmt.Errorf("flag reversal for operator review: %w", err)
		}
		s.logger.WithFields(logging.Fields{
			"tenant_id":    tenantID,
			"reversal_id":  reversalID,
			"amount_cents": amountCents,
			"currency":     currency,
		}).Warn("Prepaid balance reversal would go negative; flagged for operator review")
	}

	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableUUIDString(s string) any {
	if s == "" {
		return nil
	}
	if _, err := uuid.Parse(s); err != nil {
		return nil
	}
	return s
}

type providerPaymentObjectInput struct {
	provider         string
	objectType       string
	providerObjectID string
	tenantID         string
	localRefType     string
	localRefID       string
	intentID         string
	metadata         map[string]any
}

func (s *Service) upsertProviderPaymentObject(ctx context.Context, in providerPaymentObjectInput) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	if in.provider == "" || in.objectType == "" || in.providerObjectID == "" {
		return fmt.Errorf("missing provider object identity")
	}
	metadata := []byte(`{}`)
	if in.metadata != nil {
		b, err := json.Marshal(in.metadata)
		if err != nil {
			return fmt.Errorf("marshal provider object metadata: %w", err)
		}
		metadata = b
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO purser.provider_payment_objects (
			provider, object_type, provider_object_id, tenant_id,
			local_reference_type, local_reference_id, intent_id, metadata,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, NOW(), NOW())
		ON CONFLICT (provider, object_type, provider_object_id) DO UPDATE SET
			tenant_id = COALESCE(EXCLUDED.tenant_id, purser.provider_payment_objects.tenant_id),
			local_reference_type = COALESCE(EXCLUDED.local_reference_type, purser.provider_payment_objects.local_reference_type),
			local_reference_id = COALESCE(EXCLUDED.local_reference_id, purser.provider_payment_objects.local_reference_id),
			intent_id = COALESCE(EXCLUDED.intent_id, purser.provider_payment_objects.intent_id),
			metadata = purser.provider_payment_objects.metadata || EXCLUDED.metadata,
			updated_at = NOW()
	`, in.provider, in.objectType, in.providerObjectID,
		nullableUUIDString(in.tenantID), nullableString(in.localRefType),
		nullableUUIDString(in.localRefID), nullableUUIDString(in.intentID),
		string(metadata))
	if err != nil {
		return fmt.Errorf("upsert provider payment object: %w", err)
	}
	return nil
}

// applyMolliePaymentReversalsIfAny reconciles Mollie's cumulative refunded /
// charged-back totals by applying only the not-yet-recorded delta.
func (s *Service) applyMolliePaymentReversalsIfAny(ctx context.Context, payment *mollie.Payment) (bool, error) {
	if payment == nil {
		return false, nil
	}
	applied := false
	if payment.AmountRefunded != nil {
		cents, _, err := mollieAmountToCents(payment.AmountRefunded.Value, payment.AmountRefunded.Currency)
		if err != nil {
			return applied, err
		}
		delta, err := s.mollieReversalDelta(ctx, "refund", payment.ID, cents)
		if err != nil {
			return applied, err
		}
		if delta > 0 {
			didApply, applyErr := s.applyProviderReversal(ctx, providerReversalInput{
				provider:           "mollie",
				reversalType:       "refund",
				providerReversalID: fmt.Sprintf("mollie-refund:%s:%d", payment.ID, cents),
				providerChargeID:   payment.ID,
				providerPaymentID:  payment.ID,
				amountCents:        delta,
				currency:           strings.ToUpper(payment.AmountRefunded.Currency),
				reason:             "refund",
			})
			if applyErr != nil {
				return applied, applyErr
			}
			if didApply {
				applied = true
			}
		}
	}
	if payment.AmountChargedBack != nil {
		cents, _, err := mollieAmountToCents(payment.AmountChargedBack.Value, payment.AmountChargedBack.Currency)
		if err != nil {
			return applied, err
		}
		delta, err := s.mollieReversalDelta(ctx, "chargeback", payment.ID, cents)
		if err != nil {
			return applied, err
		}
		if delta > 0 {
			didApply, applyErr := s.applyProviderReversal(ctx, providerReversalInput{
				provider:           "mollie",
				reversalType:       "chargeback",
				providerReversalID: fmt.Sprintf("mollie-chargeback:%s:%d", payment.ID, cents),
				providerChargeID:   payment.ID,
				providerPaymentID:  payment.ID,
				amountCents:        delta,
				currency:           strings.ToUpper(payment.AmountChargedBack.Currency),
				reason:             "chargeback",
			})
			if applyErr != nil {
				return applied, applyErr
			}
			if didApply {
				applied = true
			}
		}
	}
	return applied, nil
}

func (s *Service) mollieReversalDelta(ctx context.Context, reversalType, paymentID string, cumulativeCents int64) (int64, error) {
	prefix := fmt.Sprintf("mollie-%s:%s:", reversalType, paymentID)
	var alreadyApplied int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM purser.payment_reversals
		WHERE provider = 'mollie'
		  AND reversal_type = $1
		  AND provider_reversal_id LIKE $2
		  AND status = 'succeeded'
	`, reversalType, prefix+"%").Scan(&alreadyApplied); err != nil {
		return 0, fmt.Errorf("lookup Mollie reversal delta: %w", err)
	}
	if cumulativeCents <= alreadyApplied {
		return 0, nil
	}
	return cumulativeCents - alreadyApplied, nil
}

func (s *Service) upsertMolliePaymentObservation(ctx context.Context, tenantID string, payment *mollie.Payment, rawBody []byte) error {
	if tenantID == "" {
		return fmt.Errorf("missing tenant_id for Mollie payment observation")
	}
	if payment == nil || payment.ID == "" {
		return fmt.Errorf("missing Mollie payment for observation")
	}
	if payment.Amount == nil {
		return fmt.Errorf("missing Mollie payment amount for observation")
	}
	amountCents, _, err := mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
	if err != nil {
		return err
	}
	var paidAt *time.Time
	if payment.PaidAt != nil {
		t := *payment.PaidAt
		paidAt = &t
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO purser.mollie_payment_observations (
			tenant_id, mollie_payment_id, mollie_subscription_id, mollie_mandate_id,
			sequence_type, status, amount_cents, currency, paid_at, raw_payload
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (mollie_payment_id) DO UPDATE SET
			status = EXCLUDED.status,
			amount_cents = EXCLUDED.amount_cents,
			currency = EXCLUDED.currency,
			paid_at = EXCLUDED.paid_at,
			attempt_count = purser.mollie_payment_observations.attempt_count + 1,
			updated_at = NOW()
	`, tenantID, payment.ID, payment.SubscriptionID, payment.MandateID,
		string(payment.SequenceType), strings.ToLower(payment.Status),
		amountCents, payment.Amount.Currency, paidAt, rawBody)
	return err
}

// drainMolliePaymentObservationsForInvoice attaches any unresolved Mollie
// subscription payment observations that belong to the given invoice's
// tenant and subscription, inserting billing_payments rows and routing them
// through the partial-payment-aware settlement helper. Called after invoice
// finalization commits so the newly-finalized invoice can consume observations
// the webhook handler parked earlier.
func (s *Service) drainMolliePaymentObservationsForInvoice(ctx context.Context, invoiceID string) error {
	if s.db == nil || invoiceID == "" {
		return nil
	}
	var tenantID, subscriptionID, invoiceCurrency string
	var periodStart, periodEnd sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT bi.tenant_id, COALESCE(ts.mollie_subscription_id, ''), bi.currency,
		       bi.period_start, bi.period_end
		FROM purser.billing_invoices bi
		JOIN purser.tenant_subscriptions ts ON ts.tenant_id = bi.tenant_id
		WHERE bi.id = $1
	`, invoiceID).Scan(&tenantID, &subscriptionID, &invoiceCurrency, &periodStart, &periodEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup invoice for observation drain: %w", err)
	}
	if subscriptionID == "" {
		return nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT mollie_payment_id, status, amount_cents, currency, paid_at
		FROM purser.mollie_payment_observations
		WHERE tenant_id = $1
		  AND mollie_subscription_id = $2
		  AND resolved_at IS NULL
		  AND ($3::timestamptz IS NULL OR paid_at IS NULL OR paid_at >= $3)
		  AND ($4::timestamptz IS NULL OR paid_at IS NULL OR paid_at <= $4)
		ORDER BY created_at ASC
	`, tenantID, subscriptionID, periodStart, periodEnd)
	if err != nil {
		return fmt.Errorf("list mollie observations: %w", err)
	}
	defer rows.Close()

	type pending struct {
		paymentID string
		status    string
		cents     int64
		currency  string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		var paidAt sql.NullTime
		if scanErr := rows.Scan(&p.paymentID, &p.status, &p.cents, &p.currency, &paidAt); scanErr != nil {
			return fmt.Errorf("scan mollie observation: %w", scanErr)
		}
		todo = append(todo, p)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("iterate mollie observations: %w", rowsErr)
	}

	for _, p := range todo {
		mapped, ok := mapMolliePaymentStatus(p.status)
		if !ok {
			continue
		}
		if p.currency != invoiceCurrency {
			// Currency mismatch: refuse to settle against this invoice.
			// The observation stays unresolved for operator review rather
			// than being silently dropped.
			s.logger.WithFields(logging.Fields{
				"mollie_payment_id": p.paymentID,
				"invoice_id":        invoiceID,
				"observed_currency": p.currency,
				"invoice_currency":  invoiceCurrency,
			}).Warn("Mollie observation currency does not match invoice; leaving unresolved")
			continue
		}
		amountStr := centsToDecimalString(p.cents, p.currency)
		if _, insertErr := s.db.ExecContext(ctx, `
			INSERT INTO purser.billing_payments (invoice_id, method, amount, currency, tx_id, status, created_at, updated_at)
			VALUES ($1, 'card', $2::numeric, $3, $4, 'pending', NOW(), NOW())
			ON CONFLICT DO NOTHING
		`, invoiceID, amountStr, p.currency, p.paymentID); insertErr != nil {
			return fmt.Errorf("insert drained mollie payment %s: %w", p.paymentID, insertErr)
		}
		if _, settleErr := s.updateInvoicePaymentStatus("mollie", p.paymentID, invoiceID, mapped); settleErr != nil {
			return fmt.Errorf("settle drained mollie payment %s: %w", p.paymentID, settleErr)
		}
		if _, resErr := s.db.ExecContext(ctx, `
			UPDATE purser.mollie_payment_observations
			SET resolved_at = NOW(), resolution = 'attached', invoice_id = $1, updated_at = NOW()
			WHERE mollie_payment_id = $2
		`, invoiceID, p.paymentID); resErr != nil {
			return fmt.Errorf("mark mollie observation resolved %s: %w", p.paymentID, resErr)
		}
	}
	return nil
}

// resolveMollieSubscriptionInvoice finds the local invoice that the given
// Mollie subscription installment payment should reconcile against. It
// matches by tenant + period containing payment.CreatedAt. Only payable
// invoices are returned; draft/manual_review invoices must not consume a real
// payment webhook before they can be finalized.
func (s *Service) resolveMollieSubscriptionInvoice(ctx context.Context, mollieSubscriptionID string, payment *mollie.Payment) (string, error) {
	if payment == nil || payment.CreatedAt == nil {
		return "", nil
	}
	var invoiceID string
	err := s.db.QueryRowContext(ctx, `
		SELECT bi.id
		FROM purser.billing_invoices bi
		JOIN purser.tenant_subscriptions ts ON ts.tenant_id = bi.tenant_id
		WHERE ts.mollie_subscription_id = $1
		  AND ($2::timestamptz)::date >= bi.period_start::date
		  AND ($2::timestamptz)::date <= bi.period_end::date
		  AND bi.status IN ('pending', 'overdue')
		ORDER BY bi.created_at DESC
		LIMIT 1
	`, mollieSubscriptionID, *payment.CreatedAt).Scan(&invoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup subscription invoice: %w", err)
	}
	return invoiceID, nil
}

func mollieMetadataString(meta any, key string) string {
	switch m := meta.(type) {
	case map[string]any:
		if val, ok := m[key]; ok {
			return fmt.Sprint(val)
		}
	case map[string]string:
		if val, ok := m[key]; ok {
			return val
		}
	case string:
		var parsed map[string]any
		if err := json.Unmarshal([]byte(m), &parsed); err == nil {
			if val, ok := parsed[key]; ok {
				return fmt.Sprint(val)
			}
		}
	}
	return ""
}

// mollieAmountToCents converts a Mollie amount string (e.g. "9.95") to integer
// minor units using exact decimal parsing. Float intermediates are not used
// because they round at fractional cents. The exponent comes from the
// currency: Mollie's two-decimal currencies (EUR, USD, GBP, etc.) use ×100;
// zero-decimal currencies (JPY, ISK) use ×1; three-decimal (BHD, KWD, OMR)
// use ×1000.
func mollieAmountToCents(value, currency string) (int64, string, error) {
	if value == "" || currency == "" {
		return 0, "", fmt.Errorf("missing Mollie amount")
	}
	exponent := currencyMinorUnitExponent(currency)
	d, err := decimal.NewFromString(value)
	if err != nil {
		return 0, "", fmt.Errorf("invalid Mollie amount %q: %w", value, err)
	}
	scaled := d.Shift(int32(exponent))
	if !scaled.Equal(scaled.Truncate(0)) {
		return 0, "", fmt.Errorf("mollie amount %q has more precision than %s allows", value, currency)
	}
	cents := scaled.IntPart()
	return cents, currency, nil
}

// currencyMinorUnitExponent returns the number of decimal places used by the
// currency's minor unit. Stripe and Mollie agree on these exponents.
func currencyMinorUnitExponent(currency string) int {
	switch strings.ToUpper(currency) {
	case "JPY", "ISK", "KRW", "VND", "CLP", "PYG", "RWF", "UGX", "XAF", "XOF":
		return 0
	case "BHD", "KWD", "OMR", "JOD", "TND":
		return 3
	default:
		return 2
	}
}

// centsToDecimalString renders integer minor units as a fixed-point decimal
// string ("995" with exponent 2 -> "9.95") for binding to NUMERIC columns.
// Avoids any float intermediate so values round-trip exactly.
func centsToDecimalString(cents int64, currency string) string {
	exponent := currencyMinorUnitExponent(currency)
	return decimal.New(cents, int32(-exponent)).StringFixed(int32(exponent))
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

func (s *Service) updateInvoicePaymentStatus(provider, txID, invoiceID, newStatus string) (bool, error) {
	var paymentID string
	var foundInvoiceID string
	ctx := context.Background()
	method := invoicePaymentMethodForProvider(provider)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin invoice payment status transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				s.logger.WithError(rollbackErr).Warn("Failed to roll back invoice payment status transaction")
			}
		}
	}()

	err = tx.QueryRowContext(ctx, `
		SELECT id, invoice_id FROM purser.billing_payments
		WHERE tx_id = $1 AND method = $2
	`, txID, method).Scan(&paymentID, &foundInvoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		if invoiceID == "" {
			return false, nil
		}
		err = tx.QueryRowContext(ctx, `
			SELECT id, invoice_id FROM purser.billing_payments
			WHERE invoice_id = $1 AND method = $2 AND status = 'pending' AND tx_id IS NULL
			ORDER BY created_at DESC
			LIMIT 1
		`, invoiceID, method).Scan(&paymentID, &foundInvoiceID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
	}
	if err != nil {
		return false, fmt.Errorf("failed to lookup payment: %w", err)
	}
	if invoiceID == "" {
		invoiceID = foundInvoiceID
	} else if foundInvoiceID != "" && foundInvoiceID != invoiceID {
		return false, fmt.Errorf("provider payment %s is linked to invoice %s, not webhook invoice %s", txID, foundInvoiceID, invoiceID)
	}

	var confirmedAt *time.Time
	now := time.Now()
	if newStatus == "confirmed" {
		confirmedAt = &now
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE purser.billing_payments
		SET status = $1, confirmed_at = $2, tx_id = COALESCE(NULLIF(tx_id, ''), $4), updated_at = NOW()
		WHERE id = $3
	`, newStatus, confirmedAt, paymentID, txID)
	if err != nil {
		return false, fmt.Errorf("failed to update payment status: %w", err)
	}
	attemptStatus := newStatus
	switch newStatus {
	case "confirmed":
		attemptStatus = "succeeded"
	case "failed":
		attemptStatus = "failed"
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE purser.billing_payment_attempts
		SET status = $1,
		    provider_payment_id = COALESCE(provider_payment_id, NULLIF($2, '')),
		    next_retry_at = NULL,
		    updated_at = NOW()
		WHERE payment_id = $3 AND provider = $4
	`, attemptStatus, txID, paymentID, provider); err != nil {
		return false, fmt.Errorf("failed to update payment attempt status: %w", err)
	}

	if invoiceID == "" {
		if err = tx.Commit(); err != nil {
			return false, fmt.Errorf("commit invoice payment status transaction: %w", err)
		}
		committed = true
		return true, nil
	}

	if newStatus == "confirmed" {
		// Settlement is partial-payment-aware and same-currency only. Sum
		// confirmed payments in the invoice's currency minus reversed
		// amounts; the invoice flips to paid only when net confirmed
		// payments cover the invoice amount. paid_at is set to the first
		// time the invoice reaches fully-paid and preserved if a later
		// refund reopens the invoice.
		result, updateErr := tx.ExecContext(ctx, `
			UPDATE purser.billing_invoices i
			SET status = 'paid',
			    paid_at = COALESCE(i.paid_at, $1),
			    updated_at = NOW()
			WHERE i.id = $2
			  AND i.status IN ('pending', 'overdue')
			  AND (
			      SELECT COALESCE(SUM(p.amount - COALESCE(p.reversed_amount_cents, 0)::numeric / 100), 0)
			      FROM purser.billing_payments p
			      WHERE p.invoice_id = i.id
			        AND p.status = 'confirmed'
			        AND p.currency = i.currency
			  ) >= i.amount
		`, now, invoiceID)
		if updateErr != nil {
			s.logger.WithFields(logging.Fields{
				"error":      updateErr.Error(),
				"invoice_id": invoiceID,
			}).Error("Failed to update invoice status")
			return false, fmt.Errorf("failed to update invoice status: %w", updateErr)
		}
		rowsAffected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return false, fmt.Errorf("check invoice update rows: %w", rowsErr)
		}
		if rowsAffected > 0 {
			if creditErr := operator.ComputeAndPersistCredits(ctx, tx, invoiceID, "paid"); creditErr != nil {
				return false, fmt.Errorf("persist operator credits: %w", creditErr)
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit invoice payment status transaction: %w", err)
	}
	committed = true

	if newStatus == "confirmed" || newStatus == "failed" {
		s.sendPaymentStatusEmail(invoiceID, provider, newStatus)
	}

	return true, nil
}

func invoicePaymentMethodForProvider(provider string) string {
	switch provider {
	case "stripe", "mollie":
		return "card"
	default:
		return provider
	}
}

func (s *Service) upsertMollieMandate(tenantID string, info billingmollie.MandateInfo) error {
	if tenantID == "" {
		return fmt.Errorf("missing tenant_id for Mollie mandate")
	}
	details, err := json.Marshal(info.Details)
	if err != nil {
		return fmt.Errorf("failed to serialize Mollie mandate details: %w", err)
	}

	_, err = s.db.ExecContext(context.Background(), `
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
func (s *Service) getTenantInfo(tenantID string) (*models.Tenant, error) {
	if s.qmClient == nil {
		return nil, fmt.Errorf("quartermaster client not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, err := s.qmClient.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenant from Quartermaster: %w", err)
	}

	if response.GetError() != "" {
		return nil, fmt.Errorf("quartermaster error: %s", response.GetError())
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
