//go:build schema_verify

package grpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

type wantBillingDocument struct {
	id, kind, number, currency, status string
	amountCents                        int64
	eurAmountCents                     *int64
	netEURCents, vatEURCents           *int64
	unitsPerEUR, fxReferenceDate       string
}

func cents(value int64) *int64 { return &value }

func optionalCents(value *int64) string {
	if value == nil {
		return "nil"
	}
	return fmt.Sprint(*value)
}

func TestListBillingDocumentsUnionStatesChargedAndEURAmounts_RealPG(t *testing.T) { //nolint:funlen // One engine run covers every document kind, tenant isolation, ordering, and the page limit.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	tenantID := uuid.NewString()
	otherTenantID := uuid.NewString()
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}

	usdInvoiceID := uuid.NewString()
	exec(`INSERT INTO purser.billing_invoices (id, tenant_id, invoice_number, status, currency, amount, due_date, created_at,
	          presentment_amount_cents, presentment_currency, presentment_units_per_eur, presentment_reference_date, finalized_at)
	      VALUES ($1, $2, 'INV-USD-1', 'pending', 'EUR', 249.46, $3, $3, 29218, 'USD', 1.1712, DATE '2026-09-01', $3)`,
		usdInvoiceID, tenantID, base.AddDate(0, 0, -5))
	eurInvoiceID := uuid.NewString()
	exec(`INSERT INTO purser.billing_invoices (id, tenant_id, invoice_number, status, currency, amount, due_date, created_at,
	          presentment_amount_cents, presentment_currency, presentment_units_per_eur, presentment_reference_date, finalized_at)
	      VALUES ($1, $2, 'INV-EUR-1', 'paid', 'EUR', 10.00, $3, $3, 1000, 'EUR', 1, DATE '2026-08-01', $3)`,
		eurInvoiceID, tenantID, base.AddDate(0, 0, -40))
	legacyInvoiceID := uuid.NewString()
	exec(`INSERT INTO purser.billing_invoices (id, tenant_id, invoice_number, status, currency, amount, due_date, created_at)
	      VALUES ($1, $2, 'INV-LEGACY-1', 'paid', 'EUR', 5.00, $3, $3)`,
		legacyInvoiceID, tenantID, base.AddDate(0, 0, -60))
	exec(`INSERT INTO purser.billing_invoices (tenant_id, invoice_number, status, currency, amount, due_date, created_at)
	      VALUES ($1, 'INV-DRAFT-1', 'draft', 'EUR', 7.00, $2, $2)`, tenantID, base)

	receiptID := uuid.NewString()
	exec(`INSERT INTO purser.billing_payments (id, invoice_id, method, amount, currency, tx_id, status, confirmed_at, created_at,
	          original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date)
	      VALUES ($1, $2, 'card', 292.18, 'USD', 'pi_usd', 'confirmed', $3, $3, 29218, 'USD', 24946, 1.1712, 'ecb', DATE '2026-09-01')`,
		receiptID, usdInvoiceID, base.AddDate(0, 0, -4))
	exec(`INSERT INTO purser.billing_payments (invoice_id, method, amount, currency, tx_id, status, created_at,
	          original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date)
	      VALUES ($1, 'card', 292.18, 'USD', 'pi_pending', 'pending', $2, 29218, 'USD', 24946, 1.1712, 'ecb', DATE '2026-09-01')`,
		usdInvoiceID, base)

	simplifiedID := uuid.NewString()
	insertSimplified := func(id, number, status string, issuedAt time.Time) {
		exec(`INSERT INTO purser.simplified_invoices (id, invoice_number, tenant_id, reference_type, reference_id,
		          gross_amount_cents, net_amount_cents, vat_amount_cents, vat_rate_bps, tax_validation_status, currency,
		          amount_eur_cents, net_eur_cents, vat_eur_cents, fx_units_per_eur, fx_reference_date,
		          supplier_name, supplier_address, supplier_vat_number, issued_at)
		      VALUES ($1, $2, $3, 'x402', $2, 1210, 1000, 210, 2100, $4, 'USD',
		          1034, 855, 179, 1.17, DATE '2026-09-10', 'Supplier', 'Address', 'NL000', $5)`,
			id, number, tenantID, status, issuedAt)
	}
	insertSimplified(simplifiedID, "SI-USD-1", "not_validated", base.AddDate(0, 0, -3))
	insertSimplified(uuid.NewString(), "SI-REVIEW-1", "location_review", base)

	cryptoID := uuid.NewString()
	exec(`INSERT INTO purser.crypto_invoices (id, invoice_number, tenant_id, reference_type, reference_id,
	          gross_amount_cents, net_amount_cents, vat_amount_cents, vat_rate_bps, tax_validation_status, currency,
	          amount_eur_cents, net_eur_cents, vat_eur_cents, fx_units_per_eur, fx_reference_date, evidence_status,
	          supplier_name, supplier_address, supplier_vat_number, supplier_registration_number,
	          service_description, service_quantity, service_date, customer_email, customer_name, customer_address, issued_at)
	      VALUES ($1, 'CI-EUR-1', $2, 'crypto', '0xabc', 2420, 2000, 420, 2100, 'validated', 'EUR',
	          2420, 2000, 420, 1, DATE '2026-09-11', 'complete',
	          'Supplier', 'Address', 'NL000', 'KVK1', 'Prepaid credit', 1, DATE '2026-09-14',
	          'billing@example.com', 'Customer', '{}'::jsonb, $3)`,
		cryptoID, tenantID, base.AddDate(0, 0, -2))

	lowNoteID := "00000000-0000-4000-8000-000000000001"
	highNoteID := "00000000-0000-4000-8000-000000000002"
	insertCreditNote := func(id, tenant, number string, issuedAt time.Time) {
		exec(`INSERT INTO purser.credit_notes (id, credit_note_number, tenant_id, source_document_type, source_document_id,
		          reversal_reference_type, reversal_reference_id, amount_cents, currency, reason, issued_at)
		      VALUES ($1, $2, $3, 'invoice', $4, 'refund', $2, 500, 'USD', 'refund', $5)`,
			id, number, tenant, usdInvoiceID, issuedAt)
	}
	insertCreditNote(lowNoteID, tenantID, "CN-TIE-LOW", base.AddDate(0, 0, -1))
	insertCreditNote(highNoteID, tenantID, "CN-TIE-HIGH", base.AddDate(0, 0, -1))
	exec(`INSERT INTO purser.billing_invoices (tenant_id, invoice_number, status, currency, amount, due_date, created_at)
	      VALUES ($1, 'INV-OTHER-1', 'pending', 'EUR', 99.00, $2, $2)`, otherTenantID, base)
	insertCreditNote(uuid.NewString(), otherTenantID, "CN-OTHER-1", base)

	want := []wantBillingDocument{
		{id: highNoteID, kind: "credit_note", number: "CN-TIE-HIGH", currency: "USD", status: "issued", amountCents: 500},
		{id: lowNoteID, kind: "credit_note", number: "CN-TIE-LOW", currency: "USD", status: "issued", amountCents: 500},
		{id: cryptoID, kind: "crypto_invoice", number: "CI-EUR-1", currency: "EUR", status: "validated", amountCents: 2420,
			eurAmountCents: cents(2420), netEURCents: cents(2000), vatEURCents: cents(420), unitsPerEUR: "1", fxReferenceDate: "2026-09-11"},
		{id: simplifiedID, kind: "simplified_invoice", number: "SI-USD-1", currency: "USD", status: "not_validated", amountCents: 1210,
			eurAmountCents: cents(1034), netEURCents: cents(855), vatEURCents: cents(179), unitsPerEUR: "1.17", fxReferenceDate: "2026-09-10"},
		{id: receiptID, kind: "payment_receipt", currency: "USD", status: "confirmed", amountCents: 29218,
			eurAmountCents: cents(24946), unitsPerEUR: "1.1712", fxReferenceDate: "2026-09-01"},
		{id: usdInvoiceID, kind: "invoice", number: "INV-USD-1", currency: "USD", status: "pending", amountCents: 29218,
			eurAmountCents: cents(24946), unitsPerEUR: "1.1712", fxReferenceDate: "2026-09-01"},
		{id: eurInvoiceID, kind: "invoice", number: "INV-EUR-1", currency: "EUR", status: "paid", amountCents: 1000,
			eurAmountCents: cents(1000), unitsPerEUR: "1", fxReferenceDate: "2026-08-01"},
		{id: legacyInvoiceID, kind: "invoice", number: "INV-LEGACY-1", currency: "EUR", status: "paid", amountCents: 500},
	}

	response, err := server.ListBillingDocuments(serviceTestContext(), &purserpb.ListBillingDocumentsRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("ListBillingDocuments: %v", err)
	}
	documents := response.GetDocuments()
	if len(documents) != len(want) {
		for _, document := range documents {
			t.Logf("got %s %s %s", document.GetKind(), document.GetDocumentNumber(), document.GetIssuedAt().AsTime())
		}
		t.Fatalf("documents = %d, want %d (drafts, pending payments, location-review documents and other tenants excluded)", len(documents), len(want))
	}
	for index, expected := range want {
		got := documents[index]
		number := expected.number
		if expected.kind == "payment_receipt" {
			number = got.GetDocumentNumber()
			if len(number) != len("PAY-")+12 {
				t.Errorf("receipt number = %q", number)
			}
		}
		if got.GetId() != expected.id || got.GetKind() != expected.kind || got.GetDocumentNumber() != number ||
			got.GetCurrency() != expected.currency || got.GetStatus() != expected.status || got.GetAmountCents() != expected.amountCents {
			t.Errorf("document %d = %s %s %s %s %s %d, want %s %s %s %s %s %d", index,
				got.GetId(), got.GetKind(), got.GetDocumentNumber(), got.GetCurrency(), got.GetStatus(), got.GetAmountCents(),
				expected.id, expected.kind, number, expected.currency, expected.status, expected.amountCents)
		}
		if optionalCents(got.EurAmountCents) != optionalCents(expected.eurAmountCents) ||
			optionalCents(got.NetEurCents) != optionalCents(expected.netEURCents) ||
			optionalCents(got.VatEurCents) != optionalCents(expected.vatEURCents) ||
			got.GetUnitsPerEur() != expected.unitsPerEUR || got.GetFxReferenceDate() != expected.fxReferenceDate {
			t.Errorf("%s %s EUR fields = eur %s net %s vat %s units %q date %q, want eur %s net %s vat %s units %q date %q",
				expected.kind, number, optionalCents(got.EurAmountCents), optionalCents(got.NetEurCents), optionalCents(got.VatEurCents),
				got.GetUnitsPerEur(), got.GetFxReferenceDate(), optionalCents(expected.eurAmountCents), optionalCents(expected.netEURCents),
				optionalCents(expected.vatEURCents), expected.unitsPerEUR, expected.fxReferenceDate)
		}
	}

	other, err := server.ListBillingDocuments(serviceTestContext(), &purserpb.ListBillingDocumentsRequest{TenantId: otherTenantID})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.GetDocuments()) != 2 {
		t.Fatalf("other tenant documents = %d, want its own invoice and credit note", len(other.GetDocuments()))
	}
	for _, document := range other.GetDocuments() {
		if document.GetDocumentNumber() != "INV-OTHER-1" && document.GetDocumentNumber() != "CN-OTHER-1" {
			t.Fatalf("other tenant sees %s", document.GetDocumentNumber())
		}
	}

	pagedTenantID := uuid.NewString()
	exec(`INSERT INTO purser.credit_notes (credit_note_number, tenant_id, source_document_type, source_document_id,
	          reversal_reference_type, reversal_reference_id, amount_cents, currency, reason, issued_at)
	      SELECT 'CN-PAGE-' || LPAD(n::text, 4, '0'), $1, 'invoice', $2, 'refund', 'page-' || n, 100, 'EUR', 'refund',
	             $3::timestamptz + n * INTERVAL '1 second'
	      FROM generate_series(1, 1001) AS n`, pagedTenantID, usdInvoiceID, base)
	paged, err := server.ListBillingDocuments(serviceTestContext(), &purserpb.ListBillingDocumentsRequest{TenantId: pagedTenantID})
	if err != nil {
		t.Fatal(err)
	}
	pagedDocuments := paged.GetDocuments()
	if len(pagedDocuments) != 1000 {
		t.Fatalf("paged documents = %d, want the 1000 limit", len(pagedDocuments))
	}
	if first, last := pagedDocuments[0].GetDocumentNumber(), pagedDocuments[999].GetDocumentNumber(); first != "CN-PAGE-1001" || last != "CN-PAGE-0002" {
		t.Fatalf("paged window = %s .. %s, want the newest 1000 (CN-PAGE-1001 .. CN-PAGE-0002)", first, last)
	}
}
