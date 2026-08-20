package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

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

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO purser.billing_collection_balances (tenant_id, currency, balance_cents)
		VALUES ($1, $2, 0)
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, tenantID, currency); err != nil {
		return invoiceCollectionDecision{}, fmt.Errorf("initialize collection balance: %w", err)
	}

	var openingCents int64
	if err = tx.QueryRowContext(ctx, `
		SELECT balance_cents
		FROM purser.billing_collection_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&openingCents); err != nil {
		return invoiceCollectionDecision{}, fmt.Errorf("lock collection balance: %w", err)
	}

	decision := decideInvoiceCollection(openingCents, currentChargeCents, minimumCents)
	decision.Provider = provider
	decision.Currency = currency
	if _, err = tx.ExecContext(ctx, `
		UPDATE purser.billing_collection_balances
		SET balance_cents = $3, updated_at = NOW()
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency, decision.ClosingBalanceCents); err != nil {
		return invoiceCollectionDecision{}, fmt.Errorf("update collection balance: %w", err)
	}
	return decision, nil
}

func persistInvoiceCollectionDecisionTx(
	ctx context.Context,
	tx *sql.Tx,
	invoiceID, tenantID string,
	decision invoiceCollectionDecision,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO purser.billing_collection_entries (
			invoice_id, tenant_id, provider, currency, minimum_cents,
			opening_balance_cents, current_charge_cents, collected_cents,
			closing_balance_cents, outcome
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, invoiceID, tenantID, decision.Provider, decision.Currency, decision.MinimumCents,
		decision.OpeningBalanceCents, decision.CurrentChargeCents, decision.CollectedCents,
		decision.ClosingBalanceCents, decision.Outcome); err != nil {
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
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO purser.invoice_line_items (
			invoice_id, tenant_id, line_key, unit, dimensions, description,
			quantity, included_quantity, billable_quantity, unit_price, amount,
			currency, pricing_source
		) VALUES ($1, $2, $3, 'balance', $4, $5, 1, 0, 1, $6::numeric, $6::numeric, $7, 'tier')
	`, invoiceID, tenantID, lineKey, dimensions, description, amount, decision.Currency); err != nil {
		return fmt.Errorf("persist invoice collection line: %w", err)
	}
	return nil
}
