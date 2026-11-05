//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/fx"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestPrepaidAdvancesAreNotTaxedTwice_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL123456789B01", "SUPPLIER_REGISTRATION_NUMBER": "12345678", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	for _, legacy := range []bool{false, true} {
		name := "taxed"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			tenant, tier := uuid.NewString(), uuid.NewString()
			for _, stmt := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES($1,$2,'Advance test')`, []any{tier, "advance-" + tier}},
				{`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_customer_type,billing_name,billing_email,billing_address) VALUES($1,$2,'consumer','Original Buyer','buyer@example.test','{"street":"Main 1","city":"Leiden","postal_code":"1000","country":"NL"}')`, []any{tenant, tier}},
				{`INSERT INTO purser.prepaid_balances(tenant_id,balance_cents,currency) VALUES($1,3000,'EUR')`, []any{tenant}},
			} {
				if _, err := db.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
					t.Fatal(err)
				}
			}
			if !legacy {
				net, err := fx.RecordFromEUR(3000, fx.Rate{Currency: "EUR", UnitsPerEUR: decimal.NewFromInt(1), ReferenceDate: time.Now(), Source: "identity"})
				if err != nil {
					t.Fatal(err)
				}
				_, cash, raw, err := QuoteFiatPrepaidAdvance(ctx, db, tenant, net)
				if err != nil {
					t.Fatal(err)
				}
				var a invoiceTaxAssessment
				if err = json.Unmarshal(raw, &a); err != nil {
					t.Fatal(err)
				}
				err = database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
					_, _, err := issueFundedAdvanceInvoiceTx(ctx, tx, tenant, "test_advance", uuid.NewString(), "test-payment", a, cash, time.Now(), 0)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now().UTC().Add(-time.Hour)
			end := start.Add(time.Hour)
			amount := decimal.NewFromInt(100)
			run := &postpaidInvoiceRun{tenantID: tenant, currency: "EUR", presentmentCurrency: "EUR", periodStart: start, periodEnd: end, invoiceID: uuid.NewString(), billingEmail: "buyer@example.test", usageDetails: map[string]any{}, phase: usagePhase{start: start, end: end}, rating: &clusterRatingResult{TotalAmount: amount, BaseAmount: amount, BaseLine: pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: "base_subscription", Description: "Support", Currency: "EUR", Amount: amount, UnitPrice: amount, Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1)}}}}
			var result postpaidInvoiceResult
			err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error { var err error; result, err = writePostpaidInvoiceTx(ctx, tx, run); return err })
			if err != nil {
				t.Fatal(err)
			}
			var net, vat, gross, collected, credit, absorbed, balance int64
			err = db.QueryRowContext(ctx, `SELECT net_eur_cents,vat_eur_cents,gross_eur_cents,ROUND(amount*100)::bigint,prepaid_financial_credit_eur_cents,absorbed_vat_eur_cents FROM purser.billing_invoices WHERE tenant_id=$1 AND id=$2`, tenant, result.invoiceID).Scan(&net, &vat, &gross, &collected, &credit, &absorbed)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id=$1`, tenant).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			if collected != 8470 || balance != 0 {
				t.Fatalf("cash due %d balance %d; want8470/0", collected, balance)
			}
			if legacy {
				if net != 10000 || vat != 2100 || gross != 12100 || credit != 3000 || absorbed != 630 {
					t.Fatalf("legacy facts %d/%d/%d credit%d absorbed%d", net, vat, gross, credit, absorbed)
				}
			} else {
				if net != 7000 || vat != 1470 || gross != 8470 || credit != 0 || absorbed != 0 {
					t.Fatalf("advance taxed twice: %d/%d/%d credit%d absorbed%d", net, vat, gross, credit, absorbed)
				}
			}
			var ln, lv int64
			if err = db.QueryRowContext(ctx, `SELECT SUM(net_eur_cents),SUM(vat_eur_cents) FROM purser.invoice_line_items WHERE tenant_id=$1 AND invoice_id=$2`, tenant, result.invoiceID).Scan(&ln, &lv); err != nil || ln != net || lv != vat {
				t.Fatalf("line/header mismatch %d/%d err%v", ln, lv, err)
			}
		})
	}
}
