//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestRegularInvoicesFreezeExclusiveVAT_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Amsterdam, NL", "SUPPLIER_VAT_NUMBER": "NL123456789B01", "SUPPLIER_REGISTRATION_NUMBER": "12345678", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	for _, tc := range []struct {
		country, kind, vat string
		wantVAT            int64
		carry              int64
	}{
		{"NL", "consumer", "", 2100, 0}, {"NL", "business", "", 2100, 25}, {"DE", "business", "DE123456789", 0, 0}, {"CA", "business", "", 0, 0}, {"DE", "consumer", "", 2100, 0},
	} {
		t.Run(tc.country+tc.kind, func(t *testing.T) {
			tenant, tier := uuid.NewString(), uuid.NewString()
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES($1,$2,'Invoice tax test')`, tier, "tax-"+tier); err != nil {
				t.Fatal(err)
			}
			address := fmt.Sprintf(`{"street":"Main 1","city":"City","postal_code":"1000","country":%q}`, tc.country)
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_customer_type,billing_name,billing_email,billing_address,tax_id) VALUES($1,$2,$3,'Original Buyer','buyer@example.test',$4::jsonb,NULLIF($5,''))`, tenant, tier, tc.kind, address, tc.vat); err != nil {
				t.Fatal(err)
			}
			if tc.vat != "" {
				hash := sha256.Sum256([]byte(tc.vat))
				if _, err := db.ExecContext(ctx, `INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,expires_at,raw_response) VALUES($1,$2,$3,'DE*****6789',true,CURRENT_DATE,NOW()+INTERVAL '1 day','{}')`, tenant, tc.country, hex.EncodeToString(hash[:])); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now().UTC().Add(-time.Hour)
			end := start.Add(time.Hour)
			amount := decimal.NewFromInt(100)
			run := &postpaidInvoiceRun{tenantID: tenant, currency: "EUR", presentmentCurrency: "EUR", periodStart: start, periodEnd: end, invoiceID: uuid.NewString(), billingEmail: "buyer@example.test", usageDetails: map[string]any{}, phase: usagePhase{start: start, end: end}, rating: &clusterRatingResult{TotalAmount: amount, BaseAmount: amount, BaseLine: pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: "base_subscription", Description: "Support subscription", Currency: "EUR", Amount: amount, UnitPrice: amount, Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1)}}}}
			var result postpaidInvoiceResult
			if tc.carry != 0 {
				run.collectionProvider = "stripe"
				if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_collection_balances(tenant_id,currency,balance_cents) VALUES($1,'EUR',$2)`, tenant, tc.carry); err != nil {
					t.Fatal(err)
				}
			}
			err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error { var err error; result, err = writePostpaidInvoiceTx(ctx, tx, run); return err })
			if err != nil {
				t.Fatal(err)
			}
			var net, vat, gross, collected int64
			var status, wording, buyer string
			if err = db.QueryRowContext(ctx, `SELECT net_eur_cents,vat_eur_cents,gross_eur_cents,ROUND(amount*100)::bigint,tax_status,COALESCE(tax_decision->'decision'->>'wording',''),customer_snapshot->>'name' FROM purser.billing_invoices WHERE tenant_id=$1 AND id=$2`, tenant, result.invoiceID).Scan(&net, &vat, &gross, &collected, &status, &wording, &buyer); err != nil {
				t.Fatal(err)
			}
			if net != 10000 || vat != tc.wantVAT || gross != 10000+tc.wantVAT || collected != gross+tc.carry || status != "ready" || buyer != "Original Buyer" {
				t.Fatalf("frozen invoice: net=%d VAT=%d gross=%d collection=%d status=%s buyer=%s", net, vat, gross, collected, status, buyer)
			}
			var lineVAT int64
			if err = db.QueryRowContext(ctx, `SELECT SUM(vat_eur_cents) FROM purser.invoice_line_items WHERE tenant_id=$1 AND invoice_id=$2`, tenant, result.invoiceID).Scan(&lineVAT); err != nil || lineVAT != vat {
				t.Fatalf("line VAT %d != header %d: %v", lineVAT, vat, err)
			}
			if tc.vat != "" && !strings.Contains(wording, "btw verlegd") {
				t.Fatalf("missing reverse-charge wording: %q", wording)
			}
			if _, err = db.ExecContext(ctx, `UPDATE purser.billing_invoices SET vat_eur_cents=vat_eur_cents+1,gross_eur_cents=gross_eur_cents+1 WHERE tenant_id=$1 AND id=$2`, tenant, result.invoiceID); err == nil {
				t.Fatal("issued tax facts were mutable")
			}
			if _, err = db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_name='Changed Buyer',billing_address='{"country":"GB"}' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			var creditedGross, creditedVAT int64
			for part := 0; part < 3; part++ {
				amount := gross / 3
				if part == 2 {
					amount = gross - creditedGross
				}
				credit := uuid.NewString()
				_, err = db.ExecContext(ctx, `INSERT INTO purser.credit_notes(credit_note_number,tenant_id,source_document_type,source_document_id,reversal_reference_type,reversal_reference_id,amount_cents,currency,reason) VALUES($1,$2,'invoice',$3,'test_refund',$4,$5,'EUR','Partial refund') ON CONFLICT(source_document_type,source_document_id,reversal_reference_type,reversal_reference_id) DO NOTHING`, "CN-"+credit, tenant, result.invoiceID, credit, amount)
				if err != nil {
					t.Fatal(err)
				}
				var noteVAT int64
				var noteBuyer string
				if err = db.QueryRowContext(ctx, `SELECT vat_eur_cents,customer_snapshot->>'name' FROM purser.credit_notes WHERE tenant_id=$1 AND reversal_reference_id=$2`, tenant, credit).Scan(&noteVAT, &noteBuyer); err != nil {
					t.Fatal(err)
				}
				if noteBuyer != "Original Buyer" {
					t.Fatalf("credit used the changed buyer: %s", noteBuyer)
				}
				if _, err = db.ExecContext(ctx, `UPDATE purser.credit_notes SET vat_eur_cents=vat_eur_cents+1 WHERE tenant_id=$1 AND reversal_reference_id=$2`, tenant, credit); err == nil {
					t.Fatal("issued credit tax was mutable")
				}
				creditedGross += amount
				creditedVAT += noteVAT
				if _, err = db.ExecContext(ctx, `INSERT INTO purser.credit_notes(credit_note_number,tenant_id,source_document_type,source_document_id,reversal_reference_type,reversal_reference_id,amount_cents,currency,reason) VALUES($1,$2,'invoice',$3,'test_refund',$4,$5,'EUR','Duplicate delivery') ON CONFLICT(source_document_type,source_document_id,reversal_reference_type,reversal_reference_id) DO NOTHING`, "CN-repeat-"+credit, tenant, result.invoiceID, credit, amount); err != nil {
					t.Fatalf("credit replay was not idempotent: %v", err)
				}
			}
			if creditedGross != gross || creditedVAT != vat {
				t.Fatalf("partial notes lost original tax: gross=%d/%d VAT=%d/%d", creditedGross, gross, creditedVAT, vat)
			}
			if _, err = db.ExecContext(ctx, `INSERT INTO purser.credit_notes(credit_note_number,tenant_id,source_document_type,source_document_id,reversal_reference_type,reversal_reference_id,amount_cents,currency,reason) VALUES($1,$2,'invoice',$3,'test_refund',$4,1,'EUR','Over refund')`, "CN-"+uuid.NewString(), tenant, result.invoiceID, uuid.NewString()); err == nil {
				t.Fatal("cumulative credits exceeded source gross")
			}
		})
	}
}
