//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"frameworks/api_billing/internal/fx"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestTaxedAdvanceRefundDebitsOnlyNetAndCreditsOriginalVAT_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenant := seedPrepaidLedgerTenant(t, db)
	topup := uuid.NewString()
	now := time.Now().UTC()
	money, cash, err := quoteAdvanceMoney(10000, 2100, fx.Rate{Currency: "USD", UnitsPerEUR: decimal.RequireFromString("1.25"), Source: "ecb", ReferenceDate: fx.Date(now)})
	if err != nil {
		t.Fatal(err)
	}
	a := invoiceTaxAssessment{Advance: &money, DocumentKind: "full", Decision: vatDecision{Country: "NL", RateBPS: 2100, Treatment: "domestic_vat", Source: "Dutch VAT", CheckedOn: now.Format(time.DateOnly), EffectiveFrom: "2026-01-01"}, Customer: CryptoBillingProfile{CustomerType: "consumer", Name: "Original Buyer", Email: "buyer@example.test", Address: json.RawMessage(`{"street":"Main1","city":"Leiden","postal_code":"1234","country":"NL"}`)}, Supplier: DocumentSupplier{Name: "DDVTech B.V.", Address: "Leiden, NL", VATNumber: "NL123456789B01", RegistrationNumber: "12345678"}, Net: money.NetEUR, VAT: money.VATEUR, Gross: money.GrossEUR, TaxPoint: now}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	pi := "pi_advance_" + topup
	if _, err = db.ExecContext(ctx, `INSERT INTO purser.pending_topups(id,tenant_id,provider,amount_cents,currency,status,expires_at,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date,net_eur_cents,vat_eur_cents,gross_eur_cents,tax_assessment) VALUES($1,$2,'stripe',12100,'USD','pending',NOW()+INTERVAL '1 hour',12100,'USD',9680,1.25,'ecb',CURRENT_DATE,8000,1680,9680,$3)`, topup, tenant, raw); err != nil {
		t.Fatal(err)
	}
	service := &Service{db: db, logger: logging.NewLogger()}
	if err = service.handlePrepaidCheckoutCompleted(ctx, "cs_"+topup, pi, tenant, topup, cash.OriginalMinor, "usd", ProviderStripe, true); err != nil {
		t.Fatalf("settlement SQL: %#v", err)
	}
	if got := admissionBalanceCents(t, db, tenant); got != 8000 {
		t.Fatalf("paid VAT became spendable credit: %d", got)
	}
	for i, amount := range []int64{4033, 4033, 4034} {
		refund := providerReversalInput{provider: "stripe", reversalType: "refund", providerReversalID: fmt.Sprintf("re_%s_%d", topup, i), providerPaymentID: pi, amountCents: amount, currency: "usd", reason: "requested_by_customer"}
		if applied, err := service.applyProviderReversal(ctx, refund); err != nil || !applied {
			t.Fatalf("refund %d failed: %t %v", i, applied, err)
		}
		if applied, err := service.applyProviderReversal(ctx, refund); err != nil || applied {
			t.Fatalf("refund %d replay changed money: %t %v", i, applied, err)
		}
	}
	var balance, net, vat, gross, cashReversed, creditDebited, remaining, remainingVAT, revokedNet, revokedVAT int64
	if err = db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id=$1 AND currency='EUR'`, tenant).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT SUM(net_eur_cents),SUM(vat_eur_cents),SUM(gross_eur_cents) FROM purser.credit_notes WHERE tenant_id=$1 AND reversal_reference_type='payment_reversal'`, tenant).Scan(&net, &vat, &gross); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT SUM(eur_amount_cents) FROM purser.payment_reversals WHERE tenant_id=$1 AND pending_topup_id=$2`, tenant, topup).Scan(&cashReversed); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT -SUM(amount_cents) FROM purser.balance_transactions WHERE tenant_id=$1 AND reference_type='payment_reversal'`, tenant).Scan(&creditDebited); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT remaining_net_eur_cents,remaining_vat_eur_cents,revoked_net_eur_cents,revoked_vat_eur_cents FROM purser.prepaid_funding_lots WHERE tenant_id=$1 AND source_reference_type='prepaid_topup' AND source_reference_id=$2`, tenant, topup).Scan(&remaining, &remainingVAT, &revokedNet, &revokedVAT); err != nil {
		t.Fatal(err)
	}
	if balance != 0 || net != 8000 || vat != 1680 || gross != 9680 || cashReversed != 9680 || creditDebited != 8000 || remaining != 0 || remainingVAT != 0 || revokedNet != 8000 || revokedVAT != 1680 {
		t.Fatalf("full refund: balance=%d note=%d/%d/%d cash=%d netdebit=%d lotremaining=%d/%d revoked=%d/%d", balance, net, vat, gross, cashReversed, creditDebited, remaining, remainingVAT, revokedNet, revokedVAT)
	}
}
