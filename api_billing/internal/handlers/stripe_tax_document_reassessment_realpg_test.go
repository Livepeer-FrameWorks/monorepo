//go:build schema_verify

package handlers

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func newStripeReassessmentService(t *testing.T) (*sql.DB, *Service, *providerFixture) {
	t.Helper()
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	fixture, client := installStripeFixture(t)
	fixture.route("GET", "/v1/account", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "acct_reassessment", "object": "account", "country": "NL"}
	})
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = providerPDFTestTransport{next: previous, handler: fixtureRoundTripper{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.4\nOriginal Stripe invoice\n%%EOF"))
	})}}
	return db, &Service{db: db, stripeClient: client, logger: logging.NewLogger()}, fixture
}

func stripeReassessmentInvoice(t *testing.T, db *sql.DB, invoice string) (string, json.RawMessage) {
	t.Helper()
	tenant := seedPrepaidLedgerTenant(t, db)
	return tenant, stripeReassessmentInvoiceForTenant(t, db, tenant, invoice)
}

func stripeReassessmentInvoiceForTenant(t *testing.T, db *sql.DB, tenant, invoice string) json.RawMessage {
	t.Helper()
	customer := "cus_" + tenant
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_customer_id=$2,billing_customer_type='consumer',billing_name='Original Buyer',billing_email='buyer@example.test',billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant, customer); err != nil {
		t.Fatal(err)
	}
	profile, err := AssessStripeTaxProfile(t.Context(), db, tenant, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"id": invoice, "customer": customer, "number": "DDV-" + invoice, "status": "paid", "currency": "eur", "total": 12100, "total_excluding_tax": 10000, "total_taxes": []any{map[string]any{"amount": 2100}}, "created": time.Now().Unix(), "invoice_pdf": "https://invoice.stripe.com/" + invoice + ".pdf", "account_name": "DDVTech B.V.", "account_country": "NL", "customer_name": "Original Buyer", "customer_email": "buyer@example.test", "customer_address": map[string]string{"line1": "Main1", "city": "Leiden", "postal_code": "2332ED", "country": "NL"}, "metadata": map[string]string{"frameworks_tax_profile": profile.ProfileSHA}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func stripeCapturedTaxFacts(t *testing.T, db *sql.DB, tenant, id string) []byte {
	t.Helper()
	var facts []byte
	if err := db.QueryRow(`SELECT jsonb_build_object('tax_status',tax_status,'tax_assessment',tax_assessment,'raw_facts',raw_facts,'original_pdf',encode(original_pdf,'base64'),'pdf_sha256',pdf_sha256,'net_minor',net_minor,'vat_minor',vat_minor,'gross_minor',gross_minor,'currency',currency,'original_number',original_number,'supplier_snapshot',supplier_snapshot,'customer_snapshot',customer_snapshot,'issued_at',issued_at,'retention_until',retention_until) FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, id).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestStripeConfigurationReviewRetainsAndReassessesOriginalDocument_RealPG(t *testing.T) {
	db, service, fixture := newStripeReassessmentService(t)
	tenant, raw := stripeReassessmentInvoice(t, db, "in_configuration_review")
	appconfigtest.Set(t, "SUPPLIER_NAME", "")
	if err := service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Errorf("missing local supplier configuration discarded provider evidence instead of acknowledging review: %v", err)
	}
	q := purserdb.New(db)
	found, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_configuration_review", DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatalf("original provider document was not retained during configuration review: %v", err)
	}
	initial, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: found.ID, DocumentKind: "provider_invoice"})
	if err != nil || initial.TaxStatus == "ready" || len(initial.OriginalPdf) == 0 || initial.GrossMinor != 12100 || initial.NetMinor.Int64 != 10000 || initial.VatMinor.Int64 != 2100 {
		t.Fatalf("unconfigured import lost original money/PDF or falsely became ready: %+v %v", initial, err)
	}
	var reviews int
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND reference_id=$2 AND status='open'`, tenant, found.ID).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Fatalf("captured configuration review has%d operational anomalies, want1", reviews)
	}
	captured := stripeCapturedTaxFacts(t, db, tenant, found.ID)
	creditRaw, err := json.Marshal(map[string]any{"id": "cn_configuration_review", "customer": "cus_" + tenant, "number": "DDV-CREDIT-REVIEW", "status": "issued", "currency": "eur", "total": 1210, "total_excluding_tax": 1000, "total_taxes": []any{map[string]any{"amount": 210}}, "created": time.Now().Unix(), "invoice": "in_configuration_review", "pdf": "https://invoice.stripe.com/cn_configuration_review.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.importStripeTaxDocument(t.Context(), "provider_credit_note", creditRaw); err != nil {
		t.Fatal(err)
	}
	credit, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "cn_configuration_review", DocumentKind: "provider_credit_note"})
	if err != nil {
		t.Fatal(err)
	}
	creditCaptured := stripeCapturedTaxFacts(t, db, tenant, credit.ID)
	appconfigtest.Set(t, "SUPPLIER_NAME", "DDVTech B.V.")
	for attempt := 0; attempt < 2; attempt++ {
		if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
			t.Fatal(err)
		}
		if err = service.importStripeTaxDocument(t.Context(), "provider_credit_note", creditRaw); err != nil {
			t.Fatal(err)
		}
	}
	current, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: found.ID, DocumentKind: "provider_invoice"})
	if err != nil || current.TaxStatus != "ready" {
		t.Fatalf("verified current review stayed permanently unknown after same-payload retry: status%s err%v", current.TaxStatus, err)
	}
	for _, statement := range []string{
		`UPDATE purser.provider_billing_documents SET reviewed_tax_status='legacy_unknown' WHERE tenant_id=$1::uuid AND id=$2::uuid`,
		`UPDATE purser.provider_billing_documents SET tax_reviewed_at=tax_reviewed_at+INTERVAL '1 second' WHERE tenant_id=$1::uuid AND id=$2::uuid`,
		`UPDATE purser.provider_billing_documents SET net_minor=net_minor+1,gross_minor=gross_minor+1 WHERE tenant_id=$1::uuid AND id=$2::uuid`,
		`UPDATE purser.provider_billing_documents SET raw_facts=jsonb_set(raw_facts,'{total}','99999'::jsonb) WHERE tenant_id=$1::uuid AND id=$2::uuid`,
		`UPDATE purser.provider_billing_documents SET tax_assessment='{}'::jsonb WHERE tenant_id=$1::uuid AND id=$2::uuid`,
	} {
		if _, guardErr := db.Exec(statement, tenant, found.ID); guardErr == nil {
			t.Fatalf("verified review or captured original was mutable: %s", statement)
		}
	}
	if !bytes.Equal(captured, stripeCapturedTaxFacts(t, db, tenant, found.ID)) {
		t.Fatal("reassessment rewrote immutable captured tax claim, PDF, money or parties")
	}
	currentCredit, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: credit.ID, DocumentKind: "provider_credit_note"})
	if err != nil || currentCredit.TaxStatus != "ready" || currentCredit.GrossMinor != 1210 || currentCredit.NetMinor.Int64 != 1000 || currentCredit.VatMinor.Int64 != 210 || !bytes.Equal(creditCaptured, stripeCapturedTaxFacts(t, db, tenant, credit.ID)) {
		t.Fatalf("credit reassessment lost parent verification or original facts: status%s err%v", currentCredit.TaxStatus, err)
	}
	archive, err := q.FinancialAuditPage(t.Context(), purserdb.FinancialAuditPageParams{TenantID: tenant, Source: "provider_billing_documents", FromAt: time.Now().Add(-24 * time.Hour), ThroughAt: time.Now().Add(time.Hour)})
	if err != nil || len(archive) != 2 {
		t.Fatalf("current review disappeared from financial archive: rows%d err%v", len(archive), err)
	}
	for _, item := range archive {
		var exported, original map[string]json.RawMessage
		if err = json.Unmarshal([]byte(item.Payload), &exported); err != nil {
			t.Fatal(err)
		}
		originalFacts := captured
		if item.ID == credit.ID {
			originalFacts = creditCaptured
		}
		if err = json.Unmarshal(originalFacts, &original); err != nil {
			t.Fatal(err)
		}
		if string(exported["tax_status"]) != `"ready"` || !bytes.Equal(exported["captured_tax_status"], original["tax_status"]) || !bytes.Equal(exported["captured_tax_assessment"], original["tax_assessment"]) {
			t.Fatalf("financial archive lost current review or captured evidence: %s", item.Payload)
		}
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND reference_id=$2 AND status='open'`, tenant, found.ID).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 {
		t.Fatalf("successful review left%d open configuration anomalies", reviews)
	}
	if _, err = q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: uuid.NewString(), DocumentID: found.ID, DocumentKind: "provider_invoice"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other tenant read reviewed provider facts: %v", err)
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		fixture.mu.Lock()
		for _, request := range fixture.requests {
			if request.method == method {
				t.Errorf("read-only reassessment moved provider state: %s%s", method, request.path)
			}
		}
		fixture.mu.Unlock()
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid`, tenant).Scan(&count); err != nil || count != 2 {
		t.Fatalf("reassessment duplicated original invoice: count%d err%v", count, err)
	}
}

func TestStripeLegacyReassessmentIsBoundedFairAndTenantScoped_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	q := purserdb.New(db)
	tenant, first := stripeReassessmentInvoice(t, db, "in_incomplete_0")
	captured := map[string][]byte{}
	ids := []string{}
	const batchLimit = 20
	for n := 0; n < batchLimit; n++ {
		raw := first
		invoice := fmt.Sprintf("in_incomplete_%d", n)
		if n > 0 {
			raw = stripeReassessmentInvoiceForTenant(t, db, tenant, invoice)
		}
		var original map[string]any
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatal(err)
		}
		delete(original, "total_excluding_tax")
		delete(original, "total_taxes")
		withoutTax, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", withoutTax); err != nil {
			t.Fatal(err)
		}
		doc, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: invoice, DocumentKind: "provider_invoice"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, doc.ID)
		captured[doc.ID] = stripeCapturedTaxFacts(t, db, tenant, doc.ID)
	}
	complete := stripeReassessmentInvoiceForTenant(t, db, tenant, "in_complete_after_backlog")
	otherTenant, otherRaw := stripeReassessmentInvoice(t, db, "in_other_tenant")
	appconfigtest.Set(t, "SUPPLIER_NAME", "")
	for _, raw := range []json.RawMessage{complete, otherRaw} {
		if err := service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
			t.Fatal(err)
		}
	}
	appconfigtest.Set(t, "SUPPLIER_NAME", "DDVTech B.V.")
	readyCandidate, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_complete_after_backlog", DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: otherTenant, ProviderDocumentID: "in_other_tenant", DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	captured[readyCandidate.ID] = stripeCapturedTaxFacts(t, db, tenant, readyCandidate.ID)
	otherCaptured := stripeCapturedTaxFacts(t, db, otherTenant, other.ID)
	if _, err = db.Exec(`UPDATE purser.provider_billing_documents SET tax_reviewed_at=NOW()-CASE WHEN id=$2::uuid THEN INTERVAL '10 minutes' ELSE INTERVAL '20 minutes' END WHERE tenant_id=$1::uuid`, tenant, readyCandidate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE purser.provider_billing_documents SET tax_reviewed_at=NOW()-INTERVAL '20 minutes' WHERE tenant_id=$1::uuid`, otherTenant); err != nil {
		t.Fatal(err)
	}
	var firstPassAt time.Time
	if err = db.QueryRow(`SELECT NOW()`).Scan(&firstPassAt); err != nil {
		t.Fatal(err)
	}
	if err = service.reassessStripeTaxDocuments(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err = db.QueryRow(`SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND tax_reviewed_at>=$2`, tenant, firstPassAt).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != batchLimit {
		t.Fatalf("one worker pass attempted%d originals, want bounded%d", attempts, batchLimit)
	}
	for _, id := range ids {
		doc, readErr := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: id, DocumentKind: "provider_invoice"})
		if readErr != nil {
			t.Fatal(readErr)
		}
		if doc.TaxStatus != "legacy_unknown" || doc.NetMinor.Valid || doc.VatMinor.Valid || doc.GrossMinor != 12100 {
			t.Fatalf("current profile invented original tax money: %+v", doc)
		}
	}
	readCandidate := func(scopedTenant, id string) purserdb.GetProviderBillingDocumentRow {
		t.Helper()
		doc, readErr := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: scopedTenant, DocumentID: id, DocumentKind: "provider_invoice"})
		if readErr != nil {
			t.Fatal(readErr)
		}
		return doc
	}
	if got := readCandidate(tenant, readyCandidate.ID).TaxStatus; got != "legacy_unknown" {
		t.Fatalf("first pass crossed batch boundary: %s", got)
	}
	if err = service.reassessStripeTaxDocuments(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	if got := readCandidate(tenant, readyCandidate.ID).TaxStatus; got != "ready" {
		t.Fatalf("incomplete originals starved verified candidate: %s", got)
	}
	if got := readCandidate(otherTenant, other.ID).TaxStatus; got != "legacy_unknown" {
		t.Fatalf("tenant worker reviewed another tenant: %s", got)
	}
	var readyAt, repeatAt time.Time
	if err = db.QueryRow(`SELECT tax_reviewed_at FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, readyCandidate.ID).Scan(&readyAt); err != nil {
		t.Fatal(err)
	}
	if err = service.reassessStripeTaxDocuments(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT tax_reviewed_at FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, readyCandidate.ID).Scan(&repeatAt); err != nil {
		t.Fatal(err)
	}
	if !readyAt.Equal(repeatAt) {
		t.Fatal("ready original was repeatedly reassessed")
	}
	for id, original := range captured {
		if !bytes.Equal(original, stripeCapturedTaxFacts(t, db, tenant, id)) {
			t.Fatalf("worker changed captured original%s", id)
		}
	}
	if !bytes.Equal(otherCaptured, stripeCapturedTaxFacts(t, db, otherTenant, other.ID)) {
		t.Fatal("worker changed other tenant's captured original")
	}
	if err = service.reassessStripeTaxDocuments(t.Context(), otherTenant); err != nil {
		t.Fatal(err)
	}
	if got := readCandidate(otherTenant, other.ID).TaxStatus; got != "ready" {
		t.Fatalf("other tenant's own worker could not verify original: %s", got)
	}
}

func TestStripeReassessmentRefusesMismatchedCapturedMoney_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant, raw := stripeReassessmentInvoice(t, db, "in_mismatched_capture")
	original, err := billingstripe.ParseProviderTaxDocument("provider_invoice", raw)
	if err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\nOriginal mismatch fixture\n%%EOF")
	digest := sha256.Sum256(pdf)
	q := purserdb.New(db)
	stored, err := q.StoreProviderBillingDocument(t.Context(), purserdb.StoreProviderBillingDocumentParams{TenantID: tenant, ProviderAccountID: "acct_reassessment", DocumentKind: "provider_invoice", ProviderDocumentID: original.ID, OriginalNumber: original.Number, Status: original.Status, Currency: original.Currency, NetMinor: sql.NullInt64{Int64: 10000, Valid: true}, VatMinor: sql.NullInt64{Int64: 10000, Valid: true}, GrossMinor: 20000, TaxStatus: "legacy_unknown", TaxGroups: original.TaxGroups, SupplierSnapshot: original.Supplier, CustomerSnapshot: original.Customer, RawFacts: original.Raw, TaxAssessment: json.RawMessage(`{}`), OriginalPdf: pdf, PdfSha256: sql.NullString{String: hex.EncodeToString(digest[:]), Valid: true}, IssuedAt: original.IssuedAt})
	if err != nil {
		t.Fatal(err)
	}
	captured := stripeCapturedTaxFacts(t, db, tenant, stored.ID)
	if _, err = db.Exec(`UPDATE purser.provider_billing_documents SET tax_reviewed_at=NOW()-INTERVAL '10 minutes' WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, stored.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.reassessStripeTaxDocuments(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	current, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: stored.ID, DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	if current.TaxStatus != "legacy_unknown" || current.NetMinor.Int64 != 10000 || current.VatMinor.Int64 != 10000 || current.GrossMinor != 20000 || !bytes.Equal(captured, stripeCapturedTaxFacts(t, db, tenant, stored.ID)) {
		t.Fatalf("current profile laundered mismatched captured money into ready: status%s net%d VAT%d gross%d", current.TaxStatus, current.NetMinor.Int64, current.VatMinor.Int64, current.GrossMinor)
	}
	var reviews int
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND reference_id=$2 AND status='open'`, tenant, stored.ID).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Fatalf("mismatched original has%d actionable review anomalies, want1", reviews)
	}
}
