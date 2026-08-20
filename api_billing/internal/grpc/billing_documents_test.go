package grpc

import (
	"strings"
	"testing"
	"time"
)

func TestRenderBillingDocumentEscapesPersistedCustomerData(t *testing.T) {
	now := time.Now().UTC()
	response, err := renderBillingDocument(billingDocumentRow{
		id: "id", kind: "invoice", number: "INV-1", amountCents: 1234,
		currency: "EUR", status: "paid", issuedAt: now, retentionUntil: now.AddDate(10, 0, 0),
	}, billingDocumentHTMLData{
		Title: "Invoice", SupplierName: "FrameWorks", Customer: `<script>alert("x")</script>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(response.GetContent())
	if strings.Contains(content, "<script>") || !strings.Contains(content, "&lt;script&gt;") {
		t.Fatalf("customer field was not safely escaped: %s", content)
	}
	if response.GetSha256() == "" || response.GetDocument().GetDownloadFilename() != "INV-1.html" {
		t.Fatalf("missing document integrity metadata: %+v", response)
	}
}

func TestRenderCryptoInvoiceContainsLegalIdentityAndServiceLine(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	response, err := renderBillingDocument(billingDocumentRow{
		id: "id", kind: "crypto_invoice", number: "B2B-0000000001", amountCents: 1000,
		currency: "EUR", status: "reverse_charge", issuedAt: now, retentionUntil: now.AddDate(10, 0, 0),
	}, billingDocumentHTMLData{
		Title: "Invoice", SupplierName: "FrameWorks B.V.", SupplierAddress: "Amsterdam, NL",
		SupplierVAT: "NL000000000B01", SupplierRegistration: "12345678",
		Customer: "Erika Mustermann", CustomerCompany: "Example GmbH",
		CustomerAddress: `{"street":"Hauptstrasse 1","city":"Berlin","country":"DE"}`,
		CustomerVAT:     "DE123456789",
		Fields: []billingDocumentHTMLField{
			{Label: "Service", Value: "FrameWorks prepaid usage credit"},
			{Label: "Quantity", Value: "1"},
			{Label: "Supply date", Value: "2026-08-20"},
			{Label: "Net", Value: "EUR 10.00"},
			{Label: "VAT", Value: "EUR 0.00 (0.00%)"},
			{Label: "VAT treatment", Value: "Reverse charge — btw verlegd"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(response.GetContent())
	for _, want := range []string{
		"FrameWorks B.V.", "NL000000000B01", "12345678", "Erika Mustermann",
		"Example GmbH", "DE123456789", "FrameWorks prepaid usage credit",
		"Quantity", "2026-08-20", "EUR 10.00", "Reverse charge — btw verlegd",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered document missing %q: %s", want, content)
		}
	}
}
