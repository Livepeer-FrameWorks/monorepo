package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
)

// CheckoutPurpose identifies the reason for creating a checkout session.
// Used in webhook handling to dispatch to the correct handler.
type CheckoutPurpose string

const (
	// PurposeSubscription is for tier subscription payments
	PurposeSubscription CheckoutPurpose = "subscription"
	// PurposeClusterSubscription is for paid cluster subscriptions
	PurposeClusterSubscription CheckoutPurpose = "cluster_subscription"
	// PurposeInvoice is for paying an existing invoice
	PurposeInvoice CheckoutPurpose = "invoice"
	// PurposePrepaid is for prepaid balance top-ups
	PurposePrepaid CheckoutPurpose = "prepaid"
)

// CheckoutProvider identifies the payment provider
type CheckoutProvider string

const (
	ProviderStripe CheckoutProvider = "stripe"
	ProviderMollie CheckoutProvider = "mollie"
)

// CheckoutRequest contains all parameters needed to create a checkout session
type CheckoutRequest struct {
	Purpose     CheckoutPurpose
	Provider    CheckoutProvider
	TenantID    string
	ReferenceID string // tier_id, invoice_id, or topup_id depending on purpose
	AmountCents int64
	Currency    string
	SuccessURL  string
	CancelURL   string
	Description string // Line item description

	// Optional billing details (for prepaid top-ups)
	BillingEmail     string
	BillingName      string
	BillingCompany   string
	BillingVATNumber string
}

// CheckoutResult contains the response from creating a checkout session
type CheckoutResult struct {
	CheckoutURL string
	SessionID   string    // Provider's session/payment ID
	ExpiresAt   time.Time // When the checkout session expires
}

// CheckoutService provides unified checkout creation across providers
type CheckoutService struct {
	db     *sql.DB
	logger logging.Logger
}

// NewCheckoutService creates a new checkout service
func NewCheckoutService(database *sql.DB, log logging.Logger) *CheckoutService {
	return &CheckoutService{
		db:     database,
		logger: log,
	}
}

// CreateCheckout creates a checkout session with the appropriate provider
func (s *CheckoutService) CreateCheckout(ctx context.Context, req CheckoutRequest) (*CheckoutResult, error) {
	switch req.Provider {
	case ProviderStripe:
		return s.createStripeCheckout(ctx, req)
	case ProviderMollie:
		return s.createMollieCheckout(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported payment provider: %s", req.Provider)
	}
}

// createStripeCheckout creates a Stripe Checkout Session
func (s *CheckoutService) createStripeCheckout(ctx context.Context, req CheckoutRequest) (*CheckoutResult, error) {
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")
	if stripe.Key == "" {
		return nil, fmt.Errorf("STRIPE_SECRET_KEY not configured")
	}

	// Determine checkout mode based on purpose
	mode := stripe.CheckoutSessionModePayment
	if req.Purpose == PurposeSubscription {
		mode = stripe.CheckoutSessionModeSubscription
	}

	// Build metadata - this is critical for webhook dispatch
	metadata := map[string]string{
		"purpose":      string(req.Purpose),
		"tenant_id":    req.TenantID,
		"reference_id": req.ReferenceID,
	}

	// Build line items
	var lineItems []*stripe.CheckoutSessionLineItemParams
	if req.Purpose == PurposeSubscription {
		// For subscriptions, we need a price ID from the tier
		// The reference_id should be the Stripe price ID
		lineItems = []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(req.ReferenceID),
				Quantity: stripe.Int64(1),
			},
		}
	} else {
		// For invoice/prepaid, use inline price data
		lineItems = []*stripe.CheckoutSessionLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency: stripe.String(req.Currency),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name:        stripe.String(req.Description),
						Description: stripe.String(fmt.Sprintf("Tenant: %s", req.TenantID)),
					},
					UnitAmount: stripe.Int64(req.AmountCents),
				},
				Quantity: stripe.Int64(1),
			},
		}
	}

	// Create checkout session
	params := &stripe.CheckoutSessionParams{
		Mode:       stripe.String(string(mode)),
		SuccessURL: stripe.String(req.SuccessURL),
		CancelURL:  stripe.String(req.CancelURL),
		LineItems:  lineItems,
		Metadata:   metadata,
	}

	// Pre-fill customer email if provided
	if req.BillingEmail != "" {
		params.CustomerEmail = stripe.String(req.BillingEmail)
	}

	sess, err := session.New(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create Stripe checkout session: %w", err)
	}

	// Stripe sessions expire after 24 hours by default
	expiresAt := time.Now().Add(24 * time.Hour)
	if sess.ExpiresAt > 0 {
		expiresAt = time.Unix(sess.ExpiresAt, 0)
	}

	s.logger.WithFields(logging.Fields{
		"purpose":      req.Purpose,
		"tenant_id":    req.TenantID,
		"reference_id": req.ReferenceID,
		"session_id":   sess.ID,
		"checkout_url": sess.URL,
	}).Info("Created Stripe checkout session")

	return &CheckoutResult{
		CheckoutURL: sess.URL,
		SessionID:   sess.ID,
		ExpiresAt:   expiresAt,
	}, nil
}

// createMollieCheckout creates a Mollie payment
func (s *CheckoutService) createMollieCheckout(ctx context.Context, req CheckoutRequest) (*CheckoutResult, error) {
	mollieKey := os.Getenv("MOLLIE_API_KEY")
	if mollieKey == "" {
		return nil, fmt.Errorf("MOLLIE_API_KEY not configured")
	}

	// Build metadata for webhook dispatch
	metadata := map[string]string{
		"purpose":      string(req.Purpose),
		"tenant_id":    req.TenantID,
		"reference_id": req.ReferenceID,
	}

	// Convert amount from cents to decimal string (Mollie requires "10.00" format)
	amountStr := fmt.Sprintf("%.2f", float64(req.AmountCents)/100)

	webhookURL := ""
	webhookBase := strings.TrimSpace(os.Getenv("API_PUBLIC_URL"))
	if webhookBase == "" {
		webhookBase = strings.TrimSpace(os.Getenv("GATEWAY_PUBLIC_URL"))
	}
	if webhookBase != "" {
		webhookURL = webhookBase + "/webhooks/billing/mollie"
	}

	// Build Mollie payment request
	mollieReq := map[string]interface{}{
		"amount": map[string]string{
			"currency": req.Currency,
			"value":    amountStr,
		},
		"description": req.Description,
		"redirectUrl": req.SuccessURL,
		"cancelUrl":   req.CancelURL,
		"webhookUrl":  webhookURL,
		"metadata":    metadata,
	}

	// Make Mollie API call
	body, err := json.Marshal(mollieReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Mollie request: %w", err)
	}

	// Use the existing Mollie HTTP client pattern
	resp, err := makeMollieAPICall("POST", "https://api.mollie.com/v2/payments", body, mollieKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie payment: %w", err)
	}

	s.logger.WithFields(logging.Fields{
		"purpose":      req.Purpose,
		"tenant_id":    req.TenantID,
		"reference_id": req.ReferenceID,
		"payment_id":   resp.ID,
		"checkout_url": resp.CheckoutURL,
	}).Info("Created Mollie payment")

	// Mollie payments expire based on method, default to 12 hours
	expiresAt := time.Now().Add(12 * time.Hour)
	if !resp.ExpiresAt.IsZero() {
		expiresAt = resp.ExpiresAt
	}

	return &CheckoutResult{
		CheckoutURL: resp.CheckoutURL,
		SessionID:   resp.ID,
		ExpiresAt:   expiresAt,
	}, nil
}

// MolliePaymentResponse contains the response from creating a Mollie payment
type MolliePaymentResponse struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	CheckoutURL string    `json:"_links,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
}

// makeMollieAPICall makes an authenticated request to Mollie API
func makeMollieAPICall(method, url string, body []byte, apiKey string) (*MolliePaymentResponse, error) {
	var reqBody *string
	if body != nil {
		s := string(body)
		reqBody = &s
	}

	client := &httpClient{}
	resp, err := client.doRequest(method, url, reqBody, map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ExpiresAt string `json:"expiresAt"`
		Links     struct {
			Checkout struct {
				Href string `json:"href"`
			} `json:"checkout"`
		} `json:"_links"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse Mollie response: %w", err)
	}

	var expiresAt time.Time
	if result.ExpiresAt != "" {
		expiresAt, _ = time.Parse(time.RFC3339, result.ExpiresAt)
	}

	return &MolliePaymentResponse{
		ID:          result.ID,
		Status:      result.Status,
		CheckoutURL: result.Links.Checkout.Href,
		ExpiresAt:   expiresAt,
	}, nil
}

// httpClient is a simple HTTP client wrapper for Mollie
type httpClient struct{}

func (c *httpClient) doRequest(method, url string, body *string, headers map[string]string) (string, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(*body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return "", err
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("mollie API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return string(respBody), nil
}

// ============================================================================
// WEBHOOK DISPATCH BY PURPOSE
// These functions are called from the main webhook handlers to route
// checkout.session.completed events to the appropriate handler.
// ============================================================================

// DispatchStripeCheckoutCompleted routes a completed checkout session to the
// appropriate handler based on the purpose in metadata.
func DispatchStripeCheckoutCompleted(sessionData []byte) error {
	var sess struct {
		ID           string `json:"id"`
		CustomerID   string `json:"customer"`
		Subscription string `json:"subscription"`
		Mode         string `json:"mode"`
		Metadata     struct {
			Purpose     string `json:"purpose"`
			TenantID    string `json:"tenant_id"`
			ReferenceID string `json:"reference_id"`
			ClusterID   string `json:"cluster_id"`
			// Legacy fields for backwards compatibility
			TierID string `json:"tier_id"`
		} `json:"metadata"`
		AmountTotal int64  `json:"amount_total"`
		Currency    string `json:"currency"`
	}
	if err := json.Unmarshal(sessionData, &sess); err != nil {
		return fmt.Errorf("failed to parse checkout session: %w", err)
	}

	purpose := CheckoutPurpose(sess.Metadata.Purpose)

	// Backwards compatibility: if no purpose, infer from mode
	if purpose == "" {
		if sess.Mode == "subscription" {
			purpose = PurposeSubscription
		} else {
			// Legacy one-time payments were for invoices
			purpose = PurposeInvoice
		}
	}

	logger.WithFields(logging.Fields{
		"session_id":   sess.ID,
		"purpose":      purpose,
		"tenant_id":    sess.Metadata.TenantID,
		"reference_id": sess.Metadata.ReferenceID,
	}).Info("Dispatching Stripe checkout.session.completed")

	switch purpose {
	case PurposeSubscription:
		return handleSubscriptionCheckoutCompleted(
			sess.ID,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
			sess.CustomerID,
			sess.Subscription,
		)
	case PurposeInvoice:
		return handleInvoiceCheckoutCompleted(
			sess.ID,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
		)
	case PurposePrepaid:
		return handlePrepaidCheckoutCompleted(
			sess.ID,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
			sess.AmountTotal,
			sess.Currency,
			ProviderStripe,
		)
	case PurposeClusterSubscription:
		clusterID := sess.Metadata.ClusterID
		if clusterID == "" {
			clusterID = sess.Metadata.ReferenceID
		}
		if clusterID == "" {
			clusterID = sess.Metadata.TierID
		}
		return handleClusterSubscriptionCheckoutCompleted(
			sess.ID,
			sess.Metadata.TenantID,
			clusterID,
			sess.CustomerID,
			sess.Subscription,
		)
	default:
		logger.WithField("purpose", purpose).Warn("Unknown checkout purpose, ignoring")
		return nil
	}
}

// handleSubscriptionCheckoutCompleted handles tier subscription activation
func handleSubscriptionCheckoutCompleted(sessionID, tenantID, tierID, customerID, subscriptionID string) error {
	if tenantID == "" {
		logger.WithField("session_id", sessionID).Warn("No tenant_id in subscription checkout metadata")
		return nil
	}

	// Update tenant subscription with Stripe IDs
	_, err := db.Exec(`
		UPDATE purser.tenant_subscriptions
		SET stripe_customer_id = $1,
		    stripe_subscription_id = $2,
		    stripe_subscription_status = 'active',
		    status = 'active',
		    updated_at = NOW()
		WHERE tenant_id = $3
	`, customerID, subscriptionID, tenantID)
	if err != nil {
		return fmt.Errorf("failed to update tenant subscription: %w", err)
	}

	logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"tier_id":         tierID,
		"customer_id":     customerID,
		"subscription_id": subscriptionID,
	}).Info("Activated subscription from Stripe checkout")

	return nil
}

// handleClusterSubscriptionCheckoutCompleted handles paid cluster subscription activation
func handleClusterSubscriptionCheckoutCompleted(sessionID, tenantID, clusterID, customerID, subscriptionID string) error {
	if tenantID == "" || clusterID == "" {
		logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"tenant_id":  tenantID,
			"cluster_id": clusterID,
		}).Warn("Missing tenant_id or cluster_id in cluster subscription checkout metadata")
		return nil
	}

	_, err := db.Exec(`
		INSERT INTO purser.cluster_subscriptions (
			tenant_id, cluster_id, status, stripe_customer_id, stripe_subscription_id,
			stripe_subscription_status, checkout_session_id, created_at, updated_at
		) VALUES ($1, $2, 'active', $3, $4, 'active', $5, NOW(), NOW())
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			status = 'active',
			stripe_customer_id = EXCLUDED.stripe_customer_id,
			stripe_subscription_id = EXCLUDED.stripe_subscription_id,
			stripe_subscription_status = 'active',
			checkout_session_id = EXCLUDED.checkout_session_id,
			updated_at = NOW()
	`, tenantID, clusterID, customerID, subscriptionID, sessionID)
	if err != nil {
		return fmt.Errorf("failed to update cluster subscription: %w", err)
	}

	if qmClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = qmClient.GrantClusterAccess(ctx, &pb.GrantClusterAccessRequest{
			TenantId:    tenantID,
			ClusterId:   clusterID,
			AccessLevel: "shared",
		})
		if err != nil {
			return fmt.Errorf("failed to grant cluster access: %w", err)
		}
	}

	logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"cluster_id":      clusterID,
		"customer_id":     customerID,
		"subscription_id": subscriptionID,
	}).Info("Activated paid cluster subscription from Stripe checkout")

	return nil
}

// handleInvoiceCheckoutCompleted handles invoice payment completion
func handleInvoiceCheckoutCompleted(sessionID, tenantID, invoiceID string) error {
	if invoiceID == "" {
		logger.WithField("session_id", sessionID).Debug("No invoice_id in checkout metadata, skipping")
		return nil
	}

	now := time.Now()
	_, err := db.Exec(`
		UPDATE purser.billing_invoices
		SET status = 'paid', paid_at = $1, updated_at = NOW()
		WHERE id = $2 AND status IN ('pending', 'overdue')
	`, now, invoiceID)
	if err != nil {
		return fmt.Errorf("failed to update invoice status: %w", err)
	}

	logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"invoice_id": invoiceID,
	}).Info("Marked invoice as paid from Stripe checkout")

	// Send payment success email
	go sendPaymentStatusEmail(invoiceID, "stripe", "confirmed")

	return nil
}

// handlePrepaidCheckoutCompleted handles prepaid balance top-up completion
func handlePrepaidCheckoutCompleted(sessionID, tenantID, topupID string, amountCents int64, currency string, provider CheckoutProvider) error {
	if topupID == "" || tenantID == "" {
		logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"topup_id":   topupID,
			"tenant_id":  tenantID,
		}).Warn("Missing topup_id or tenant_id in prepaid checkout metadata")
		return nil
	}

	now := time.Now()

	// Start transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// 1. Update pending_topup status
	var currentStatus string
	err = tx.QueryRow(`
		SELECT status FROM purser.pending_topups WHERE id = $1
	`, topupID).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("failed to find pending topup: %w", err)
	}

	if currentStatus != "pending" {
		logger.WithFields(logging.Fields{
			"topup_id": topupID,
			"status":   currentStatus,
		}).Info("Top-up already processed, skipping")
		return nil
	}

	// 2. Credit prepaid balance
	var balanceID string
	var currentBalance int64
	err = tx.QueryRow(`
		SELECT id, balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&balanceID, &currentBalance)

	if errors.Is(err, sql.ErrNoRows) {
		// Create balance if doesn't exist
		err = tx.QueryRow(`
			INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
			VALUES ($1, $2, $3)
			RETURNING id, balance_cents
		`, tenantID, amountCents, currency).Scan(&balanceID, &currentBalance)
		if err != nil {
			return fmt.Errorf("failed to create prepaid balance: %w", err)
		}
		currentBalance = amountCents
	} else if err != nil {
		return fmt.Errorf("failed to get prepaid balance: %w", err)
	} else {
		// Update existing balance
		newBalance := currentBalance + amountCents
		_, err = tx.Exec(`
			UPDATE purser.prepaid_balances
			SET balance_cents = $1, updated_at = NOW()
			WHERE id = $2
		`, newBalance, balanceID)
		if err != nil {
			return fmt.Errorf("failed to update prepaid balance: %w", err)
		}
		currentBalance = newBalance
	}

	// 3. Create balance transaction
	var txID string
	err = tx.QueryRow(`
		INSERT INTO purser.balance_transactions (
			tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id
		) VALUES ($1, $2, $3, 'topup', $4, $5)
		RETURNING id
	`, tenantID, amountCents, currentBalance,
		fmt.Sprintf("Card top-up via %s", provider), topupID).Scan(&txID)
	if err != nil {
		return fmt.Errorf("failed to create balance transaction: %w", err)
	}

	// 4. Update pending_topup to completed
	_, err = tx.Exec(`
		UPDATE purser.pending_topups
		SET status = 'completed', completed_at = $1,
		    balance_transaction_id = $2, updated_at = NOW()
		WHERE id = $3
	`, now, txID, topupID)
	if err != nil {
		return fmt.Errorf("failed to update pending topup: %w", err)
	}

	// 5. If tenant was suspended due to balance, unsuspend
	_, err = tx.Exec(`
		UPDATE purser.tenant_subscriptions
		SET status = 'active', updated_at = NOW()
		WHERE tenant_id = $1 AND status = 'suspended'
	`, tenantID)
	if err != nil {
		logger.WithError(err).Warn("Failed to unsuspend tenant (may not have been suspended)")
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"topup_id":       topupID,
		"amount_cents":   amountCents,
		"new_balance":    currentBalance,
		"provider":       provider,
		"transaction_id": txID,
	}).Info("Credited prepaid balance from card top-up")

	emitBillingEvent(eventTopupCredited, tenantID, "topup", topupID, &pb.BillingEvent{
		TopupId:  topupID,
		Amount:   float64(amountCents) / 100.0,
		Currency: currency,
		Provider: string(provider),
		Status:   "credited",
	})

	return nil
}
