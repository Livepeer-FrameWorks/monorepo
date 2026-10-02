//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// splitTiers seeds a prepaid pay-as-you-go tier that rates egress at 0.01
// EUR per GiB and a postpaid tier with a 79.00 base fee that rates it at 0.02.
func splitTiers(t *testing.T, db *sql.DB) (paygID, supporterID string) {
	t.Helper()
	paygID, supporterID = uuid.NewString(), uuid.NewString()
	for _, statement := range []string{
		`INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid, metering_enabled)
			VALUES ($1, 'payg', 'Pay As You Go', 0, 'EUR', 0, true, true), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false, true)`,
		`INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
			VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.01, '{}'), ($2, 'egress_gb', 'all_usage', 'EUR', 0, 0.02, '{}')`,
	} {
		if _, err := db.ExecContext(context.Background(), statement, paygID, supporterID); err != nil {
			t.Fatalf("seed tiers: %v", err)
		}
	}
	return paygID, supporterID
}

// recordEgressReceived writes one five-minute egress usage record that
// reached Purser at receivedAt.
func recordEgressReceived(t *testing.T, db *sql.DB, tenantID, reportID string, start time.Time, gib float64, receivedAt time.Time) models.UsageSummary {
	t.Helper()
	end := start.Add(5 * time.Minute)
	dimensionKey := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO purser.usage_records (
			tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
			source_id, report_id, usage_value, usage_details,
			period_start, period_end, granularity, value_kind, created_at
		) VALUES ($1, '', 'egress_gb', 'gibibyte', '{}', $2,
			'periscope-default', $3, $4, '{}', $5, $6, 'minute_5', 'delta', $7)`,
		tenantID, dimensionKey, reportID, gib, start, end, receivedAt); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	return models.UsageSummary{
		TenantID: tenantID, ClusterID: "", SourceID: "periscope-default", ReportID: reportID,
		PeriodStart: start, PeriodEnd: end,
	}
}

// commitSwitchToPrepaid runs a prepared postpaid phase close inside a switch
// transaction the way AdminAssignTier does: under the subscription row lock,
// before the billing model changes.
func commitSwitchToPrepaid(ctx context.Context, db *sql.DB, closing *PostpaidPhaseClose, tenantID, paygID string) (bool, error) {
	closed := false
	err := withTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM purser.tenant_subscriptions WHERE tenant_id = $1 FOR UPDATE`, tenantID); err != nil {
			return err
		}
		var err error
		if closed, err = closing.CommitTx(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `
			UPDATE purser.tenant_subscriptions SET billing_model = 'prepaid', tier_id = $2, updated_at = NOW()
			WHERE tenant_id = $1`, tenantID, paygID); err != nil {
			return err
		}
		return purserdb.New(tx).EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency})
	})
	if err == nil && closed {
		closing.AfterCommit(ctx)
	}
	return closed, err
}

type closingInvoice struct {
	id, number, kind, status       string
	amount, base, metered, credit  string
	details                        map[string]any
	periodStart, periodEnd, dueDay time.Time
}

func readInvoice(t *testing.T, db *sql.DB, tenantID string, periodStart time.Time) closingInvoice {
	t.Helper()
	var invoice closingInvoice
	var details []byte
	if err := db.QueryRowContext(context.Background(), `
		SELECT id::text, COALESCE(invoice_number, ''), document_kind, status, amount::text, base_amount::text,
		       metered_amount::text, prepaid_credit_applied::text, usage_details, period_start, period_end, due_date
		FROM purser.billing_invoices WHERE tenant_id = $1 AND period_start = $2`, tenantID, periodStart).Scan(
		&invoice.id, &invoice.number, &invoice.kind, &invoice.status, &invoice.amount, &invoice.base,
		&invoice.metered, &invoice.credit, &details, &invoice.periodStart, &invoice.periodEnd, &invoice.dueDay); err != nil {
		t.Fatalf("read the document of the period starting %s: %v", periodStart, err)
	}
	if err := json.Unmarshal(details, &invoice.details); err != nil {
		t.Fatal(err)
	}
	return invoice
}

func cents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

// A postpaid plan's entitlement ends at the switch to prepaid, so what it used
// is owed then. The switch finalizes the postpaid phase's invoice from the
// period's draft: usage at the postpaid tier's prices, including records of
// the earlier prepaid phase that reached Purser while postpaid, and the base
// fee and monthly cluster fee for the phase's share of the month. Usage that
// reaches Purser after the switch, also for activity before it, is paid from
// the prepaid balance, and the month end states only the prepaid phase. Every
// record is billed exactly once.
func TestPostpaidToPrepaidSwitchBillsEachPhaseOnce_RealPG(t *testing.T) { //nolint:funlen // One split period is followed to its month end.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	periodStart := now.Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	postpaidFrom := periodStart.Add(4 * 24 * time.Hour)
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	paygID, supporterID := splitTiers(t, db)
	tenantID := uuid.NewString()
	clusterID, owner := "cluster-monthly-"+uuid.NewString()[:8], uuid.NewString()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email,
			billing_period_start, billing_period_end, next_billing_date, presentment_currency)
			VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', $3, $4, $4, 'EUR')`, []any{tenantID, supporterID, postpaidFrom, periodEnd}},
		{`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 1000, 'EUR')`, []any{tenantID}},
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
	// The tenant was prepaid until postpaidFrom: that phase closed with its
	// statement, and the balance paid its 200 GiB as they were reported.
	seedClosingStatement(t, db, tenantID, periodStart, postpaidFrom)
	settled := recordEgressReceived(t, db, tenantID, strings.Repeat("1", 64), periodStart.Add(2*time.Hour), 200, periodStart.Add(2*time.Hour+6*time.Minute))
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.prepaid_usage_settlements (report_id, tenant_id, billing_period_start, billing_period_end, amount_micro, cumulative_amount_micro, currency)
		VALUES ($1, $2, $3, $4, 2000000, 2000000, 'EUR')`, settled.ReportID, tenantID, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
	// While postpaid: the last prepaid-phase window's report arrived late (20
	// GiB the balance never paid), and 100 GiB of postpaid usage.
	recordEgressReceived(t, db, tenantID, strings.Repeat("2", 64), postpaidFrom.Add(-10*time.Minute), 20, postpaidFrom.Add(time.Hour))
	recordEgressReceived(t, db, tenantID, strings.Repeat("3", 64), postpaidFrom.Add(24*time.Hour), 100, postpaidFrom.Add(24*time.Hour+6*time.Minute))

	logger := logging.NewLogger()
	qm := statementQuartermaster{marketplaceClusterResolver{clusterID: clusterID, ownerTenantID: owner}}
	svc := &Service{db: db, logger: logger, qmClient: qm}
	jm := &JobManager{db: db, logger: logger, billing: svc}
	if err := jm.updateInvoiceDraft(ctx, tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft: %v", err)
	}
	draft := readInvoice(t, db, tenantID, postpaidFrom)
	var draftBalance int64
	if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1`, tenantID).Scan(&draftBalance); err != nil {
		t.Fatal(err)
	}
	if draft.status != "draft" || draft.credit != "0.00" || draftBalance != 1000 {
		t.Fatalf("draft = status %s credit %s, balance %d; want a draft holding no credit and the whole 10.00 on the balance", draft.status, draft.credit, draftBalance)
	}

	closing, prepareErr := PreparePostpaidPhaseClose(ctx, db, logger, svc, tenantID)
	if prepareErr != nil {
		t.Fatalf("PreparePostpaidPhaseClose: %v", prepareErr)
	}
	if closed, err := commitSwitchToPrepaid(ctx, db, closing, tenantID, paygID); err != nil || !closed {
		t.Fatalf("switch to prepaid = closed %v err %v, want the postpaid phase closed", closed, err)
	}
	switchAt, prepaidEnd := subscriptionPeriod(t, db, tenantID)
	if !switchAt.After(postpaidFrom) || !prepaidEnd.Equal(periodEnd) {
		t.Fatalf("prepaid period after the switch = %s..%s, want it to start at the switch and end at %s", switchAt, prepaidEnd, periodEnd)
	}

	invoice := readInvoice(t, db, tenantID, postpaidFrom)
	// The postpaid phase pays the base fee for its share of the period the
	// switches split.
	baseCents := usagePhase{start: postpaidFrom, end: switchAt, chainFrom: periodStart, periodEnd: periodEnd}.
		baseFeeShare(decimal.New(7900, -2)).Shift(2).IntPart()
	clusterCents := proratedMonthlyCents(3000, postpaidFrom, switchAt, periodStart.AddDate(0, 0, -10), time.Time{})
	usageCents := int64(240) // (100 + 20) GiB at 0.02
	grossCents := baseCents + usageCents + clusterCents
	if invoice.id != draft.id || invoice.kind != "invoice" || !strings.HasPrefix(invoice.number, "INV-") || invoice.status != "pending" ||
		!invoice.periodEnd.Equal(switchAt) || invoice.base != cents(baseCents) || invoice.metered != cents(usageCents+clusterCents) ||
		invoice.credit != "10.00" || invoice.amount != cents(grossCents-1000) || invoice.details[closesPostpaidPhaseKey] != true {
		t.Fatalf("closing invoice = id %s (draft %s) kind %s number %s status %s period end %s base %s metered %s credit %s amount %s closes %v; "+
			"want the draft finalized pending for %s..%s with base %s, usage and cluster fee %s, credit 10.00, amount %s",
			invoice.id, draft.id, invoice.kind, invoice.number, invoice.status, invoice.periodEnd, invoice.base, invoice.metered,
			invoice.credit, invoice.amount, invoice.details[closesPostpaidPhaseKey], postpaidFrom, switchAt,
			cents(baseCents), cents(usageCents+clusterCents), cents(grossCents-1000))
	}
	var egressGiB float64
	if err := db.QueryRowContext(ctx, `SELECT quantity::float8 FROM purser.invoice_line_items WHERE invoice_id = $1 AND meter = 'egress_gb'`, invoice.id).Scan(&egressGiB); err != nil {
		t.Fatal(err)
	}
	if egressGiB != 120 {
		t.Fatalf("closing invoice egress = %v GiB, want 120: the postpaid usage and the late prepaid-phase report, not the 200 GiB the balance paid", egressGiB)
	}
	var emails, created int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.invoice_email_outbox WHERE invoice_id = $1 AND notification_type = 'invoice_created'`, invoice.id).Scan(&emails); err != nil {
		t.Fatal(err)
	}
	created = len(invoiceCreatedEvents(t, db, tenantID))
	if emails != 1 || created != 1 {
		t.Fatalf("closing invoice emails %d, invoice_created events %d, want one of each", emails, created)
	}
	balance := func(t *testing.T) int64 {
		t.Helper()
		var c int64
		if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1`, tenantID).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if got := balance(t); got != 0 {
		t.Fatalf("balance after the switch = %d, want 0: the closing invoice holds the 10.00 as credit", got)
	}

	// After the switch: the report of the last postpaid window arrives late
	// (50 GiB), then prepaid usage (30 GiB). The balance pays both.
	late := recordEgressReceived(t, db, tenantID, strings.Repeat("4", 64), switchAt.Truncate(5*time.Minute).Add(-5*time.Minute), 50, time.Now().UTC())
	if err := jm.processPrepaidUsage(ctx, late, []canonicalUsageDelta{{usageType: "egress_gb", usageValue: 50}}); err != nil {
		t.Fatalf("settle the late report: %v", err)
	}
	prepaid := recordEgressReceived(t, db, tenantID, strings.Repeat("5", 64), switchAt.Add(10*time.Minute), 30, time.Now().UTC())
	if err := jm.processPrepaidUsage(ctx, prepaid, []canonicalUsageDelta{{usageType: "egress_gb", usageValue: 30}}); err != nil {
		t.Fatalf("settle prepaid usage: %v", err)
	}
	if got := balance(t); got != -80 {
		t.Fatalf("balance after prepaid usage = %d, want -80: 80 GiB at 0.01 that reached Purser after the switch", got)
	}

	// ListSubscriptionsDueForInvoice compares the period end with the run's date.
	monthEnd := periodEnd.Add(24 * time.Hour)
	due, err := purserdb.New(db).ListSubscriptionsDueForInvoice(ctx, monthEnd)
	if err != nil {
		t.Fatal(err)
	}
	if generated := jm.finalizeSubscriptionPeriods(ctx, due, monthEnd, time.Time{}); generated != 1 {
		t.Fatalf("month end closed %d of %d due periods, want the prepaid statement", generated, len(due))
	}
	statement := readInvoice(t, db, tenantID, switchAt)
	statementFees := proratedMonthlyCents(3000, switchAt, periodEnd, periodStart.AddDate(0, 0, -10), time.Time{})
	if statement.kind != "prepaid_statement" || !statement.periodEnd.Equal(periodEnd) || statement.metered != cents(80+statementFees) {
		t.Fatalf("month-end document = kind %s period %s..%s metered %s, want the prepaid statement of %s..%s with 80 GiB at 0.01 and cluster fee %s",
			statement.kind, statement.periodStart, statement.periodEnd, statement.metered, switchAt, periodEnd, cents(statementFees))
	}
	if paid := statement.details["statement"].(map[string]any)["paid_from_balance_cents"]; paid != float64(80) {
		t.Fatalf("statement paid from balance = %v, want 80", paid)
	}
	if got := balance(t); got != -80-statementFees {
		t.Fatalf("balance after month end = %d, want %d: only the prepaid phase's cluster fee is charged", got, -80-statementFees)
	}
	nextStart, nextEnd := subscriptionPeriod(t, db, tenantID)
	if !nextStart.Equal(periodEnd) || !nextEnd.Equal(periodEnd.AddDate(0, 1, 0)) {
		t.Fatalf("period after month end = %s..%s, want the whole next period %s..%s", nextStart, nextEnd, periodEnd, periodEnd.AddDate(0, 1, 0))
	}

	// Every record of the period is on exactly one document or settlement.
	var invoiced float64
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(line.quantity), 0)::float8 FROM purser.invoice_line_items line
		JOIN purser.billing_invoices invoice ON invoice.id = line.invoice_id
		WHERE invoice.tenant_id = $1 AND line.meter = 'egress_gb' AND invoice.status NOT IN ('draft', 'manual_review')`, tenantID).Scan(&invoiced); err != nil {
		t.Fatal(err)
	}
	if invoiced != 200 {
		t.Fatalf("egress on the closing invoice and the month-end statement = %v GiB, want 200 (120 + 80)", invoiced)
	}
}

// The closing invoice goes through normal postpaid collection: a tenant
// collected through Mollie is charged off session for its amount after the
// switch commits. Charges the collection minimum deferred are charged to the
// prepaid balance, since no later postpaid invoice would collect them.
func TestPostpaidPhaseCloseCollectsTheClosingInvoice_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	ctx := context.Background()
	logger := logging.NewLogger()
	mollie, mollieClient := installMollieFixture(t)
	mollie.route(http.MethodPost, "/v2/customers/cst_phase_close/payments", func(recordedProviderRequest) (int, any) {
		return http.StatusCreated, map[string]any{"resource": "payment", "id": "tr_phase_close", "status": "open"}
	})

	for _, tc := range []struct {
		name string
		// basePrice is the postpaid tier's base fee; egressGiB at 0.02.
		basePrice   string
		egressGiB   float64
		wantCharged bool
	}{
		{name: "charged off session", basePrice: "79.00", egressGiB: 100, wantCharged: true},
		{name: "below the collection minimum", basePrice: "0", egressGiB: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := startPurserUsageRealPG(t)
			paygID, supporterID := splitTiers(t, db)
			if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET base_price = $2 WHERE id = $1`, supporterID, tc.basePrice); err != nil {
				t.Fatal(err)
			}
			tenantID := uuid.NewString()
			now := time.Now().UTC()
			periodStart := now.Truncate(time.Hour).Add(-10 * 24 * time.Hour)
			for _, statement := range []string{
				`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, payment_method,
					billing_period_start, billing_period_end, next_billing_date, presentment_currency)
					VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', 'mollie', $3, $4, $4, 'EUR')`,
				`INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id) VALUES ($1, 'cst_phase_close')`,
				`INSERT INTO purser.mollie_mandates (tenant_id, mollie_customer_id, mollie_mandate_id, status, method)
					VALUES ($1, 'cst_phase_close', 'mdt_phase_close', 'valid', 'creditcard')`,
			} {
				args := []any{tenantID}
				if strings.Contains(statement, "tenant_subscriptions") {
					args = []any{tenantID, supporterID, periodStart, periodStart.AddDate(0, 1, 0)}
				}
				if _, err := db.ExecContext(ctx, statement, args...); err != nil {
					t.Fatalf("seed: %v\n%s", err, statement)
				}
			}
			recordEgressReceived(t, db, tenantID, strings.Repeat("6", 64), periodStart.Add(time.Hour), tc.egressGiB, periodStart.Add(time.Hour+6*time.Minute))
			svc := &Service{db: db, logger: logger, mollieClient: mollieClient}
			closing, err := PreparePostpaidPhaseClose(ctx, db, logger, svc, tenantID)
			if err != nil {
				t.Fatalf("PreparePostpaidPhaseClose: %v", err)
			}
			if closed, err := commitSwitchToPrepaid(ctx, db, closing, tenantID, paygID); err != nil || !closed {
				t.Fatalf("switch = closed %v err %v", closed, err)
			}
			invoice := readInvoice(t, db, tenantID, periodStart)
			var payments int
			var paymentAmount sql.NullString
			if err := db.QueryRowContext(ctx, `SELECT count(*), max(amount::text) FROM purser.billing_payments WHERE invoice_id = $1`, invoice.id).Scan(&payments, &paymentAmount); err != nil {
				t.Fatal(err)
			}
			var balance int64
			if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1`, tenantID).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			if tc.wantCharged {
				if invoice.status != "pending" || payments != 1 || paymentAmount.String != invoice.amount || balance != 0 {
					t.Fatalf("closing invoice %s status %s amount %s: %d payments of %s, balance %d; want one Mollie charge of the amount due",
						invoice.id, invoice.status, invoice.amount, payments, paymentAmount.String, balance)
				}
				return
			}
			var carry int64
			if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.billing_collection_balances WHERE tenant_id = $1`, tenantID).Scan(&carry); err != nil {
				t.Fatal(err)
			}
			var carryRows int
			if err := db.QueryRowContext(ctx, `
				SELECT count(*) FROM purser.balance_transactions
				WHERE tenant_id = $1 AND reference_type = 'collection_carry' AND reference_id = $2::uuid AND amount_cents = -20`, tenantID, invoice.id).Scan(&carryRows); err != nil {
				t.Fatal(err)
			}
			if payments != 0 || carry != 0 || carryRows != 1 || balance != -20 {
				t.Fatalf("below-minimum closing invoice: %d payments, collection carry %d, %d carry ledger rows, balance %d; want 0.20 charged to the prepaid balance",
					payments, carry, carryRows, balance)
			}
		})
	}
}

// The closing invoice is rated before the switch transaction. A usage report
// that reached Purser before the switch but committed after the rating, an
// operator grant set meanwhile, or a grant that expired meanwhile changes
// what the phase owes, so the switch refuses the stale rating; rated again it
// commits. An expiring grant never changes the billing model by itself. A
// switch transaction that rolls back and runs again writes the invoice and
// takes its credit once.
func TestPostpaidPhaseCloseRetriesWhenThePhaseChangedSinceRating_RealPG(t *testing.T) { //nolint:funlen // One tenant through each stale rating.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	logger := logging.NewLogger()
	paygID, supporterID := splitTiers(t, db)
	tenantID := uuid.NewString()
	now := time.Now().UTC()
	periodStart := now.Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	var subscriptionID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email,
			billing_period_start, billing_period_end, next_billing_date, presentment_currency)
		VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', $3, $4, $4, 'EUR') RETURNING id::text`,
		tenantID, supporterID, periodStart, periodStart.AddDate(0, 1, 0)).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 500, 'EUR')`, tenantID); err != nil {
		t.Fatal(err)
	}
	recordEgressReceived(t, db, tenantID, strings.Repeat("7", 64), periodStart.Add(time.Hour), 100, now.Add(-2*time.Hour))
	svc := &Service{db: db, logger: logger}
	prepare := func(t *testing.T) *PostpaidPhaseClose {
		t.Helper()
		closing, err := PreparePostpaidPhaseClose(ctx, db, logger, svc, tenantID)
		if err != nil {
			t.Fatalf("PreparePostpaidPhaseClose: %v", err)
		}
		return closing
	}
	refused := func(t *testing.T, closing *PostpaidPhaseClose, why string) {
		t.Helper()
		if closed, err := commitSwitchToPrepaid(ctx, db, closing, tenantID, paygID); !errors.Is(err, ErrPostpaidPhaseChanged) || closed {
			t.Fatalf("switch after %s = closed %v err %v, want ErrPostpaidPhaseChanged", why, closed, err)
		}
		var model string
		var documents int
		if err := db.QueryRowContext(ctx, `
			SELECT billing_model, (SELECT count(*) FROM purser.billing_invoices WHERE tenant_id = $1)
			FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&model, &documents); err != nil {
			t.Fatal(err)
		}
		if model != "postpaid" || documents != 0 {
			t.Fatalf("after the refused switch (%s) the tenant runs %s with %d documents, want postpaid with none", why, model, documents)
		}
	}

	// A report received an hour ago commits after the rating.
	stale := prepare(t)
	recordEgressReceived(t, db, tenantID, strings.Repeat("8", 64), periodStart.Add(2*time.Hour), 10, now.Add(-time.Hour))
	refused(t, stale, "a report committed after the rating")

	// An operator grant waives the base fee after the rating.
	stale = prepare(t)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.subscription_operator_grants (subscription_id, base_price, waive_usage, collection, expires_at, reason, granted_at)
		VALUES ($1::uuid, 0, false, 'provider', NOW() + INTERVAL '1 hour', 'beta supporter', NOW())`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	refused(t, stale, "a grant set after the rating")

	// The grant expires after a rating made while it was in force.
	stale = prepare(t)
	if _, err := db.ExecContext(ctx, `UPDATE purser.subscription_operator_grants SET expires_at = NOW() - INTERVAL '1 second' WHERE subscription_id = $1::uuid`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	refused(t, stale, "the grant expired after the rating")

	// Rated again, the switch commits; its transaction first rolls back once.
	closing := prepare(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if closed, commitErr := closing.CommitTx(ctx, tx); commitErr != nil || !closed {
		t.Fatalf("first CommitTx = %v %v", closed, commitErr)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if closed, err := commitSwitchToPrepaid(ctx, db, closing, tenantID, paygID); err != nil || !closed {
		t.Fatalf("switch = closed %v err %v", closed, err)
	}
	switchAt, _ := subscriptionPeriod(t, db, tenantID)
	invoice := readInvoice(t, db, tenantID, periodStart)
	baseCents := proratedMonthlyCents(7900, periodStart, switchAt, periodStart, time.Time{})
	if invoice.metered != "2.20" || invoice.base != cents(baseCents) || invoice.credit != "5.00" || invoice.amount != cents(baseCents+220-500) {
		t.Fatalf("closing invoice = base %s metered %s credit %s amount %s, want base %s at the tier price the expired grant no longer covers, 110 GiB at 0.02, credit 5.00",
			invoice.base, invoice.metered, invoice.credit, invoice.amount, cents(baseCents))
	}
	var documents, creditRows int
	var balance int64
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM purser.billing_invoices WHERE tenant_id = $1),
		       (SELECT count(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit'),
		       (SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1)`, tenantID).Scan(&documents, &creditRows, &balance); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || creditRows != 1 || balance != 0 {
		t.Fatalf("after a rolled-back and a committed switch: %d documents, %d credit ledger rows, balance %d; want 1, 1, 0", documents, creditRows, balance)
	}
}
