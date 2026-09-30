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
	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

// A tenant that moves from prepaid to postpaid mid-period paid the prepaid
// part of the period from its balance as usage was reported. That part goes
// on a prepaid statement closed at the switch; the postpaid invoice rates only
// the usage from the switch on, including prepaid-phase usage that reached
// Purser after the switch and so was never paid from the balance.
func TestPrepaidToPostpaidSwitchInvoicesOnlyUsageFromTheSwitch_RealPG(t *testing.T) { //nolint:funlen // One switch, followed through to the postpaid invoice.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tenantID, paygID, supporterID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	periodStart := time.Now().UTC().Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	dimensionKey := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
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
			tenantID, dimensionKey, reportID, gib, start, start.Add(5*time.Minute))
	}

	exec(t, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid, metering_enabled)
		VALUES ($1, 'payg', 'Pay As You Go', 0, 'EUR', 0, true, true), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false, true)`, paygID, supporterID)
	exec(t, `INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
		VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.01, '{}'), ($2, 'egress_gb', 'all_usage', 'EUR', 0, 0.02, '{}')`, paygID, supporterID)
	exec(t, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end, presentment_currency)
		VALUES ($1, $2, 'active', 'prepaid', 'billing@example.com', $3, $4, 'EUR')`, tenantID, paygID, periodStart, periodEnd)
	exec(t, `INSERT INTO purser.metering_sources (source_id, region, active_from, required) VALUES ('periscope-default', '', $1, TRUE)`, periodStart.AddDate(0, -1, 0))
	exec(t, `INSERT INTO purser.metering_windows (source_id, period_start, period_end, complete)
		SELECT 'periscope-default', window_start, window_start + INTERVAL '5 minutes', TRUE
		FROM generate_series($1::timestamptz, $2::timestamptz, INTERVAL '5 minutes') AS window_start`, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))

	// Prepaid phase: a 10.00 top-up, then 200 GiB the balance paid (2.00 at
	// the prepaid tier's 0.01) through a prepaid usage settlement.
	prepaidReport := strings.Repeat("1", 64)
	recordEgress(t, prepaidReport, periodStart.Add(2*time.Hour), 200)
	exec(t, `INSERT INTO purser.prepaid_usage_settlements (report_id, tenant_id, billing_period_start, billing_period_end, amount_micro, cumulative_amount_micro, currency)
		VALUES ($1, $2, $3, $4, 2000000, 2000000, 'EUR')`, prepaidReport, tenantID, periodStart, periodEnd)
	exec(t, `INSERT INTO purser.balance_transactions (tenant_id, amount_cents, balance_after_cents, transaction_type, description, reference_id, reference_type, created_at)
		VALUES ($1, 1000, 1000, 'topup', 'Card top-up', NULL, NULL, $2),
		       ($1, -200, 800, 'usage', 'Usage', $3, 'usage_summary', $4)`,
		tenantID, periodStart.Add(time.Hour), uuid.NewString(), periodStart.Add(2*time.Hour+5*time.Minute))
	exec(t, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 800, 'EUR')`, tenantID)

	logger := logging.NewLogger()
	server := &PurserServer{db: db, logger: logger, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	if _, err := server.AdminAssignTier(operatorAssignCtx("7a000000-0000-4000-8000-000000000002"), &purserpb.AdminAssignTierRequest{
		TenantId: tenantID, TierName: "supporter", Reason: "moves to postpaid",
	}); err != nil {
		t.Fatalf("AdminAssignTier: %v", err)
	}
	var switchAt, postpaidEnd time.Time
	if err := db.QueryRowContext(ctx, `SELECT billing_period_start, billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&switchAt, &postpaidEnd); err != nil {
		t.Fatal(err)
	}
	if !switchAt.After(periodStart) || !postpaidEnd.Equal(periodEnd) {
		t.Fatalf("postpaid period after the switch = %s..%s, want it to start at the switch after %s and end at %s", switchAt, postpaidEnd, periodStart, periodEnd)
	}

	// After the switch: the report of the last prepaid-phase window arrives
	// late (50 GiB, never paid from the balance), then postpaid usage (100 GiB).
	window := switchAt.Truncate(5 * time.Minute)
	recordEgress(t, strings.Repeat("2", 64), window.Add(-5*time.Minute), 50)
	recordEgress(t, strings.Repeat("3", 64), window.Add(10*time.Minute), 100)

	jobs := handlers.NewJobManager(db, logger, nil, nil, nil, nil, handlers.NewService(db, logger, nil, nil, nil, nil, nil), nil)
	if _, err := jobs.CloseAdvanceBilledPeriod(ctx, tenantID, periodEnd); err != nil {
		t.Fatalf("finalize the postpaid period: %v", err)
	}

	var invoiceStart time.Time
	var metered, base, details string
	if err := db.QueryRowContext(ctx, `
		SELECT period_start, metered_amount::text, base_amount::text, usage_details::text FROM purser.billing_invoices
		WHERE tenant_id = $1 AND status NOT IN ('draft', 'manual_review') AND period_end = $2 AND invoice_number LIKE 'INV-%'`,
		tenantID, periodEnd).Scan(&invoiceStart, &metered, &base, &details); err != nil {
		t.Fatalf("read postpaid invoice: %v", err)
	}
	if metered != "3.00" || base != "79.00" || !invoiceStart.Equal(switchAt) {
		t.Fatalf("postpaid invoice = %s..%s usage %s base %s, want usage 3.00 (150 GiB at 0.02) from the switch at %s: prepaid-phase usage the balance paid was rated again",
			invoiceStart, periodEnd, metered, base, switchAt)
	}
	if !strings.Contains(details, `"prepaid_settled_usage_excluded": true`) && !strings.Contains(details, `"prepaid_settled_usage_excluded":true`) {
		t.Fatalf("postpaid invoice does not record that it left out prepaid-settled usage: %s", details)
	}

	var kind, number, amount, credit, statementMetered, statementDetails string
	var statementStart, statementEnd time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT document_kind, invoice_number, amount::text, prepaid_credit_applied::text, metered_amount::text,
		       usage_details::text, period_start, period_end
		FROM purser.billing_invoices WHERE tenant_id = $1 AND period_start = $2`, tenantID, periodStart).Scan(
		&kind, &number, &amount, &credit, &statementMetered, &statementDetails, &statementStart, &statementEnd); err != nil {
		t.Fatalf("read closing statement: %v", err)
	}
	if kind != "prepaid_statement" || !strings.HasPrefix(number, "STM-") || amount != "0.00" || credit != "0.00" ||
		statementMetered != "2.00" || !statementEnd.Equal(switchAt) || !strings.Contains(statementDetails, `"closes_prepaid_phase": true`) {
		t.Fatalf("closing statement = kind %s number %s amount %s credit %s usage %s period %s..%s details %s", kind, number, amount, credit, statementMetered, statementStart, statementEnd, statementDetails)
	}
	var emails int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.invoice_email_outbox WHERE tenant_id = $1 AND notification_type = 'prepaid_statement'`, tenantID).Scan(&emails); err != nil {
		t.Fatal(err)
	}
	if emails != 1 {
		t.Fatalf("closing statement emails = %d, want 1", emails)
	}
}
