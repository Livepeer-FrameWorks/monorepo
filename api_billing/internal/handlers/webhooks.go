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

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/operator"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
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
	ID             string `json:"id"`
	Status         string `json:"status"`
	LatestCharge   string `json:"latest_charge"`
	AmountReceived int64  `json:"amount_received"`
	Currency       string `json:"currency"`
	Metadata       struct {
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
	details, err := purserdb.New(s.db).GetPaymentStatusEmailDetails(ctx, invoiceID)

	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error":      err.Error(),
			"invoice_id": invoiceID,
		}).Error("Failed to get invoice and subscription info for payment email notification")
		return
	}
	tenantID, amount, currency := details.TenantID, details.Amount, details.Currency
	billingEmail, tenantName := details.BillingEmail.String, ""

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
	billingEmail, err := purserdb.New(s.db).GetTenantBillingEmail(context.Background(), tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get billing email for SCA notification")
		return
	}
	if !billingEmail.Valid || billingEmail.String == "" {
		s.logger.WithField("tenant_id", tenantID).Warn("No tenant email found for SCA notification")
		return
	}
	tenantName := ""
	if info, infoErr := s.getTenantInfo(tenantID); infoErr == nil && info != nil {
		tenantName = info.Name
	}
	if err := s.emailService.SendPaymentActionRequiredEmail(billingEmail.String, tenantName, invoiceRef, amount, strings.ToUpper(currency), actionURL); err != nil {
		s.logger.WithError(err).WithField("invoice_id", invoiceRef).Error("Failed to send payment action-required email")
	}
}

func (s *Service) sendTenantPaymentStatusEmail(tenantID, invoiceRef, provider, status string, amount float64, currency string) {
	if tenantID == "" {
		return
	}

	billingEmail, err := purserdb.New(s.db).GetTenantBillingEmail(context.Background(), tenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"error":     err.Error(),
			"tenant_id": tenantID,
		}).Error("Failed to get billing email for tenant payment notification")
		return
	}
	if !billingEmail.Valid || billingEmail.String == "" {
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
		err = s.emailService.SendPaymentSuccessEmail(billingEmail.String, tenantName, invoiceRef, amount, currency, provider)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail.String,
				"invoice_id":   invoiceRef,
				"provider":     provider,
			}).Error("Failed to send payment success email")
		}
	case "failed":
		err = s.emailService.SendPaymentFailedEmail(billingEmail.String, tenantName, invoiceRef, amount, currency, provider)
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"tenant_email": billingEmail.String,
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

// ProcessStripeWebhookGRPC verifies and durably accepts a Stripe webhook.
// Reconciliation is performed by the provider webhook inbox worker.
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
	if payload.ID == "" {
		return false, "Invalid payload", 400
	}

	s.logger.WithFields(logging.Fields{
		"event_id":   payload.ID,
		"event_type": payload.Type,
	}).Info("Received Stripe webhook via gRPC")

	return s.enqueueProviderWebhook(context.Background(), "stripe", payload.ID, headers, body)
}

func (s *Service) processStripeWebhookPayload(ctx context.Context, payload StripeWebhookPayload, signature string, body []byte) (bool, string, int) {
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
	row, err := purserdb.New(s.db).ClaimWebhookEvent(ctx, purserdb.ClaimWebhookEventParams{
		Provider: provider, EventID: eventID,
		EventType:       sql.NullString{String: eventType, Valid: eventType != ""},
		SignatureHeader: signatureHeader, RawPayload: rawPayload,
		LeaseSeconds: int32(webhookClaimLease / time.Second),
	})
	if err != nil {
		return nil, fmt.Errorf("claim webhook event: %w", err)
	}
	status, acquired := row.Status, row.Acquired
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
	_, err := purserdb.New(s.db).MarkWebhookEventSucceeded(ctx, purserdb.MarkWebhookEventSucceededParams{
		ProviderObjectID: providerObjectID, Provider: provider, EventID: eventID,
	})
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
	_, err := purserdb.New(s.db).MarkWebhookEventFailed(ctx, purserdb.MarkWebhookEventFailedParams{
		Status: target, LastError: sql.NullString{String: errMsg, Valid: errMsg != ""},
		Terminal: terminal, Provider: provider, EventID: eventID,
	})
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

	updated, err := s.updateInvoicePaymentStatus("stripe", obj.ID, invoiceID, status, providerSettlementEvidence{
		TenantID: obj.Metadata.TenantID, AmountCents: obj.AmountReceived, Currency: obj.Currency,
	})
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

	payment, paymentErr := purserdb.New(s.db).GetStripeInvoiceCardPayment(ctx, purserdb.GetStripeInvoiceCardPaymentParams{
		InvoiceID: invoiceID, TransactionID: sql.NullString{String: obj.ID, Valid: true},
	})
	if paymentErr == nil && payment.TenantID != "" {
		paymentID, tenantID, amountCents, currency := payment.PaymentID, payment.TenantID, payment.AmountCents, payment.Currency
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
	tenantID, err := purserdb.New(s.db).GetTenantByStripeSubscription(ctx, sql.NullString{String: obj.ID, Valid: true})
	if err != nil {
		// Try to find by customer ID if subscription ID not found
		tenantID, err = purserdb.New(s.db).GetTenantByStripeCustomer(ctx, sql.NullString{String: obj.CustomerID, Valid: true})
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
		if intentErr := purserdb.New(s.db).MarkStripeSubscriptionIntentSucceeded(ctx, obj.ID); intentErr != nil {
			return fmt.Errorf("failed to mark subscription intent succeeded: %w", intentErr)
		}
	} else {
		toNullTime := func(value *time.Time) sql.NullTime {
			if value == nil {
				return sql.NullTime{}
			}
			return sql.NullTime{Time: *value, Valid: true}
		}
		if _, err = purserdb.New(s.db).UpdateTenantStripeSubscriptionStatus(ctx, purserdb.UpdateTenantStripeSubscriptionStatusParams{
			StripeStatus: sql.NullString{String: obj.Status, Valid: obj.Status != ""}, Status: ourStatus,
			PeriodEnd: toNullTime(periodEnd), PeriodStart: toNullTime(periodStart), TenantID: tenantID,
		}); err != nil {
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
	if subscriptionID, err = purserdb.New(s.db).GetInternalSubscriptionID(ctx, tenantID); err != nil && !errors.Is(err, sql.ErrNoRows) {
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
	tenantID, err := purserdb.New(s.db).GetTenantByStripeCustomer(ctx, sql.NullString{String: obj.CustomerID, Valid: true})
	if err != nil {
		if obj.Metadata.TenantID != "" {
			tenantID = obj.Metadata.TenantID
		} else {
			s.logger.WithField("customer_id", obj.CustomerID).Debug("No tenant found for Stripe customer, skipping invoice.paid")
			return nil
		}
	}

	// Reset dunning attempts on successful payment
	err = purserdb.New(s.db).ResetTenantDunningAttempts(ctx, tenantID)
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
	row, err := purserdb.New(s.db).GetClusterSubscriptionByStripeID(ctx, sql.NullString{String: subscriptionID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not a cluster subscription
	}
	if err != nil {
		return fmt.Errorf("lookup cluster_subscription by stripe_subscription_id: %w", err)
	}
	clusterID, consumingTenantID = row.ClusterID, row.TenantID
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
	tenantID, err := purserdb.New(s.db).GetTenantByStripeCustomer(ctx, sql.NullString{String: obj.CustomerID, Valid: true})
	if err != nil {
		if obj.Metadata.TenantID != "" {
			tenantID = obj.Metadata.TenantID
		} else {
			s.logger.WithField("customer_id", obj.CustomerID).Debug("No tenant found for Stripe customer, skipping invoice.payment_failed")
			return nil
		}
	}

	// Increment dunning attempts
	err = purserdb.New(s.db).IncrementTenantDunningAttempts(ctx, tenantID)
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
		if _, err := s.updateInvoicePaymentStatus("stripe", txID, sess.Metadata.ReferenceID, "failed", providerSettlementEvidence{TenantID: sess.Metadata.TenantID}); err != nil {
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
	tenantID, err := purserdb.New(s.db).GetTenantByStripeCustomer(ctx, sql.NullString{String: obj.CustomerID, Valid: true})
	if err != nil {
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
	nullPeriodEnd := sql.NullTime{}
	if periodEnd != nil {
		nullPeriodEnd = sql.NullTime{Time: *periodEnd, Valid: true}
	}
	queries := purserdb.New(s.db)
	updated, err := queries.UpdateClusterStripeSubscriptionStatus(ctx, purserdb.UpdateClusterStripeSubscriptionStatusParams{
		StripeStatus: sql.NullString{String: obj.Status, Valid: obj.Status != ""}, Status: ourStatus,
		PeriodEnd: nullPeriodEnd, StripeSubscriptionID: sql.NullString{String: obj.ID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("failed to update cluster subscription status: %w", err)
	}

	if updated == 0 && obj.Metadata.TenantID != "" && obj.Metadata.ClusterID != "" {
		_, err = queries.AttachAndUpdateClusterStripeSubscription(ctx, purserdb.AttachAndUpdateClusterStripeSubscriptionParams{
			StripeSubscriptionID: sql.NullString{String: obj.ID, Valid: true},
			StripeStatus:         sql.NullString{String: obj.Status, Valid: obj.Status != ""}, Status: ourStatus,
			PeriodEnd: nullPeriodEnd, TenantID: obj.Metadata.TenantID, ClusterID: obj.Metadata.ClusterID,
		})
		if err != nil {
			return fmt.Errorf("failed to update cluster subscription by tenant/cluster: %w", err)
		}
	}

	if ourStatus == "cancelled" && s.qmClient != nil {
		row, lookupErr := queries.GetClusterSubscriptionByStripeID(ctx, sql.NullString{String: obj.ID, Valid: true})
		err = lookupErr
		tenantID, clusterID := row.TenantID, row.ClusterID
		if err == nil && tenantID != "" && clusterID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.qmClient.RevokeMaterializedClusterAccess(ctx, &quartermasterpb.RevokeMaterializedClusterAccessRequest{
				TenantId: tenantID, ClusterId: clusterID,
				AccessSource:           clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION,
				AuthorizationReference: "stripe:" + obj.ID,
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

// ProcessMollieWebhookGRPC validates and durably accepts a Mollie webhook.
// Reconciliation is performed by the provider webhook inbox worker.
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
	return s.enqueueProviderWebhook(context.Background(), "mollie", paymentID+":"+uuid.NewString(), headers, body)
}

func (s *Service) processMollieWebhookPayload(ctx context.Context, paymentID string, body []byte) (bool, string, int) {
	// Mollie does not sign its webhook bodies, so the only safe pattern is
	// to fetch the payment authoritatively from the Mollie API and
	// reconcile on (mollie_payment_id, status). The synthesized event id
	// claim/lock pattern collapses concurrent deliveries for the same
	// payment-state transition; subsequent transitions get distinct event
	// ids and are processed in order.
	eventID, err := s.handleMolliePaymentWebhook(ctx, paymentID, body)
	if errors.Is(err, errMollieUnknownPayment) {
		s.logger.WithField("payment_id", paymentID).Warn("Mollie webhook references unknown payment id")
		return true, "", 200
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

		if execErr := purserdb.New(s.db).UpsertMollieCustomer(ctx, purserdb.UpsertMollieCustomerParams{
			TenantID: tenantID, MollieCustomerID: payment.CustomerID,
		}); execErr != nil {
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
			var scanErr error
			tenantID, scanErr = purserdb.New(s.db).GetTenantByMollieSubscription(ctx, sql.NullString{String: payment.SubscriptionID, Valid: true})
			if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
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
				if insertErr := purserdb.New(s.db).InsertMollieSubscriptionPayment(ctx, purserdb.InsertMollieSubscriptionPaymentParams{
					InvoiceID: invoiceID, Amount: amountStr, Currency: payment.Amount.Currency,
					TransactionID: sql.NullString{String: payment.ID, Valid: true},
				}); insertErr != nil {
					s.logger.WithError(insertErr).WithField("mollie_payment_id", payment.ID).Warn("Failed to insert subscription-installment billing_payment")
				}
			}
		}
		if sub, subErr := s.mollieClient.GetSubscription(ctx, payment.CustomerID, payment.SubscriptionID); subErr == nil && sub.NextPaymentDate != nil {
			if persistErr := purserdb.New(s.db).UpdateMollieNextPaymentDate(ctx, purserdb.UpdateMollieNextPaymentDateParams{
				NextPaymentDate:      sub.NextPaymentDate.String(),
				MollieSubscriptionID: sql.NullString{String: payment.SubscriptionID, Valid: true},
			}); persistErr != nil {
				s.logger.WithError(persistErr).WithField("mollie_subscription_id", payment.SubscriptionID).Warn("Failed to persist next_payment_date")
			}
		}
	}

	if invoiceID == "" {
		invoiceID = referenceID
	}
	if billingPaymentID != "" {
		if _, attachErr := purserdb.New(s.db).AttachMollieBillingPayment(ctx, purserdb.AttachMollieBillingPaymentParams{
			TransactionID: sql.NullString{String: payment.ID, Valid: true}, PaymentID: billingPaymentID,
		}); attachErr != nil {
			return "", fmt.Errorf("attach Mollie payment id to billing payment: %w", attachErr)
		}
	}
	settlement := providerSettlementEvidence{TenantID: tenantID}
	if payment.Amount != nil {
		settlement.AmountCents, settlement.Currency, err = mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
		if err != nil {
			return "", err
		}
	}
	paymentUpdated, err := s.updateInvoicePaymentStatus("mollie", payment.ID, invoiceID, newStatus, settlement)
	if err != nil {
		return "", err
	}
	if !paymentUpdated {
		return eventID, nil
	}

	if newStatus == "confirmed" || newStatus == "failed" {
		if tenantID == "" && invoiceID != "" {
			var lookupErr error
			tenantID, lookupErr = purserdb.New(s.db).GetBillingInvoiceTenant(ctx, invoiceID)
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				s.logger.WithError(lookupErr).WithField("invoice_id", invoiceID).Warn("Failed to resolve tenant from invoice, billing event will be skipped")
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
		mapping, scanErr := purserdb.New(s.db).GetStripePaymentMappingByIntent(ctx, sql.NullString{String: charge.PaymentIntent, Valid: true})
		if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			s.logger.WithError(scanErr).WithField("payment_intent_id", charge.PaymentIntent).Debug("Stripe charge mapping payment lookup failed")
		}
		if scanErr == nil && mapping.TenantID != "" && mapping.PaymentID != "" {
			if mapErr := s.upsertProviderPaymentObject(ctx, providerPaymentObjectInput{
				provider:         "stripe",
				objectType:       "charge",
				providerObjectID: charge.ID,
				tenantID:         mapping.TenantID,
				localRefType:     "payment",
				localRefID:       mapping.PaymentID,
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
	providerPaymentID, scanErr := purserdb.New(s.db).GetStripePaymentIntentForCharge(ctx, dispute.Charge)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		s.logger.WithError(scanErr).WithField("charge_id", dispute.Charge).Debug("provider_payment_objects lookup failed for dispute")
	}
	if providerPaymentID == "" {
		return fmt.Errorf("dispute %s references unmapped charge %s: %w", dispute.ID, dispute.Charge, errWebhookMissingLocalReference)
	}

	switch payload.Type {
	case "charge.dispute.created":
		// Informational: persist a pending reversal row but do not move
		// money until funds_withdrawn.
		err := purserdb.New(s.db).InsertPendingStripeDispute(ctx, purserdb.InsertPendingStripeDisputeParams{
			DisputeID: dispute.ID, ChargeID: sql.NullString{String: dispute.Charge, Valid: true},
			AmountCents: dispute.Amount, Currency: strings.ToUpper(dispute.Currency),
			Reason:          sql.NullString{String: dispute.Reason, Valid: dispute.Reason != ""},
			PaymentIntentID: sql.NullString{String: providerPaymentID, Valid: true},
		})
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
			providerPaymentID:  providerPaymentID,
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
		_, err := purserdb.New(s.db).MarkStripeDisputeNeedsReview(ctx, dispute.ID)
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
	queries := purserdb.New(tx)
	payment, err := queries.GetInvoicePaymentForReversal(ctx, sql.NullString{String: in.providerPaymentID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		// Maybe it was a prepaid top-up rather than an invoice payment.
		topup, topupErr := queries.GetPendingTopupForReversal(ctx, sql.NullString{String: in.providerPaymentID, Valid: true})
		err = topupErr
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("reversal %s references unknown provider payment %s: %w",
				in.providerReversalID, in.providerPaymentID, errWebhookMissingLocalReference)
		}
		if err != nil {
			return false, fmt.Errorf("lookup topup for reversal: %w", err)
		}
		pendingTopupID = sql.NullString{String: topup.TopupID, Valid: true}
		tenantID, paymentCurrency = topup.TenantID, topup.Currency
	} else if err != nil {
		return false, fmt.Errorf("lookup payment for reversal: %w", err)
	} else {
		paymentID, invoiceID, tenantID, paymentCurrency = payment.PaymentID, payment.InvoiceID, payment.TenantID, payment.Currency
	}

	// Sanity: provider may report the reversal in a different currency
	// than the original payment. Refuse to reconcile rather than mixing.
	if paymentCurrency != "" && in.currency != "" && paymentCurrency != in.currency {
		return false, fmt.Errorf("reversal currency %s != payment currency %s", in.currency, paymentCurrency)
	}

	// Idempotent reversal-ledger insert. A pending dispute observation may
	// transition to succeeded when the money-moving provider event arrives;
	// already-succeeded rows return no id and are treated as replays.
	reversalID, err := queries.UpsertSucceededPaymentReversal(ctx, purserdb.UpsertSucceededPaymentReversalParams{
		TenantID: tenantID, PaymentID: optionalSQLString(paymentID), PendingTopupID: pendingTopupID,
		InvoiceID: optionalSQLString(invoiceID), Provider: in.provider, ReversalType: in.reversalType,
		ProviderReversalID: in.providerReversalID, ProviderChargeID: optionalSQLString(in.providerChargeID),
		AmountCents: in.amountCents, Currency: in.currency, Reason: optionalSQLString(in.reason),
	})
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
	queries := purserdb.New(tx)
	if err := queries.AddBillingPaymentReversedAmount(ctx, purserdb.AddBillingPaymentReversedAmountParams{
		AmountCents: amountCents, PaymentID: paymentID,
	}); err != nil {
		return fmt.Errorf("credit reversed_amount_cents: %w", err)
	}
	if err := queries.AddBillingInvoiceReversedAmount(ctx, purserdb.AddBillingInvoiceReversedAmountParams{
		AmountCents: amountCents, InvoiceID: invoiceID,
	}); err != nil {
		return fmt.Errorf("credit invoice reversed_paid_cents: %w", err)
	}
	// Reopen if net confirmed payments now fall below the invoice amount.
	// paid_at is preserved as the first-paid timestamp; reopened_at records
	// the most recent transition out of paid.
	_, err := queries.ReopenUnderpaidBillingInvoice(ctx, purserdb.ReopenUnderpaidBillingInvoiceParams{
		InvoiceID: invoiceID, Currency: currency,
	})
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
	queries := purserdb.New(tx)
	invoiceCents, err := queries.GetBillingInvoiceAmountCents(ctx, invoiceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read invoice amount for clawback: %w", err)
	}
	if invoiceCents <= 0 {
		return nil
	}
	accruals, err := queries.ListOperatorAccrualsForInvoice(ctx, invoiceID)
	if err != nil {
		return fmt.Errorf("list operator accruals: %w", err)
	}
	if len(accruals) == 0 {
		return nil
	}
	// Proration factor: reversedCents / invoiceCents. We compute each
	// clawback in cents by (accrual.x * reversedCents / invoiceCents)
	// using integer math so totals stay exact for typical refunds.
	var linkedClawbackID string
	for _, accrual := range accruals {
		clawGross := (accrual.GrossCents * reversedCents) / invoiceCents
		clawFee := (accrual.PlatformFeeCents * reversedCents) / invoiceCents
		clawPayable := (accrual.PayableCents * reversedCents) / invoiceCents
		if clawGross == 0 && clawFee == 0 && clawPayable == 0 {
			continue
		}
		clawbackID, err := queries.UpsertOperatorCreditClawback(ctx, purserdb.UpsertOperatorCreditClawbackParams{
			PaymentReversalID: reversalID, AccrualLedgerID: accrual.ID,
			GrossCents: clawGross, FeeCents: clawFee, PayableCents: clawPayable,
		})
		if err != nil {
			return fmt.Errorf("insert clawback for accrual %s: %w", accrual.ID, err)
		}
		if linkedClawbackID == "" {
			linkedClawbackID = clawbackID
		}
		// Mark the original accrual clawed_back if the signed clawback fully
		// covers the signed payable amount; otherwise leave at its current state.
		if absCents(clawPayable) >= absCents(accrual.PayableCents) {
			if err := queries.MarkOperatorAccrualClawedBack(ctx, accrual.ID); err != nil {
				return fmt.Errorf("mark accrual clawed_back: %w", err)
			}
		}
	}
	if linkedClawbackID != "" {
		if err := queries.LinkPaymentReversalToOperatorCredit(ctx, purserdb.LinkPaymentReversalToOperatorCreditParams{
			OperatorCreditLedgerID: linkedClawbackID, PaymentReversalID: reversalID,
		}); err != nil {
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
	queries := purserdb.New(tx)
	// Increment the refunded marker on pending_topups.
	if err := queries.AddPendingTopupRefundedAmount(ctx, purserdb.AddPendingTopupRefundedAmountParams{
		AmountCents: amountCents, TopupID: topupID,
	}); err != nil {
		return fmt.Errorf("credit pending_topups refunded_amount_cents: %w", err)
	}

	// Look at the current balance before debiting so we can flag negative.
	currentBalance, err := queries.GetX402CurrentBalance(ctx, purserdb.GetX402CurrentBalanceParams{
		TenantID: tenantID, Currency: currency,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read prepaid balance: %w", err)
	}
	willGoNegative := currentBalance < amountCents

	// Negative balance transaction. Idempotent on (tenant_id, reference_type,
	// reference_id) where reference_id is the reversal row id.
	reversalUUID, err := uuid.Parse(reversalID)
	if err != nil {
		return fmt.Errorf("parse reversal id: %w", err)
	}
	if err := queries.InsertPrepaidTopupReversalTransaction(ctx, purserdb.InsertPrepaidTopupReversalTransactionParams{
		TenantID: tenantID, AmountCents: amountCents, Currency: currency,
		Description: sql.NullString{String: fmt.Sprintf("Refund/chargeback %s", reason), Valid: true},
		ReversalID:  reversalUUID.String(), Reason: optionalSQLString(reason),
	}); err != nil {
		return fmt.Errorf("insert reversal balance_transaction: %w", err)
	}

	// Apply to the live balance.
	if _, err := queries.SubtractPrepaidBalance(ctx, purserdb.SubtractPrepaidBalanceParams{
		AmountCents: amountCents, TenantID: tenantID, Currency: currency,
	}); err != nil {
		return fmt.Errorf("debit prepaid balance: %w", err)
	}

	if willGoNegative {
		if err := queries.MarkPaymentReversalForReview(ctx, reversalUUID.String()); err != nil {
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

func optionalSQLString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
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
	validUUID := func(value string) sql.NullString {
		if _, err := uuid.Parse(value); err != nil {
			return sql.NullString{}
		}
		return sql.NullString{String: value, Valid: true}
	}
	err := purserdb.New(s.db).UpsertProviderPaymentObject(ctx, purserdb.UpsertProviderPaymentObjectParams{
		Provider: in.provider, ObjectType: in.objectType, ProviderObjectID: in.providerObjectID,
		TenantID: validUUID(in.tenantID), LocalReferenceType: optionalSQLString(in.localRefType),
		LocalReferenceID: validUUID(in.localRefID), IntentID: validUUID(in.intentID), Metadata: metadata,
	})
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
	alreadyApplied, err := purserdb.New(s.db).GetMollieAppliedReversalCents(ctx, purserdb.GetMollieAppliedReversalCentsParams{
		ReversalType: reversalType, ProviderReversalPrefix: prefix + "%",
	})
	if err != nil {
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
	paidAtValue := sql.NullTime{}
	if paidAt != nil {
		paidAtValue = sql.NullTime{Time: *paidAt, Valid: true}
	}
	return purserdb.New(s.db).UpsertMolliePaymentObservation(ctx, purserdb.UpsertMolliePaymentObservationParams{
		TenantID: tenantID, MolliePaymentID: payment.ID,
		MollieSubscriptionID: optionalSQLString(payment.SubscriptionID), MollieMandateID: optionalSQLString(payment.MandateID),
		SequenceType: optionalSQLString(string(payment.SequenceType)), Status: strings.ToLower(payment.Status),
		AmountCents: amountCents, Currency: payment.Amount.Currency, PaidAt: paidAtValue, RawPayload: rawBody,
	})
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
	queries := purserdb.New(s.db)
	invoice, err := queries.GetMollieObservationDrainInvoice(ctx, invoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup invoice for observation drain: %w", err)
	}
	if invoice.MollieSubscriptionID == "" {
		return nil
	}

	observations, err := queries.ListMolliePaymentObservationsForInvoice(ctx, purserdb.ListMolliePaymentObservationsForInvoiceParams{
		TenantID: invoice.TenantID, MollieSubscriptionID: optionalSQLString(invoice.MollieSubscriptionID),
		PeriodStart: invoice.PeriodStart, PeriodEnd: invoice.PeriodEnd,
	})
	if err != nil {
		return fmt.Errorf("list mollie observations: %w", err)
	}

	for _, observation := range observations {
		mapped, ok := mapMolliePaymentStatus(observation.Status)
		if !ok {
			continue
		}
		if observation.Currency != invoice.Currency {
			// Currency mismatch: refuse to settle against this invoice.
			// The observation stays unresolved for operator review rather
			// than being silently dropped.
			s.logger.WithFields(logging.Fields{
				"mollie_payment_id": observation.MolliePaymentID,
				"invoice_id":        invoiceID,
				"observed_currency": observation.Currency,
				"invoice_currency":  invoice.Currency,
			}).Warn("Mollie observation currency does not match invoice; leaving unresolved")
			continue
		}
		amountStr := centsToDecimalString(observation.AmountCents, observation.Currency)
		if insertErr := queries.InsertMollieSubscriptionPayment(ctx, purserdb.InsertMollieSubscriptionPaymentParams{
			InvoiceID: invoiceID, Amount: amountStr, Currency: observation.Currency,
			TransactionID: optionalSQLString(observation.MolliePaymentID),
		}); insertErr != nil {
			return fmt.Errorf("insert drained mollie payment %s: %w", observation.MolliePaymentID, insertErr)
		}
		if _, settleErr := s.updateInvoicePaymentStatus("mollie", observation.MolliePaymentID, invoiceID, mapped, providerSettlementEvidence{
			TenantID: invoice.TenantID, AmountCents: observation.AmountCents, Currency: observation.Currency,
		}); settleErr != nil {
			return fmt.Errorf("settle drained mollie payment %s: %w", observation.MolliePaymentID, settleErr)
		}
		if resErr := queries.ResolveMolliePaymentObservation(ctx, purserdb.ResolveMolliePaymentObservationParams{
			InvoiceID: invoiceID, MolliePaymentID: observation.MolliePaymentID,
		}); resErr != nil {
			return fmt.Errorf("mark mollie observation resolved %s: %w", observation.MolliePaymentID, resErr)
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
	invoiceID, err := purserdb.New(s.db).ResolveMollieSubscriptionInvoice(ctx, purserdb.ResolveMollieSubscriptionInvoiceParams{
		MollieSubscriptionID: optionalSQLString(mollieSubscriptionID), PaymentCreatedAt: *payment.CreatedAt,
	})
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
	return CurrencyMinorUnitExponent(currency)
}

// CurrencyMinorUnitExponent returns the ISO-4217 exponent used by the card
// providers supported by Purser.
func CurrencyMinorUnitExponent(currency string) int {
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

type providerSettlementEvidence struct {
	TenantID    string
	AmountCents int64
	Currency    string
}

func (s *Service) updateInvoicePaymentStatus(provider, txID, invoiceID, newStatus string, evidence providerSettlementEvidence) (bool, error) {
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

	queries := purserdb.New(tx)
	payment, err := queries.GetBillingPaymentByProviderTransaction(ctx, purserdb.GetBillingPaymentByProviderTransactionParams{
		TransactionID: optionalSQLString(txID), Method: method,
	})
	paymentID, foundInvoiceID := payment.PaymentID, payment.InvoiceID
	paymentTenantID, paymentAmount, paymentCurrency := payment.TenantID, payment.Amount, payment.Currency
	paymentStatus := payment.Status
	if errors.Is(err, sql.ErrNoRows) {
		if newStatus == "confirmed" {
			return false, nil
		}
		if invoiceID == "" {
			return false, nil
		}
		pendingPayment, pendingErr := queries.GetPendingBillingPaymentForInvoice(ctx, purserdb.GetPendingBillingPaymentForInvoiceParams{
			InvoiceID: invoiceID, Method: method,
		})
		err = pendingErr
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		paymentID, foundInvoiceID = pendingPayment.PaymentID, pendingPayment.InvoiceID
		paymentTenantID, paymentAmount, paymentCurrency = pendingPayment.TenantID, pendingPayment.Amount, pendingPayment.Currency
		paymentStatus = pendingPayment.Status
	}
	if err != nil {
		return false, fmt.Errorf("failed to lookup payment: %w", err)
	}
	if invoiceID == "" {
		invoiceID = foundInvoiceID
	} else if foundInvoiceID != "" && foundInvoiceID != invoiceID {
		return false, fmt.Errorf("provider payment %s is linked to invoice %s, not webhook invoice %s", txID, foundInvoiceID, invoiceID)
	}
	if evidence.TenantID != "" && evidence.TenantID != paymentTenantID {
		return false, fmt.Errorf("provider payment %s tenant mismatch", txID)
	}
	if newStatus == "confirmed" {
		if evidence.AmountCents <= 0 || strings.TrimSpace(evidence.Currency) == "" {
			return false, fmt.Errorf("provider payment %s is missing settlement amount or currency", txID)
		}
		if !strings.EqualFold(paymentCurrency, evidence.Currency) {
			return false, fmt.Errorf("provider payment %s currency mismatch: stored %s, received %s", txID, paymentCurrency, evidence.Currency)
		}
		expected, parseErr := decimal.NewFromString(paymentAmount)
		if parseErr != nil {
			return false, fmt.Errorf("parse stored payment amount: %w", parseErr)
		}
		received := decimal.New(evidence.AmountCents, int32(-currencyMinorUnitExponent(evidence.Currency)))
		if !expected.Equal(received) {
			return false, fmt.Errorf("provider payment %s amount mismatch: stored %s, received %s", txID, expected, received)
		}
	}
	if paymentStatus == newStatus {
		if err = tx.Commit(); err != nil {
			return false, fmt.Errorf("commit idempotent invoice payment status transaction: %w", err)
		}
		committed = true
		return true, nil
	}
	if paymentStatus != "pending" {
		return false, fmt.Errorf("provider payment %s cannot transition from %s to %s", txID, paymentStatus, newStatus)
	}

	now := time.Now()
	confirmedAt := sql.NullTime{}
	if newStatus == "confirmed" {
		confirmedAt = sql.NullTime{Time: now, Valid: true}
	}

	err = queries.UpdateBillingPaymentProviderStatus(ctx, purserdb.UpdateBillingPaymentProviderStatusParams{
		Status: newStatus, ConfirmedAt: confirmedAt,
		TransactionID: optionalSQLString(txID), PaymentID: paymentID,
	})
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
	if err = queries.UpdateBillingPaymentAttemptProviderStatus(ctx, purserdb.UpdateBillingPaymentAttemptProviderStatusParams{
		Status: attemptStatus, ProviderPaymentID: txID, PaymentID: paymentID, Provider: provider,
	}); err != nil {
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
		rowsAffected, updateErr := queries.MarkFullySettledBillingInvoicePaid(ctx, purserdb.MarkFullySettledBillingInvoicePaidParams{
			PaidAt: sql.NullTime{Time: now, Valid: true}, InvoiceID: invoiceID,
		})
		if updateErr != nil {
			s.logger.WithFields(logging.Fields{
				"error":      updateErr.Error(),
				"invoice_id": invoiceID,
			}).Error("Failed to update invoice status")
			return false, fmt.Errorf("failed to update invoice status: %w", updateErr)
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

	err = purserdb.New(s.db).UpsertMollieMandate(context.Background(), purserdb.UpsertMollieMandateParams{
		TenantID: tenantID, MollieCustomerID: info.MollieCustomerID, MollieMandateID: info.MollieMandateID,
		Status: info.Status, Method: info.Method, Details: details,
		CreatedAt: sql.NullTime{Time: info.CreatedAt, Valid: !info.CreatedAt.IsZero()},
	})
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
