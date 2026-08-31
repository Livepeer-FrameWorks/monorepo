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
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/google/uuid"
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

	amountStr := minorUnitsDecimalString(req.AmountCents, req.Currency)

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

func minorUnitsDecimalString(amount int64, currency string) string {
	exponent := CurrencyMinorUnitExponent(currency)
	return decimal.NewFromInt(amount).Shift(int32(-exponent)).StringFixed(int32(exponent))
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
func (s *Service) DispatchStripeCheckoutCompleted(ctx context.Context, sessionData []byte) error {
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
		s.logger.WithField("session_id", sess.ID).Warn("Stripe checkout session missing purpose metadata, ignoring")
		return nil
	}

	s.logger.WithFields(logging.Fields{
		"session_id":   sess.ID,
		"purpose":      purpose,
		"tenant_id":    sess.Metadata.TenantID,
		"reference_id": sess.Metadata.ReferenceID,
	}).Info("Dispatching Stripe checkout.session.completed")

	switch purpose {
	case PurposeSubscription:
		return s.handleSubscriptionCheckoutCompleted(
			ctx,
			sess.ID,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
			sess.CustomerID,
			sess.Subscription,
			stripeSubscriptionProvisionable(sess.PaymentStatus),
		)
	case PurposeInvoice:
		return s.handleInvoiceCheckoutCompleted(
			ctx,
			sess.ID,
			sess.PaymentIntent,
			sess.Metadata.TenantID,
			sess.Metadata.ReferenceID,
			sess.AmountTotal,
			sess.Currency,
			stripeCheckoutPaid(sess.PaymentStatus),
		)
	case PurposePrepaid:
		return s.handlePrepaidCheckoutCompleted(
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
		return s.handleClusterSubscriptionCheckoutCompleted(
			ctx,
			sess.ID,
			sess.Metadata.TenantID,
			clusterID,
			sess.CustomerID,
			sess.Subscription,
			stripeSubscriptionProvisionable(sess.PaymentStatus),
		)
	default:
		s.logger.WithField("purpose", purpose).Warn("Unknown checkout purpose, ignoring")
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
func (s *Service) handleSubscriptionCheckoutCompleted(ctx context.Context, sessionID, tenantID, tierID, customerID, subscriptionID string, settled bool) error {
	if tenantID == "" {
		s.logger.WithField("session_id", sessionID).Warn("No tenant_id in subscription checkout metadata")
		return nil
	}

	if !settled {
		return s.stageTenantSubscriptionPending(ctx, sessionID, tenantID, customerID, subscriptionID)
	}

	rows, err := s.activateTenantSubscriptionFromStripe(ctx, tenantID, customerID, subscriptionID, tierID, nil, nil)
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("no tenant_subscriptions row for tenant %s; cannot activate Stripe subscription %s", tenantID, subscriptionID)
	}
	if err := purserdb.New(s.db).MarkStripeIntentSucceededBySession(ctx, purserdb.MarkStripeIntentSucceededBySessionParams{
		SubscriptionID: subscriptionID, SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("failed to mark subscription checkout intent succeeded: %w", err)
	}

	s.logger.WithFields(logging.Fields{
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
func (s *Service) activateTenantSubscriptionFromStripe(ctx context.Context, tenantID, customerID, subscriptionID, tierID string, periodStart, periodEnd *time.Time) (int64, error) {
	toNullTime := func(value *time.Time) sql.NullTime {
		if value == nil {
			return sql.NullTime{}
		}
		return sql.NullTime{Time: *value, Valid: true}
	}
	rows, err := purserdb.New(s.db).ActivateTenantSubscriptionFromStripe(ctx, purserdb.ActivateTenantSubscriptionFromStripeParams{
		CustomerID: customerID, SubscriptionID: subscriptionID, TierID: tierID,
		PeriodEnd: toNullTime(periodEnd), PeriodStart: toNullTime(periodStart), TenantID: tenantID,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to activate tenant subscription: %w", err)
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
func (s *Service) stageTenantSubscriptionPending(ctx context.Context, sessionID, tenantID, customerID, subscriptionID string) error {
	if err := purserdb.New(s.db).StageTenantSubscriptionPendingStripe(ctx, purserdb.StageTenantSubscriptionPendingStripeParams{
		CustomerID: customerID, SubscriptionID: subscriptionID, TenantID: tenantID,
	}); err != nil {
		return fmt.Errorf("failed to stage pending tenant subscription: %w", err)
	}
	if err := s.linkStripeIntentSubscription(ctx, sessionID, subscriptionID); err != nil {
		return err
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"subscription_id": subscriptionID,
	}).Info("Staged subscription pending async settlement; awaiting customer.subscription.updated")
	return nil
}

// handleClusterSubscriptionCheckoutCompleted handles paid cluster subscription
// activation. An async first payment (settled=false) stages the row without
// granting access; activation arrives via customer.subscription.updated /
// invoice.paid once Stripe collects the funds.
func (s *Service) handleClusterSubscriptionCheckoutCompleted(ctx context.Context, sessionID, tenantID, clusterID, customerID, subscriptionID string, settled bool) error {
	if tenantID == "" || clusterID == "" {
		s.logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"tenant_id":  tenantID,
			"cluster_id": clusterID,
		}).Warn("Missing tenant_id or cluster_id in cluster subscription checkout metadata")
		return nil
	}

	if !settled {
		return s.stageClusterSubscriptionPending(ctx, sessionID, tenantID, clusterID, customerID, subscriptionID)
	}
	return s.activateClusterSubscriptionFromStripe(ctx, tenantID, clusterID, customerID, subscriptionID, sessionID)
}

// activateClusterSubscriptionFromStripe is the single idempotent authority that
// marks a cluster subscription active and grants cluster access. It is called
// from checkout.session.completed (paid), customer.subscription.updated
// (active), and invoice.paid; whichever lands first activates and the rest are
// no-ops. When tenant/cluster are unknown (invoice.paid carries only the
// subscription id) it resolves them from the existing row, and returns nil when
// the subscription is not a cluster subscription.
func (s *Service) activateClusterSubscriptionFromStripe(ctx context.Context, tenantID, clusterID, customerID, subscriptionID, sessionID string) error {
	if tenantID == "" || clusterID == "" {
		if subscriptionID == "" {
			return nil
		}
		resolved, err := purserdb.New(s.db).ResolveClusterSubscriptionByStripeID(ctx, subscriptionID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not a cluster subscription
		}
		if err != nil {
			return fmt.Errorf("resolve cluster subscription %s: %w", subscriptionID, err)
		}
		tenantID, clusterID = resolved.TenantID, resolved.ClusterID
		if customerID == "" {
			customerID = resolved.StripeCustomerID
		}
	}

	// Skip the grant when the row is already active so duplicate events do not
	// re-enqueue access work. Best-effort read; the upsert below is the
	// authority for the row state.
	currentStatus, statusErr := purserdb.New(s.db).GetClusterSubscriptionStatus(ctx, purserdb.GetClusterSubscriptionStatusParams{TenantID: tenantID, ClusterID: clusterID})
	if statusErr != nil && !errors.Is(statusErr, sql.ErrNoRows) {
		return fmt.Errorf("lookup cluster subscription status: %w", statusErr)
	}
	alreadyActive := currentStatus == "active"

	// Grant access BEFORE marking the row active. A failed grant returns an
	// error and leaves the row non-active, so the webhook retry re-attempts the
	// grant — there is no active-without-access stranding, and no crash window
	// can leave the row active but ungranted. Quartermaster's grant is
	// idempotent, so a rare concurrent double-grant is harmless.
	if !alreadyActive && s.qmClient != nil {
		grantCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.qmClient.MaterializeClusterAccess(grantCtx, &quartermasterpb.MaterializeClusterAccessRequest{
			TenantId: tenantID, ClusterId: clusterID,
			AccessSource:           clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION,
			AuthorizationReference: "stripe:" + subscriptionID,
		}); err != nil {
			return fmt.Errorf("failed to grant cluster access: %w", err)
		}
	}

	queries := purserdb.New(s.db)
	if err := queries.UpsertActiveStripeClusterSubscription(ctx, purserdb.UpsertActiveStripeClusterSubscriptionParams{
		TenantID: tenantID, ClusterID: clusterID, CustomerID: customerID,
		SubscriptionID: subscriptionID, SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("failed to activate cluster subscription: %w", err)
	}

	if intentErr := queries.MarkStripeClusterIntentSucceeded(ctx, purserdb.MarkStripeClusterIntentSucceededParams{
		SubscriptionID: subscriptionID, SessionID: sessionID,
	}); intentErr != nil {
		return fmt.Errorf("failed to mark cluster checkout intent succeeded: %w", intentErr)
	}

	if !alreadyActive {
		s.logger.WithFields(logging.Fields{
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
func (s *Service) stageClusterSubscriptionPending(ctx context.Context, sessionID, tenantID, clusterID, customerID, subscriptionID string) error {
	if err := purserdb.New(s.db).UpsertPendingStripeClusterSubscription(ctx, purserdb.UpsertPendingStripeClusterSubscriptionParams{
		TenantID: tenantID, ClusterID: clusterID, CustomerID: customerID,
		SubscriptionID: subscriptionID, SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("failed to stage pending cluster subscription: %w", err)
	}
	if err := s.linkStripeIntentSubscription(ctx, sessionID, subscriptionID); err != nil {
		return err
	}
	s.logger.WithFields(logging.Fields{
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
func (s *Service) clearStagedStripeCheckout(ctx context.Context, tenantID, subscriptionID string) error {
	if subscriptionID != "" {
		if err := purserdb.New(s.db).ExpireStripeIntentBySubscription(ctx, subscriptionID); err != nil {
			return fmt.Errorf("expire staged stripe intent for %s: %w", subscriptionID, err)
		}
	}
	if err := purserdb.New(s.db).ClearTenantStripeCheckoutPending(ctx, tenantID); err != nil {
		return fmt.Errorf("clear staged stripe checkout for tenant %s: %w", tenantID, err)
	}
	return nil
}

// markPendingTopupTerminal moves a still-pending top-up to a terminal status
// (failed or expired). Guarded on status='pending' so a completed top-up that
// already credited the balance is never reverted. Idempotent.
func (s *Service) markPendingTopupTerminal(ctx context.Context, topupID, status string) error {
	if topupID == "" {
		return nil
	}
	if err := purserdb.New(s.db).MarkPendingTopupTerminal(ctx, purserdb.MarkPendingTopupTerminalParams{Status: status, TopupID: topupID}); err != nil {
		return fmt.Errorf("mark pending top-up %s as %s: %w", topupID, status, err)
	}
	return nil
}

// linkStripeIntentSubscription stamps the Stripe subscription id onto the
// session-keyed provider intent. Async checkouts stage before the subscription
// is active, and activation later closes the intent by provider_subscription_id;
// without this link that update matches zero rows and the intent stays open.
func (s *Service) linkStripeIntentSubscription(ctx context.Context, sessionID, subscriptionID string) error {
	if sessionID == "" || subscriptionID == "" {
		return nil
	}
	if err := purserdb.New(s.db).LinkStripeIntentSubscription(ctx, purserdb.LinkStripeIntentSubscriptionParams{
		SubscriptionID: subscriptionID, SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("link stripe intent %s to subscription %s: %w", sessionID, subscriptionID, err)
	}
	return nil
}

// expireStripeCheckoutIntent marks a still-open provider intent expired when its
// Checkout Session expires. Guarded so terminal intents are left untouched.
func (s *Service) expireStripeCheckoutIntent(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if err := purserdb.New(s.db).ExpireStripeIntentBySession(ctx, sessionID); err != nil {
		return fmt.Errorf("expire stripe checkout intent %s: %w", sessionID, err)
	}
	return nil
}

// clearStagedClusterSubscription cancels a cluster_subscriptions row that was
// staged pending_payment for a Stripe checkout that expired or failed before
// activating. Guarded on status='pending_payment' so an active subscription is
// never touched. Matched by the checkout session id (always set at staging) or
// the subscription id when one was created. Idempotent.
func (s *Service) clearStagedClusterSubscription(ctx context.Context, sessionID, subscriptionID string) error {
	if sessionID == "" && subscriptionID == "" {
		return nil
	}
	if err := purserdb.New(s.db).ClearStagedStripeClusterSubscription(ctx, purserdb.ClearStagedStripeClusterSubscriptionParams{
		SessionID: sessionID, SubscriptionID: subscriptionID,
	}); err != nil {
		return fmt.Errorf("clear staged cluster subscription: %w", err)
	}
	return nil
}

// handleInvoiceCheckoutCompleted handles invoice payment completion. The
// payment_intent is always attached to the pending billing_payment so a later
// async settlement can match it, but the invoice is confirmed only once funds
// have actually settled (settled=true); async methods confirm via
// checkout.session.async_payment_succeeded.
func (s *Service) handleInvoiceCheckoutCompleted(ctx context.Context, sessionID, paymentIntentID, tenantID, invoiceID string, amountCents int64, currency string, settled bool) error {
	if invoiceID == "" || tenantID == "" {
		s.logger.WithField("session_id", sessionID).Debug("No invoice_id in checkout metadata, skipping")
		return fmt.Errorf("invoice checkout is missing tenant or invoice identity")
	}
	txID := sessionID
	if paymentIntentID != "" {
		txID = paymentIntentID
		rows, err := purserdb.New(s.db).AttachStripeIntentToInvoicePayment(ctx, purserdb.AttachStripeIntentToInvoicePaymentParams{
			PaymentIntentID: paymentIntentID, InvoiceID: invoiceID, TenantID: tenantID, SessionID: sessionID,
		})
		if err != nil {
			return fmt.Errorf("attach stripe payment_intent to invoice payment: %w", err)
		}
		if rows != 1 && !settled {
			return fmt.Errorf("invoice checkout identity did not match exactly one pending payment")
		}
	}
	if !settled {
		s.logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
		}).Info("Invoice checkout pending async settlement; awaiting async_payment_succeeded")
		return nil
	}
	updated, err := s.updateInvoicePaymentStatus("stripe", txID, invoiceID, "confirmed", providerSettlementEvidence{
		TenantID: tenantID, AmountCents: amountCents, Currency: currency,
	})
	if err != nil {
		return err
	}
	if !updated {
		s.logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
		}).Warn("Stripe checkout did not match a pending invoice payment")
		return nil
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"invoice_id": invoiceID,
	}).Info("Marked invoice as paid from Stripe checkout")

	return nil
}

// handlePrepaidCheckoutCompleted handles prepaid balance top-up completion. The
// provider payment id is attached to the pending_topup regardless, but the
// balance is credited only once funds have settled (settled=true); async
// methods credit via checkout.session.async_payment_succeeded.
func (s *Service) handlePrepaidCheckoutCompleted(ctx context.Context, sessionID, providerPaymentID, tenantID, topupID string, amountCents int64, currency string, provider CheckoutProvider, settled bool) error {
	if topupID == "" || tenantID == "" {
		s.logger.WithFields(logging.Fields{
			"session_id": sessionID,
			"topup_id":   topupID,
			"tenant_id":  tenantID,
		}).Warn("Missing topup_id or tenant_id in prepaid checkout metadata")
		return nil
	}

	now := time.Now()

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// 1. Lock the pending_topup row so concurrent webhook deliveries serialize
	//    on the idempotency check below.
	queries := purserdb.New(tx)
	topup, err := queries.LockPendingTopupForCheckout(ctx, topupID)
	if err != nil {
		return fmt.Errorf("failed to find pending topup: %w", err)
	}
	if topup.TenantID != tenantID {
		s.logger.WithFields(logging.Fields{
			"topup_id":         topupID,
			"tenant_id":        tenantID,
			"stored_tenant_id": topup.TenantID,
		}).Warn("Pending top-up tenant mismatch")
		return fmt.Errorf("pending top-up tenant mismatch")
	}
	if topup.Provider != string(provider) {
		return fmt.Errorf("pending top-up provider mismatch: stored %s, received %s", topup.Provider, provider)
	}
	if topup.AmountCents != amountCents || amountCents <= 0 {
		return fmt.Errorf("pending top-up amount mismatch: stored %d, received %d", topup.AmountCents, amountCents)
	}
	if !strings.EqualFold(topup.Currency, currency) || strings.TrimSpace(currency) == "" {
		return fmt.Errorf("pending top-up currency mismatch: stored %s, received %s", topup.Currency, currency)
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("pending top-up provider session is missing")
	}
	if topup.CheckoutID.Valid && topup.CheckoutID.String != sessionID {
		return fmt.Errorf("pending top-up checkout identity mismatch")
	}
	if topup.ProviderPaymentID.Valid && topup.ProviderPaymentID.String != providerPaymentID {
		return fmt.Errorf("pending top-up payment identity mismatch")
	}
	if settled && strings.TrimSpace(providerPaymentID) == "" {
		return fmt.Errorf("settled pending top-up provider payment identity is missing")
	}

	if topup.Status != "pending" {
		s.logger.WithFields(logging.Fields{
			"topup_id": topupID,
			"status":   topup.Status,
		}).Info("Top-up already processed, skipping")
		return nil
	}

	attached, attachErr := queries.AttachProviderPaymentToPendingTopup(ctx, purserdb.AttachProviderPaymentToPendingTopupParams{
		ProviderPaymentID: providerPaymentID, SessionID: sessionID, TopupID: topupID,
		TenantID: tenantID, Provider: string(provider), AmountCents: amountCents, Currency: currency,
	})
	if attachErr != nil {
		return fmt.Errorf("failed to attach provider payment to topup: %w", attachErr)
	}
	if attached != 1 {
		return fmt.Errorf("pending top-up evidence changed before provider payment attachment")
	}

	// Async methods complete the Checkout Session before funds settle; persist
	// the linkage but do not credit until async_payment_succeeded arrives.
	if !settled {
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("commit pending top-up linkage: %w", commitErr)
		}
		s.logger.WithFields(logging.Fields{
			"topup_id":  topupID,
			"tenant_id": tenantID,
		}).Info("Prepaid top-up pending async settlement; awaiting async_payment_succeeded")
		return nil
	}

	// 2. Credit prepaid balance.
	if err = queries.EnsurePrepaidBalanceRow(ctx, purserdb.EnsurePrepaidBalanceRowParams{TenantID: tenantID, Currency: currency}); err != nil {
		return fmt.Errorf("failed to ensure prepaid balance: %w", err)
	}
	currentBalance, err := queries.AddPrepaidBalance(ctx, purserdb.AddPrepaidBalanceParams{
		AmountCents: amountCents, TenantID: tenantID, Currency: currency,
	})
	if err != nil {
		return fmt.Errorf("failed to update prepaid balance: %w", err)
	}

	// 3. Create balance transaction. reference_type='topup' activates the
	//    partial unique index at purser.sql:idx_balance_transactions_idempotency
	//    so replayed webhooks cannot double-credit.
	txID := uuid.New()
	err = queries.InsertBalanceTransaction(ctx, purserdb.InsertBalanceTransactionParams{
		ID: txID, TenantID: tenantID, AmountCents: amountCents, BalanceAfterCents: currentBalance,
		TransactionType: "topup",
		Description:     sql.NullString{String: fmt.Sprintf("Card top-up via %s", provider), Valid: true},
		ReferenceID:     sql.NullString{String: topupID, Valid: true},
		ReferenceType:   sql.NullString{String: "topup", Valid: true},
		ActorKind:       sql.NullString{String: "webhook", Valid: true},
		Reason:          sql.NullString{String: fmt.Sprintf("%s checkout completed", provider), Valid: true},
		EvidenceRef:     sql.NullString{String: sessionID, Valid: true},
		CreatedAt:       sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("failed to create balance transaction: %w", err)
	}

	// 4. Update pending_topup to completed
	err = queries.CompletePendingTopup(ctx, purserdb.CompletePendingTopupParams{
		CompletedAt: sql.NullTime{Time: now, Valid: true}, BalanceTransactionID: txID.String(), TopupID: topupID,
	})
	if err != nil {
		return fmt.Errorf("failed to update pending topup: %w", err)
	}
	if err = queries.CompletePendingTopupProviderIntent(ctx, purserdb.CompletePendingTopupProviderIntentParams{
		ProviderPaymentID: providerPaymentID, SessionID: sessionID,
		CompletedAt: sql.NullTime{Time: now, Valid: true}, TopupID: topupID,
	}); err != nil {
		return fmt.Errorf("failed to update topup provider intent: %w", err)
	}

	// 5. If tenant was suspended due to balance, unsuspend
	_, err = queries.ReactivateFundedSubscription(ctx, tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to unsuspend tenant (may not have been suspended)")
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	s.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"topup_id":       topupID,
		"amount_cents":   amountCents,
		"new_balance":    currentBalance,
		"provider":       provider,
		"transaction_id": txID,
	}).Info("Credited prepaid balance from card top-up")

	emitBillingEvent(s.db, s.logger, eventTopupCredited, tenantID, "topup", topupID, &ipcpb.BillingEvent{
		TopupId:  topupID,
		Amount:   float64(amountCents) / 100.0,
		Currency: currency,
		Provider: string(provider),
		Status:   "credited",
	})

	return nil
}
