//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"

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

// A finalized postpaid invoice downloads as a PDF that states its base fee
// with the period it covers, its usage lines, the prepaid credit applied, the
// totals and the amount due, the customer's billing details and the supplier.
// It is issued when it was finalized, not when its draft was written.
func TestInvoiceDocumentStatesLinesCreditAndBothParties_RealPG(t *testing.T) { //nolint:funlen // One persisted invoice and its full document.
	appconfigtest.Set(t, "SUPPLIER_NAME", "FrameWorks B.V.")
	appconfigtest.Set(t, "SUPPLIER_ADDRESS", "Amsterdam, NL")
	appconfigtest.Set(t, "SUPPLIER_VAT_NUMBER", "NL000000000B01")
	appconfigtest.Set(t, "SUPPLIER_REGISTRATION_NUMBER", "12345678")
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	tenantID, tierID, invoiceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	periodStart := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 10, 2, 13, 58, 9, 0, time.UTC)
	draftWritten := time.Date(2026, 9, 22, 8, 5, 0, 0, time.UTC)
	finalized := time.Date(2026, 10, 2, 13, 58, 34, 0, time.UTC)
	exec(`INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level)
	      VALUES ($1, 'supporter', 'Supporter', 79.00, 'EUR', 2)`, tierID)
	exec(`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_name, billing_company, billing_address, tax_id, presentment_currency)
	      VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', 'Ada Lovelace', 'Analytical Engines Ltd',
	              '{"street":"1 Engine Street","city":"London","postal_code":"EC1A 1BB","country":"GB"}', 'GB123456789', 'USD')`, tenantID, tierID)
	exec(`INSERT INTO purser.billing_invoices (id, tenant_id, invoice_number, status, currency, amount, base_amount, metered_amount, gross_metered_amount,
	          prepaid_credit_applied, usage_details, period_start, period_end, due_date, created_at,
	          presentment_amount_cents, presentment_currency, presentment_units_per_eur, presentment_reference_date, finalized_at)
	      VALUES ($1, $2, 'INV-0000283601', 'pending', 'EUR', 18.75, 26.35, 2.40, 2.40, 10.00, '{}', $3, $4, $5, $6,
	          2194, 'USD', 1.1700000000, ($7::timestamptz)::date, $7::timestamptz)`,
		invoiceID, tenantID, periodStart, periodEnd, periodEnd.AddDate(0, 0, 14), draftWritten, finalized)
	exec(`INSERT INTO purser.invoice_line_items (invoice_id, tenant_id, line_key, meter, unit, dimensions, description, quantity, billable_quantity, unit_price, amount, currency, cluster_id, cluster_kind, pricing_source)
	      VALUES ($1, $2, 'base_subscription', NULL, '', '{}', 'Supporter 2026-09-22 to 2026-10-02', 1, 1, 26.35, 26.35, 'EUR', NULL, NULL, 'tier'),
	             ($1, $2, 'meter:egress_gb', 'egress_gb', 'gibibyte', '{"region":"eu"}', 'Delivered bandwidth', 120, 120, 0.02, 2.40, 'EUR', 'cluster-eu-1', 'platform_official', 'cluster_metered')`,
		invoiceID, tenantID)

	response, err := server.GetBillingDocument(serviceTestContext(), &purserpb.GetBillingDocumentRequest{
		TenantId: tenantID, DocumentId: invoiceID, Kind: "invoice",
	})
	if err != nil {
		t.Fatalf("GetBillingDocument: %v", err)
	}
	document := response.GetDocument()
	if response.GetContentType() != "application/pdf" || document.GetDownloadFilename() != "INV-0000283601.pdf" {
		t.Fatalf("invoice downloads as %q %q, want application/pdf INV-0000283601.pdf", response.GetContentType(), document.GetDownloadFilename())
	}
	if !document.GetIssuedAt().AsTime().Equal(finalized) || document.GetAmountCents() != 2194 || document.GetCurrency() != "USD" {
		t.Fatalf("invoice metadata = issued %s amount %d %s, want issued at finalization %s, USD 21.94",
			document.GetIssuedAt().AsTime(), document.GetAmountCents(), document.GetCurrency(), finalized)
	}
	listed, err := server.ListBillingDocuments(serviceTestContext(), &purserpb.ListBillingDocumentsRequest{TenantId: tenantID})
	if err != nil || len(listed.GetDocuments()) != 1 || !listed.GetDocuments()[0].GetIssuedAt().AsTime().Equal(finalized) ||
		listed.GetDocuments()[0].GetDownloadFilename() != "INV-0000283601.pdf" {
		t.Fatalf("listed documents = %+v, %v; want the invoice issued at its finalization as a PDF", listed.GetDocuments(), err)
	}
	requireRuns(t, pdfTextRuns(t, response.GetContent()),
		"Invoice", "INV-0000283601", "Issued 2026-10-02 13:58:34 UTC", "USD 21.94",
		"FrameWorks B.V.", "VAT NL000000000B01", "Registration 12345678",
		"Ada Lovelace", "Analytical Engines Ltd", "1 Engine Street", "EC1A 1BB London", "GB", "VAT GB123456789", "billing@example.com",
		"Billing period", "2026-09-22 08:00 UTC to 2026-10-02 13:58 UTC", "Due", "2026-10-16",
		"Supporter 2026-09-22 to 2026-10-02", "26.35",
		"Delivered bandwidth", "region: eu", "cluster-eu-1", "120 gibibyte", "0.02", "2.40",
		"Subtotal", "EUR 28.75", "Prepaid credit applied", "-EUR 10.00", "Amount due (EUR)", "EUR 18.75",
		"Amount due", "USD 21.94", "1 EUR = 1.17 USD",
	)
}

// A finalized document states the customer and supplier as they were when it
// was issued. The postpaid phase's closing invoice keeps the billing address,
// name and supplier address it was finalized with after the tenant and the
// operator change theirs; the receipt issued after the change states the new
// ones.
func TestFinalizedDocumentKeepsThePartiesItWasIssuedTo_RealPG(t *testing.T) { //nolint:funlen // One finalization, one change of both parties, and two documents.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	appconfigtest.Set(t, "SUPPLIER_NAME", "FrameWorks B.V.")
	appconfigtest.Set(t, "SUPPLIER_ADDRESS", "Keizersgracht 1, Amsterdam, NL")
	appconfigtest.Set(t, "SUPPLIER_VAT_NUMBER", "NL000000000B01")
	appconfigtest.Set(t, "SUPPLIER_REGISTRATION_NUMBER", "12345678")
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tenantID, paygID, supporterID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	periodStart := time.Now().UTC().Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid, metering_enabled)
		VALUES ($1, 'payg', 'Pay As You Go', 0, 'EUR', 0, true, true), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false, true)`, paygID, supporterID)
	exec(`INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
		VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.01, '{}'), ($2, 'egress_gb', 'all_usage', 'EUR', 0, 0.02, '{}')`, paygID, supporterID)
	exec(`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_name, billing_address, tax_id,
		          billing_period_start, billing_period_end, presentment_currency)
		VALUES ($1, $2, 'active', 'postpaid', 'ada@example.com', 'Ada Lovelace',
		        '{"street":"Prinsengracht 263","city":"Amsterdam","postal_code":"1016 GV","country":"NL"}', 'NL111111111B01', $3, $4, 'EUR')`,
		tenantID, supporterID, periodStart, periodStart.AddDate(0, 1, 0))
	exec(`INSERT INTO purser.usage_records (
			tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
			source_id, report_id, usage_value, usage_details,
			period_start, period_end, granularity, value_kind
		) VALUES ($1, '', 'egress_gb', 'gibibyte', '{}', $2, 'periscope-default', $3, 150, '{}', $4, $5, 'minute_5', 'delta')`,
		tenantID, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), strings.Repeat("7", 64), periodStart.Add(2*time.Hour), periodStart.Add(2*time.Hour+5*time.Minute))

	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	operator := operatorAssignCtx("7a000000-0000-4000-8000-000000000005")
	if _, err := server.AdminAssignTier(operator, &purserpb.AdminAssignTierRequest{TenantId: tenantID, TierName: "payg", Reason: "moves to pay as you go"}); err != nil {
		t.Fatalf("AdminAssignTier: %v", err)
	}
	var invoiceID string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.billing_invoices WHERE tenant_id = $1 AND document_kind = 'invoice' AND finalized_at IS NOT NULL`,
		tenantID).Scan(&invoiceID); err != nil {
		t.Fatalf("read the finalized closing invoice: %v", err)
	}

	name, street, city, postal := "Ada King", "Unter den Linden 1", "Berlin", "10117"
	if _, err := server.UpdateBillingDetails(ctx, &purserpb.UpdateBillingDetailsRequest{
		TenantId: tenantID, Name: &name,
		Address: &purserpb.BillingAddress{Street: street, City: city, PostalCode: postal, Country: "DE"},
	}); err != nil {
		t.Fatalf("UpdateBillingDetails: %v", err)
	}
	appconfigtest.Set(t, "SUPPLIER_ADDRESS", "Herengracht 2, Amsterdam, NL")

	render := func(kind, id string) []string {
		t.Helper()
		response, err := server.GetBillingDocument(serviceTestContext(), &purserpb.GetBillingDocumentRequest{TenantId: tenantID, DocumentId: id, Kind: kind})
		if err != nil {
			t.Fatalf("GetBillingDocument(%s): %v", kind, err)
		}
		return pdfTextRuns(t, response.GetContent())
	}
	invoice := render("invoice", invoiceID)
	requireRuns(t, invoice, "Ada Lovelace", "Prinsengracht 263", "1016 GV Amsterdam", "NL", "VAT NL111111111B01", "ada@example.com", "Keizersgracht 1, Amsterdam, NL")
	forbidRuns(t, invoice, "Ada King", "Unter den Linden 1", "Berlin", "Herengracht 2")

	paid, err := server.AdminRecordInvoicePayment(operator, &purserpb.AdminRecordInvoicePaymentRequest{
		TenantId: tenantID, InvoiceId: invoiceID, Reference: "bank transfer 1", Reason: "paid by transfer",
	})
	if err != nil {
		t.Fatalf("AdminRecordInvoicePayment: %v", err)
	}
	receipt := render("payment_receipt", paid.GetPaymentId())
	requireRuns(t, receipt, "Ada King", "Unter den Linden 1", "10117 Berlin", "DE", "VAT NL111111111B01", "Herengracht 2, Amsterdam, NL")
	forbidRuns(t, receipt, "Ada Lovelace", "Prinsengracht 263", "Keizersgracht 1")

	again := render("invoice", invoiceID)
	requireRuns(t, again, "Prinsengracht 263", "Keizersgracht 1, Amsterdam, NL")
}

// forbidRuns fails when any of the texts appears within a drawn run.
func forbidRuns(t *testing.T, runs []string, forbidden ...string) {
	t.Helper()
	for _, text := range forbidden {
		for _, run := range runs {
			if strings.Contains(run, text) {
				t.Errorf("PDF text states %q in %q", text, run)
			}
		}
	}
}
