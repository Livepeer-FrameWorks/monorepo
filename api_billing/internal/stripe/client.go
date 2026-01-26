package stripe

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/customer"
	"github.com/stripe/stripe-go/v82/subscription"
	"github.com/stripe/stripe-go/v82/webhook"
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
	TenantID string
	Email    string
	Name     string
	Metadata map[string]string
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
	for k, v := range info.Metadata {
		createParams.Metadata[k] = v
	}

	cust, err := customer.New(createParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create Stripe customer: %w", err)
	}

	c.logger.WithFields(map[string]interface{}{
		"customer_id": cust.ID,
		"tenant_id":   info.TenantID,
	}).Info("Created new Stripe customer")

	return cust, nil
}

// CheckoutSessionParams for creating a checkout session
type CheckoutSessionParams struct {
	CustomerID  string // Stripe customer ID
	TenantID    string // For metadata
	TierID      string // For metadata
	Purpose     string // subscription, cluster_subscription, etc.
	ReferenceID string // tier_id, cluster_id, etc.
	ClusterID   string // For cluster subscriptions
	PriceID     string // Stripe Price ID (monthly or yearly)
	SuccessURL  string
	CancelURL   string
	TrialDays   int64 // Optional trial period
}

// CreateCheckoutSession creates a Stripe Checkout Session for subscription
func (c *Client) CreateCheckoutSession(ctx context.Context, params CheckoutSessionParams) (*stripe.CheckoutSession, error) {
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

	sess, err := checkoutsession.New(sessionParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create checkout session: %w", err)
	}

	c.logger.WithFields(map[string]interface{}{
		"session_id": sess.ID,
		"tenant_id":  params.TenantID,
		"price_id":   params.PriceID,
	}).Info("Created Stripe checkout session")

	return sess, nil
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

	c.logger.WithFields(map[string]interface{}{
		"subscription_id": subscriptionID,
		"new_price_id":    newPriceID,
	}).Info("Subscription updated")

	return updatedSub, nil
}

// WebhookEvent represents a parsed and verified Stripe webhook event
type WebhookEvent struct {
	Type     string
	ID       string
	Data     map[string]interface{}
	RawEvent *stripe.Event
}

// VerifyAndParseWebhook verifies the webhook signature and parses the event
func (c *Client) VerifyAndParseWebhook(payload []byte, signature string) (*stripe.Event, error) {
	event, err := webhook.ConstructEvent(payload, signature, c.webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("webhook signature verification failed: %w", err)
	}
	return &event, nil
}

// SubscriptionFromEvent extracts subscription data from a webhook event
func (c *Client) SubscriptionFromEvent(event *stripe.Event) (*stripe.Subscription, error) {
	switch event.Type {
	case "customer.subscription.created",
		"customer.subscription.updated",
		"customer.subscription.deleted",
		"customer.subscription.paused",
		"customer.subscription.resumed":
		var sub stripe.Subscription
		if err := sub.UnmarshalJSON(event.Data.Raw); err != nil {
			return nil, fmt.Errorf("failed to unmarshal subscription: %w", err)
		}
		return &sub, nil
	default:
		return nil, fmt.Errorf("event type %s does not contain subscription data", event.Type)
	}
}

// CheckoutSessionFromEvent extracts checkout session from a webhook event
func (c *Client) CheckoutSessionFromEvent(event *stripe.Event) (*stripe.CheckoutSession, error) {
	if event.Type != "checkout.session.completed" {
		return nil, fmt.Errorf("event type %s is not checkout.session.completed", event.Type)
	}

	var sess stripe.CheckoutSession
	if err := sess.UnmarshalJSON(event.Data.Raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal checkout session: %w", err)
	}
	return &sess, nil
}

// InvoiceFromEvent extracts invoice from a webhook event
func (c *Client) InvoiceFromEvent(event *stripe.Event) (*stripe.Invoice, error) {
	switch event.Type {
	case "invoice.paid", "invoice.payment_failed", "invoice.created", "invoice.finalized":
		var inv stripe.Invoice
		if err := inv.UnmarshalJSON(event.Data.Raw); err != nil {
			return nil, fmt.Errorf("failed to unmarshal invoice: %w", err)
		}
		return &inv, nil
	default:
		return nil, fmt.Errorf("event type %s does not contain invoice data", event.Type)
	}
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
