package stripe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/balancetransaction"
	checkoutsession "github.com/stripe/stripe-go/v85/checkout/session"
	"github.com/stripe/stripe-go/v85/customer"
	"github.com/stripe/stripe-go/v85/paymentintent"
	"github.com/stripe/stripe-go/v85/setupintent"
)

// SetupCheckoutParams describes a Checkout Session that saves a card for
// off-session charges without creating a subscription.
type SetupCheckoutParams struct {
	CustomerID     string
	TenantID       string
	TierID         string
	Currency       string
	SuccessURL     string
	CancelURL      string
	IdempotencyKey string
}

// PurposeSubscriptionSetup marks a setup-mode Checkout Session that activates a
// tier whose base fee Purser charges off-session.
const PurposeSubscriptionSetup = "subscription_setup"

// CreateSetupCheckoutSession creates a setup-mode Checkout Session in the
// tenant's presentment currency. Card is the only method: the saved method is
// charged off-session in USD or GBP.
func (c *Client) CreateSetupCheckoutSession(ctx context.Context, params SetupCheckoutParams) (*stripe.CheckoutSession, error) {
	if params.IdempotencyKey == "" {
		return nil, fmt.Errorf("CreateSetupCheckoutSession requires a deterministic IdempotencyKey")
	}
	if params.CustomerID == "" || params.Currency == "" {
		return nil, fmt.Errorf("CreateSetupCheckoutSession requires a customer and currency")
	}
	metadata := map[string]string{
		"tenant_id":    params.TenantID,
		"tier_id":      params.TierID,
		"reference_id": params.TierID,
		"purpose":      PurposeSubscriptionSetup,
	}
	sessionParams := &stripe.CheckoutSessionParams{
		Customer:           stripe.String(params.CustomerID),
		Mode:               stripe.String(string(stripe.CheckoutSessionModeSetup)),
		Currency:           stripe.String(strings.ToLower(params.Currency)),
		SuccessURL:         stripe.String(params.SuccessURL),
		CancelURL:          stripe.String(params.CancelURL),
		Metadata:           metadata,
		PaymentMethodTypes: []*string{stripe.String("card")},
		SetupIntentData:    &stripe.CheckoutSessionSetupIntentDataParams{Metadata: metadata},
	}
	sessionParams.Context = ctx
	sessionParams.SetIdempotencyKey(params.IdempotencyKey)
	sess, err := checkoutsession.New(sessionParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create setup checkout session: %w", err)
	}
	return sess, nil
}

// SetDefaultPaymentMethodFromSetupIntent reads the payment method a completed
// SetupIntent saved and makes it the customer's invoice default, which
// ResolveDefaultPaymentMethod returns for off-session collection.
func (c *Client) SetDefaultPaymentMethodFromSetupIntent(ctx context.Context, customerID, setupIntentID string) (string, error) {
	if customerID == "" || setupIntentID == "" {
		return "", fmt.Errorf("stripe customer and setup intent ids are required")
	}
	getParams := &stripe.SetupIntentParams{}
	getParams.Context = ctx
	intent, err := setupintent.Get(setupIntentID, getParams)
	if err != nil {
		return "", fmt.Errorf("get Stripe setup intent: %w", err)
	}
	if intent.Status != stripe.SetupIntentStatusSucceeded || intent.PaymentMethod == nil || intent.PaymentMethod.ID == "" {
		return "", fmt.Errorf("setup intent %s has not saved a payment method (status %s)", setupIntentID, intent.Status)
	}
	updateParams := &stripe.CustomerParams{
		InvoiceSettings: &stripe.CustomerInvoiceSettingsParams{
			DefaultPaymentMethod: stripe.String(intent.PaymentMethod.ID),
		},
	}
	updateParams.Context = ctx
	updateParams.SetIdempotencyKey("stripe-default-payment-method:" + setupIntentID)
	if _, err := customer.Update(customerID, updateParams); err != nil {
		return "", fmt.Errorf("set Stripe customer default payment method: %w", err)
	}
	return intent.PaymentMethod.ID, nil
}

// BalanceTransaction is what Stripe settled into the platform balance for a
// charge: amount, fee, and net in the settlement currency, and the rate that
// converted the charge currency into it.
type BalanceTransaction struct {
	ID           string
	Amount       int64
	Fee          int64
	Net          int64
	Currency     string
	ExchangeRate *decimal.Decimal
	Created      time.Time
}

func balanceTransactionFrom(bt *stripe.BalanceTransaction) *BalanceTransaction {
	out := &BalanceTransaction{
		ID:       bt.ID,
		Amount:   bt.Amount,
		Fee:      bt.Fee,
		Net:      bt.Net,
		Currency: strings.ToUpper(string(bt.Currency)),
		Created:  time.Unix(bt.Created, 0).UTC(),
	}
	if bt.ExchangeRate > 0 {
		rate := decimal.NewFromFloat(bt.ExchangeRate)
		out.ExchangeRate = &rate
	}
	return out
}

// GetBalanceTransaction retrieves one balance transaction.
func (c *Client) GetBalanceTransaction(ctx context.Context, id string) (*BalanceTransaction, error) {
	params := &stripe.BalanceTransactionParams{}
	params.Context = ctx
	bt, err := balancetransaction.Get(id, params)
	if err != nil {
		return nil, fmt.Errorf("get Stripe balance transaction %s: %w", id, err)
	}
	return balanceTransactionFrom(bt), nil
}

// PaymentIntentBalanceTransaction returns the balance transaction of a
// PaymentIntent's latest charge, or nil while Stripe has not created it yet.
func (c *Client) PaymentIntentBalanceTransaction(ctx context.Context, paymentIntentID string) (*BalanceTransaction, error) {
	params := &stripe.PaymentIntentParams{}
	params.Context = ctx
	params.AddExpand("latest_charge.balance_transaction")
	pi, err := paymentintent.Get(paymentIntentID, params)
	if err != nil {
		return nil, fmt.Errorf("get Stripe payment intent %s: %w", paymentIntentID, err)
	}
	if pi.LatestCharge == nil || pi.LatestCharge.BalanceTransaction == nil || pi.LatestCharge.BalanceTransaction.ID == "" {
		return nil, nil
	}
	if pi.LatestCharge.BalanceTransaction.Amount == 0 && pi.LatestCharge.BalanceTransaction.Currency == "" {
		return c.GetBalanceTransaction(ctx, pi.LatestCharge.BalanceTransaction.ID)
	}
	return balanceTransactionFrom(pi.LatestCharge.BalanceTransaction), nil
}
