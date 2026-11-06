//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func retainUnknownStripeOriginal(t *testing.T, db *sql.DB, tenant string, raw json.RawMessage) string {
	t.Helper()
	doc, err := billingstripe.ParseProviderTaxDocument("provider_invoice", raw)
	if err != nil {
		t.Fatal(err)
	}
	var net, vat sql.NullInt64
	if doc.Net != nil && doc.VAT != nil {
		net = sql.NullInt64{Int64: *doc.Net, Valid: true}
		vat = sql.NullInt64{Int64: *doc.VAT, Valid: true}
	}
	pdf := []byte("%PDF-1.4\nRetained original\n%%EOF")
	hash := sha256.Sum256(pdf)
	stored, err := purserdb.New(db).StoreProviderBillingDocument(t.Context(), purserdb.StoreProviderBillingDocumentParams{TenantID: tenant, ProviderAccountID: "acct_reassessment", Livemode: doc.Livemode, DocumentKind: doc.Kind, ProviderDocumentID: doc.ID, OriginalNumber: doc.Number, Status: doc.Status, Currency: doc.Currency, NetMinor: net, VatMinor: vat, GrossMinor: doc.Gross, TaxStatus: "legacy_unknown", TaxGroups: doc.TaxGroups, SupplierSnapshot: doc.Supplier, CustomerSnapshot: doc.Customer, RawFacts: doc.Raw, TaxAssessment: json.RawMessage(`{"review_required":true,"review_reasons":["supplier_configuration_required"]}`), OriginalPdfUrl: sql.NullString{String: doc.PDFURL, Valid: true}, OriginalPdf: pdf, PdfSha256: sql.NullString{String: hex.EncodeToString(hash[:]), Valid: true}, IssuedAt: doc.IssuedAt})
	if err != nil {
		t.Fatal(err)
	}
	return stored.ID
}

func TestStripeResolvedOriginalReviewStaysResolved_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant, raw := stripeReassessmentInvoice(t, db, "in_terminal_review")
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "metadata")
	raw, _ = json.Marshal(payload)
	if err := service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	doc, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_terminal_review", DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW(),resolution_note='Historical VAT absorbed by supplier' WHERE tenant_id=$1::uuid AND reference_id=$2 AND kind='provider_tax_document_review'`, tenant, doc.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
			t.Fatal(err)
		}
	}
	var status, note string
	var occurrences int
	if err = db.QueryRow(`SELECT status,occurrences,COALESCE(resolution_note,'') FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND reference_id=$2 AND kind='provider_tax_document_review'`, tenant, doc.ID).Scan(&status, &occurrences, &note); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" || occurrences != 1 || note != "Historical VAT absorbed by supplier" {
		t.Fatalf("same original facts reopened resolved review: status=%s occurrences=%d note=%s", status, occurrences, note)
	}
}

func TestStripeInvoiceRejectsEvidenceAfterTaxPoint_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant, raw := stripeReassessmentInvoice(t, db, "in_future_evidence")
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["created"] = time.Now().Add(-48 * time.Hour).Unix()
	raw, _ = json.Marshal(payload)
	if err := service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT tax_status FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND provider_document_id='in_future_evidence'`, tenant).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "ready" {
		t.Fatal("evidence created after tax point verified a historical invoice")
	}
}

func TestStripeUnknownWebhookDoesNotReassessToday_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant, raw := stripeReassessmentInvoice(t, db, "in_retained_unknown")
	retainUnknownStripeOriginal(t, db, tenant, raw)
	if _, err := db.Exec(`CREATE FUNCTION purser.reject_today_profile() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'today assessment unavailable' USING ERRCODE='08006'; END $$; CREATE TRIGGER reject_today_profile BEFORE INSERT ON purser.provider_tax_profiles FOR EACH ROW EXECUTE FUNCTION purser.reject_today_profile()`); err != nil {
		t.Fatal(err)
	}
	if err := service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Fatalf("retained webhook depended on today's assessment: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT tax_status FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND provider_document_id='in_retained_unknown'`, tenant).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "legacy_unknown" {
		t.Fatal("redelivery changed retained tax classification")
	}
}
