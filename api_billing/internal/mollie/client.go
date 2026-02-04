package mollie

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"frameworks/pkg/logging"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

// Client wraps Mollie API operations for subscription management.
// Uses the first-payment flow: iDEAL/card → SEPA DD mandate → recurring subscriptions.
type Client struct {
	client        *mollie.Client
	webhookSecret string // For webhook signature verification (if enabled)
	logger        logging.Logger
}

// Config for creating a new Mollie client
type Config struct {
	APIKey        string // MOLLIE_API_KEY (live_xxx or test_xxx)
	WebhookSecret string // Optional: for webhook signature verification
	Logger        logging.Logger
}

// NewClient creates a new Mollie client
func NewClient(config Config) (*Client, error) {
	mollieConfig := mollie.NewAPITestingConfig(true) // Use testing mode for test keys
	if len(config.APIKey) > 5 && config.APIKey[:5] == "live_" {
		mollieConfig = mollie.NewAPIConfig(true) // Use live mode for live keys
	}

	client, err := mollie.NewClient(nil, mollieConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie client: %w", err)
	}

	// Set API key
	if err := client.WithAuthenticationValue(config.APIKey); err != nil {
		return nil, fmt.Errorf("failed to set Mollie API key: %w", err)
	}

	return &Client{
		client:        client,
		webhookSecret: config.WebhookSecret,
		logger:        config.Logger,
	}, nil
}

// HasWebhookSecret returns true when webhook signature verification is configured.
func (c *Client) HasWebhookSecret() bool {
	return c.webhookSecret != ""
}

// CustomerInfo for Mollie customer creation
type CustomerInfo struct {
	TenantID string
	Email    string
	Name     string
	Locale   string // nl_NL, en_US, etc.
}

// CreateOrGetCustomer finds existing customer by tenant ID metadata or creates a new one
func (c *Client) CreateOrGetCustomer(ctx context.Context, info CustomerInfo) (*mollie.Customer, error) {
	// Mollie doesn't support metadata search, so we store mapping in our DB
	// For now, just create a new customer - caller should check DB first

	locale := mollie.Locale(info.Locale)
	if info.Locale == "" {
		locale = mollie.English
	}

	_, customer, err := c.client.Customers.Create(ctx, mollie.CreateCustomer{
		Name:   info.Name,
		Email:  info.Email,
		Locale: locale,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Mollie customer: %w", err)
	}

	c.logger.WithFields(map[string]interface{}{
		"customer_id": customer.ID,
		"tenant_id":   info.TenantID,
	}).Info("Created Mollie customer")

	return customer, nil
}

// GetCustomer retrieves a customer by ID
func (c *Client) GetCustomer(ctx context.Context, customerID string) (*mollie.Customer, error) {
	_, customer, err := c.client.Customers.Get(ctx, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get Mollie customer: %w", err)
	}
	return customer, nil
}

// FirstPaymentParams for creating the initial payment that establishes a mandate
type FirstPaymentParams struct {
	CustomerID  string               // Mollie customer ID
	TenantID    string               // For metadata/logging
	TierID      string               // For price lookup
	Amount      *mollie.Amount       // Payment amount
	Description string               // Payment description
	Method      mollie.PaymentMethod // ideal, creditcard, bancontact
	RedirectURL string               // Where to redirect after payment
	WebhookURL  string               // Webhook for payment status updates
}

// CreateFirstPayment creates the initial payment to establish a mandate.
// For iDEAL: User pays via bank → Creates SEPA Direct Debit mandate
// For card: User enters card → Creates card mandate
func (c *Client) CreateFirstPayment(ctx context.Context, params FirstPaymentParams) (*mollie.Payment, error) {
	paymentParams := mollie.CreatePayment{
		Amount:      params.Amount,
		Description: params.Description,
		RedirectURL: params.RedirectURL,
		WebhookURL:  params.WebhookURL,
		Method:      []mollie.PaymentMethod{params.Method},
		Metadata: map[string]interface{}{
			"purpose":      "mandate_setup",
			"tenant_id":    params.TenantID,
			"tier_id":      params.TierID,
			"reference_id": params.TierID,
			"payment_type": "first_payment",
		},
		CreateRecurrentPaymentFields: mollie.CreateRecurrentPaymentFields{
			SequenceType: mollie.FirstSequence,
		},
	}

	// Create payment for this customer
	_, payment, err := c.client.Customers.CreatePayment(ctx, params.CustomerID, paymentParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create first payment: %w", err)
	}

	c.logger.WithFields(map[string]interface{}{
		"payment_id":  payment.ID,
		"customer_id": params.CustomerID,
		"tenant_id":   params.TenantID,
		"method":      params.Method,
	}).Info("Created Mollie first payment")

	return payment, nil
}

// GetPayment retrieves a payment by ID
func (c *Client) GetPayment(ctx context.Context, paymentID string) (*mollie.Payment, error) {
	_, payment, err := c.client.Payments.Get(ctx, paymentID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get Mollie payment: %w", err)
	}
	return payment, nil
}

// ListMandates lists all mandates for a customer
func (c *Client) ListMandates(ctx context.Context, customerID string) ([]*mollie.Mandate, error) {
	_, list, err := c.client.Mandates.List(ctx, customerID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list mandates: %w", err)
	}
	return list.Embedded.Mandates, nil
}

// GetMandate retrieves a specific mandate
func (c *Client) GetMandate(ctx context.Context, customerID, mandateID string) (*mollie.Mandate, error) {
	_, mandate, err := c.client.Mandates.Get(ctx, customerID, mandateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get mandate: %w", err)
	}
	return mandate, nil
}

// SubscriptionParams for creating a subscription
type SubscriptionParams struct {
	CustomerID  string
	TenantID    string
	TierID      string
	Amount      *mollie.Amount
	Interval    string // 1 month, 1 year
	Description string
	StartDate   string // YYYY-MM-DD format, or empty for immediate
	WebhookURL  string
}

// CreateSubscription creates a recurring subscription using an existing mandate
func (c *Client) CreateSubscription(ctx context.Context, params SubscriptionParams) (*mollie.Subscription, error) {
	subParams := mollie.CreateSubscription{
		Amount:      params.Amount,
		Interval:    params.Interval,
		Description: params.Description,
		WebhookURL:  params.WebhookURL,
		Metadata: map[string]interface{}{
			"purpose":      "subscription",
			"tenant_id":    params.TenantID,
			"tier_id":      params.TierID,
			"reference_id": params.TierID,
		},
	}

	if params.StartDate != "" {
		sd := &mollie.ShortDate{}
		if err := sd.UnmarshalJSON([]byte(`"` + params.StartDate + `"`)); err == nil {
			subParams.StartDate = sd
		}
	}

	_, sub, err := c.client.Subscriptions.Create(ctx, params.CustomerID, subParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}

	c.logger.WithFields(map[string]interface{}{
		"subscription_id": sub.ID,
		"customer_id":     params.CustomerID,
		"tenant_id":       params.TenantID,
		"interval":        params.Interval,
	}).Info("Created Mollie subscription")

	return sub, nil
}

// GetSubscription retrieves a subscription
func (c *Client) GetSubscription(ctx context.Context, customerID, subscriptionID string) (*mollie.Subscription, error) {
	_, sub, err := c.client.Subscriptions.Get(ctx, customerID, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}
	return sub, nil
}

// CancelSubscription cancels a subscription
func (c *Client) CancelSubscription(ctx context.Context, customerID, subscriptionID string) error {
	_, _, err := c.client.Subscriptions.Cancel(ctx, customerID, subscriptionID)
	if err != nil {
		return fmt.Errorf("failed to cancel subscription: %w", err)
	}

	c.logger.WithFields(map[string]interface{}{
		"subscription_id": subscriptionID,
		"customer_id":     customerID,
	}).Info("Cancelled Mollie subscription")

	return nil
}

// VerifyWebhook verifies the webhook signature (if webhook secret is configured)
// Mollie doesn't sign webhooks by default - they recommend IP allowlisting or
// fetching the payment/subscription from their API to verify authenticity.
// This method provides optional HMAC verification if configured.
func (c *Client) VerifyWebhook(payload []byte, signature string) bool {
	if c.webhookSecret == "" {
		// No secret configured, skip verification
		// Caller should verify by fetching from Mollie API
		return true
	}

	mac := hmac.New(sha256.New, []byte(c.webhookSecret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expected))
}

// MandateInfo contains extracted mandate details for database storage
type MandateInfo struct {
	MollieMandateID  string
	MollieCustomerID string
	Status           string // valid, pending, invalid
	Method           string // directdebit, creditcard
	Details          map[string]interface{}
	CreatedAt        time.Time
}

// ExtractMandateInfo extracts relevant fields from a Mollie mandate
func (c *Client) ExtractMandateInfo(mandate *mollie.Mandate, customerID string) MandateInfo {
	details := make(map[string]interface{})

	if mandate.Details.ConsumerName != "" {
		details["consumer_name"] = mandate.Details.ConsumerName
	}
	if mandate.Details.ConsumerAccount != "" {
		details["consumer_account"] = mandate.Details.ConsumerAccount
	}
	if mandate.Details.ConsumerBic != "" {
		details["consumer_bic"] = mandate.Details.ConsumerBic
	}
	if mandate.Details.CardHolder != "" {
		details["card_holder"] = mandate.Details.CardHolder
	}
	if mandate.Details.CardNumber != "" {
		details["card_number"] = mandate.Details.CardNumber
	}
	if mandate.Details.CardLabel != "" {
		details["card_label"] = mandate.Details.CardLabel
	}

	var createdAt time.Time
	if mandate.CreatedAt != nil {
		createdAt = *mandate.CreatedAt
	}

	return MandateInfo{
		MollieMandateID:  mandate.ID,
		MollieCustomerID: customerID,
		Status:           string(mandate.Status),
		Method:           string(mandate.Method),
		Details:          details,
		CreatedAt:        createdAt,
	}
}

// SubscriptionInfo contains extracted subscription details for database updates
type SubscriptionInfo struct {
	MollieSubscriptionID string
	MollieCustomerID     string
	Status               string // active, pending, suspended, completed, canceled
	Amount               string
	Currency             string
	Interval             string
	NextPaymentDate      string
	TenantID             string
	TierID               string
}

// ExtractSubscriptionInfo extracts relevant fields from a Mollie subscription
func (c *Client) ExtractSubscriptionInfo(sub *mollie.Subscription, customerID string) SubscriptionInfo {
	info := SubscriptionInfo{
		MollieSubscriptionID: sub.ID,
		MollieCustomerID:     customerID,
		Status:               string(sub.Status),
		Interval:             sub.Interval,
	}

	if sub.Amount != nil {
		info.Amount = sub.Amount.Value
		info.Currency = sub.Amount.Currency
	}

	if sub.NextPaymentDate != nil {
		info.NextPaymentDate = sub.NextPaymentDate.String()
	}

	if metadata, ok := sub.Metadata.(map[string]interface{}); ok {
		if tenantID, ok := metadata["tenant_id"].(string); ok {
			info.TenantID = tenantID
		}
		if tierID, ok := metadata["tier_id"].(string); ok {
			info.TierID = tierID
		}
	}

	return info
}

// Amount helper to create Mollie amount
func Amount(value string, currency string) *mollie.Amount {
	return &mollie.Amount{
		Value:    value,
		Currency: currency,
	}
}
