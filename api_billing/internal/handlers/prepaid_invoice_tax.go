package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/shopspring/decimal"
)

type advanceTaxApplication struct {
	LineKey       string `json:"line_key"`
	SourceInvoice string `json:"source_invoice"`
	Net           int64  `json:"net_eur_cents"`
	VAT           int64  `json:"vat_eur_cents"`
	Rate          int    `json:"rate_bps"`
	Treatment     string `json:"treatment"`
}

// The wallet stores net service credit. A final invoice deducts already taxed
// advances with their original VAT; unclassified old credit is a financial
// payment, with any missing historic VAT absorbed by the supplier.
func applyInvoiceAdvanceTaxTx(ctx context.Context, tx *sql.Tx, tenant string, start time.Time, applied int64, a *invoiceTaxAssessment) ([]pricedLine, int64, int64, error) {
	if applied == 0 {
		return nil, 0, 0, nil
	}
	key, err := documentInvoiceCreditKey(ctx, purserdb.New(tx), tenant, start)
	if err != nil {
		return nil, 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT l.id::text,COALESCE(l.source_document_id::text,''),
 SUM(f.net_eur_cents-f.returned_net_eur_cents)::bigint,
 SUM(f.vat_eur_cents-f.returned_vat_eur_cents)::bigint,
 COALESCE((l.tax_assessment->'decision'->>'RateBPS')::integer,0),
 COALESCE(l.tax_assessment->'decision'->>'treatment','')
 FROM purser.prepaid_funding_allocations f
 JOIN purser.prepaid_funding_lots l ON l.tenant_id=f.tenant_id AND l.id=f.lot_id
 JOIN purser.balance_transactions b ON b.tenant_id=f.tenant_id AND b.id::text=f.reference_id
 WHERE f.tenant_id=$1::uuid AND f.reference_type='balance_transaction'
 AND b.reference_type='invoice_credit' AND b.description=$2 AND l.classification='taxed_advance'
 GROUP BY l.id ORDER BY l.created_at,l.id`, tenant, key.applied)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var lines []pricedLine
	var taxedNet, taxedVAT int64
	for rows.Next() {
		var lot string
		var c advanceTaxApplication
		if err = rows.Scan(&lot, &c.SourceInvoice, &c.Net, &c.VAT, &c.Rate, &c.Treatment); err != nil {
			return nil, 0, 0, err
		}
		if c.Net <= 0 {
			continue
		}
		c.LineKey = "advance_applied:" + lot
		taxedNet += c.Net
		taxedVAT += c.VAT
		a.Advances = append(a.Advances, c)
		amount := decimal.New(-c.Net, -2)
		lines = append(lines, pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: c.LineKey, Description: "Previously invoiced service advance (" + c.SourceInvoice + ")", Currency: "EUR", Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1), UnitPrice: amount, Amount: amount}})
	}
	if err = rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	if taxedNet > applied || taxedVAT > a.VAT {
		return nil, 0, 0, fmt.Errorf("prepaid advance tax exceeds final supply; reconcile the original advance before finalization")
	}
	legacy := applied - taxedNet
	absorbed, _ := exclusiveTax(legacy, a.Decision.RateBPS)
	a.Net -= taxedNet
	a.VAT -= taxedVAT
	a.Gross = a.Net + a.VAT
	return lines, legacy, absorbed, nil
}
