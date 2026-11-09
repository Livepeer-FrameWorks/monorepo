//go:build schema_verify

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingtax "frameworks/api_billing/internal/tax"
	"testing"
	"time"
)

func TestStripeIncompleteRecurringRenewalsRetainVATAndSeparateReviews_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET stripe_customer_id=$2,billing_name='',billing_email='buyer@example.test',billing_customer_type='business',tax_id='unverified-claim',billing_address='{"street":"","city":"Austin","postal_code":"78701","country":"US"}' WHERE tenant_id=$1::uuid`, tenant, "cus_"+tenant); err != nil {
		t.Fatal(err)
	}
	profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
	if err != nil || profile.RateBPS != 2100 || profile.ReverseCharge || !profile.IncompleteProfile || profile.Country != "US" || profile.Name != "" || profile.Street != "" {
		t.Fatalf("incomplete recurring fallback=%+v err=%v", profile, err)
	}
	issued := func(id string) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"id": id, "customer": "cus_" + tenant, "number": "DDV-" + id, "status": "paid", "currency": "eur", "total": 12100, "total_excluding_tax": 10000, "total_taxes": []any{map[string]any{"amount": 2100}}, "created": time.Now().Unix(), "invoice_pdf": "https://invoice.stripe.com/" + id + ".pdf", "account_name": "DDVTech B.V.", "account_country": "NL", "footer": profile.SupplierIdentity + "\n" + profile.Wording, "customer_name": "Known Provider Buyer", "customer_email": "buyer@example.test", "customer_address": map[string]string{"line1": "Known Street", "city": "Austin", "postal_code": "78701", "country": "US"}, "metadata": map[string]string{"frameworks_tax_profile": profile.Fingerprint()}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	first := issued("in_incomplete_first")
	if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", first); err != nil {
		t.Fatal(err)
	}
	var firstID string
	var net, vat, gross int64
	var assessment []byte
	if err = db.QueryRowContext(t.Context(), `SELECT id::text,net_minor,vat_minor,gross_minor,tax_assessment FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND provider_document_id='in_incomplete_first'`, tenant).Scan(&firstID, &net, &vat, &gross, &assessment); err != nil {
		t.Fatal(err)
	}
	if net != 10000 || vat != 2100 || gross != 12100 || !bytes.Contains(assessment, []byte("incomplete_recurring_billing_profile")) || !bytes.Contains(assessment, []byte("unverified-claim")) {
		t.Fatalf("fallback invoice lost original gross/VAT/review evidence: %d/%d/%d %s", net, vat, gross, assessment)
	}
	if _, err = db.ExecContext(t.Context(), `UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW(),resolution_note='First renewal reviewed' WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND reference_type='provider_invoice' AND reference_id=$2`, tenant, firstID); err != nil {
		t.Fatal(err)
	}
	if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", first); err != nil {
		t.Fatal(err)
	}
	if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", issued("in_incomplete_second")); err != nil {
		t.Fatal(err)
	}
	var resolved, open, occurrences int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FILTER(WHERE reference_id=$2 AND status='resolved'),count(*) FILTER(WHERE reference_id<>$2 AND status='open'),max(occurrences) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND reference_type='provider_invoice'`, tenant, firstID).Scan(&resolved, &open, &occurrences); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || open != 1 || occurrences != 1 {
		t.Fatalf("renewal review reused a closed issue or churned on replay: resolved=%d open=%d occurrences=%d", resolved, open, occurrences)
	}
	appconfigtest.Set(t, "SUPPLIER_COUNTRY", "FR")
	profile, err = AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
	var review *billingtax.ReviewError
	if profile != nil || err == nil || errors.As(err, &review) {
		t.Fatalf("unsupported supplier must return an operational configuration error without a tax profile or customer review: profile=%+v err=%v", profile, err)
	}
}
