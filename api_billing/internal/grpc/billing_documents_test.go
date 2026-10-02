package grpc

import (
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

func TestRenderBillingDocumentDrawsPersistedCustomerDataAsText(t *testing.T) {
	now := time.Now().UTC()
	response, err := renderBillingDocument(billingDocumentRow{
		id: "id", kind: "invoice", number: "INV-1", amountCents: 1234,
		currency: "EUR", status: "paid", issuedAt: now, retentionUntil: now.AddDate(10, 0, 0),
	}, billingDocumentData{
		Title: "Invoice", SupplierName: "FrameWorks", Customer: `<script>alert("x") (1) \ </script>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireRuns(t, pdfTextRuns(t, response.GetContent()), `<script>alert("x") (1) \ </script>`)
	if response.GetSha256() == "" || response.GetDocument().GetDownloadFilename() != "INV-1.pdf" {
		t.Fatalf("missing document integrity metadata: %+v", response)
	}
}

func TestRenderCryptoInvoiceContainsLegalIdentityAndServiceLine(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	response, err := renderBillingDocument(billingDocumentRow{
		id: "id", kind: "crypto_invoice", number: "B2B-0000000001", amountCents: 1000,
		currency: "EUR", status: "reverse_charge", issuedAt: now, retentionUntil: now.AddDate(10, 0, 0),
	}, billingDocumentData{
		Title: "Invoice", SupplierName: "FrameWorks B.V.", SupplierAddress: "Amsterdam, NL",
		SupplierVAT: "NL000000000B01", SupplierRegistration: "12345678",
		Customer: "Erika Mustermann", CustomerCompany: "Example GmbH",
		CustomerAddress: customerAddressLines(`{"street":"Hauptstrasse 1","city":"Berlin","postal_code":"10115","country":"de"}`),
		CustomerVAT:     "DE123456789",
		Fields: []billingDocumentField{
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
	requireRuns(t, pdfTextRuns(t, response.GetContent()),
		"FrameWorks B.V.", "NL000000000B01", "12345678", "Erika Mustermann",
		"Example GmbH", "Hauptstrasse 1", "10115 Berlin", "DE", "DE123456789", "FrameWorks prepaid usage credit",
		"Quantity", "2026-08-20", "EUR 10.00", "Reverse charge — btw verlegd",
	)
}

func TestRenderBillingDocumentStatesAMissingCustomerProfile(t *testing.T) {
	now := time.Date(2026, 10, 2, 13, 58, 34, 0, time.UTC)
	response, err := renderBillingDocument(billingDocumentRow{
		id: "id", kind: "invoice", number: "INV-2", currency: "USD", status: "paid", issuedAt: now, retentionUntil: now.AddDate(10, 0, 0),
	}, billingDocumentData{Title: "Invoice", SupplierName: "FrameWorks B.V.", CustomerEmail: "billing@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	requireRuns(t, pdfTextRuns(t, response.GetContent()), "billing@example.com", "No billing name or address on file")
}

func TestCustomerAddressLinesStateTheStoredAddress(t *testing.T) {
	for stored, want := range map[string][]string{
		``:   nil,
		`{}`: nil,
		`{"street":"1 Engine Street","city":"London","postal_code":"EC1A 1BB","state":"","country":"gb"}`: {"1 Engine Street", "EC1A 1BB London", "GB"},
		`Keizersgracht 1, Amsterdam`: {"Keizersgracht 1, Amsterdam"},
	} {
		got := customerAddressLines(stored)
		if len(got) != len(want) {
			t.Fatalf("customerAddressLines(%q) = %q, want %q", stored, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("customerAddressLines(%q) = %q, want %q", stored, got, want)
			}
		}
	}
}

func TestLineItemTextStatesQuantitiesAndPrices(t *testing.T) {
	for _, test := range []struct{ value, quantity, unitPrice, amount string }{
		{"120.000000", "120", "120.00", "120.00"},
		{"0.020000000", "0.02", "0.02", "0.02"},
		{"0.000123000", "0.000123", "0.000123", "0.00"},
		{"26.350000000", "26.35", "26.35", "26.35"},
	} {
		if got := quantityText(test.value); got != test.quantity {
			t.Errorf("quantityText(%s) = %s, want %s", test.value, got, test.quantity)
		}
		if got := unitPriceText(test.value); got != test.unitPrice {
			t.Errorf("unitPriceText(%s) = %s, want %s", test.value, got, test.unitPrice)
		}
		if got := amountText(test.value); got != test.amount {
			t.Errorf("amountText(%s) = %s, want %s", test.value, got, test.amount)
		}
	}
}

func TestInvoiceTotalsStateCreditAndCollectionCarry(t *testing.T) {
	lines := []billingDocumentLine{{Description: "Usage", Amount: "3.00"}}
	labels := func(totals []billingDocumentField) []string {
		var out []string
		for _, total := range totals {
			out = append(out, total.Label+": "+total.Value)
		}
		return out
	}
	deferred := invoiceTotals(purserdb.GetInvoiceDocumentRow{
		BaseAmountCents: 0, MeteredAmountCents: 300, PrepaidCreditCents: 100, EurAmountCents: 0, CollectionMinimumApplied: true,
	}, lines, 300, "EUR", 0)
	want := []string{"Subtotal: EUR 3.00", "Prepaid credit applied: -EUR 1.00", "Carried to a later invoice (collection minimum): -EUR 2.00", "Amount due (EUR): EUR 0.00"}
	if got := labels(deferred); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("deferred totals = %q, want %q", got, want)
	}
	collected := invoiceTotals(purserdb.GetInvoiceDocumentRow{
		MeteredAmountCents: 300, EurAmountCents: 700, CollectionMinimumApplied: true,
	}, lines, 300, "USD", 820)
	want = []string{"Subtotal: EUR 3.00", "Carried from earlier invoices (collection minimum): EUR 4.00", "Amount due (EUR): EUR 7.00", "Amount due: USD 8.20"}
	if got := labels(collected); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("collected totals = %q, want %q", got, want)
	}
}
