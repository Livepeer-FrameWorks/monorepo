package stripe

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/customer"
	"github.com/stripe/stripe-go/v82/paymentintent"
	stripeprice "github.com/stripe/stripe-go/v82/price"
	stripeproduct "github.com/stripe/stripe-go/v82/product"
	"github.com/stripe/stripe-go/v82/subscription"
)

// Client wraps Stripe API operations for subscription management.
// All subscription-related operations flow through Stripe Checkout or Billing Portal.
type Client struct {
	secretKey     string
	webhookSecret string
	logger        logging.Logger
}

// Config for creating a new Stripe client
type Config struct {
	SecretKey     string // STRIPE_SECRET_KEY
	WebhookSecret string // STRIPE_WEBHOOK_SECRET
	Logger        logging.Logger
}

// NewClient creates a new Stripe client
func NewClient(config Config) *Client {
	// Set the global API key for the stripe-go library
	stripe.Key = config.SecretKey

	return &Client{
		secretKey:     config.SecretKey,
		webhookSecret: config.WebhookSecret,
		logger:        config.Logger,
	}
}

// CustomerInfo represents tenant data for Stripe customer creation
type CustomerInfo struct {
	TenantID       string
	Email          string
	Name           string
	Metadata       map[string]string
	IdempotencyKey string
}

// CreateOrGetCustomer finds existing customer by tenant ID or creates a new one
func (c *Client) CreateOrGetCustomer(ctx context.Context, info CustomerInfo) (*stripe.Customer, error) {
	// Search for existing customer by tenant_id metadata
	params := &stripe.CustomerSearchParams{}
	params.Query = fmt.Sprintf("metadata['tenant_id']:'%s'", info.TenantID)
	iter := customer.Search(params)

	for iter.Next() {
		cust := iter.Customer()
		c.logger.WithField("customer_id", cust.ID).Debug("Found existing Stripe customer")
		return cust, nil
	}
	if err := iter.Err(); err != nil {
		c.logger.WithError(err).Warn("Error searching for Stripe customer, will create new")
	}

	// Create new customer
	createParams := &stripe.CustomerParams{
		Email: stripe.String(info.Email),
		Name:  stripe.String(info.Name),
		Metadata: map[string]string{
			"tenant_id": info.TenantID,
		},
	}
	maps.Copy(createParams.Metadata, info.Metadata)
	idempotencyKey := info.IdempotencyKey
	if idempotencyKey == "" && info.TenantID != "" {
		idempotencyKey = "stripe-customer:" + info.TenantID
	}
	if idempotencyKey != "" {
		createParams.SetIdempotencyKey(idempotencyKey)
	}

	cust, err := customer.New(createParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create Stripe customer: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"customer_id": cust.ID,
		"tenant_id":   info.TenantID,
	}).Info("Created new Stripe customer")

	return cust, nil
}

// CheckoutSessionParams for creating a checkout session
type CheckoutSessionParams struct {
	CustomerID     string // Stripe customer ID
	TenantID       string // For metadata
	TierID         string // For metadata
	Purpose        string // subscription, cluster_subscription, etc.
	ReferenceID    string // tier_id, cluster_id, etc.
	ClusterID      string // For cluster subscriptions
	PriceID        string // Stripe Price ID (monthly or yearly)
	SuccessURL     string
	CancelURL      string
	TrialDays      int64 // Optional trial period
	IdempotencyKey string
}

// CreateCheckoutSession creates a Stripe Checkout Session for subscription
func (c *Client) CreateCheckoutSession(ctx context.Context, params CheckoutSessionParams) (*stripe.CheckoutSession, error) {
	if params.IdempotencyKey == "" {
		return nil, fmt.Errorf("CreateCheckoutSession requires a deterministic IdempotencyKey")
	}
	metadata := map[string]string{
		"tenant_id": params.TenantID,
	}
	if params.Purpose != "" {
		metadata["purpose"] = params.Purpose
	}
	if params.ReferenceID != "" {
		metadata["reference_id"] = params.ReferenceID
	}
	if params.TierID != "" {
		metadata["tier_id"] = params.TierID
	}
	if params.ClusterID != "" {
		metadata["cluster_id"] = params.ClusterID
	}

	sessionParams := &stripe.CheckoutSessionParams{
		Customer: stripe.String(params.CustomerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(params.PriceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String(params.SuccessURL),
		CancelURL:  stripe.String(params.CancelURL),
		Metadata:   metadata,
	}

	// Ensure subscription metadata is set on the created Stripe subscription.
	subscriptionData := &stripe.CheckoutSessionSubscriptionDataParams{
		Metadata: metadata,
	}
	if params.TrialDays > 0 {
		subscriptionData.TrialPeriodDays = stripe.Int64(params.TrialDays)
	}
	sessionParams.SubscriptionData = subscriptionData
	sessionParams.SetIdempotencyKey(params.IdempotencyKey)

	sess, err := checkoutsession.New(sessionParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create checkout session: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"session_id": sess.ID,
		"tenant_id":  params.TenantID,
		"price_id":   params.PriceID,
	}).Info("Created Stripe checkout session")

	return sess, nil
}

// OffSessionChargeParams describes a Purser-owned off-session charge for
// metered overage on a finalized invoice. The PaymentMethodID is the
// customer's saved card; if empty, Stripe falls back to the customer's
// default payment method. IdempotencyKey is required and must be
// deterministic on (invoice, attempt) so retries collapse to one charge.
type OffSessionChargeParams struct {
	CustomerID       string
	PaymentMethodID  string
	TenantID         string
	InvoiceID        string
	BillingPaymentID string
	AmountCents      int64
	Currency         string
	IdempotencyKey   string
	Description      string
}

// OffSessionChargeResult is the slim shape callers consume. SCARequired
// distinguishes the "needs customer action" outcome from generic
// success/failure so the caller can park the local intent and notify the
// customer instead of retrying. NextAction.HostedURL holds Stripe's
// authentication URL when set.
type OffSessionChargeResult struct {
	PaymentIntentID string
	Status          string
	SCARequired     bool
	NextActionURL   string
	FailureCode     string
	FailureMessage  string
	AmountReceived  int64
}

// ChargeOffSession creates a Stripe PaymentIntent for the customer's saved
// payment method and confirms it off-session. Success means Stripe captured
// the charge synchronously; the corresponding payment_intent.succeeded
// webhook still flows through the partial-payment-aware settlement so
// retries and out-of-order events stay consistent. SCA-required is
// surfaced as SCARequired=true so the caller does NOT mark the attempt
// failed — the customer must complete authentication via NextActionURL.
func (c *Client) ChargeOffSession(ctx context.Context, params OffSessionChargeParams) (*OffSessionChargeResult, error) {
	if params.IdempotencyKey == "" {
		return nil, fmt.Errorf("ChargeOffSession requires a deterministic IdempotencyKey")
	}
	if params.CustomerID == "" {
		return nil, fmt.Errorf("ChargeOffSession requires a Stripe customer id")
	}
	if params.AmountCents <= 0 {
		return nil, fmt.Errorf("ChargeOffSession amount must be positive, got %d", params.AmountCents)
	}
	if params.Currency == "" {
		return nil, fmt.Errorf("ChargeOffSession requires a currency")
	}

	piParams := &stripe.PaymentIntentParams{
		Amount:     stripe.Int64(params.AmountCents),
		Currency:   stripe.String(strings.ToLower(params.Currency)),
		Customer:   stripe.String(params.CustomerID),
		Confirm:    stripe.Bool(true),
		OffSession: stripe.Bool(true),
	}
	if params.PaymentMethodID != "" {
		piParams.PaymentMethod = stripe.String(params.PaymentMethodID)
	}
	if params.Description != "" {
		piParams.Description = stripe.String(params.Description)
	}
	piParams.Metadata = map[string]string{
		"tenant_id":          params.TenantID,
		"invoice_id":         params.InvoiceID,
		"billing_payment_id": params.BillingPaymentID,
		"purpose":            "overage",
	}
	piParams.IdempotencyKey = stripe.String(params.IdempotencyKey)

	pi, err := paymentintent.New(piParams)
	if err != nil {
		// Stripe surfaces SCA-required as an API error with
		// code="authentication_required". Treat that distinctly.
		var apiErr *stripe.Error
		if errors.As(err, &apiErr) {
			if apiErr.Code == stripe.ErrorCodeAuthenticationRequired {
				return &OffSessionChargeResult{
					PaymentIntentID: extractStripePaymentIntentID(apiErr),
					Status:          "requires_action",
					SCARequired:     true,
					FailureCode:     string(apiErr.Code),
					FailureMessage:  apiErr.Msg,
				}, nil
			}
			// Generic API errors (card_declined, expired_card, etc.)
			// become failed attempts the caller can retry with backoff.
			return &OffSessionChargeResult{
				PaymentIntentID: extractStripePaymentIntentID(apiErr),
				Status:          "failed",
				FailureCode:     string(apiErr.Code),
				FailureMessage:  apiErr.Msg,
			}, nil
		}
		return nil, fmt.Errorf("failed to create off-session PaymentIntent: %w", err)
	}

	result := &OffSessionChargeResult{
		PaymentIntentID: pi.ID,
		Status:          string(pi.Status),
		AmountReceived:  pi.AmountReceived,
	}
	if pi.Status == stripe.PaymentIntentStatusRequiresAction {
		result.SCARequired = true
		if pi.NextAction != nil && pi.NextAction.RedirectToURL != nil {
			result.NextActionURL = pi.NextAction.RedirectToURL.URL
		}
	}
	c.logger.WithFields(map[string]any{
		"payment_intent_id": pi.ID,
		"status":            pi.Status,
		"customer_id":       params.CustomerID,
		"invoice_id":        params.InvoiceID,
	}).Info("Created Stripe off-session PaymentIntent")
	return result, nil
}

// extractStripePaymentIntentID pulls the PaymentIntent id from a Stripe
// API error when present. Stripe attaches the partially-created intent
// id to authentication_required errors so the customer reauthorization
// flow can resume against the same intent.
func extractStripePaymentIntentID(err *stripe.Error) string {
	if err == nil || err.PaymentIntent == nil {
		return ""
	}
	return err.PaymentIntent.ID
}

// ExpireCheckoutSession expires an open Checkout Session so it cannot be paid.
func (c *Client) ExpireCheckoutSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if _, err := checkoutsession.Expire(sessionID, nil); err != nil {
		return fmt.Errorf("failed to expire checkout session: %w", err)
	}
	c.logger.WithField("session_id", sessionID).Info("Expired Stripe checkout session")
	return nil
}

// CreateBillingPortalSession creates a session for customers to manage their subscription
func (c *Client) CreateBillingPortalSession(ctx context.Context, customerID, returnURL string) (*stripe.BillingPortalSession, error) {
	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	}

	sess, err := session.New(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create billing portal session: %w", err)
	}

	return sess, nil
}

// GetSubscription retrieves a subscription by ID
func (c *Client) GetSubscription(ctx context.Context, subscriptionID string) (*stripe.Subscription, error) {
	sub, err := subscription.Get(subscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}
	return sub, nil
}

// CancelSubscription cancels a subscription at period end
func (c *Client) CancelSubscription(ctx context.Context, subscriptionID string) (*stripe.Subscription, error) {
	params := &stripe.SubscriptionParams{
		CancelAtPeriodEnd: stripe.Bool(true),
	}

	sub, err := subscription.Update(subscriptionID, params)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel subscription: %w", err)
	}

	c.logger.WithField("subscription_id", subscriptionID).Info("Subscription scheduled for cancellation")
	return sub, nil
}

// CancelSubscriptionImmediately cancels a subscription immediately
func (c *Client) CancelSubscriptionImmediately(ctx context.Context, subscriptionID string) (*stripe.Subscription, error) {
	sub, err := subscription.Cancel(subscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel subscription immediately: %w", err)
	}

	c.logger.WithField("subscription_id", subscriptionID).Info("Subscription cancelled immediately")
	return sub, nil
}

// UpdateSubscription updates a subscription's price (tier change)
func (c *Client) UpdateSubscription(ctx context.Context, subscriptionID, newPriceID string) (*stripe.Subscription, error) {
	// First get the subscription to find the current item
	sub, err := subscription.Get(subscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription for update: %w", err)
	}

	if len(sub.Items.Data) == 0 {
		return nil, fmt.Errorf("subscription has no items")
	}

	// Update the subscription item with new price
	params := &stripe.SubscriptionParams{
		Items: []*stripe.SubscriptionItemsParams{
			{
				ID:    stripe.String(sub.Items.Data[0].ID),
				Price: stripe.String(newPriceID),
			},
		},
		ProrationBehavior: stripe.String("create_prorations"),
	}

	updatedSub, err := subscription.Update(subscriptionID, params)
	if err != nil {
		return nil, fmt.Errorf("failed to update subscription: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"subscription_id": subscriptionID,
		"new_price_id":    newPriceID,
	}).Info("Subscription updated")

	return updatedSub, nil
}

// SubscriptionInfo contains extracted subscription details for database updates
type SubscriptionInfo struct {
	StripeCustomerID     string
	StripeSubscriptionID string
	Status               string // active, past_due, canceled, trialing, etc.
	CurrentPeriodEnd     time.Time
	CancelAtPeriodEnd    bool
	TenantID             string // From metadata
	TierID               string // From metadata
}

// ExtractSubscriptionInfo extracts relevant fields from a Stripe subscription
func (c *Client) ExtractSubscriptionInfo(sub *stripe.Subscription) SubscriptionInfo {
	info := SubscriptionInfo{
		StripeCustomerID:     sub.Customer.ID,
		StripeSubscriptionID: sub.ID,
		Status:               string(sub.Status),
		CancelAtPeriodEnd:    sub.CancelAtPeriodEnd,
	}

	// CurrentPeriodEnd is on SubscriptionItem in v82
	if sub.Items != nil && len(sub.Items.Data) > 0 {
		info.CurrentPeriodEnd = time.Unix(sub.Items.Data[0].CurrentPeriodEnd, 0)
	}

	if sub.Metadata != nil {
		info.TenantID = sub.Metadata["tenant_id"]
		info.TierID = sub.Metadata["tier_id"]
	}

	return info
}

// FindOrCreateProduct searches for an existing product by tier_name metadata, creates one if not found.
func (c *Client) FindOrCreateProduct(ctx context.Context, tierName, displayName, description string) (*stripe.Product, error) {
	params := &stripe.ProductSearchParams{}
	params.Query = fmt.Sprintf("metadata['tier_name']:'%s'", tierName)
	iter := stripeproduct.Search(params)

	for iter.Next() {
		prod := iter.Product()
		c.logger.WithFields(map[string]any{
			"product_id": prod.ID,
			"tier_name":  tierName,
		}).Debug("Found existing Stripe product")
		return prod, nil
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to search Stripe products: %w", err)
	}

	prod, err := stripeproduct.New(&stripe.ProductParams{
		Name:        stripe.String(displayName),
		Description: stripe.String(description),
		Metadata: map[string]string{
			"tier_name": tierName,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Stripe product: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"product_id": prod.ID,
		"tier_name":  tierName,
	}).Info("Created Stripe product")

	return prod, nil
}

// FindOrCreatePrice finds an existing recurring price on a product matching the amount/currency/interval,
// or creates a new one.
func (c *Client) FindOrCreatePrice(ctx context.Context, productID string, amountCents int64, currency, interval string) (*stripe.Price, error) {
	listParams := &stripe.PriceListParams{
		Product:  stripe.String(productID),
		Active:   stripe.Bool(true),
		Currency: stripe.String(strings.ToLower(currency)),
		Recurring: &stripe.PriceListRecurringParams{
			Interval: stripe.String(interval),
		},
	}
	iter := stripeprice.List(listParams)

	for iter.Next() {
		p := iter.Price()
		if p.UnitAmount == amountCents {
			c.logger.WithFields(map[string]any{
				"price_id":   p.ID,
				"product_id": productID,
			}).Debug("Found existing Stripe price")
			return p, nil
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list Stripe prices: %w", err)
	}

	p, err := stripeprice.New(&stripe.PriceParams{
		Product:    stripe.String(productID),
		Currency:   stripe.String(strings.ToLower(currency)),
		UnitAmount: stripe.Int64(amountCents),
		Recurring: &stripe.PriceRecurringParams{
			Interval: stripe.String(interval),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Stripe price: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"price_id":     p.ID,
		"product_id":   productID,
		"amount_cents": amountCents,
		"currency":     currency,
		"interval":     interval,
	}).Info("Created Stripe price")

	return p, nil
}

// SyncTier ensures a Stripe product and monthly price exist for the given tier.
// Returns the Stripe product ID and monthly price ID. Idempotent.
func (c *Client) SyncTier(ctx context.Context, tierName, displayName, description string, basePrice decimal.Decimal, currency string) (productID, monthlyPriceID string, err error) {
	prod, err := c.FindOrCreateProduct(ctx, tierName, displayName, description)
	if err != nil {
		return "", "", err
	}

	amountCents := basePrice.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
	p, err := c.FindOrCreatePrice(ctx, prod.ID, amountCents, currency, "month")
	if err != nil {
		return prod.ID, "", err
	}

	return prod.ID, p.ID, nil
}

// DeactivatePrice marks a Stripe price as inactive. Stripe prices are immutable,
// so when a tier's base_price changes we create a new price and deactivate the
// previous one. Existing subscriptions on the old price keep billing at the old
// rate; new subscriptions can only use the active price.
func (c *Client) DeactivatePrice(ctx context.Context, priceID string) error {
	if priceID == "" {
		return nil
	}
	_, err := stripeprice.Update(priceID, &stripe.PriceParams{
		Active: stripe.Bool(false),
	})
	if err != nil {
		return fmt.Errorf("deactivate price %s: %w", priceID, err)
	}
	c.logger.WithField("price_id", priceID).Info("Deactivated stale Stripe price")
	return nil
}
