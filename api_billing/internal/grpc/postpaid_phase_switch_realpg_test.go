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
	"github.com/shopspring/decimal"
)

// A postpaid tenant moved to prepaid mid-period owes what its postpaid plan
// used up to the switch. AdminAssignTier finalizes the postpaid phase's
// invoice at the switch, at the postpaid tier's prices with the base fee for
// the phase's share of the month, and starts the prepaid period there. The
// invoice is a normal postpaid invoice: emailed, announced, and paid like any
// other.
func TestPostpaidToPrepaidSwitchInvoicesThePostpaidPhase_RealPG(t *testing.T) { //nolint:funlen // One switch, followed through to the invoice's payment.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tenantID, paygID, supporterID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	periodStart := time.Now().UTC().Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(t, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid, metering_enabled)
		VALUES ($1, 'payg', 'Pay As You Go', 0, 'EUR', 0, true, true), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false, true)`, paygID, supporterID)
	exec(t, `INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
		VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.01, '{}'), ($2, 'egress_gb', 'all_usage', 'EUR', 0, 0.02, '{}')`, paygID, supporterID)
	exec(t, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end, presentment_currency)
		VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', $3, $4, 'EUR')`, tenantID, supporterID, periodStart, periodEnd)
	// 150 GiB of postpaid usage, reported while the tenant was postpaid.
	exec(t, `INSERT INTO purser.usage_records (
			tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
			source_id, report_id, usage_value, usage_details,
			period_start, period_end, granularity, value_kind
		) VALUES ($1, '', 'egress_gb', 'gibibyte', '{}', $2, 'periscope-default', $3, 150, '{}', $4, $5, 'minute_5', 'delta')`,
		tenantID, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), strings.Repeat("9", 64), periodStart.Add(2*time.Hour), periodStart.Add(2*time.Hour+5*time.Minute))

	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	if _, err := server.AdminAssignTier(operatorAssignCtx("7a000000-0000-4000-8000-000000000003"), &purserpb.AdminAssignTierRequest{
		TenantId: tenantID, TierName: "payg", Reason: "moves to pay as you go",
	}); err != nil {
		t.Fatalf("AdminAssignTier: %v", err)
	}
	var model string
	var switchAt, prepaidEnd time.Time
	if err := db.QueryRowContext(ctx, `SELECT billing_model, billing_period_start, billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id = $1`,
		tenantID).Scan(&model, &switchAt, &prepaidEnd); err != nil {
		t.Fatal(err)
	}
	if model != "prepaid" || !switchAt.After(periodStart) || !prepaidEnd.Equal(periodEnd) {
		t.Fatalf("subscription after the switch = %s %s..%s, want prepaid from the switch after %s to %s", model, switchAt, prepaidEnd, periodStart, periodEnd)
	}

	var invoiceID, number, status, base, metered, amount, details string
	var invoiceEnd time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT id::text, COALESCE(invoice_number, ''), status, base_amount::text, metered_amount::text, amount::text, usage_details::text, period_end
		FROM purser.billing_invoices WHERE tenant_id = $1 AND period_start = $2 AND document_kind = 'invoice'`,
		tenantID, periodStart).Scan(&invoiceID, &number, &status, &base, &metered, &amount, &details, &invoiceEnd); err != nil {
		t.Fatalf("read the invoice of the postpaid phase: %v; the 150 GiB of postpaid usage and the base fee up to the switch were never billed", err)
	}
	seconds := decimal.NewFromInt(int64(switchAt.Sub(periodStart) / time.Second))
	month := decimal.NewFromInt(int64(periodStart.AddDate(0, 1, 0).Sub(periodStart) / time.Second))
	wantBase := decimal.NewFromInt(7900).Mul(seconds).Div(month).Round(0).Shift(-2)
	wantAmount := wantBase.Add(decimal.RequireFromString("3.00"))
	if !strings.HasPrefix(number, "INV-") || status != "pending" || !invoiceEnd.Equal(switchAt) || metered != "3.00" ||
		base != wantBase.StringFixed(2) || amount != wantAmount.StringFixed(2) || !strings.Contains(strings.ReplaceAll(details, " ", ""), `"closes_postpaid_phase":true`) {
		t.Fatalf("postpaid phase invoice = number %s status %s ending %s base %s usage %s amount %s details %s; want a pending invoice to the switch at %s with base %s and usage 3.00 (150 GiB at 0.02)",
			number, status, invoiceEnd, base, metered, amount, details, switchAt, wantBase.StringFixed(2))
	}
	var emails, events int
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM purser.invoice_email_outbox WHERE invoice_id = $1::uuid AND notification_type = 'invoice_created'),
		       (SELECT count(*) FROM purser.domain_event_outbox WHERE aggregate_id = $1::text AND event_type = 'billing.invoice_created')`, invoiceID).Scan(&emails, &events); err != nil {
		t.Fatal(err)
	}
	if emails != 1 || events != 1 {
		t.Fatalf("postpaid phase invoice: %d emails, %d invoice_created events; want one of each", emails, events)
	}

	paid, err := server.AdminRecordInvoicePayment(operatorAssignCtx("7a000000-0000-4000-8000-000000000003"), &purserpb.AdminRecordInvoicePaymentRequest{
		TenantId: tenantID, InvoiceId: invoiceID, Reference: "bank transfer 1", Reason: "paid by transfer",
	})
	if err != nil || paid.GetInvoiceStatus() != "paid" || paid.GetAmount() != wantAmount.StringFixed(2) {
		t.Fatalf("AdminRecordInvoicePayment = %+v, %v; want the invoice paid for %s", paid, err, wantAmount.StringFixed(2))
	}
}

// A tenant moved from prepaid to postpaid and back within one period has each
// phase billed once by the document that closed it: the prepaid phase's usage
// on its statement, paid from the balance; the postpaid phase's usage on its
// invoice, at the postpaid tier's prices; and the prepaid period resumes at
// the second switch until the period would have ended.
func TestPrepaidPostpaidPrepaidRoundTripBillsEachPhaseOnce_RealPG(t *testing.T) { //nolint:funlen // Two switches, followed through every document.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tenantID, paygID, supporterID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	periodStart := time.Now().UTC().Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	recordEgress := func(t *testing.T, reportID string, start time.Time, gib float64) {
		t.Helper()
		exec(t, `INSERT INTO purser.usage_records (
				tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
				source_id, report_id, usage_value, usage_details,
				period_start, period_end, granularity, value_kind
			) VALUES ($1, '', 'egress_gb', 'gibibyte', '{}', $2, 'periscope-default', $3, $4, '{}', $5, $6, 'minute_5', 'delta')`,
			tenantID, fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), reportID, gib, start, start.Add(5*time.Minute))
	}
	exec(t, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid, metering_enabled)
		VALUES ($1, 'payg', 'Pay As You Go', 0, 'EUR', 0, true, true), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false, true)`, paygID, supporterID)
	exec(t, `INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
		VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.01, '{}'), ($2, 'egress_gb', 'all_usage', 'EUR', 0, 0.02, '{}')`, paygID, supporterID)
	exec(t, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end, presentment_currency)
		VALUES ($1, $2, 'active', 'prepaid', 'billing@example.com', $3, $4, 'EUR')`, tenantID, paygID, periodStart, periodEnd)
	// Prepaid phase: 200 GiB the balance paid (2.00 at 0.01) from a 10.00 top-up.
	prepaidReport := strings.Repeat("a", 64)
	recordEgress(t, prepaidReport, periodStart.Add(2*time.Hour), 200)
	exec(t, `INSERT INTO purser.prepaid_usage_settlements (report_id, tenant_id, billing_period_start, billing_period_end, amount_micro, cumulative_amount_micro, currency)
		VALUES ($1, $2, $3, $4, 2000000, 2000000, 'EUR')`, prepaidReport, tenantID, periodStart, periodEnd)
	exec(t, `INSERT INTO purser.balance_transactions (tenant_id, amount_cents, balance_after_cents, transaction_type, description, reference_id, reference_type, created_at)
		VALUES ($1, 1000, 1000, 'topup', 'Card top-up', NULL, NULL, $2),
		       ($1, -200, 800, 'usage', 'Usage', $3, 'usage_summary', $4)`,
		tenantID, periodStart.Add(time.Hour), uuid.NewString(), periodStart.Add(2*time.Hour+5*time.Minute))
	exec(t, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 800, 'EUR')`, tenantID)

	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	assign := func(t *testing.T, tier string) (time.Time, time.Time) {
		t.Helper()
		if _, err := server.AdminAssignTier(operatorAssignCtx("7a000000-0000-4000-8000-000000000004"), &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: tier, Reason: "switch to " + tier,
		}); err != nil {
			t.Fatalf("AdminAssignTier(%s): %v", tier, err)
		}
		var start, end time.Time
		if err := db.QueryRowContext(ctx, `SELECT billing_period_start, billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&start, &end); err != nil {
			t.Fatal(err)
		}
		return start, end
	}
	toPostpaid, _ := assign(t, "supporter")
	// Postpaid phase: 100 GiB reported while postpaid, for the window the
	// first switch fell in. The phases here last milliseconds, so the window
	// starts before the switch; the record belongs to the postpaid phase by
	// the time it reached Purser.
	recordEgress(t, strings.Repeat("b", 64), toPostpaid.Add(-time.Minute), 100)
	toPrepaid, prepaidEnd := assign(t, "payg")
	if !toPrepaid.After(toPostpaid) || !prepaidEnd.Equal(periodEnd) {
		t.Fatalf("prepaid period after the round trip = %s..%s, want it to start at the second switch after %s and end at %s", toPrepaid, prepaidEnd, toPostpaid, periodEnd)
	}

	type document struct {
		kind, metered, credit string
		start, end            time.Time
		egress                float64
	}
	rows, err := db.QueryContext(ctx, `
		SELECT invoice.document_kind, invoice.metered_amount::text, invoice.prepaid_credit_applied::text, invoice.period_start, invoice.period_end,
		       COALESCE((SELECT SUM(line.quantity) FROM purser.invoice_line_items line WHERE line.invoice_id = invoice.id AND line.meter = 'egress_gb'), 0)::float8
		FROM purser.billing_invoices invoice WHERE invoice.tenant_id = $1 ORDER BY invoice.period_start`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var documents []document
	for rows.Next() {
		var d document
		if err := rows.Scan(&d.kind, &d.metered, &d.credit, &d.start, &d.end, &d.egress); err != nil {
			t.Fatal(err)
		}
		documents = append(documents, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(documents) != 2 ||
		documents[0].kind != "prepaid_statement" || !documents[0].start.Equal(periodStart) || !documents[0].end.Equal(toPostpaid) || documents[0].egress != 200 || documents[0].metered != "2.00" ||
		documents[1].kind != "invoice" || !documents[1].start.Equal(toPostpaid) || !documents[1].end.Equal(toPrepaid) || documents[1].egress != 100 || documents[1].metered != "2.00" {
		t.Fatalf("documents = %+v; want the prepaid statement of %s..%s with 200 GiB at 0.01 and the invoice of %s..%s with 100 GiB at 0.02",
			documents, periodStart, toPostpaid, toPostpaid, toPrepaid)
	}
	var balance int64
	if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1`, tenantID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	credit := decimal.RequireFromString(documents[1].credit).Shift(2).IntPart()
	if credit <= 0 || balance != 800-credit {
		t.Fatalf("balance %d with %d cents invoice credit, want the 800 left after the prepaid phase to pay the postpaid invoice as credit once", balance, credit)
	}
}
