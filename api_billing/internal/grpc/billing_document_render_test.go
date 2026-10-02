package grpc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/sfnt"
)

func sampleInvoiceDocument() (billingDocumentRow, billingDocumentData) {
	issued := time.Date(2026, 10, 2, 13, 58, 34, 0, time.UTC)
	row := billingDocumentRow{
		id: "id", kind: "invoice", number: "INV-0000283601", amountCents: 8352,
		currency: "USD", status: "pending", issuedAt: issued, retentionUntil: issued.AddDate(10, 0, 0),
	}
	data := billingDocumentData{
		Title: "Invoice", SupplierName: "FrameWorks B.V.", SupplierAddress: "Keizersgracht 1, 1015 CJ Amsterdam, NL",
		SupplierVAT: "NL000000000B01", SupplierRegistration: "12345678",
		Customer: "Ada Lovelace", CustomerCompany: "Analytical Engines Ltd",
		CustomerAddress: []string{"1 Engine Street", "EC1A 1BB London", "United Kingdom (GB)"},
		CustomerVAT:     "GB123456789", CustomerEmail: "billing@example.com",
		Fields: []billingDocumentField{
			{Label: "Billing period", Value: "2026-09-22 08:00 UTC to 2026-10-02 13:58 UTC"},
			{Label: "Due", Value: "2026-10-16"},
		},
		LineCurrency: "EUR",
		Lines: []billingDocumentLine{
			{Description: "Supporter base fee 2026-09-22 to 2026-10-02", Quantity: "1", UnitPrice: "26.35", Amount: "26.35"},
			{Description: "Delivered bandwidth", Detail: "region: eu · cluster cluster-eu-1", Quantity: "120 gibibyte", UnitPrice: "0.02", Amount: "2.40"},
		},
		Totals: []billingDocumentField{
			{Label: "Subtotal", Value: "EUR 28.75"},
			{Label: "Prepaid credit applied", Value: "-EUR 10.00"},
			{Label: "Amount due (EUR)", Value: "EUR 18.75"},
			{Label: "Amount due", Value: "USD 83.52"},
		},
	}
	return row, data
}

func TestRenderBillingDocumentIsAPDFStatingLinesCreditAndParties(t *testing.T) {
	row, data := sampleInvoiceDocument()
	response, err := renderBillingDocument(row, data)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetContentType() != "application/pdf" || response.GetDocument().GetDownloadFilename() != "INV-0000283601.pdf" {
		t.Fatalf("content type %q filename %q, want application/pdf and INV-0000283601.pdf",
			response.GetContentType(), response.GetDocument().GetDownloadFilename())
	}
	digest := sha256.Sum256(response.GetContent())
	if response.GetSha256() != hex.EncodeToString(digest[:]) {
		t.Fatal("sha256 does not bind the PDF bytes")
	}
	content := response.GetContent()
	if !bytes.Contains(content, []byte("/FontFile2")) {
		t.Fatal("the PDF does not embed its font")
	}
	runs := pdfTextRuns(t, content)
	requireRuns(t, runs,
		"Invoice", "INV-0000283601", "Issued 2026-10-02 13:58:34 UTC", "USD 83.52", "pending",
		"Supplier", "FrameWorks B.V.", "Keizersgracht 1, 1015 CJ Amsterdam, NL", "VAT NL000000000B01", "Registration 12345678",
		"Customer", "Ada Lovelace", "Analytical Engines Ltd", "1 Engine Street", "EC1A 1BB London", "United Kingdom (GB)",
		"VAT GB123456789", "billing@example.com",
		"Billing period", "2026-09-22 08:00 UTC to 2026-10-02 13:58 UTC",
		"Description", "Quantity", "Unit price", "Amount (EUR)",
		"Supporter base fee 2026-09-22 to 2026-10-02", "26.35",
		"Delivered bandwidth", "region: eu · cluster cluster-eu-1", "120 gibibyte", "0.02", "2.40",
		"Subtotal", "EUR 28.75", "Prepaid credit applied", "-EUR 10.00", "Amount due (EUR)", "EUR 18.75",
		"Retained until at least 2036-10-02",
	)
}

func TestRenderBillingDocumentIsDeterministic(t *testing.T) {
	row, data := sampleInvoiceDocument()
	first, err := renderBillingDocument(row, data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderBillingDocument(row, data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.GetContent(), second.GetContent()) || first.GetSha256() != second.GetSha256() {
		t.Fatal("rendering the same document twice produced different bytes")
	}
}

func TestRenderBillingDocumentDrawsTextOutsideLatin1(t *testing.T) {
	row, data := sampleInvoiceDocument()
	data.Customer = "Zoë Ångström-Łukasiewicz"
	data.Fields = append(data.Fields, billingDocumentField{Label: "Note", Value: "Charged in € at the ECB rate — see below"})
	response, err := renderBillingDocument(row, data)
	if err != nil {
		t.Fatal(err)
	}
	requireRuns(t, pdfTextRuns(t, response.GetContent()), "Zoë Ångström-Łukasiewicz", "Charged in € at the ECB rate — see below")

	for _, font := range [][]byte{billingDocumentRegularFont, billingDocumentBoldFont} {
		parsed, err := sfnt.Parse(font)
		if err != nil {
			t.Fatal(err)
		}
		var buffer sfnt.Buffer
		for _, r := range "€£$—·ëÅŁ" {
			if index, err := parsed.GlyphIndex(&buffer, r); err != nil || index == 0 {
				t.Errorf("document font has no glyph for %q", r)
			}
		}
	}
}

func TestRenderBillingDocumentBreaksLongLineTablesAcrossPages(t *testing.T) {
	row, data := sampleInvoiceDocument()
	for i := range 80 {
		data.Lines = append(data.Lines, billingDocumentLine{
			Description: "Storage line " + strings.Repeat("x", i%7), Quantity: "1", UnitPrice: "0.01", Amount: "0.01",
		})
	}
	response, err := renderBillingDocument(row, data)
	if err != nil {
		t.Fatal(err)
	}
	pages := bytes.Count(response.GetContent(), []byte("/Type /Page\n")) + bytes.Count(response.GetContent(), []byte("/Type /Page "))
	if pages < 2 {
		t.Fatalf("pages = %d, want the line table continued on a later page", pages)
	}
	runs := pdfTextRuns(t, response.GetContent())
	headers := 0
	for _, run := range runs {
		if run == "Unit price" {
			headers++
		}
	}
	if headers < 2 {
		t.Fatalf("line table header drawn %d times, want it repeated on each page", headers)
	}
	requireRuns(t, runs, "Amount due (EUR)", "Page 2 of")
}
