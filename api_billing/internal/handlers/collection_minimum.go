package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/shopspring/decimal"
)

type invoiceCollectionDecision struct {
	Provider            string
	Currency            string
	MinimumCents        int64
	OpeningBalanceCents int64
	CurrentChargeCents  int64
	CollectedCents      int64
	ClosingBalanceCents int64
	Outcome             string
}

func resolveInvoiceCollectionProvider(paymentMethod string, hasStripeSubscription, hasMollieSubscription bool) (string, error) {
	switch paymentMethod {
	case "stripe":
		if !hasStripeSubscription {
			return "", fmt.Errorf("subscription selects Stripe without a Stripe subscription id")
		}
		return "stripe", nil
	case "mollie":
		if !hasMollieSubscription {
			return "", fmt.Errorf("subscription selects Mollie without a Mollie subscription id")
		}
		return "mollie", nil
	}

	switch {
	case hasStripeSubscription && !hasMollieSubscription:
		return "stripe", nil
	case hasMollieSubscription && !hasStripeSubscription:
		return "mollie", nil
	case hasStripeSubscription && hasMollieSubscription:
		return "", fmt.Errorf("subscription has both Stripe and Mollie ids but no selected provider")
	default:
		return "", nil
	}
}

func decideInvoiceCollection(openingCents, currentCents, minimumCents int64) invoiceCollectionDecision {
	decision := invoiceCollectionDecision{
		MinimumCents:        minimumCents,
		OpeningBalanceCents: openingCents,
		CurrentChargeCents:  currentCents,
		Outcome:             "none",
	}
	combined := openingCents + currentCents
	switch {
	case combined == 0:
		return decision
	case combined < minimumCents:
		decision.ClosingBalanceCents = combined
		decision.Outcome = "deferred"
	default:
		decision.CollectedCents = combined
		decision.Outcome = "collected"
	}
	return decision
}

// applyInvoiceCollectionMinimumTx locks the tenant/currency carry balance and
// moves the current rounded invoice charge into either the closing carry or the
// amount to collect. The caller persists the invoice and audit entry in this
// same transaction.
func applyInvoiceCollectionMinimumTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, provider, currency string,
	currentChargeCents int64,
) (invoiceCollectionDecision, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	minimumCents, err := billing.InvoiceCollectionMinimumCents(provider, currency)
	if err != nil {
		return invoiceCollectionDecision{}, err
	}
	if currentChargeCents < 0 {
		return invoiceCollectionDecision{}, fmt.Errorf("current collection charge cannot be negative")
	}

	queries := purserdb.New(tx)
	if err = queries.EnsureBillingCollectionBalance(ctx, purserdb.EnsureBillingCollectionBalanceParams{
		TenantID: tenantID, Currency: currency,
	}); err != nil {
		return invoiceCollectionDecision{}, fmt.Errorf("initialize collection balance: %w", err)
	}

	openingCents, err := queries.LockBillingCollectionBalance(ctx, purserdb.LockBillingCollectionBalanceParams{
		TenantID: tenantID, Currency: currency,
	})
	if err != nil {
		return invoiceCollectionDecision{}, fmt.Errorf("lock collection balance: %w", err)
	}

	decision := decideInvoiceCollection(openingCents, currentChargeCents, minimumCents)
	decision.Provider = provider
	decision.Currency = currency
	affected, err := queries.UpdateBillingCollectionBalance(ctx, purserdb.UpdateBillingCollectionBalanceParams{
		BalanceCents: decision.ClosingBalanceCents, TenantID: tenantID, Currency: currency,
	})
	if err != nil {
		return invoiceCollectionDecision{}, fmt.Errorf("update collection balance: %w", err)
	}
	if affected != 1 {
		return invoiceCollectionDecision{}, errors.New("update collection balance: locked row disappeared")
	}
	return decision, nil
}

func persistInvoiceCollectionDecisionTx(
	ctx context.Context,
	tx *sql.Tx,
	invoiceID, tenantID string,
	decision invoiceCollectionDecision,
) error {
	queries := purserdb.New(tx)
	if err := queries.InsertBillingCollectionDecision(ctx, purserdb.InsertBillingCollectionDecisionParams{
		InvoiceID: invoiceID, TenantID: tenantID, Provider: decision.Provider,
		Currency: decision.Currency, MinimumCents: decision.MinimumCents,
		OpeningBalanceCents: decision.OpeningBalanceCents, CurrentChargeCents: decision.CurrentChargeCents,
		CollectedCents: decision.CollectedCents, ClosingBalanceCents: decision.ClosingBalanceCents,
		Outcome: decision.Outcome,
	}); err != nil {
		return fmt.Errorf("persist invoice collection decision: %w", err)
	}

	var lineKey, description string
	var amountCents int64
	switch {
	case decision.Outcome == "deferred" && decision.CurrentChargeCents > 0:
		lineKey = "collection_balance_deferred"
		description = "Small balance carried forward (no payment due)"
		amountCents = -decision.CurrentChargeCents
	case decision.Outcome == "collected" && decision.OpeningBalanceCents > 0:
		lineKey = "collection_balance_opening"
		description = "Balance carried forward from earlier periods"
		amountCents = decision.OpeningBalanceCents
	default:
		return nil
	}

	dimensions, err := json.Marshal(map[string]int64{
		"minimum_cents":         decision.MinimumCents,
		"opening_balance_cents": decision.OpeningBalanceCents,
		"closing_balance_cents": decision.ClosingBalanceCents,
	})
	if err != nil {
		return fmt.Errorf("marshal collection line dimensions: %w", err)
	}
	amount := decimal.NewFromInt(amountCents).Div(decimal.NewFromInt(100)).StringFixed(2)
	if err = queries.InsertBillingCollectionLineItem(ctx, purserdb.InsertBillingCollectionLineItemParams{
		InvoiceID: invoiceID, TenantID: tenantID, LineKey: lineKey,
		Dimensions: dimensions, Description: description, Amount: amount, Currency: decision.Currency,
	}); err != nil {
		return fmt.Errorf("persist invoice collection line: %w", err)
	}
	return nil
}
