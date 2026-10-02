//go:build schema_verify

package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

// The diagnostic lists the invoices an earlier month end wrote for tenants
// that were prepaid in the period: they rated usage the balance had already
// paid. Invoices that leave prepaid-settled usage out, statements, and
// invoices of tenants that were never prepaid are not listed.
func TestAdminListPrepaidDoubleChargesFindsPastFinalizations_RealPG(t *testing.T) { //nolint:funlen // Seeds every document shape the diagnostic must tell apart.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	settle := func(t *testing.T, tenantID string, start, end time.Time, micro int64) {
		t.Helper()
		exec(t, `INSERT INTO purser.prepaid_usage_settlements (report_id, tenant_id, billing_period_start, billing_period_end, amount_micro, cumulative_amount_micro, currency)
			VALUES ($1, $2, $3, $4, $5, $5, 'EUR')`, strings.ReplaceAll(uuid.NewString(), "-", ""), tenantID, start, end, micro)
	}
	invoice := func(t *testing.T, tenantID, kind, amount, credit, metered, details string, start, end time.Time) string {
		t.Helper()
		id := uuid.NewString()
		exec(t, `INSERT INTO purser.billing_invoices (id, tenant_id, document_kind, status, amount, currency, due_date, base_amount, metered_amount, prepaid_credit_applied, usage_details, period_start, period_end)
			VALUES ($1, $2, $3, 'paid', $4::numeric, 'EUR', $7, 0, $6::numeric, $5::numeric, $8::jsonb, $9, $10)`,
			id, tenantID, kind, amount, credit, metered, end.AddDate(0, 0, 14), details, start, end)
		exec(t, `INSERT INTO purser.invoice_line_items (invoice_id, tenant_id, line_key, description, quantity, included_quantity, billable_quantity, unit_price, amount, currency, pricing_source)
			VALUES ($1, $2, 'base_subscription', 'Base subscription', 1, 0, 1, 0, 0, 'EUR', 'tier'),
			       ($1, $2, 'egress_gb', 'Egress', 200, 0, 200, 0.01, $3::numeric, 'EUR', 'tier')`, id, tenantID, metered)
		return id
	}

	// A prepaid tenant whose balance paid 2.00 of usage in August, and whose
	// August invoice rated that 2.00 again and took it as invoice credit.
	doubleCharged := uuid.NewString()
	settle(t, doubleCharged, periodStart, periodEnd, 2_000_000)
	doubleChargedInvoice := invoice(t, doubleCharged, "invoice", "0.00", "2.00", "2.00", `{}`, periodStart, periodEnd)

	// A second one in July, charged to the card instead of invoice credit.
	july := periodStart.AddDate(0, -1, 0)
	cardCharged := uuid.NewString()
	settle(t, cardCharged, july, periodStart, 1_500_000)
	invoice(t, cardCharged, "invoice", "1.50", "0.00", "1.50", `{}`, july, periodStart)

	// Not listed: a statement; an invoice that leaves prepaid-settled usage
	// out; a tenant that was never prepaid.
	stated := uuid.NewString()
	settle(t, stated, periodStart, periodEnd, 2_000_000)
	invoice(t, stated, "prepaid_statement", "0.00", "0.00", "2.00", `{"statement":{}}`, periodStart, periodEnd)
	switched := uuid.NewString()
	settle(t, switched, periodStart, periodEnd, 2_000_000)
	invoice(t, switched, "invoice", "3.00", "0.00", "3.00", `{"prepaid_settled_usage_excluded":true}`, periodStart.Add(10*24*time.Hour), periodEnd)
	postpaid := uuid.NewString()
	invoice(t, postpaid, "invoice", "2.00", "0.00", "2.00", `{}`, periodStart, periodEnd)

	server := &PurserServer{db: db, logger: logging.NewLogger()}
	operator := operatorAssignCtx("7a000000-0000-4000-8000-000000000003")
	resp, err := server.AdminListPrepaidDoubleCharges(operator, &purserpb.AdminListPrepaidDoubleChargesRequest{})
	if err != nil {
		t.Fatalf("AdminListPrepaidDoubleCharges: %v", err)
	}
	if len(resp.GetCharges()) != 2 || resp.GetTotalDoubleChargedCents() != 350 || resp.GetTruncated() {
		t.Fatalf("charges = %+v total %d truncated %v, want the July and August invoices, 3.50 in total", resp.GetCharges(), resp.GetTotalDoubleChargedCents(), resp.GetTruncated())
	}
	august := resp.GetCharges()[1]
	if august.GetTenantId() != doubleCharged || august.GetInvoiceId() != doubleChargedInvoice || august.GetDoubleChargedCents() != 200 ||
		august.GetInvoiceCreditCents() != 200 || august.GetInvoiceUsageCents() != 200 || august.GetPrepaidUsagePaidCents() != 200 ||
		!august.GetPeriodStart().AsTime().Equal(periodStart) || !august.GetPeriodEnd().AsTime().Equal(periodEnd) {
		t.Fatalf("August double charge = %+v", august)
	}
	if july := resp.GetCharges()[0]; july.GetTenantId() != cardCharged || july.GetDoubleChargedCents() != 150 || july.GetInvoiceAmountCents() != 150 || july.GetInvoiceCreditCents() != 0 {
		t.Fatalf("July double charge = %+v", july)
	}

	one, err := server.AdminListPrepaidDoubleCharges(operator, &purserpb.AdminListPrepaidDoubleChargesRequest{TenantId: doubleCharged})
	if err != nil || len(one.GetCharges()) != 1 || one.GetCharges()[0].GetInvoiceId() != doubleChargedInvoice {
		t.Fatalf("tenant filter = %+v, %v", one, err)
	}
	limited, err := server.AdminListPrepaidDoubleCharges(operator, &purserpb.AdminListPrepaidDoubleChargesRequest{Limit: 1})
	if err != nil || len(limited.GetCharges()) != 1 || !limited.GetTruncated() {
		t.Fatalf("limit 1 = %+v, %v; want one invoice and truncated", limited, err)
	}
	if n := purserCount(t, db, `SELECT count(*) FROM purser.balance_transactions`); n != 0 {
		t.Fatalf("the diagnostic wrote %d balance transactions, want none", n)
	}
}

// A prepaid statement is listed and downloaded as a statement, never as an
// invoice, and its document says nothing is due. It is issued when it was
// finalized, after the period it closes, also when it was converted from a
// draft written earlier in the period.
func TestPrepaidStatementIsABillingDocumentNotAnInvoice_RealPG(t *testing.T) {
	appconfigtest.Set(t, "SUPPLIER_NAME", "FrameWorks B.V.")
	appconfigtest.Set(t, "SUPPLIER_ADDRESS", "Amsterdam, NL")
	appconfigtest.Set(t, "SUPPLIER_VAT_NUMBER", "NL000000000B01")
	appconfigtest.Set(t, "SUPPLIER_REGISTRATION_NUMBER", "12345678")
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tenantID, statementID := uuid.NewString(), uuid.NewString()
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 8, 20, 13, 58, 9, 0, time.UTC)
	draftWritten := time.Date(2026, 8, 20, 2, 49, 27, 0, time.UTC)
	finalized := time.Date(2026, 8, 20, 13, 58, 34, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_invoices (id, tenant_id, invoice_number, document_kind, status, amount, currency, due_date, base_amount, metered_amount, prepaid_credit_applied, usage_details, period_start, period_end,
		                                     presentment_amount_cents, presentment_currency, presentment_units_per_eur, presentment_reference_date, created_at, finalized_at)
		VALUES ($1, $2, 'STM-0000000042', 'prepaid_statement', 'paid', 0, 'EUR', $3, 0, 2.00, 0,
		        '{"statement":{"rated_usage_cents":200,"paid_from_balance_cents":200,"opening_balance_cents":0,"topup_cents":1000,"topups":1,"usage_posted_cents":200,"period_end_balance_cents":800,"closing_balance_cents":800}}',
		        $4, $3, 0, 'EUR', 1, ($6::timestamptz)::date, $5, $6::timestamptz)`, statementID, tenantID, periodEnd, periodStart, draftWritten, finalized); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.invoice_line_items (invoice_id, tenant_id, line_key, meter, unit, description, quantity, billable_quantity, unit_price, amount, currency)
		VALUES ($1, $2, 'meter:egress_gb', 'egress_gb', 'gibibyte', 'Delivered bandwidth', 200, 200, 0.01, 2.00, 'EUR')`, statementID, tenantID); err != nil {
		t.Fatal(err)
	}
	tenantCtx := context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	documents, err := server.ListBillingDocuments(tenantCtx, &purserpb.ListBillingDocumentsRequest{})
	if err != nil || len(documents.GetDocuments()) != 1 || documents.GetDocuments()[0].GetKind() != "prepaid_statement" {
		t.Fatalf("billing documents = %+v, %v; want the statement listed as prepaid_statement", documents, err)
	}
	invoices, err := server.ListInvoices(tenantCtx, &purserpb.ListInvoicesRequest{TenantId: tenantID})
	if err != nil || len(invoices.GetInvoices()) != 0 {
		t.Fatalf("invoices = %+v, %v; a statement is not an invoice", invoices, err)
	}
	if _, err := server.GetBillingDocument(tenantCtx, &purserpb.GetBillingDocumentRequest{DocumentId: statementID, Kind: "invoice"}); err == nil {
		t.Fatal("the statement downloaded as an invoice")
	}
	document, err := server.GetBillingDocument(tenantCtx, &purserpb.GetBillingDocumentRequest{DocumentId: statementID, Kind: "prepaid_statement"})
	if err != nil {
		t.Fatalf("GetBillingDocument: %v", err)
	}
	if !document.GetDocument().GetIssuedAt().AsTime().Equal(finalized) || !documents.GetDocuments()[0].GetIssuedAt().AsTime().Equal(finalized) {
		t.Fatalf("statement issued at %s (listed %s), want its finalization %s, after the period end %s",
			document.GetDocument().GetIssuedAt().AsTime(), documents.GetDocuments()[0].GetIssuedAt().AsTime(), finalized, periodEnd)
	}
	if document.GetContentType() != "application/pdf" || !strings.HasSuffix(document.GetDocument().GetDownloadFilename(), ".pdf") {
		t.Fatalf("statement downloads as %q %q, want a PDF", document.GetContentType(), document.GetDocument().GetDownloadFilename())
	}
	requireRuns(t, pdfTextRuns(t, document.GetContent()),
		"Prepaid balance statement", "Issued 2026-08-20 13:58:34 UTC", "Not an invoice", "nothing to pay",
		"Paid from prepaid balance for this usage", "EUR 2.00", "EUR 8.00",
		"Delivered bandwidth", "200 gibibyte", "0.01", "2.00")
}
