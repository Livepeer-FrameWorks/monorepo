package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/shopspring/decimal"
)

// createUpgradeInvoiceTx issues the new paid month and its unused-fee credit
// in the transaction that changes local pricing. Stripe's fixed subscription
// issues its own replacement invoice; advance collection and fixed Mollie
// subscriptions use Purser's invoice and existing off-session payment ledger.
func createUpgradeInvoiceTx(ctx context.Context, tx *sql.Tx, row purserdb.GetTierUpgradeRow, receipt billing.TierUpgradeReceipt) error {
	q := purserdb.New(tx)
	previous, err := planPreviousBaseFee(ctx, q, row.TenantID, receipt.PeriodStart)
	if err != nil {
		return err
	}
	credit := previous.credit
	if !previous.found && row.Provider == "mollie" {
		credit = baseFeeCredit{cents: receipt.UnusedPaidCents, providerPaymentID: receipt.PaidReceiptID, periodStart: receipt.PaidPeriodStart, periodEnd: receipt.PaidPeriodEnd}
	}
	tier, err := billingpkg.LoadEffectiveTierTx(ctx, tx, row.TenantID)
	if err != nil {
		return err
	}
	profile, err := q.GetTenantCollectionProfile(ctx, row.TenantID)
	if err != nil {
		return err
	}
	// Unused service credit only offsets this replacement purchase. It never
	// becomes prepaid funds, including when a provider receipt exceeds the fee.
	credit = cappedReplacementCredit(credit, row.BaseCents)
	baseEUR := decimal.New(row.BaseCents, -2)
	collectCents := max(row.BaseCents-credit.cents, 0)
	now := time.Now().UTC()
	quote, err := fx.QuoteFromEUR(ctx, tx, profile.PresentmentCurrency, collectCents, now)
	if err != nil {
		return err
	}
	details, err := baseFeeInvoiceDetails(tier, baseEUR, receipt.PeriodStart, receipt.PeriodEnd, credit)
	if err != nil {
		return err
	}
	var audit map[string]any
	if err = json.Unmarshal(details, &audit); err != nil {
		return err
	}
	audit["tier_upgrade_id"], audit["requested_by"] = row.ID, row.ActorID
	audit["previous_list_base_fee_eur"] = decimal.New(row.OldBaseCents, -2).StringFixed(2)
	details, err = json.Marshal(audit)
	if err != nil {
		return err
	}
	status := finalizedInvoiceStatus(decimal.New(collectCents, -2))
	due := now.AddDate(0, 0, baseFeeDueDays)
	invoiceID, err := q.InsertBaseFeeInvoice(ctx, purserdb.InsertBaseFeeInvoiceParams{
		TenantID: row.TenantID, Status: status, Amount: decimal.New(collectCents, -2).StringFixed(2), BaseAmount: baseEUR.StringFixed(2),
		DueDate: due, UsageDetails: details, PeriodStart: receipt.PeriodStart, PeriodEnd: receipt.PeriodEnd,
		PresentmentAmountCents: quote.OriginalMinor, PresentmentCurrency: quote.OriginalCurrency,
		PresentmentUnitsPerEur: quote.UnitsText(), PresentmentReferenceDate: quote.ReferenceDate, FinalizedAt: now, SupplierSnapshot: DocumentSupplierSnapshot(),
	})
	if err != nil {
		return err
	}
	line := func(key, description string, amount decimal.Decimal) pricedLine {
		return pricedLine{LineItem: rating.LineItem{LineKey: key, Description: description, Quantity: decimal.NewFromInt(1), IncludedQuantity: decimal.Zero,
			BillableQuantity: decimal.NewFromInt(1), UnitPrice: amount, Amount: amount, Currency: billing.LedgerCurrency}, PricingSource: pricing.SourceTier}
	}
	lines := &clusterRatingResult{BaseLine: line(rating.LineKeyBaseSubscription, fmt.Sprintf("Base subscription %s to %s", receipt.PeriodStart.Format(time.DateOnly), receipt.PeriodEnd.Format(time.DateOnly)), baseEUR)}
	if credit.cents > 0 {
		lines.UsageLines = append(lines.UsageLines, line(unusedBaseFeeCreditLineKey, "Credit for the unused paid share of the previous base subscription", decimal.New(-credit.cents, -2)))
	}
	if err = persistInvoiceLineItems(ctx, tx, invoiceID, row.TenantID, lines); err != nil {
		return err
	}
	if previous.reduction != nil {
		if err = reducePreviousBaseFeeInvoiceTx(ctx, tx, row.TenantID, *previous.reduction, now); err != nil {
			return err
		}
	}
	if err = consumePreviousBaseFee(ctx, q, row.TenantID, previous, credit, invoiceID, receipt.PeriodStart); err != nil {
		return err
	}

	email, err := q.GetTenantBillingEmail(ctx, row.TenantID)
	if err != nil {
		return err
	}
	if err = enqueueInvoiceEmailTx(ctx, tx, invoiceID, row.TenantID, email.String, status); err != nil {
		return err
	}
	return enqueueInvoiceCreatedTx(ctx, tx, row.TenantID, invoiceID, status, collectCents, receipt.PeriodStart, receipt.PeriodEnd, due)
}

func (jm *JobManager) chargeTierUpgradeInvoice(ctx context.Context, tenantID, provider string, periodStart time.Time) error {
	invoice, err := purserdb.New(jm.db).GetTierUpgradeInvoice(ctx, purserdb.GetTierUpgradeInvoiceParams{TenantID: tenantID, PeriodStart: periodStart})
	if err != nil {
		return err
	}
	if invoice.Status == "paid" || invoice.CollectionStarted || invoice.PresentmentAmountCents.Int64 <= 0 {
		return nil
	}
	if err = jm.chargeInvoice(ctx, provider, tenantID, invoice.ID, invoice.PresentmentAmountCents.Int64, invoice.Currency); err != nil {
		// The payment-attempt ledger owns provider retries once a charge was
		// durably recorded. An error before that point leaves inbox work pending.
		current, readErr := purserdb.New(jm.db).GetTierUpgradeInvoice(ctx, purserdb.GetTierUpgradeInvoiceParams{TenantID: tenantID, PeriodStart: periodStart})
		if readErr != nil || !current.CollectionStarted {
			return err
		}
		jm.logger.WithError(err).WithField("invoice_id", invoice.ID).Warn("Upgrade invoice collection recorded; payment retry or customer action is pending")
	}
	current, err := purserdb.New(jm.db).GetTierUpgradeInvoice(ctx, purserdb.GetTierUpgradeInvoiceParams{TenantID: tenantID, PeriodStart: periodStart})
	if err != nil {
		return err
	}
	if current.Status != "paid" && !current.CollectionStarted {
		return fmt.Errorf("upgrade invoice %s has no collection attempt; restore the saved payment method and retry recovery", invoice.ID)
	}
	return nil
}
