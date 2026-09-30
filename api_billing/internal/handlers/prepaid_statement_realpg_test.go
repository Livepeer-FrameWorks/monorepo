//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
)

// statementQuartermaster resolves the one marketplace cluster a test bills.
type statementQuartermaster struct {
	marketplaceClusterResolver
}

func (statementQuartermaster) GetTenant(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
	return &quartermasterpb.GetTenantResponse{}, nil
}

func (statementQuartermaster) MaterializeClusterAccess(context.Context, *quartermasterpb.MaterializeClusterAccessRequest) error {
	return nil
}

func (statementQuartermaster) RevokeMaterializedClusterAccess(context.Context, *quartermasterpb.RevokeMaterializedClusterAccessRequest) error {
	return nil
}

// prepaidStatementPeriod is last calendar month: it has ended, and the period
// after it has not.
func prepaidStatementPeriod() (time.Time, time.Time) {
	now := time.Now().UTC()
	end := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return end.AddDate(0, -1, 0), end
}

// seedCompleteMetering registers one required metering source with every
// five-minute window in [from, to) complete, so invoice finalization's
// metering gate passes.
func seedCompleteMetering(t *testing.T, db *sql.DB, from, to time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.metering_sources (source_id, region, active_from, required)
		VALUES ('periscope-default', '', $1, TRUE)
		ON CONFLICT (source_id) DO NOTHING`, from); err != nil {
		t.Fatalf("seed metering source: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.metering_windows (source_id, period_start, period_end, complete)
		SELECT 'periscope-default', window_start, window_start + INTERVAL '5 minutes', TRUE
		FROM generate_series($1::timestamptz, $2::timestamptz - INTERVAL '5 minutes', INTERVAL '5 minutes') AS window_start
		ON CONFLICT DO NOTHING`, from, to); err != nil {
		t.Fatalf("seed metering windows: %v", err)
	}
}

// seedPrepaidTenant creates an active prepaid tenant on a pay-as-you-go tier
// that rates egress at 0.01 EUR per GiB, in the given period, with the given
// balance and no ledger history.
func seedPrepaidTenant(t *testing.T, db *sql.DB, periodStart, periodEnd time.Time, balanceCents int64) string {
	t.Helper()
	ctx := context.Background()
	tenantID, tierID := uuid.NewString(), uuid.NewString()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid, metering_enabled)
			VALUES ($1, $2, 'Pay As You Go', 0, 'EUR', 0, true, true)`, []any{tierID, "payg-" + tierID[:8]}},
		{`INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
			VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.01, '{}')`, []any{tierID}},
		{`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end, presentment_currency)
			VALUES ($1, $2, 'active', 'prepaid', 'billing@example.com', $3, $4, 'EUR')`, []any{tenantID, tierID, periodStart, periodEnd}},
		{`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, $2, 'EUR')`, []any{tenantID, balanceCents}},
	} {
		if _, err := db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement.sql)
		}
	}
	return tenantID
}

// recordEgress writes one five-minute egress usage record, as the Kafka
// consumer does before it routes the report by billing model.
func recordEgress(t *testing.T, db *sql.DB, tenantID, reportID string, start time.Time, gib float64) models.UsageSummary {
	t.Helper()
	end := start.Add(5 * time.Minute)
	dimensionKey := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO purser.usage_records (
			tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
			source_id, report_id, usage_value, usage_details,
			period_start, period_end, granularity, value_kind
		) VALUES ($1, '', 'egress_gb', 'gibibyte', '{}', $2,
			'periscope-default', $3, $4, '{}', $5, $6, 'minute_5', 'delta')`,
		tenantID, dimensionKey, reportID, gib, start, end); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	return models.UsageSummary{
		TenantID: tenantID, ClusterID: "", SourceID: "periscope-default", ReportID: reportID,
		PeriodStart: start, PeriodEnd: end,
	}
}

// A prepaid tenant's usage is paid from its balance as it is reported. The
// month-end close must state that usage at rated prices on a statement with
// nothing due and must not take the balance again as invoice credit.
func TestPrepaidMonthEndStatesUsageWithoutChargingTheBalanceAgain_RealPG(t *testing.T) { //nolint:funlen // One period end, checked from every side.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	periodStart, periodEnd := prepaidStatementPeriod()
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	tenantID := seedPrepaidTenant(t, db, periodStart, periodEnd, 0)
	logger := logging.NewLogger()
	jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger}}

	// A 10.00 top-up during the period, then 200 GiB of egress the balance
	// pays as the report arrives.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (tenant_id, amount_cents, balance_after_cents, transaction_type, description, created_at)
		VALUES ($1, 1000, 1000, 'topup', 'Card top-up', $2)`, tenantID, periodStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.prepaid_balances SET balance_cents = 1000 WHERE tenant_id = $1`, tenantID); err != nil {
		t.Fatal(err)
	}
	summary := recordEgress(t, db, tenantID, strings.Repeat("a", 64), periodStart.Add(2*time.Hour), 200)
	if err := jm.processPrepaidUsage(ctx, summary, []canonicalUsageDelta{{usageType: "egress_gb", usageValue: 200}}); err != nil {
		t.Fatalf("processPrepaidUsage: %v", err)
	}
	// The deduction was posted when the report arrived, inside the period.
	if _, err := db.ExecContext(ctx, `UPDATE purser.balance_transactions SET created_at = $2 WHERE tenant_id = $1 AND reference_type = 'usage_summary'`,
		tenantID, periodStart.Add(2*time.Hour+5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	balance := func(t *testing.T) int64 {
		t.Helper()
		var cents int64
		if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1`, tenantID).Scan(&cents); err != nil {
			t.Fatal(err)
		}
		return cents
	}
	if got := balance(t); got != 800 {
		t.Fatalf("balance after the usage deduction = %d, want 800", got)
	}

	jm.generateMonthlyInvoices(ctx)

	if got := balance(t); got != 800 {
		t.Fatalf("balance after month end = %d cents, want 800: the 2.00 of usage the balance already paid was taken again", got)
	}
	var creditRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit'`, tenantID).Scan(&creditRows); err != nil {
		t.Fatal(err)
	}
	if creditRows != 0 {
		t.Fatalf("month end wrote %d invoice credit ledger rows for a prepaid tenant, want none", creditRows)
	}

	var documents int
	var id, number, kind, status, amount, credit, metered, details string
	var gotStart, gotEnd time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) OVER (), id::text, invoice_number, document_kind, status, amount::text, prepaid_credit_applied::text,
		       metered_amount::text, usage_details::text, period_start, period_end
		FROM purser.billing_invoices WHERE tenant_id = $1`, tenantID).Scan(
		&documents, &id, &number, &kind, &status, &amount, &credit, &metered, &details, &gotStart, &gotEnd); err != nil {
		t.Fatalf("read month-end document: %v", err)
	}
	if documents != 1 || kind != "prepaid_statement" || !strings.HasPrefix(number, "STM-") || status != "paid" ||
		amount != "0.00" || credit != "0.00" || metered != "2.00" || !gotStart.Equal(periodStart) || !gotEnd.Equal(periodEnd) {
		t.Fatalf("month-end document = count %d kind %s number %s status %s amount %s credit %s metered %s period %s..%s, want one paid prepaid statement STM-* of 2.00 usage with nothing due",
			documents, kind, number, status, amount, credit, metered, gotStart, gotEnd)
	}

	var quantity, lineAmount string
	if err := db.QueryRowContext(ctx, `
		SELECT quantity::text, amount::text FROM purser.invoice_line_items
		WHERE invoice_id = $1 AND meter = 'egress_gb'`, id).Scan(&quantity, &lineAmount); err != nil {
		t.Fatalf("read egress line: %v", err)
	}
	if !strings.HasPrefix(quantity, "200") || lineAmount != "2.00" {
		t.Fatalf("egress line = %s GiB for %s, want 200 GiB for 2.00", quantity, lineAmount)
	}

	var parsed struct {
		Statement map[string]any `json:"statement"`
	}
	if err := json.Unmarshal([]byte(details), &parsed); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{
		"rated_usage_cents":        200,
		"paid_from_balance_cents":  200,
		"period_fees_cents":        0,
		"opening_balance_cents":    0,
		"topup_cents":              1000,
		"topups":                   1,
		"usage_posted_cents":       200,
		"other_movements_cents":    0,
		"period_end_balance_cents": 800,
		"closing_balance_cents":    800,
		"amount_due_cents":         0,
	} {
		if got, ok := parsed.Statement[key].(float64); !ok || got != want {
			t.Errorf("statement %s = %v, want %v", key, parsed.Statement[key], want)
		}
	}

	var recipient, notification string
	if err := db.QueryRowContext(ctx, `SELECT recipient, notification_type FROM purser.invoice_email_outbox WHERE invoice_id = $1`, id).Scan(&recipient, &notification); err != nil {
		t.Fatalf("read statement email: %v", err)
	}
	if recipient != "billing@example.com" || notification != "prepaid_statement" {
		t.Fatalf("statement email = %s/%s, want billing@example.com/prepaid_statement", recipient, notification)
	}

	var nextStart time.Time
	if err := db.QueryRowContext(ctx, `SELECT billing_period_start FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&nextStart); err != nil {
		t.Fatal(err)
	}
	if !nextStart.Equal(periodEnd) {
		t.Fatalf("subscription period starts %s after month end, want %s", nextStart, periodEnd)
	}

	// The next run finds the period closed and changes nothing.
	jm.generateMonthlyInvoices(ctx)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.billing_invoices WHERE tenant_id = $1`, tenantID).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || balance(t) != 800 {
		t.Fatalf("second run left %d documents and balance %d, want 1 and 800", documents, balance(t))
	}
}

// A monthly cluster fee Purser bills itself is only ever charged by the month
// end. For a prepaid tenant it is charged to the prepaid balance once, as its
// own ledger entry, and listed on the statement; it is not invoiced.
func TestPrepaidStatementChargesMonthlyClusterFeeOnce_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	periodStart, periodEnd := prepaidStatementPeriod()
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	tenantID := seedPrepaidTenant(t, db, periodStart, periodEnd, 1000)
	clusterID := "cluster-monthly-" + uuid.NewString()[:8]
	owner := uuid.NewString()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO purser.cluster_pricing_history (cluster_id, pricing_model, base_price, currency, effective_from)
			VALUES ($1, 'monthly', 30.00, 'EUR', $2)`, []any{clusterID, periodStart.AddDate(-1, 0, 0)}},
		{`INSERT INTO purser.platform_fee_policy (id, cluster_kind, cluster_owner_tenant_id, pricing_source, fee_basis_points, effective_from)
			VALUES ($1, 'third_party_marketplace', $2, 'cluster_monthly', 1000, $3)`, []any{uuid.NewString(), owner, periodStart.AddDate(-1, 0, 0)}},
		{`INSERT INTO purser.cluster_subscriptions (tenant_id, cluster_id, status, activated_at, created_at)
			VALUES ($1, $2, 'active', $3, $3)`, []any{tenantID, clusterID, periodStart.AddDate(0, 0, -10)}},
	} {
		if _, err := db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement.sql)
		}
	}
	logger := logging.NewLogger()
	qm := statementQuartermaster{marketplaceClusterResolver{clusterID: clusterID, ownerTenantID: owner}}
	jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger, qmClient: qm}}

	jm.generateMonthlyInvoices(ctx)
	jm.generateMonthlyInvoices(ctx)

	var balance int64
	var feeRows, creditRows int
	var feeCents int64
	var feeDescription string
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1),
		       (SELECT count(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit'),
		       count(*), COALESCE(SUM(amount_cents), 0), COALESCE(MAX(description), '')
		FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'prepaid_statement_fees'`, tenantID).Scan(
		&balance, &creditRows, &feeRows, &feeCents, &feeDescription); err != nil {
		t.Fatal(err)
	}
	if balance != -2000 || creditRows != 0 || feeRows != 1 || feeCents != -3000 || !strings.HasPrefix(feeDescription, "Monthly fees ") {
		t.Fatalf("balance %d with %d invoice credit rows and %d fee rows of %d cents (%q), want -2000 after one -3000 monthly fee entry and no invoice credit",
			balance, creditRows, feeRows, feeCents, feeDescription)
	}

	var documents int
	var id, kind, amount, credit, clusterLine string
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) OVER (), invoice.id::text, invoice.document_kind, invoice.amount::text, invoice.prepaid_credit_applied::text,
		       (SELECT amount::text FROM purser.invoice_line_items line WHERE line.invoice_id = invoice.id AND line.pricing_source = 'cluster_monthly')
		FROM purser.billing_invoices invoice WHERE invoice.tenant_id = $1`, tenantID).Scan(&documents, &id, &kind, &amount, &credit, &clusterLine); err != nil {
		t.Fatalf("read statement: %v", err)
	}
	if documents != 1 || kind != "prepaid_statement" || amount != "0.00" || credit != "0.00" || clusterLine != "30.00" {
		t.Fatalf("documents = %d, kind %s amount %s credit %s cluster line %s; want one statement listing the 30.00 fee with nothing due", documents, kind, amount, credit, clusterLine)
	}
	var feeStated float64
	if err := db.QueryRowContext(ctx, `SELECT (usage_details->'statement'->>'period_fees_cents')::float8 FROM purser.billing_invoices WHERE id = $1`, id).Scan(&feeStated); err != nil {
		t.Fatal(err)
	}
	if feeStated != 3000 {
		t.Fatalf("statement states %v cents of monthly fees, want 3000", feeStated)
	}
	var operatorCredits int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.operator_credit_ledger WHERE invoice_id = $1`, id).Scan(&operatorCredits); err != nil {
		t.Fatal(err)
	}
	if operatorCredits != 1 {
		t.Fatalf("marketplace operator credit rows = %d, want the cluster owner's share once", operatorCredits)
	}
}
