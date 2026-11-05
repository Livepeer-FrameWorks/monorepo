package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderDocumentRetainsActualCurrencyTaxAndNumber(t *testing.T) {
	raw := json.RawMessage(`{"id":"in_original","customer":"cus_original","number":"DDV-0042","status":"paid","currency":"usd","total":12100,"total_excluding_tax":10000,"total_taxes":[{"amount":2100}],"created":1793876400,"invoice_pdf":"https://invoice.stripe.com/original.pdf","account_name":"DDVTech B.V.","footer":"VAT NL853319145B01; Registration 59104694","customer_name":"Original buyer","parent":{"subscription_details":{"metadata":{"frameworks_tax_profile":"frozen-proof"}}}}`)
	doc, err := ParseProviderTaxDocument("provider_invoice", raw)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Number != "DDV-0042" || doc.Currency != "USD" || doc.Gross != 12100 || doc.Net == nil || *doc.Net != 10000 || doc.VAT == nil || *doc.VAT != 2100 || doc.ProfileSHA != "frozen-proof" {
		t.Fatalf("provider facts changed: %+v", doc)
	}
	if string(doc.Raw) != string(raw) || !strings.Contains(string(doc.Customer), "Original buyer") {
		t.Fatal("original document facts lost")
	}
	legacy, err := ParseProviderTaxDocument("provider_invoice", json.RawMessage(`{"id":"in_legacy","number":"LEGACY-1","status":"paid","currency":"eur","total":10000,"created":1793876400}`))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Net != nil || legacy.VAT != nil {
		t.Fatal("missing provider tax must remain unknown")
	}
	if _, err = ParseProviderTaxDocument("provider_invoice", json.RawMessage(`{"id":"in_draft","number":"DRAFT","status":"draft","currency":"eur","total":100,"created":1793876400}`)); err == nil {
		t.Fatal("draft imported as tax invoice")
	}
}

type documentRoundTripper func(*http.Request) (*http.Response, error)

func (f documentRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOriginalProviderPDFDownloadBoundariesAndHash(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	requests := 0
	body := "%PDF-1.4\nOriginal provider PDF\n%%EOF"
	http.DefaultTransport = documentRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Authorization") != "" {
			t.Fatal("provider credentials leaked to PDF host")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	c := &Client{}
	for _, url := range []string{"http://invoice.stripe.com/x", "https://evil.test/x", "https://stripe.com.evil.test/x", "https://user:secret@invoice.stripe.com/x", "https://invoice.stripe.com/x#fragment"} {
		if _, _, err := c.DownloadOriginalPDF(context.Background(), url); err == nil {
			t.Fatalf("accepted unsafe URL %s", url)
		}
	}
	if requests != 0 {
		t.Fatal("unsafe URL made a request")
	}
	pdf, hash, err := c.DownloadOriginalPDF(context.Background(), "https://invoice.stripe.com/original.pdf")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(body))
	if string(pdf) != body || hash != hex.EncodeToString(digest[:]) {
		t.Fatal("original bytes or hash changed")
	}
	body = "<html>not a PDF</html>"
	if _, _, err = c.DownloadOriginalPDF(context.Background(), "https://invoice.stripe.com/original.pdf"); err == nil {
		t.Fatal("accepted HTML as original PDF")
	}
}
