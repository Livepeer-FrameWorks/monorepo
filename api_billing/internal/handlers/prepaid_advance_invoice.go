package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/shopspring/decimal"
)

func issueFundedAdvanceInvoiceTx(ctx context.Context, tx *sql.Tx, tenant, source, reference, paymentReference string, a invoiceTaxAssessment, cash fx.Record, at time.Time, balanceBefore int64, balanceTransactionIDs ...string) (string, string, error) {
	q := purserdb.New(tx)
	if a.Advance == nil || a.Advance.NetEUR != a.Net || a.Advance.VATEUR != a.VAT || a.Advance.GrossEUR != a.Gross || cash.EURMinor != a.Gross || cash.OriginalMinor != a.Advance.GrossOriginal {
		return "", "", fmt.Errorf("advance receipt does not match frozen tax and credit")
	}
	row, err := q.InsertFundedAdvanceInvoice(ctx, purserdb.InsertFundedAdvanceInvoiceParams{TenantID: tenant, GrossEurCents: a.Gross, NetEurCents: a.Net, TaxPointAt: at, PaymentReference: paymentReference, GrossOriginalMinor: cash.OriginalMinor, Currency: cash.OriginalCurrency, UnitsPerEur: cash.UnitsText(), ReferenceDate: cash.ReferenceDate, SourceReferenceType: sql.NullString{String: source, Valid: true}, SourceReferenceID: sql.NullString{String: reference, Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		prior, readErr := q.GetFundedAdvanceInvoice(ctx, purserdb.GetFundedAdvanceInvoiceParams{TenantID: tenant, SourceReferenceType: sql.NullString{String: source, Valid: true}, SourceReferenceID: sql.NullString{String: reference, Valid: true}})
		if readErr != nil {
			return "", "", fmt.Errorf("read funded advance invoice: %w", readErr)
		}
		return prior.ID, prior.InvoiceNumber, nil
	}
	if err != nil {
		return "", "", fmt.Errorf("insert funded advance invoice: %w", err)
	}
	amount := decimal.New(a.Net, -2)
	lines := &clusterRatingResult{BaseLine: pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: "prepaid_service_advance", Description: "Prepaid service credit", Currency: "EUR", Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1), UnitPrice: amount, Amount: amount}}}
	if err = persistInvoiceLineItems(ctx, tx, row.ID, tenant, lines); err != nil {
		return "", "", fmt.Errorf("persist funded advance lines: %w", err)
	}
	a.TaxPoint = at
	if err = freezeInvoiceTaxTx(ctx, tx, tenant, row.ID, a, cash); err != nil {
		return "", "", fmt.Errorf("freeze funded advance tax: %w", err)
	}
	retained, err := json.Marshal(a)
	if err != nil {
		return "", "", fmt.Errorf("encode funded advance assessment: %w", err)
	}
	remaining, err := q.GetFundingLotRemaining(ctx, tenant)
	if err != nil {
		return "", "", fmt.Errorf("read funded advance inventory: %w", err)
	}
	if legacy := max(balanceBefore, 0) - remaining; legacy > 0 {
		if err = q.InsertFundingLot(ctx, purserdb.InsertFundingLotParams{TenantID: tenant, SourceReferenceType: "unclassified_balance", SourceReferenceID: source + ":" + reference, Classification: "legacy_unknown", NetCreditEurCents: legacy, RemainingNetEurCents: legacy, TaxAssessment: json.RawMessage(`{"tax_status":"legacy_unknown"}`)}); err != nil {
			return "", "", fmt.Errorf("classify pre-existing advance credit: %w", err)
		}
	}
	balanceTransaction := sql.NullString{}
	if len(balanceTransactionIDs) > 0 && balanceTransactionIDs[0] != "" {
		balanceTransaction = sql.NullString{String: balanceTransactionIDs[0], Valid: true}
	}
	if err = q.InsertFundingLot(ctx, purserdb.InsertFundingLotParams{TenantID: tenant, SourceReferenceType: source, SourceReferenceID: reference, SourceDocumentID: sql.NullString{String: row.ID, Valid: true}, FundingBalanceTransactionID: balanceTransaction, Classification: "taxed_advance", NetCreditEurCents: a.Net, RemainingNetEurCents: max(a.Net-max(-balanceBefore, 0), 0), VatEurCents: sql.NullInt64{Int64: a.VAT, Valid: true}, GrossEurCents: sql.NullInt64{Int64: a.Gross, Valid: true}, TaxAssessment: retained}); err != nil {
		return "", "", fmt.Errorf("insert taxed advance funding lot: %w", err)
	}
	if err = enqueueInvoiceEmailTx(ctx, tx, row.ID, tenant, a.Customer.Email, "paid"); err != nil {
		return "", "", fmt.Errorf("enqueue funded advance receipt: %w", err)
	}
	return row.ID, row.InvoiceNumber, nil
}
