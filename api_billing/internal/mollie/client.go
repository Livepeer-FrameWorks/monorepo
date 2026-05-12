package mollie

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

// Client wraps Mollie API operations for subscription management.
// Uses the first-payment flow: iDEAL/card → SEPA DD mandate → recurring subscriptions.
type Client struct {
	client *mollie.Client
	logger logging.Logger
	apiKey string
}

// Config for creating a new Mollie client
type Config struct {
	APIKey string // MOLLIE_API_KEY (live_xxx or test_xxx)
	Logger logging.Logger
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
		client: client,
		logger: config.Logger,
		apiKey: config.APIKey,
	}, nil
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
	// Mollie customer lookup is keyed by Purser's local mollie_customers
	// table; callers must check that mapping before creating a customer.

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

	c.logger.WithFields(map[string]any{
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
	CustomerID     string               // Mollie customer ID
	TenantID       string               // For metadata/logging
	TierID         string               // For price lookup
	Amount         *mollie.Amount       // Payment amount
	Description    string               // Payment description
	Method         mollie.PaymentMethod // ideal, creditcard, bancontact
	RedirectURL    string               // Where to redirect after payment
	WebhookURL     string               // Webhook for payment status updates
	IdempotencyKey string
}

// CreateFirstPayment creates the initial payment to establish a mandate.
// For iDEAL: User pays via bank → Creates SEPA Direct Debit mandate
// For card: User enters card → Creates card mandate
func (c *Client) CreateFirstPayment(ctx context.Context, params FirstPaymentParams) (*mollie.Payment, error) {
	if params.IdempotencyKey == "" {
		return nil, fmt.Errorf("CreateFirstPayment requires a deterministic IdempotencyKey")
	}
	body, err := json.Marshal(map[string]any{
		"amount":      params.Amount,
		"description": params.Description,
		"redirectUrl": params.RedirectURL,
		"webhookUrl":  params.WebhookURL,
		"method":      []mollie.PaymentMethod{params.Method},
		"metadata": map[string]any{
			"purpose":      "mandate_setup",
			"tenant_id":    params.TenantID,
			"tier_id":      params.TierID,
			"reference_id": params.TierID,
			"payment_type": "first_payment",
		},
		"sequenceType": mollie.FirstSequence,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal first payment: %w", err)
	}
	u, err := c.mollieURL(fmt.Sprintf("v2/customers/%s/payments", url.PathEscape(params.CustomerID)))
	if err != nil {
		return nil, fmt.Errorf("build Mollie first payment URL: %w", err)
	}
	respBody, err := c.postJSON(ctx, u.String(), body, params.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create first payment: %w", err)
	}
	var payment mollie.Payment
	if err := json.Unmarshal(respBody, &payment); err != nil {
		return nil, fmt.Errorf("decode Mollie first payment response: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"payment_id":  payment.ID,
		"customer_id": params.CustomerID,
		"tenant_id":   params.TenantID,
		"method":      params.Method,
	}).Info("Created Mollie first payment")

	return &payment, nil
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
	CustomerID     string
	TenantID       string
	TierID         string
	MandateID      string // explicit mandate to charge; empty lets Mollie pick any valid one
	Amount         *mollie.Amount
	Interval       string // 1 month, 1 year
	Description    string
	StartDate      string // YYYY-MM-DD format, or empty for immediate
	WebhookURL     string
	IdempotencyKey string
}

// CreateSubscription creates a recurring subscription using an existing mandate
func (c *Client) CreateSubscription(ctx context.Context, params SubscriptionParams) (*mollie.Subscription, error) {
	if params.IdempotencyKey == "" {
		return nil, fmt.Errorf("CreateSubscription requires a deterministic IdempotencyKey")
	}
	bodyMap := map[string]any{
		"amount":      params.Amount,
		"interval":    params.Interval,
		"description": params.Description,
		"mandateId":   params.MandateID,
		"webhookUrl":  params.WebhookURL,
		"metadata": map[string]any{
			"purpose":      "subscription",
			"tenant_id":    params.TenantID,
			"tier_id":      params.TierID,
			"reference_id": params.TierID,
		},
	}

	if params.StartDate != "" {
		bodyMap["startDate"] = params.StartDate
	}

	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal subscription: %w", err)
	}
	u, err := c.mollieURL(fmt.Sprintf("v2/customers/%s/subscriptions", url.PathEscape(params.CustomerID)))
	if err != nil {
		return nil, fmt.Errorf("build Mollie subscription URL: %w", err)
	}
	respBody, err := c.postJSON(ctx, u.String(), body, params.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}
	var sub mollie.Subscription
	if err := json.Unmarshal(respBody, &sub); err != nil {
		return nil, fmt.Errorf("decode Mollie subscription response: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"subscription_id": sub.ID,
		"customer_id":     params.CustomerID,
		"tenant_id":       params.TenantID,
		"interval":        params.Interval,
	}).Info("Created Mollie subscription")

	return &sub, nil
}

func (c *Client) mollieURL(path string) (*url.URL, error) {
	if c.client == nil || c.client.BaseURL == nil {
		return nil, fmt.Errorf("mollie client not initialized")
	}
	return c.client.BaseURL.Parse(path)
}

func (c *Client) postJSON(ctx context.Context, rawURL string, body []byte, idempotencyKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build Mollie request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read Mollie response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mollie status %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// OnDemandChargeParams for charging an existing mandate on-demand (e.g. for
// invoice/overage collection on Mollie subscribers).
type OnDemandChargeParams struct {
	CustomerID     string
	MandateID      string
	TenantID       string
	InvoiceID      string
	PaymentID      string
	Amount         *mollie.Amount
	Description    string
	WebhookURL     string
	IdempotencyKey string
}

// ChargeOnMandate creates a recurring-sequence payment against an existing
// mandate. The resulting payment fires the same webhook flow as subscription
// installments; reconciliation routes by metadata.invoice_id.
func (c *Client) ChargeOnMandate(ctx context.Context, params OnDemandChargeParams) (*mollie.Payment, error) {
	if params.IdempotencyKey == "" {
		return nil, fmt.Errorf("ChargeOnMandate requires a deterministic IdempotencyKey")
	}
	if c.client == nil || c.client.BaseURL == nil {
		return nil, fmt.Errorf("mollie client not initialized")
	}
	body, err := json.Marshal(map[string]any{
		"amount":      params.Amount,
		"description": params.Description,
		"webhookUrl":  params.WebhookURL,
		"metadata": map[string]any{
			"purpose":            "invoice",
			"tenant_id":          params.TenantID,
			"invoice_id":         params.InvoiceID,
			"billing_payment_id": params.PaymentID,
			"reference_id":       params.InvoiceID,
		},
		"sequenceType": mollie.RecurringSequence,
		"mandateId":    params.MandateID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal on-demand payment: %w", err)
	}
	u, err := c.client.BaseURL.Parse(fmt.Sprintf("v2/customers/%s/payments", url.PathEscape(params.CustomerID)))
	if err != nil {
		return nil, fmt.Errorf("build Mollie on-demand payment URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build Mollie on-demand payment request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", params.IdempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create on-demand payment: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read Mollie on-demand payment response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("failed to create on-demand payment: mollie status %d: %s", resp.StatusCode, string(respBody))
	}
	var payment mollie.Payment
	if err := json.Unmarshal(respBody, &payment); err != nil {
		return nil, fmt.Errorf("decode Mollie on-demand payment response: %w", err)
	}

	c.logger.WithFields(map[string]any{
		"payment_id":  payment.ID,
		"customer_id": params.CustomerID,
		"mandate_id":  params.MandateID,
		"invoice_id":  params.InvoiceID,
	}).Info("Created Mollie on-demand payment")

	return &payment, nil
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

	c.logger.WithFields(map[string]any{
		"subscription_id": subscriptionID,
		"customer_id":     customerID,
	}).Info("Cancelled Mollie subscription")

	return nil
}

// MandateInfo contains extracted mandate details for database storage
type MandateInfo struct {
	MollieMandateID  string
	MollieCustomerID string
	Status           string // valid, pending, invalid
	Method           string // directdebit, creditcard
	Details          map[string]any
	CreatedAt        time.Time
}

// ExtractMandateInfo extracts relevant fields from a Mollie mandate
func (c *Client) ExtractMandateInfo(mandate *mollie.Mandate, customerID string) MandateInfo {
	details := make(map[string]any)

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

	if metadata, ok := sub.Metadata.(map[string]any); ok {
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
