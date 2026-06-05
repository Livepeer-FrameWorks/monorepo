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

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/checkout/session"
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
	IdempotencyKey   string
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
		Mode:               stripe.String(string(mode)),
		SuccessURL:         stripe.String(req.SuccessURL),
		CancelURL:          stripe.String(req.CancelURL),
		LineItems:          lineItems,
		Metadata:           metadata,
		PaymentMethodTypes: billingstripe.PaymentMethodTypesForCurrency(req.Currency),
	}
	params.Context = ctx
	if req.IdempotencyKey != "" {
		params.SetIdempotencyKey(req.IdempotencyKey)
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

	amountStr := decimal.NewFromInt(req.AmountCents).Div(decimal.NewFromInt(100)).StringFixed(2)

	webhookURL := ""
	webhookBase := config.GetGatewayPublicURL()
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
	resp, err := makeMollieAPICall(ctx, "POST", "https://api.mollie.com/v2/payments", body, mollieKey, req.IdempotencyKey)
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
func makeMollieAPICall(ctx context.Context, method, url string, body []byte, apiKey, idempotencyKey string) (*MolliePaymentResponse, error) {
	var reqBody *string
	if body != nil {
		s := string(body)
		reqBody = &s
	}

	client := &httpClient{}
	headers := map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}
	resp, err := client.doRequest(ctx, method, url, reqBody, headers)
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

func (c *httpClient) doRequest(ctx context.Context, method, url string, body *string, headers map[string]string) (string, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(*body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
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
	defer func() { _ = resp.Body.Close() }()

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
func DispatchStripeCheckoutCompleted(ctx context.Context, sessionData []byte) error {
	var sess struct {
		ID            string `json:"id"`
		CustomerID    string `json:"customer"`
		Subscription  string `json:"subscription"`
		PaymentIntent string `json:"payment_intent"`
		PaymentStatus string `json:"payment_status"`
		Mode          string `json:"mode"`
		Metadata      struct {
			Purpose     string `json:"purpose"`
			TenantID    string `json:"tenant_id"`
			ReferenceID string `json:"reference_id"`
			ClusterID   string `json:"cluster_id"`
		} `json:"metadata"`
		AmountTotal int64  `json:"amount_total"`
		Currency    string `json:"currency"`
	}
	if err := json.Unmarshal(sessionData, &sess); err != nil {
		return fmt.Errorf("failed to parse checkout session: %w", err)
	}

	purpose := CheckoutPurpose(sess.Metadata.Purpose)

	if purpose == "" {
		logger.WithField("session_id", sess.ID).Warn("Stripe checkout session missing purpose metadata, ignoring")
		return nil
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
			ctx,
			sess.ID,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
			sess.CustomerID,
			sess.Subscription,
			stripeSubscriptionProvisionable(sess.PaymentStatus),
		)
	case PurposeInvoice:
		return handleInvoiceCheckoutCompleted(
			ctx,
			sess.ID,
			sess.PaymentIntent,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
			stripeCheckoutPaid(sess.PaymentStatus),
		)
	case PurposePrepaid:
		return handlePrepaidCheckoutCompleted(
			ctx,
			sess.ID,
			sess.PaymentIntent,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
			sess.AmountTotal,
			sess.Currency,
			ProviderStripe,
			stripeCheckoutPaid(sess.PaymentStatus),
		)
	case PurposeClusterSubscription:
		clusterID := sess.Metadata.ClusterID
		if clusterID == "" {
			clusterID = sess.Metadata.ReferenceID
		}
		return handleClusterSubscriptionCheckoutCompleted(
			ctx,
			sess.ID,
			sess.Metadata.TenantID,
			clusterID,
			sess.CustomerID,
			sess.Subscription,
			stripeSubscriptionProvisionable(sess.PaymentStatus),
		)
	default:
		logger.WithField("purpose", purpose).Warn("Unknown checkout purpose, ignoring")
		return nil
	}
}

// stripeCheckoutPaid reports whether a Checkout Session has actually collected
// funds. Asynchronous methods (SEPA Direct Debit, iDEAL, Bancontact) report
// payment_status="unpaid" at checkout.session.completed and settle later via
// checkout.session.async_payment_succeeded; granting value before then would
// credit money that has not arrived.
func stripeCheckoutPaid(paymentStatus string) bool {
	return paymentStatus == "paid"
}

// stripeSubscriptionProvisionable reports whether a subscription checkout may
// activate immediately. Trials and fully-discounted subscriptions report
// "no_payment_required"; async first payments report "unpaid" and activate
// later via customer.subscription.updated / invoice.paid.
func stripeSubscriptionProvisionable(paymentStatus string) bool {
	return paymentStatus == "paid" || paymentStatus == "no_payment_required"
}

// handleSubscriptionCheckoutCompleted activates a tenant tier subscription from
// a Stripe checkout. When the first payment settles asynchronously
// (settled=false at checkout.session.completed) it stages the provider linkage
// and stays non-active; activation then arrives via customer.subscription.updated.
func handleSubscriptionCheckoutCompleted(ctx context.Context, sessionID, tenantID, tierID, customerID, subscriptionID string, settled bool) error {
	if tenantID == "" {
		logger.WithField("session_id", sessionID).Warn("No tenant_id in subscription checkout metadata")
		return nil
	}

	if !settled {
		return stageTenantSubscriptionPending(ctx, sessionID, tenantID, customerID, subscriptionID)
	}

	rows, err := activateTenantSubscriptionFromStripe(ctx, tenantID, customerID, subscriptionID, tierID, nil, nil)
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("no tenant_subscriptions row for tenant %s; cannot activate Stripe subscription %s", tenantID, subscriptionID)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.payment_provider_intents
		SET provider_subscription_id = COALESCE(provider_subscription_id, NULLIF($1, '')),
		    status = 'succeeded',
		    succeeded_at = COALESCE(succeeded_at, NOW()),
		    updated_at = NOW()
		WHERE provider = 'stripe'
		  AND provider_session_id = $2
	`, subscriptionID, sessionID); err != nil {
		return fmt.Errorf("failed to mark subscription checkout intent succeeded: %w", err)
	}

	logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"tier_id":         tierID,
		"customer_id":     customerID,
		"subscription_id": subscriptionID,
	}).Info("Activated subscription from Stripe checkout")

	return nil
}

// activateTenantSubscriptionFromStripe applies the full activation effect for a
// tenant tier subscription: sets the row active, applies the purchased tier
// (preferring the explicit tier, then the staged stripe_checkout pending tier),
// sets payment_method=stripe, and clears the staged pending fields only while
// they still describe this checkout so a newer checkout or downgrade is not
// erased by an older delivery. customer/subscription ids and period bounds are
// COALESCEd so an event that omits them cannot wipe known values. Idempotent;
// returns the number of tenant rows updated. Shared by the
// checkout.session.completed and customer.subscription.updated paths.
func activateTenantSubscriptionFromStripe(ctx context.Context, tenantID, customerID, subscriptionID, tierID string, periodStart, periodEnd *time.Time) (int64, error) {
	result, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET stripe_customer_id = COALESCE(NULLIF($1, ''), stripe_customer_id),
		    stripe_subscription_id = COALESCE(NULLIF($2, ''), stripe_subscription_id),
		    stripe_subscription_status = 'active',
		    status = 'active',
		    tier_id = COALESCE(
		        NULLIF($3, '')::uuid,
		        CASE WHEN pending_reason = 'stripe_checkout' THEN pending_tier_id END,
		        tier_id
		    ),
		    payment_method = 'stripe',
		    pending_tier_id = CASE
		        WHEN pending_reason = 'stripe_checkout'
		         AND (NULLIF($3, '') IS NULL OR pending_tier_id = NULLIF($3, '')::uuid)
		        THEN NULL
		        ELSE pending_tier_id
		    END,
		    pending_effective_at = CASE
		        WHEN pending_reason = 'stripe_checkout'
		         AND (NULLIF($3, '') IS NULL OR pending_tier_id = NULLIF($3, '')::uuid)
		        THEN NULL
		        ELSE pending_effective_at
		    END,
		    pending_reason = CASE
		        WHEN pending_reason = 'stripe_checkout'
		         AND (NULLIF($3, '') IS NULL OR pending_tier_id = NULLIF($3, '')::uuid)
		        THEN NULL
		        ELSE pending_reason
		    END,
		    pending_intent_id = CASE
		        WHEN pending_reason = 'stripe_checkout'
		         AND (NULLIF($3, '') IS NULL OR pending_tier_id = NULLIF($3, '')::uuid)
		        THEN NULL
		        ELSE pending_intent_id
		    END,
		    stripe_current_period_end = COALESCE($5, stripe_current_period_end),
		    billing_period_start = COALESCE($6, billing_period_start),
		    billing_period_end = COALESCE($5, billing_period_end),
		    next_billing_date = COALESCE($5, next_billing_date),
		    updated_at = NOW()
		WHERE tenant_id = $4
	`, customerID, subscriptionID, tierID, tenantID, periodEnd, periodStart)
	if err != nil {
		return 0, fmt.Errorf("failed to activate tenant subscription: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("tenant subscription activation rows: %w", err)
	}
	return rows, nil
}

// stageTenantSubscriptionPending records the Stripe linkage for a subscription
// whose first payment is still settling asynchronously. It deliberately does
// not set status=active or apply the tier; activation arrives later via
// customer.subscription.updated once Stripe collects the funds. It also stamps
// the subscription id onto the session-keyed provider intent so that
// later-by-subscription-id activation can close the intent. The
// stripe_subscription_status guard preserves an already-active row against a
// late unpaid re-delivery.
func stageTenantSubscriptionPending(ctx context.Context, sessionID, tenantID, customerID, subscriptionID string) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET stripe_customer_id = COALESCE(NULLIF($1, ''), stripe_customer_id),
		    stripe_subscription_id = COALESCE(NULLIF($2, ''), stripe_subscription_id),
		    stripe_subscription_status = CASE WHEN status = 'active' THEN stripe_subscription_status ELSE 'incomplete' END,
		    updated_at = NOW()
		WHERE tenant_id = $3
	`, customerID, subscriptionID, tenantID); err != nil {
		return fmt.Errorf("failed to stage pending tenant subscription: %w", err)
	}
	if err := linkStripeIntentSubscription(ctx, sessionID, subscriptionID); err != nil {
		return err
	}
	logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": subscriptionID,
	}).Info("Staged subscription pending async settlement; awaiting customer.subscription.updated")
	return nil
}

// handleClusterSubscriptionCheckoutCompleted handles paid cluster subscription
// activation. An async first payment (settled=false) stages the row without
// granting access; activation arrives via customer.subscription.updated /
// invoice.paid once Stripe collects the funds.
func handleClusterSubscriptionCheckoutCompleted(ctx context.Context, sessionID, tenantID, clusterID, customerID, subscriptionID string, settled bool) error {
	if tenantID == "" || clusterID == "" {
		logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"tenant_id":  tenantID,
			"cluster_id": clusterID,
		}).Warn("Missing tenant_id or cluster_id in cluster subscription checkout metadata")
		return nil
	}

	if !settled {
		return stageClusterSubscriptionPending(ctx, sessionID, tenantID, clusterID, customerID, subscriptionID)
	}
	return activateClusterSubscriptionFromStripe(ctx, tenantID, clusterID, customerID, subscriptionID, sessionID)
}

// activateClusterSubscriptionFromStripe is the single idempotent authority that
// marks a cluster subscription active and grants cluster access. It is called
// from checkout.session.completed (paid), customer.subscription.updated
// (active), and invoice.paid; whichever lands first activates and the rest are
// no-ops. When tenant/cluster are unknown (invoice.paid carries only the
// subscription id) it resolves them from the existing row, and returns nil when
// the subscription is not a cluster subscription.
func activateClusterSubscriptionFromStripe(ctx context.Context, tenantID, clusterID, customerID, subscriptionID, sessionID string) error {
	if tenantID == "" || clusterID == "" {
		if subscriptionID == "" {
			return nil
		}
		var existingCustomer sql.NullString
		err := db.QueryRowContext(ctx, `
			SELECT tenant_id, cluster_id, stripe_customer_id
			FROM purser.cluster_subscriptions
			WHERE stripe_subscription_id = $1
		`, subscriptionID).Scan(&tenantID, &clusterID, &existingCustomer)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not a cluster subscription
		}
		if err != nil {
			return fmt.Errorf("resolve cluster subscription %s: %w", subscriptionID, err)
		}
		if customerID == "" && existingCustomer.Valid {
			customerID = existingCustomer.String
		}
	}

	// Skip the grant when the row is already active so duplicate events do not
	// re-enqueue access work. Best-effort read; the upsert below is the
	// authority for the row state.
	var currentStatus sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT status FROM purser.cluster_subscriptions
		WHERE tenant_id = $1 AND cluster_id = $2
	`, tenantID, clusterID).Scan(&currentStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lookup cluster subscription status: %w", err)
	}
	alreadyActive := currentStatus.Valid && currentStatus.String == "active"

	// Grant access BEFORE marking the row active. A failed grant returns an
	// error and leaves the row non-active, so the webhook retry re-attempts the
	// grant — there is no active-without-access stranding, and no crash window
	// can leave the row active but ungranted. Quartermaster's grant is
	// idempotent, so a rare concurrent double-grant is harmless.
	if !alreadyActive && qmClient != nil {
		grantCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := qmClient.GrantClusterAccess(grantCtx, &pb.GrantClusterAccessRequest{
			TenantId:    tenantID,
			ClusterId:   clusterID,
			AccessLevel: "shared",
		}); err != nil {
			return fmt.Errorf("failed to grant cluster access: %w", err)
		}
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.cluster_subscriptions (
			tenant_id, cluster_id, status, stripe_customer_id, stripe_subscription_id,
			stripe_subscription_status, checkout_session_id, intent_id, created_at, updated_at
		) VALUES (
			$1, $2, 'active', NULLIF($3, ''), NULLIF($4, ''), 'active', NULLIF($5, ''),
			(SELECT id FROM purser.payment_provider_intents
			 WHERE provider = 'stripe' AND provider_session_id = $5
			 LIMIT 1),
			NOW(), NOW()
		)
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			status = 'active',
			stripe_customer_id = COALESCE(EXCLUDED.stripe_customer_id, purser.cluster_subscriptions.stripe_customer_id),
			stripe_subscription_id = COALESCE(EXCLUDED.stripe_subscription_id, purser.cluster_subscriptions.stripe_subscription_id),
			stripe_subscription_status = 'active',
			checkout_session_id = COALESCE(EXCLUDED.checkout_session_id, purser.cluster_subscriptions.checkout_session_id),
			intent_id = COALESCE(purser.cluster_subscriptions.intent_id, EXCLUDED.intent_id),
			updated_at = NOW()
	`, tenantID, clusterID, customerID, subscriptionID, sessionID); err != nil {
		return fmt.Errorf("failed to activate cluster subscription: %w", err)
	}

	if _, intentErr := db.ExecContext(ctx, `
		UPDATE purser.payment_provider_intents
		SET provider_subscription_id = COALESCE(provider_subscription_id, NULLIF($1, '')),
		    status = 'succeeded',
		    succeeded_at = COALESCE(succeeded_at, NOW()),
		    updated_at = NOW()
		WHERE provider = 'stripe'
		  AND (provider_session_id = NULLIF($2, '') OR provider_subscription_id = NULLIF($1, ''))
	`, subscriptionID, sessionID); intentErr != nil {
		return fmt.Errorf("failed to mark cluster checkout intent succeeded: %w", intentErr)
	}

	if !alreadyActive {
		logger.WithFields(logging.Fields{
			"tenant_id":       tenantID,
			"cluster_id":      clusterID,
			"customer_id":     customerID,
			"subscription_id": subscriptionID,
		}).Info("Activated cluster subscription from Stripe")
	}

	return nil
}

// stageClusterSubscriptionPending records the cluster_subscriptions row for a
// paid-cluster checkout whose first payment is still settling asynchronously.
// It does not set status=active or grant access; activation arrives via
// customer.subscription.updated / invoice.paid. The stripe_subscription_status
// guard preserves an already-active row against a late unpaid re-delivery.
func stageClusterSubscriptionPending(ctx context.Context, sessionID, tenantID, clusterID, customerID, subscriptionID string) error {
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.cluster_subscriptions (
			tenant_id, cluster_id, status, stripe_customer_id, stripe_subscription_id,
			stripe_subscription_status, checkout_session_id, intent_id, created_at, updated_at
		) VALUES (
			$1, $2, 'pending_payment', NULLIF($3, ''), NULLIF($4, ''), 'incomplete', NULLIF($5, ''),
			(SELECT id FROM purser.payment_provider_intents
			 WHERE provider = 'stripe' AND provider_session_id = $5
			 LIMIT 1),
			NOW(), NOW()
		)
		ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
			stripe_customer_id = COALESCE(EXCLUDED.stripe_customer_id, purser.cluster_subscriptions.stripe_customer_id),
			stripe_subscription_id = COALESCE(EXCLUDED.stripe_subscription_id, purser.cluster_subscriptions.stripe_subscription_id),
			stripe_subscription_status = CASE
				WHEN purser.cluster_subscriptions.status = 'active'
				THEN purser.cluster_subscriptions.stripe_subscription_status
				ELSE 'incomplete'
			END,
			checkout_session_id = COALESCE(EXCLUDED.checkout_session_id, purser.cluster_subscriptions.checkout_session_id),
			intent_id = COALESCE(purser.cluster_subscriptions.intent_id, EXCLUDED.intent_id),
			updated_at = NOW()
	`, tenantID, clusterID, customerID, subscriptionID, sessionID); err != nil {
		return fmt.Errorf("failed to stage pending cluster subscription: %w", err)
	}
	if err := linkStripeIntentSubscription(ctx, sessionID, subscriptionID); err != nil {
		return err
	}
	logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"cluster_id":      clusterID,
		"subscription_id": subscriptionID,
	}).Info("Staged cluster subscription pending async settlement")
	return nil
}

// clearStagedStripeCheckout undoes the staged checkout state for a tenant
// subscription whose Stripe checkout failed or expired before activating: it
// expires the still-open provider intent and clears the staged pending tier
// fields, guarded by pending_reason so an active subscription's tier is never
// touched. Idempotent.
func clearStagedStripeCheckout(ctx context.Context, tenantID, subscriptionID string) error {
	if subscriptionID != "" {
		if _, err := db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET status = 'expired', updated_at = NOW()
			WHERE provider = 'stripe'
			  AND provider_subscription_id = $1
			  AND status NOT IN ('succeeded', 'cancelled', 'expired', 'terminal_failed')
		`, subscriptionID); err != nil {
			return fmt.Errorf("expire staged stripe intent for %s: %w", subscriptionID, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET pending_tier_id = NULL,
		    pending_effective_at = NULL,
		    pending_reason = NULL,
		    pending_intent_id = NULL,
		    updated_at = NOW()
		WHERE tenant_id = $1 AND pending_reason = 'stripe_checkout'
	`, tenantID); err != nil {
		return fmt.Errorf("clear staged stripe checkout for tenant %s: %w", tenantID, err)
	}
	return nil
}

// markPendingTopupTerminal moves a still-pending top-up to a terminal status
// (failed or expired). Guarded on status='pending' so a completed top-up that
// already credited the balance is never reverted. Idempotent.
func markPendingTopupTerminal(ctx context.Context, topupID, status string) error {
	if topupID == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.pending_topups
		SET status = $1, updated_at = NOW()
		WHERE id = $2 AND status = 'pending'
	`, status, topupID); err != nil {
		return fmt.Errorf("mark pending top-up %s as %s: %w", topupID, status, err)
	}
	return nil
}

// linkStripeIntentSubscription stamps the Stripe subscription id onto the
// session-keyed provider intent. Async checkouts stage before the subscription
// is active, and activation later closes the intent by provider_subscription_id;
// without this link that update matches zero rows and the intent stays open.
func linkStripeIntentSubscription(ctx context.Context, sessionID, subscriptionID string) error {
	if sessionID == "" || subscriptionID == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.payment_provider_intents
		SET provider_subscription_id = COALESCE(provider_subscription_id, $1),
		    updated_at = NOW()
		WHERE provider = 'stripe' AND provider_session_id = $2
	`, subscriptionID, sessionID); err != nil {
		return fmt.Errorf("link stripe intent %s to subscription %s: %w", sessionID, subscriptionID, err)
	}
	return nil
}

// expireStripeCheckoutIntent marks a still-open provider intent expired when its
// Checkout Session expires. Guarded so terminal intents are left untouched.
func expireStripeCheckoutIntent(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.payment_provider_intents
		SET status = 'expired', updated_at = NOW()
		WHERE provider = 'stripe'
		  AND provider_session_id = $1
		  AND status NOT IN ('succeeded', 'cancelled', 'expired', 'terminal_failed')
	`, sessionID); err != nil {
		return fmt.Errorf("expire stripe checkout intent %s: %w", sessionID, err)
	}
	return nil
}

// clearStagedClusterSubscription cancels a cluster_subscriptions row that was
// staged pending_payment for a Stripe checkout that expired or failed before
// activating. Guarded on status='pending_payment' so an active subscription is
// never touched. Matched by the checkout session id (always set at staging) or
// the subscription id when one was created. Idempotent.
func clearStagedClusterSubscription(ctx context.Context, sessionID, subscriptionID string) error {
	if sessionID == "" && subscriptionID == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.cluster_subscriptions
		SET status = 'cancelled', stripe_subscription_status = 'incomplete_expired', updated_at = NOW()
		WHERE status = 'pending_payment'
		  AND (checkout_session_id = NULLIF($1, '') OR stripe_subscription_id = NULLIF($2, ''))
	`, sessionID, subscriptionID); err != nil {
		return fmt.Errorf("clear staged cluster subscription: %w", err)
	}
	return nil
}

// handleInvoiceCheckoutCompleted handles invoice payment completion. The
// payment_intent is always attached to the pending billing_payment so a later
// async settlement can match it, but the invoice is confirmed only once funds
// have actually settled (settled=true); async methods confirm via
// checkout.session.async_payment_succeeded.
func handleInvoiceCheckoutCompleted(ctx context.Context, sessionID, paymentIntentID, tenantID, invoiceID string, settled bool) error {
	if invoiceID == "" {
		logger.WithField("session_id", sessionID).Debug("No invoice_id in checkout metadata, skipping")
		return nil
	}
	txID := sessionID
	if paymentIntentID != "" {
		txID = paymentIntentID
		if _, err := db.ExecContext(ctx, `
			UPDATE purser.billing_payments
			SET tx_id = $1, updated_at = NOW()
			WHERE invoice_id = $2
			  AND method = 'card'
			  AND status = 'pending'
			  AND tx_id = $3
		`, paymentIntentID, invoiceID, sessionID); err != nil {
			return fmt.Errorf("attach stripe payment_intent to invoice payment: %w", err)
		}
	}
	if !settled {
		logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
		}).Info("Invoice checkout pending async settlement; awaiting async_payment_succeeded")
		return nil
	}
	updated, err := updateInvoicePaymentStatus("stripe", txID, invoiceID, "confirmed")
	if err != nil {
		return err
	}
	if !updated {
		logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
		}).Warn("Stripe checkout did not match a pending invoice payment")
		return nil
	}

	logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"invoice_id": invoiceID,
	}).Info("Marked invoice as paid from Stripe checkout")

	return nil
}

// handlePrepaidCheckoutCompleted handles prepaid balance top-up completion. The
// provider payment id is attached to the pending_topup regardless, but the
// balance is credited only once funds have settled (settled=true); async
// methods credit via checkout.session.async_payment_succeeded.
func handlePrepaidCheckoutCompleted(ctx context.Context, sessionID, providerPaymentID, tenantID, topupID string, amountCents int64, currency string, provider CheckoutProvider, settled bool) error {
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// 1. Lock the pending_topup row so concurrent webhook deliveries serialize
	//    on the idempotency check below.
	var currentStatus string
	var storedTenantID string
	err = tx.QueryRowContext(ctx, `
		SELECT status, tenant_id FROM purser.pending_topups WHERE id = $1 FOR UPDATE
	`, topupID).Scan(&currentStatus, &storedTenantID)
	if err != nil {
		return fmt.Errorf("failed to find pending topup: %w", err)
	}
	if storedTenantID != tenantID {
		logger.WithFields(logging.Fields{
			"topup_id":         topupID,
			"tenant_id":        tenantID,
			"stored_tenant_id": storedTenantID,
		}).Warn("Pending top-up tenant mismatch")
		return fmt.Errorf("pending top-up tenant mismatch")
	}

	if currentStatus != "pending" {
		logger.WithFields(logging.Fields{
			"topup_id": topupID,
			"status":   currentStatus,
		}).Info("Top-up already processed, skipping")
		return nil
	}

	if _, attachErr := tx.ExecContext(ctx, `
		UPDATE purser.pending_topups
		SET provider_payment_id = COALESCE(provider_payment_id, NULLIF($1, '')),
		    checkout_id = COALESCE(checkout_id, NULLIF($2, '')),
		    updated_at = NOW()
		WHERE id = $3
	`, providerPaymentID, sessionID, topupID); attachErr != nil {
		return fmt.Errorf("failed to attach provider payment to topup: %w", attachErr)
	}

	// Async methods complete the Checkout Session before funds settle; persist
	// the linkage but do not credit until async_payment_succeeded arrives.
	if !settled {
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("commit pending top-up linkage: %w", commitErr)
		}
		logger.WithFields(logging.Fields{
			"topup_id":  topupID,
			"tenant_id": tenantID,
		}).Info("Prepaid top-up pending async settlement; awaiting async_payment_succeeded")
		return nil
	}

	// 2. Credit prepaid balance
	var balanceID string
	var currentBalance int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&balanceID, &currentBalance)

	if errors.Is(err, sql.ErrNoRows) {
		// Create balance if doesn't exist
		err = tx.QueryRowContext(ctx, `
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
		_, err = tx.ExecContext(ctx, `
			UPDATE purser.prepaid_balances
			SET balance_cents = $1, updated_at = NOW()
			WHERE id = $2
		`, newBalance, balanceID)
		if err != nil {
			return fmt.Errorf("failed to update prepaid balance: %w", err)
		}
		currentBalance = newBalance
	}

	// 3. Create balance transaction. reference_type='topup' activates the
	//    partial unique index at purser.sql:idx_balance_transactions_idempotency
	//    so replayed webhooks cannot double-credit.
	var txID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO purser.balance_transactions (
			tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type,
			actor_kind, reason, evidence_ref
		) VALUES ($1, $2, $3, 'topup', $4, $5, 'topup', 'webhook', $6, $7)
		RETURNING id
	`, tenantID, amountCents, currentBalance,
		fmt.Sprintf("Card top-up via %s", provider), topupID,
		fmt.Sprintf("%s checkout completed", provider), sessionID).Scan(&txID)
	if err != nil {
		return fmt.Errorf("failed to create balance transaction: %w", err)
	}

	// 4. Update pending_topup to completed
	_, err = tx.ExecContext(ctx, `
		UPDATE purser.pending_topups
		SET status = 'completed', completed_at = $1,
		    balance_transaction_id = $2, updated_at = NOW()
		WHERE id = $3
	`, now, txID, topupID)
	if err != nil {
		return fmt.Errorf("failed to update pending topup: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE purser.payment_provider_intents ppi
		SET provider_payment_id = COALESCE(ppi.provider_payment_id, NULLIF($1, '')),
		    provider_session_id = COALESCE(ppi.provider_session_id, NULLIF($2, '')),
		    status = 'succeeded',
		    succeeded_at = COALESCE(ppi.succeeded_at, $3),
		    updated_at = NOW()
		FROM purser.pending_topups pt
		WHERE pt.intent_id = ppi.id
		  AND pt.id = $4
	`, providerPaymentID, sessionID, now, topupID); err != nil {
		return fmt.Errorf("failed to update topup provider intent: %w", err)
	}

	// 5. If tenant was suspended due to balance, unsuspend
	_, err = tx.ExecContext(ctx, `
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
