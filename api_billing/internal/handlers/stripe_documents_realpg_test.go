//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestStripeOriginalDocumentsImportRetryAndOwnership_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_customer_id='cus_original' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	fixture, client := installStripeFixture(t)
	fixture.route("GET", "/v1/account", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "acct_frameworks", "object": "account"}
	})
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	pdfCalls := 0
	http.DefaultTransport = providerPDFTestTransport{next: previous, handler: fixtureRoundTripper{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pdfCalls++
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4\nOriginal " + r.URL.Path + "\n%%EOF"))
	})}}
	svc := &Service{db: db, stripeClient: client, logger: logging.NewLogger()}
	ctx := context.Background()
	profile := json.RawMessage(`{"country":"NL","rate_bps":2100,"evidence":{"decision":{"source":"frozen-proof"}}}`)
	sha := strings.Repeat("a", 64)
	if err := purserdb.New(db).StoreProviderTaxProfile(ctx, purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: sha, Profile: profile}); err != nil {
		t.Fatal(err)
	}
	invoice := map[string]any{"id": "in_original", "customer": "cus_original", "number": "DDV-0042", "status": "paid", "currency": "eur", "total": 12100, "total_excluding_tax": 10000, "total_taxes": []any{map[string]any{"amount": 2100}}, "created": time.Now().Unix(), "invoice_pdf": "https://invoice.stripe.com/in_original.pdf", "account_name": "DDVTech B.V.", "footer": "VAT NL853319145B01; KVK59104694", "customer_name": "Original buyer", "parent": map[string]any{"subscription_details": map[string]any{"metadata": map[string]any{"frameworks_tax_profile": sha}}}}
	raw, _ := json.Marshal(invoice)
	if err := svc.importStripeTaxDocument(ctx, "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	invoice["total"] = 99999
	invoice["customer_name"] = "Changed buyer"
	replay, _ := json.Marshal(invoice)
	if err := svc.importStripeTaxDocument(ctx, "provider_invoice", replay); err != nil {
		t.Fatal(err)
	}
	original, err := purserdb.New(db).FindProviderBillingDocument(ctx, purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_original", DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := purserdb.New(db).GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: original.ID, DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	if doc.OriginalNumber != "DDV-0042" || doc.GrossMinor != 12100 || doc.NetMinor.Int64 != 10000 || doc.VatMinor.Int64 != 2100 || !strings.Contains(string(doc.CustomerSnapshot), "Original buyer") || !strings.Contains(string(doc.TaxAssessment), "frozen-proof") || len(doc.OriginalPdf) == 0 || len(doc.PdfSha256.String) != 64 || pdfCalls != 1 {
		t.Fatalf("original provider facts or PDF changed: %+v (PDF calls%d)", doc, pdfCalls)
	}
	if _, err = purserdb.New(db).GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: uuid.NewString(), DocumentID: original.ID, DocumentKind: "provider_invoice"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("other tenant can read provider document")
	}
	list, err := purserdb.New(db).ListBillingDocuments(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != "provider_invoice" || list[0].DocumentNumber != "DDV-0042" {
		t.Fatalf("provider document missing or duplicated in financial list: %+v", list)
	}
	// Credit notes state the original invoice parties and evidence even after the saved profile changes.
	note := map[string]any{"id": "cn_original", "invoice": "in_original", "customer": "cus_original", "number": "CN-0042", "status": "issued", "currency": "eur", "total": 1210, "total_excluding_tax": 1000, "total_taxes": []any{map[string]any{"amount": 210}}, "created": time.Now().Unix(), "pdf": "https://invoice.stripe.com/cn_original.pdf", "customer_name": "Changed buyer"}
	raw, _ = json.Marshal(note)
	if err = svc.importStripeTaxDocument(ctx, "provider_credit_note", raw); err != nil {
		t.Fatal(err)
	}
	credit, err := purserdb.New(db).FindProviderBillingDocument(ctx, purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "cn_original", DocumentKind: "provider_credit_note"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(credit.CustomerSnapshot), "Original buyer") || !strings.Contains(string(credit.TaxAssessment), "frozen-proof") {
		t.Fatal("credit note lost original tax proof or parties")
	}
	// Missing PDFs remain visible and retryable; issued facts survive the retry unchanged.
	invoice["id"] = "in_pending"
	invoice["number"] = "DDV-0043"
	invoice["total"] = 12100
	invoice["invoice_pdf"] = ""
	raw, _ = json.Marshal(invoice)
	if err = svc.importStripeTaxDocument(ctx, "provider_invoice", raw); err == nil {
		t.Fatal("missing PDF did not request provider retry")
	}
	invoice["invoice_pdf"] = "https://invoice.stripe.com/in_pending.pdf"
	raw, _ = json.Marshal(invoice)
	if err = svc.importStripeTaxDocument(ctx, "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	pending, err := purserdb.New(db).FindProviderBillingDocument(ctx, purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_pending", DocumentKind: "provider_invoice"})
	if err != nil || !pending.HasPdf {
		t.Fatal("retry failed to capture original PDF", err)
	}
	// Shared-account legacy customers are ignored without fetching account or PDF.
	callsBefore := len(fixture.matching("GET", "/v1/account"))
	pdfBefore := pdfCalls
	invoice["id"] = "in_unrelated"
	invoice["customer"] = "cus_legacy"
	raw, _ = json.Marshal(invoice)
	if err = svc.importStripeTaxDocument(ctx, "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	if len(fixture.matching("GET", "/v1/account")) != callsBefore || pdfCalls != pdfBefore {
		t.Fatal("unrelated Stripe customer caused provider import calls")
	}
	var local int
	if err = db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&local); err != nil || local != 0 {
		t.Fatal("provider import created duplicate Purser invoice", err)
	}
}

type providerPDFTestTransport struct {
	next    http.RoundTripper
	handler http.RoundTripper
}

func (p providerPDFTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "invoice.stripe.com" {
		return p.handler.RoundTrip(r)
	}
	return p.next.RoundTrip(r)
}
