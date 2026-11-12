package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

type executionFixture interface {
	setup(context.Context, *sql.Conn) error
	verify(context.Context, *sql.Conn) error
}

func executionFixtureFor(id string, now time.Time) executionFixture {
	if f := authBackfillFixtureFor(id); f != nil {
		return f
	}
	if f := publisherFixtureFor(id, now); f != nil {
		return f
	}
	if f := purserFixtureFor(id, now); f != nil {
		return f
	}
	return nil
}

// Financial writes need one coherent purchase, rather than independently
// sampled invoice owners, payment IDs, currencies and major/minor amounts.
type purserPaymentFixture struct {
	name, tenant, tier, invoice, payment, event string
	now                                         time.Time
}

func purserFixtureFor(id string, now time.Time) *purserPaymentFixture {
	switch id {
	case "purser:InsertFundedAdvanceInvoice", "purser:RecordNativeMollieInvoicePayment", "purser:InsertReorgedCryptoInvoiceCreditNote", "purser:UpsertPendingProviderBillingPayment":
	default:
		return nil
	}
	rng := rand.New(rand.NewSource(int64(hashString("payment fixture:" + id))))
	return &purserPaymentFixture{name: strings.TrimPrefix(id, "purser:"), tenant: uuidFrom(rng), tier: uuidFrom(rng), invoice: uuidFrom(rng), payment: uuidFrom(rng), event: uuidFrom(rng), now: now}
}

func (f *purserPaymentFixture) parameter(name string) (string, bool) {
	switch name {
	case "tenant_id":
		return f.tenant, true
	case "invoice_id":
		return f.invoice, true
	case "payment_id":
		if f.name == "RecordNativeMollieInvoicePayment" {
			return "tr_" + f.payment, true
		}
		return f.payment, true
	case "event_id", "source_reference_id":
		return f.event, true
	case "payment_reference", "tx_id":
		return "pi_" + f.payment, true
	case "gross_eur_cents", "gross_original_minor", "gross_minor", "amount_cents", "original_amount_cents", "eur_amount_cents":
		return "1210", true
	case "net_eur_cents":
		return "1000", true
	case "currency":
		return "EUR", true
	case "units_per_eur", "fx_units_per_eur":
		return "1", true
	case "amount":
		return "12.10", true
	case "fx_source":
		return "identity", true
	case "tax_point_at", "paid_at":
		return f.now.Format(time.RFC3339Nano), true
	case "reference_date", "fx_reference_date":
		return f.now.Format(time.DateOnly), true
	case "source_reference_type":
		return "card_topup", true
	case "tx_hash":
		return "0x" + strings.Repeat("a", 64), true
	case "supplier_snapshot":
		return `{"name":"DDVTech B.V.","address":"Leiden NL","vat_number":"NL853319145B01","registration_number":"59104694","country":"NL"}`, true
	default:
		return "", false
	}
}

func (f *purserPaymentFixture) setup(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,currency,base_price) VALUES($1::uuid,$2,'Audit purchase','EUR',10)`, f.tier, "audit-"+f.tier); err != nil {
		return fmt.Errorf("payment fixture tier: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_customer_type,billing_name,billing_email,billing_address) VALUES($1::uuid,$2::uuid,'active','postpaid','business','Audit Buyer','buyer@example.test','{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}')`, f.tenant, f.tier); err != nil {
		return fmt.Errorf("payment fixture buyer: %w", err)
	}
	if f.name == "InsertFundedAdvanceInvoice" {
		return nil
	}
	supplier, _ := f.parameter("supplier_snapshot")
	invoiceStatus := "paid"
	if f.name == "UpsertPendingProviderBillingPayment" {
		invoiceStatus = "pending"
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO purser.billing_invoices(id,tenant_id,status,currency,amount,base_amount,due_date,paid_at,finalized_at,
 presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,
 net_eur_cents,vat_eur_cents,gross_eur_cents,document_net_minor,document_vat_minor,document_gross_minor,
 tax_status,tax_policy_version,tax_decision,tax_summary,tax_point_at,customer_snapshot,supplier_snapshot,source_reference_type,source_reference_id)
 VALUES($1::uuid,$2::uuid,$6::text,'EUR',12.10,10,$3,CASE WHEN $6::text='paid' THEN $3::timestamptz END,$3,1210,'EUR',1,$3::timestamptz::date,
 1000,210,1210,1000,210,1210,'ready','purser_invoice_tax_v1','{"rate_bps":2100,"treatment":"domestic"}','[{"rate_bps":2100,"net_minor":1000,"vat_minor":210}]',$3,
 purser.billing_customer_snapshot($2::uuid),$4::jsonb,'crypto_payment',$5)`, f.invoice, f.tenant, f.now, supplier, f.event, invoiceStatus); err != nil {
		return fmt.Errorf("payment fixture original invoice: %w", err)
	}
	if f.name == "UpsertPendingProviderBillingPayment" || f.name == "InsertReorgedCryptoInvoiceCreditNote" {
		status := "pending"
		if f.name == "InsertReorgedCryptoInvoiceCreditNote" {
			status = "confirmed"
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO purser.billing_payments(id,tenant_id,invoice_id,method,amount,currency,tx_id,status,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date) VALUES($1::uuid,$2::uuid,$3::uuid,'card',12.10,'EUR',$4,$5,1210,'EUR',1210,1,'identity',$6::date)`, f.payment, f.tenant, f.invoice, "pi_"+f.payment, status, f.now.Format(time.DateOnly)); err != nil {
			return fmt.Errorf("payment fixture owned payment: %w", err)
		}
	}
	return nil
}

func (f *purserPaymentFixture) verify(ctx context.Context, conn *sql.Conn) error {
	var query string
	var args []any
	switch f.name {
	case "InsertFundedAdvanceInvoice":
		query = `SELECT EXISTS(SELECT 1 FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_type='card_topup' AND source_reference_id=$2 AND amount=12.10 AND base_amount=10 AND presentment_amount_cents=1210 AND presentment_currency='EUR')`
		args = []any{f.tenant, f.event}
	case "RecordNativeMollieInvoicePayment":
		query = `SELECT EXISTS(SELECT 1 FROM purser.billing_payments WHERE tenant_id=$1::uuid AND invoice_id=$2::uuid AND tx_id=$3 AND status='confirmed' AND amount=12.10 AND original_amount_cents=1210 AND eur_amount_cents=1210 AND original_currency='EUR')`
		args = []any{f.tenant, f.invoice, "tr_" + f.payment}
	case "InsertReorgedCryptoInvoiceCreditNote":
		query = `SELECT EXISTS(SELECT 1 FROM purser.credit_notes WHERE tenant_id=$1::uuid AND source_document_id=$2::uuid AND reversal_reference_id=$3 AND amount_cents=1210 AND currency='EUR' AND net_eur_cents=1000 AND vat_eur_cents=210)`
		args = []any{f.tenant, f.invoice, f.event}
	default:
		query = `SELECT EXISTS(SELECT 1 FROM purser.billing_payments WHERE tenant_id=$1::uuid AND invoice_id=$2::uuid AND id=$3::uuid AND status='pending' AND tx_id=$4)`
		args = []any{f.tenant, f.invoice, f.payment, "pi_" + f.payment}
	}
	var valid bool
	if err := conn.QueryRowContext(ctx, query, args...).Scan(&valid); err != nil {
		return fmt.Errorf("payment fixture verification: %w", err)
	}
	if !valid {
		return fmt.Errorf("payment fixture %s did not perform its owned financial write", f.name)
	}
	return nil
}
