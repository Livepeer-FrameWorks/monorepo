//go:build schema_verify

package handlers

import (
	"errors"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestAutomaticTopupConsentAndBudget_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
	fixture, client := installStripeFixture(t)
	tenant.svc.stripeClient = client
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE purser.tenant_subscriptions SET stripe_customer_id='cus_saved',billing_customer_type='consumer',billing_company=NULL,billing_address='{"street":"Main1","city":"Leiden","postal_code":"1234AB","country":"NL"}' WHERE tenant_id=$1`, tenant.id)
	exec(`INSERT INTO purser.pending_topups(id,tenant_id,provider,amount_cents,currency,status,expires_at,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date)VALUES($1,$2,'stripe',1000,'EUR','completed',NOW(),1000,'EUR',1000,1,'identity',CURRENT_DATE)`, uuid.NewString(), tenant.id)
	fixture.route("GET", "/v1/customers/cus_saved", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "cus_saved", "invoice_settings": map[string]any{"default_payment_method": "pm_saved"}}
	})
	tenant.jobs.automaticTopupTick(ctx)
	if len(fixture.matching("POST", "/v1/payment_intents")) != 0 {
		t.Fatal("saved card alone authorised automatic charges")
	}
	policy := AutomaticTopupPolicy{Enabled: true, ThresholdEUR: 0, NetAmountMinor: 2000, MonthlyGrossLimitMinor: 2000, Currency: "EUR", Actor: "account-owner"}
	if _, err := tenant.svc.SetAutomaticTopup(ctx, tenant.id, policy); err == nil {
		t.Fatal("automatic charges enabled without explicit consent")
	}
	policy.ConsentVersion = AutomaticTopupConsentVersion
	if _, err := tenant.svc.SetAutomaticTopup(ctx, tenant.id, policy); err == nil {
		t.Fatal("tax was excluded from the monthly cash ceiling")
	}
	policy.MonthlyGrossLimitMinor = 2420
	if _, err := tenant.svc.SetAutomaticTopup(ctx, tenant.id, policy); err != nil {
		t.Fatal(err)
	}
	if err := tenant.svc.reserveAutomaticTopup(ctx, tenant.id); err != nil {
		t.Fatal(err)
	}
	attempt := func() purserdb.GetAutomaticTopupAttemptRow {
		t.Helper()
		var id string
		if err := db.QueryRow(`SELECT id::text FROM purser.automatic_topup_attempts WHERE tenant_id=$1 AND status='reserved'`, tenant.id).Scan(&id); err != nil {
			t.Fatal(err)
		}
		row, err := purserdb.New(db).GetAutomaticTopupAttempt(ctx, purserdb.GetAutomaticTopupAttemptParams{TenantID: tenant.id, AttemptID: id})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	first := attempt()
	if err := tenant.svc.DisableAutomaticTopup(ctx, tenant.id, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := tenant.svc.processAutomaticTopup(ctx, tenant.id, first.ID); err != nil {
		t.Fatal(err)
	}
	if len(fixture.matching("POST", "/v1/payment_intents")) != 0 {
		t.Fatal("disabled an undispatched attempt but still charged")
	}
	if _, err := tenant.svc.SetAutomaticTopup(ctx, tenant.id, policy); err != nil {
		t.Fatal(err)
	}
	if err := tenant.svc.reserveAutomaticTopup(ctx, tenant.id); err != nil {
		t.Fatal(err)
	}
	second := attempt()
	state := "processing"
	received := 0
	pi := "pi_" + second.ID
	receipt := func() map[string]any {
		return map[string]any{"id": pi, "status": state, "amount": 2420, "amount_received": received, "currency": "eur", "customer": "cus_saved", "payment_method": "pm_saved", "metadata": map[string]string{"tenant_id": tenant.id, "topup_id": second.TopupID, "purpose": "automatic_prepaid", "automatic_topup_attempt_id": second.ID}}
	}
	fixture.route("POST", "/v1/payment_intents", func(r recordedProviderRequest) (int, any) {
		if r.idempotencyKey != "automatic-prepaid-topup:"+second.ID || r.form.Get("amount") != "2420" || r.form.Get("metadata[purpose]") != "automatic_prepaid" {
			t.Errorf("automatic charge lacks consent key, tax or funding purpose: %+v", r)
		}
		return 200, receipt()
	})
	fixture.route("GET", "/v1/payment_intents/"+pi, func(recordedProviderRequest) (int, any) { return 200, receipt() })
	if err := tenant.svc.processAutomaticTopup(ctx, tenant.id, second.ID); !errors.Is(err, ErrAwaitingProviderSettlement) {
		t.Fatalf("pending charge did not await settlement: %v", err)
	}
	if err := tenant.svc.settleAutomaticTopup(ctx, automaticTopupReceipt{TenantID: tenant.id, TopupID: second.TopupID, PaymentID: pi, AttemptID: second.ID, CustomerID: "cus_wrong", PaymentMethodID: "pm_saved", AmountMinor: 2420, Currency: "EUR"}); err == nil {
		t.Fatal("captured funding accepted a different payer")
	}
	if balance := admissionBalanceCents(t, db, tenant.id); balance != 0 {
		t.Fatalf("pending charge credited the wallet: %d", balance)
	}
	spend, err := purserdb.New(db).GetAutomaticTopupMonthlySpend(ctx, purserdb.GetAutomaticTopupMonthlySpendParams{TenantID: tenant.id, Currency: "EUR"})
	if err != nil || spend.AmountMinor != 2420 {
		t.Fatalf("pending cash escaped monthly ceiling: %+v %v", spend, err)
	}
	if err = tenant.svc.DisableAutomaticTopup(ctx, tenant.id, "owner"); err != nil {
		t.Fatal(err)
	}
	state = "succeeded"
	received = 2420
	if err = tenant.svc.processAutomaticTopup(ctx, tenant.id, second.ID); err != nil {
		t.Fatal(err)
	}
	if err = tenant.svc.settleAutomaticTopup(ctx, automaticTopupReceipt{TenantID: tenant.id, TopupID: second.TopupID, PaymentID: pi, AttemptID: second.ID, CustomerID: "cus_saved", PaymentMethodID: "pm_saved", AmountMinor: 2420, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	if balance := admissionBalanceCents(t, db, tenant.id); balance != 2000 {
		t.Fatalf("settled funding credited tax or replayed money: %d", balance)
	}
	if _, err = tenant.svc.SetAutomaticTopup(ctx, tenant.id, policy); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=0 WHERE tenant_id=$1 AND currency='EUR'`, tenant.id)
	if err = tenant.svc.reserveAutomaticTopup(ctx, tenant.id); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err = db.QueryRow(`SELECT count(*) FROM purser.automatic_topup_attempts WHERE tenant_id=$1`, tenant.id).Scan(&attempts); err != nil || attempts != 2 || len(fixture.matching("POST", "/v1/payment_intents")) != 1 {
		t.Fatalf("disable/re-enable reset monthly cash spending: attempts=%d err=%v", attempts, err)
	}
}
