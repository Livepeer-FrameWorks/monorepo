//go:build schema_verify

package handlers

import (
	"errors"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestCompletedInvoiceCashRefund_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
	fixture, client := installStripeFixture(t)
	tenant.svc.stripeClient = client
	invoice, payment, request := uuid.NewString(), uuid.NewString(), uuid.NewString()
	pi := "pi_" + payment
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO purser.billing_invoices(id,tenant_id,status,currency,amount,base_amount,due_date,paid_at,presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,finalized_at,net_eur_cents,vat_eur_cents,gross_eur_cents,document_net_minor,document_vat_minor,document_gross_minor,tax_status,tax_policy_version,tax_summary,customer_snapshot,supplier_snapshot,tax_point_at,tax_decision)VALUES($1,$2,'paid','EUR',121,100,NOW(),NOW(),12100,'EUR',1,CURRENT_DATE,NOW(),10000,2100,12100,10000,2100,12100,'ready','test-v1','[]','{"name":"Original Buyer"}','{"name":"Original Seller"}',NOW(),'{}')`, invoice, tenant.id)
	exec(`INSERT INTO purser.billing_payments(id,invoice_id,method,amount,currency,tx_id,status,confirmed_at,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date)VALUES($1,$2,'card',121,'EUR',$3,'confirmed',NOW(),12100,'EUR',12100,1,'identity',CURRENT_DATE)`, payment, invoice, pi)
	bankState := "pending"
	fixture.route("GET", "/v1/payment_intents/"+pi, func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": pi, "status": "succeeded", "amount": 12100, "amount_received": 12100, "currency": "eur", "metadata": map[string]string{"tenant_id": tenant.id, "invoice_id": invoice}, "latest_charge": map[string]any{"id": "ch_original", "amount": 12100, "amount_refunded": 0}}
	})
	fixture.route("GET", "/v1/refunds", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"object": "list", "data": []any{}}
	})
	refund := func() map[string]any {
		return map[string]any{"id": "re_original", "status": bankState, "amount": 12100, "currency": "eur", "payment_intent": pi, "metadata": map[string]string{"frameworks_operator_refund": request}}
	}
	fixture.route("POST", "/v1/refunds", func(r recordedProviderRequest) (int, any) {
		if r.idempotencyKey != "operator-invoice-refund:"+request || r.form.Get("amount") != "12100" {
			t.Errorf("refund lost its stable key or amount: %+v", r)
		}
		return 200, refund()
	})
	fixture.route("GET", "/v1/refunds/re_original", func(recordedProviderRequest) (int, any) { return 200, refund() })
	preview, err := tenant.svc.ReviewInvoiceRefund(ctx, tenant.id, invoice, request, "return the full paid month", "operator", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "preview" || len(fixture.matching("POST", "/v1/refunds")) != 0 {
		t.Fatal("preview returned money")
	}
	exec(`UPDATE purser.billing_payments SET updated_at=NOW()+INTERVAL '1 second' WHERE tenant_id=$1 AND id=$2`, tenant.id, payment)
	if _, err = tenant.svc.ReviewInvoiceRefund(ctx, tenant.id, invoice, request, "return the full paid month", "operator", true, preview.Fingerprint); err == nil {
		t.Fatal("stale financial review queued a refund")
	}
	preview, err = tenant.svc.ReviewInvoiceRefund(ctx, tenant.id, invoice, request, "return the full paid month", "operator", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tenant.svc.ReviewInvoiceRefund(ctx, tenant.id, invoice, request, "return the full paid month", "operator", true, preview.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err = tenant.svc.processOperatorInvoiceRefund(ctx, tenant.id, request); !errors.Is(err, ErrAwaitingProviderSettlement) {
		t.Fatalf("unsettled bank return treated as final: %v", err)
	}
	checkHeld := func() {
		t.Helper()
		var state string
		if err = db.QueryRow(`SELECT status FROM purser.billing_invoices WHERE tenant_id=$1 AND id=$2`, tenant.id, invoice).Scan(&state); err != nil || state != "manual_review" {
			t.Fatalf("refund invoice became collectible: %s %v", state, err)
		}
	}
	checkHeld()
	bankState = "succeeded"
	if _, err = tenant.svc.applyProviderReversal(ctx, providerReversalInput{provider: "stripe", reversalType: "refund", providerReversalID: "re_original", providerPaymentID: pi, amountCents: 12100, currency: "EUR", reason: "return the full paid month"}); err != nil {
		t.Fatal(err)
	}
	checkHeld()
	for range 2 {
		if err = tenant.svc.processOperatorInvoiceRefund(ctx, tenant.id, request); err != nil {
			t.Fatal(err)
		}
	}
	var state, amount string
	var net, vat, gross int64
	if err = db.QueryRow(`SELECT status,amount::text FROM purser.billing_invoices WHERE tenant_id=$1 AND id=$2`, tenant.id, invoice).Scan(&state, &amount); err != nil || state != "refunded" || amount != "121.00" {
		t.Fatalf("refund rewrote or reopened invoice: %s %s %v", state, amount, err)
	}
	if err = db.QueryRow(`SELECT SUM(net_eur_cents),SUM(vat_eur_cents),SUM(gross_eur_cents) FROM purser.credit_notes WHERE tenant_id=$1 AND source_document_id=$2`, tenant.id, invoice).Scan(&net, &vat, &gross); err != nil || net != 10000 || vat != 2100 || gross != 12100 {
		t.Fatalf("original tax not reversed exactly once: %d/%d/%d %v", net, vat, gross, err)
	}
	if got := admissionBalanceCents(t, db, tenant.id); got != 0 {
		t.Fatalf("cash refund also issued wallet credit: %d", got)
	}
	if len(fixture.matching("POST", "/v1/refunds")) != 1 {
		t.Fatal("refund replay called the provider twice")
	}
	if _, err = purserdb.New(db).GetOperatorInvoiceRefund(ctx, purserdb.GetOperatorInvoiceRefundParams{TenantID: uuid.NewString(), RequestID: request}); err == nil {
		t.Fatal("refund receipt leaked across tenants")
	}
}
func TestCompletedInvoiceCashRefund_RealYugabyte(t *testing.T) {
	TestCompletedInvoiceCashRefund_RealPG(t)
}
