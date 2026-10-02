//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// phaseCluster is the platform cluster the usage reports of these tests ran
// on; its usage rates at the tenant's tier prices.
const phaseCluster = "cluster-platform-phase"

// phaseQuartermaster resolves phaseCluster as a platform-official cluster.
type phaseQuartermaster struct{}

func (phaseQuartermaster) GetCluster(_ context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error) {
	if clusterID != phaseCluster {
		return nil, errors.New("cluster not found")
	}
	return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{
		ClusterId: clusterID, IsPlatformOfficial: true,
	}}, nil
}

func (phaseQuartermaster) GetTenant(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
	return &quartermasterpb.GetTenantResponse{}, nil
}

func (phaseQuartermaster) MaterializeClusterAccess(context.Context, *quartermasterpb.MaterializeClusterAccessRequest) error {
	return nil
}

func (phaseQuartermaster) RevokeMaterializedClusterAccess(context.Context, *quartermasterpb.RevokeMaterializedClusterAccessRequest) error {
	return nil
}

// egressReport is a usage report of gib GiB of egress on phaseCluster in the
// five-minute window at start.
func egressReport(tenantID, reportID string, start time.Time, gib float64) models.UsageSummary {
	return models.UsageSummary{
		TenantID: tenantID, ClusterID: phaseCluster, SourceID: "periscope-default", ReportID: reportID,
		PeriodStart: start, PeriodEnd: start.Add(5 * time.Minute),
		Meters: []models.MeterQuantity{{Meter: "egress_gb", Unit: "gibibyte", Quantity: gib}},
	}
}

// phaseTenant is a tenant on the split tiers (splitTiers) with its jobs.
type phaseTenant struct {
	db                     *sql.DB
	jobs                   *JobManager
	svc                    *Service
	id                     string
	paygID, supporterID    string
	periodStart, periodEnd time.Time
}

// newPhaseTenant seeds a tenant on model in [periodStart, periodEnd), with a
// prepaid balance of balanceCents.
func newPhaseTenant(t *testing.T, db *sql.DB, model string, periodStart, periodEnd time.Time, balanceCents int64) *phaseTenant {
	t.Helper()
	paygID, supporterID := splitTiers(t, db)
	tierID := supporterID
	if model == "prepaid" {
		tierID = paygID
	}
	tenantID := uuid.NewString()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email,
			billing_period_start, billing_period_end, next_billing_date, presentment_currency)
		VALUES ($1, $2, 'active', $3, 'billing@example.com', $4, $5, $5, 'EUR')`,
		tenantID, tierID, model, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, $2, 'EUR')`,
		tenantID, balanceCents); err != nil {
		t.Fatal(err)
	}
	logger := logging.NewLogger()
	svc := &Service{db: db, logger: logger, qmClient: phaseQuartermaster{}}
	return &phaseTenant{
		db: db, jobs: &JobManager{db: db, logger: logger, billing: svc}, svc: svc, id: tenantID,
		paygID: paygID, supporterID: supporterID, periodStart: periodStart, periodEnd: periodEnd,
	}
}

// receive stores a usage report and processes it under the model it was
// received under, the way handleUsageReport does.
func (p *phaseTenant) receive(t *testing.T, report models.UsageSummary) string {
	t.Helper()
	ctx := context.Background()
	accepted, model, err := p.jobs.receiveUsageSummary(ctx, report, "kafka-test")
	if err != nil {
		t.Fatalf("receive report %s: %v", report.ReportID, err)
	}
	p.process(t, report, accepted, model)
	return model
}

func (p *phaseTenant) process(t *testing.T, report models.UsageSummary, accepted []canonicalUsageDelta, model string) {
	t.Helper()
	ctx := context.Background()
	if model == "prepaid" {
		if err := p.jobs.processPrepaidUsage(ctx, report, accepted); err != nil {
			t.Fatalf("settle report %s: %v", report.ReportID, err)
		}
		return
	}
	if err := p.jobs.updateInvoiceDraft(ctx, p.id); err != nil {
		t.Fatalf("update the draft for report %s: %v", report.ReportID, err)
	}
}

func (p *phaseTenant) prepareToPrepaid(t *testing.T) *PostpaidPhaseClose {
	t.Helper()
	closing, err := PreparePostpaidPhaseClose(context.Background(), p.db, p.jobs.logger, p.svc, p.id)
	if err != nil {
		t.Fatalf("PreparePostpaidPhaseClose: %v", err)
	}
	return closing
}

func (p *phaseTenant) switchToPrepaid(t *testing.T) time.Time {
	t.Helper()
	if closed, err := commitSwitchToPrepaid(context.Background(), p.db, p.prepareToPrepaid(t), p.id, p.paygID); err != nil || !closed {
		t.Fatalf("switch to prepaid = closed %v err %v", closed, err)
	}
	start, _ := subscriptionPeriod(t, p.db, p.id)
	return start
}

// commitSwitchToPostpaid runs a prepared prepaid phase close inside a switch
// transaction the way AdminAssignTier does.
func (p *phaseTenant) commitSwitchToPostpaid(closing *PrepaidPhaseClose) (bool, error) {
	ctx := context.Background()
	closed := false
	err := withTx(ctx, p.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM purser.tenant_subscriptions WHERE tenant_id = $1 FOR UPDATE`, p.id); err != nil {
			return err
		}
		var err error
		if closed, err = closing.CommitTx(ctx, tx); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE purser.tenant_subscriptions SET billing_model = 'postpaid', tier_id = $2, updated_at = NOW()
			WHERE tenant_id = $1`, p.id, p.supporterID)
		return err
	})
	if err == nil && closed {
		closing.Log()
	}
	return closed, err
}

func (p *phaseTenant) switchToPostpaid(t *testing.T) time.Time {
	t.Helper()
	closing, err := PreparePrepaidPhaseClose(context.Background(), p.db, p.jobs.logger, p.svc, p.id)
	if err != nil {
		t.Fatalf("PreparePrepaidPhaseClose: %v", err)
	}
	if closed, err := p.commitSwitchToPostpaid(closing); err != nil || !closed {
		t.Fatalf("switch to postpaid = closed %v err %v", closed, err)
	}
	start, _ := subscriptionPeriod(t, p.db, p.id)
	return start
}

// monthEnd runs the month-end job a day after the period ended.
func (p *phaseTenant) monthEnd(t *testing.T) {
	t.Helper()
	at := p.periodEnd.Add(24 * time.Hour)
	due, err := purserdb.New(p.db).ListSubscriptionsDueForInvoice(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	var mine []purserdb.ListSubscriptionsDueForInvoiceRow
	for _, row := range due {
		if row.TenantID == p.id {
			mine = append(mine, row)
		}
	}
	if generated := p.jobs.finalizeSubscriptionPeriods(context.Background(), mine, at, time.Time{}); generated != 1 {
		t.Fatalf("month end closed %d periods, want 1", generated)
	}
}

func (p *phaseTenant) balance(t *testing.T) int64 {
	t.Helper()
	var cents int64
	if err := p.db.QueryRowContext(context.Background(), `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1`, p.id).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	return cents
}

// egressOn is the egress GiB on the tenant's finalized billing document
// starting at periodStart.
func (p *phaseTenant) egressOn(t *testing.T, periodStart time.Time) float64 {
	t.Helper()
	var gib float64
	if err := p.db.QueryRowContext(context.Background(), `
		SELECT COALESCE(SUM(line.quantity), 0)::float8
		FROM purser.billing_invoices invoice
		JOIN purser.invoice_line_items line ON line.invoice_id = invoice.id AND line.meter = 'egress_gb'
		WHERE invoice.tenant_id = $1 AND invoice.period_start = $2 AND invoice.status NOT IN ('draft', 'manual_review')`,
		p.id, periodStart).Scan(&gib); err != nil {
		t.Fatal(err)
	}
	return gib
}

// statementDetails reads the statement block of the tenant's prepaid
// statement starting at periodStart.
func (p *phaseTenant) statementDetails(t *testing.T, periodStart time.Time) map[string]any {
	t.Helper()
	statement := readInvoice(t, p.db, p.id, periodStart)
	if statement.kind != "prepaid_statement" {
		t.Fatalf("document of %s is a %s, want a prepaid statement", periodStart, statement.kind)
	}
	details, ok := statement.details["statement"].(map[string]any)
	if !ok {
		t.Fatalf("statement of %s has no statement details: %v", periodStart, statement.details)
	}
	return details
}

// waitForLockWaiter waits until a session of the test database waits on a row
// lock.
func waitForLockWaiter(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := db.QueryRowContext(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no session waited on the subscription row lock")
}

// waitForUsageLockWaiter waits until a session of the test database waits on
// a lock in LockSubscriptionForUsage, and fails when a session waits on a lock
// in any other statement first.
func waitForUsageLockWaiter(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var usage, other int
		if err := db.QueryRowContext(context.Background(), `
			SELECT count(*) FILTER (WHERE query LIKE '%name: LockSubscriptionForUsage%'),
			       count(*) FILTER (WHERE query NOT LIKE '%name: LockSubscriptionForUsage%')
			FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&usage, &other); err != nil {
			t.Fatal(err)
		}
		if other > 0 {
			t.Fatal("a session waits on a lock outside LockSubscriptionForUsage: the settlement went past the subscription row")
		}
		if usage > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no session waited on the subscription row in LockSubscriptionForUsage")
}

// A usage report whose transaction began before a switch is billed by
// exactly one phase, whenever it commits. A report that holds the
// subscription row when the switch reaches it makes the switch wait for it,
// count it, and rate the closing invoice again with it. A report that takes
// the row after the switch committed is dated no earlier than the switch,
// so it belongs to the phase that followed, under that phase's model; so does
// a usage correction it carries.
func TestUsageReportInFlightAtASwitchIsBilledByExactlyOnePhase_RealPG(t *testing.T) { //nolint:funlen // Both orderings of one report against one switch.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	periodStart := now.Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodStart.AddDate(0, 2, 0))
	tenant := newPhaseTenant(t, db, "postpaid", periodStart, periodStart.AddDate(0, 1, 0), 0)
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("1", 64), periodStart.Add(2*time.Hour), 100))

	// The report's transaction begins before the switch is rated and writes
	// its record, then the switch reaches the subscription row.
	inFlight, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inFlight.Rollback() }()
	if _, err = inFlight.ExecContext(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	stale := tenant.prepareToPrepaid(t)
	report := egressReport(tenant.id, strings.Repeat("2", 64), periodStart.Add(3*time.Hour), 10)
	accepted, model, err := tenant.jobs.receiveUsageSummaryTx(ctx, inFlight, report, "kafka-test")
	if err != nil || model != "postpaid" {
		t.Fatalf("receive the in-flight report = model %q err %v, want postpaid", model, err)
	}
	switched := make(chan error, 1)
	go func() {
		_, switchErr := commitSwitchToPrepaid(ctx, db, stale, tenant.id, tenant.paygID)
		switched <- switchErr
	}()
	waitForLockWaiter(t, db)
	select {
	case switchErr := <-switched:
		t.Fatalf("the switch finished (%v) while a report it closes over was uncommitted", switchErr)
	default:
	}
	if err = inFlight.Commit(); err != nil {
		t.Fatal(err)
	}
	if switchErr := <-switched; !errors.Is(switchErr, ErrPostpaidPhaseChanged) {
		t.Fatalf("switch after the in-flight report committed = %v, want ErrPostpaidPhaseChanged", switchErr)
	}
	tenant.process(t, report, accepted, model)
	switchAt := tenant.switchToPrepaid(t)
	if gib := tenant.egressOn(t, periodStart); gib != 110 {
		t.Fatalf("closing invoice egress = %v GiB, want 110: the in-flight report belongs to the postpaid phase", gib)
	}

	// A report whose transaction began before the next switch, but reaches
	// the subscription row after that switch committed.
	late, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = late.Rollback() }()
	if _, err = late.ExecContext(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	toPostpaid := tenant.switchToPostpaid(t)
	lateReport := egressReport(tenant.id, strings.Repeat("3", 64), toPostpaid.Truncate(5*time.Minute).Add(-5*time.Minute), 20)
	// The late report also carries a correction whose window straddles the
	// switch; it is dated like the report's records.
	lateReport.UsageAdjustments = []models.UsageAdjustment{{
		SourceSystem: "periscope.projection_divergences", SourceID: "late-correction", UsageType: "egress_gb",
		Unit: "gibibyte", ClusterID: phaseCluster, DeltaValue: 5,
		PeriodStart: toPostpaid.Add(-time.Hour), PeriodEnd: toPostpaid.Add(time.Hour), Reason: "projection_divergence",
	}}
	lateAccepted, lateModel, err := tenant.jobs.receiveUsageSummaryTx(ctx, late, lateReport, "kafka-test")
	if err != nil || lateModel != "postpaid" {
		t.Fatalf("receive the late report = model %q err %v, want postpaid, the phase in force when it reached the row", lateModel, err)
	}
	if err = late.Commit(); err != nil {
		t.Fatal(err)
	}
	var receivedAt time.Time
	if err = db.QueryRowContext(ctx, `SELECT created_at FROM purser.usage_records WHERE report_id = $1`, lateReport.ReportID).Scan(&receivedAt); err != nil {
		t.Fatal(err)
	}
	if receivedAt.Before(toPostpaid) {
		t.Fatalf("late report received at %s, before the switch at %s it committed after", receivedAt, toPostpaid)
	}
	var correctedAt time.Time
	if err = db.QueryRowContext(ctx, `SELECT created_at FROM purser.usage_adjustments WHERE source_id = 'late-correction'`).Scan(&correctedAt); err != nil {
		t.Fatal(err)
	}
	if correctedAt.Before(toPostpaid) {
		t.Fatalf("late correction received at %s, before the switch at %s it committed after", correctedAt, toPostpaid)
	}
	tenant.process(t, lateReport, lateAccepted, lateModel)
	if gib := tenant.egressOn(t, switchAt); gib != 0 {
		t.Fatalf("prepaid statement of %s..%s holds %v GiB, want none: the late report reached Purser after it closed", switchAt, toPostpaid, gib)
	}
	draft := readInvoice(t, db, tenant.id, toPostpaid)
	if draft.status != "draft" || draft.metered != "0.50" {
		t.Fatalf("postpaid draft after the late report = status %s metered %s, want a draft of 20 GiB and the 5 GiB correction at 0.02", draft.status, draft.metered)
	}

	tenant.monthEnd(t)
	invoice := readInvoice(t, db, tenant.id, toPostpaid)
	if invoice.status == "draft" || invoice.metered != "0.50" {
		t.Fatalf("postpaid invoice = status %s metered %s, want finalized with the 20 GiB and the 5 GiB correction at 0.02", invoice.status, invoice.metered)
	}
	var appliedTo string
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(applied_invoice_id::text, '') FROM purser.usage_adjustments WHERE source_id = 'late-correction'`).Scan(&appliedTo); err != nil {
		t.Fatal(err)
	}
	if appliedTo != invoice.id {
		t.Fatalf("late correction applied to %q, want the postpaid invoice %s and never the closed statement", appliedTo, invoice.id)
	}
}

// A report received after a switch's rating but processed under the model
// the tenant was leaving belongs to the phase in force when it reached
// Purser, and is paid there exactly once. After a switch to prepaid it was
// put on a postpaid draft and no prepaid settlement paid it: the prepaid
// statement settles it from the balance. After a switch to postpaid it was
// settled from the balance: the closing statement returns that, the postpaid
// invoice bills it, and a settlement that runs after the switch leaves the
// balance alone.
func TestUsageProcessedUnderTheModelATenantLeftIsPaidOnceByItsPhase_RealPG(t *testing.T) { //nolint:funlen // Both switch directions.
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	ctx := context.Background()
	now := time.Now().UTC()
	periodStart := now.Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)

	t.Run("to prepaid", func(t *testing.T) {
		db := startPurserUsageRealPG(t)
		seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
		tenant := newPhaseTenant(t, db, "postpaid", periodStart, periodEnd, 0)
		tenant.receive(t, egressReport(tenant.id, strings.Repeat("4", 64), periodStart.Add(2*time.Hour), 100))
		closing := tenant.prepareToPrepaid(t)
		if model := tenant.receive(t, egressReport(tenant.id, strings.Repeat("5", 64), now.Truncate(5*time.Minute).Add(-5*time.Minute), 30)); model != "postpaid" {
			t.Fatalf("report before the switch committed processed as %s, want postpaid", model)
		}
		if closed, err := commitSwitchToPrepaid(ctx, db, closing, tenant.id, tenant.paygID); err != nil || !closed {
			t.Fatalf("switch = closed %v err %v", closed, err)
		}
		switchAt, _ := subscriptionPeriod(t, db, tenant.id)
		if gib := tenant.egressOn(t, periodStart); gib != 100 {
			t.Fatalf("closing invoice egress = %v GiB, want 100: the 30 GiB reached Purser after the switch's rating", gib)
		}
		if balance := tenant.balance(t); balance != 0 {
			t.Fatalf("balance after the switch = %d, want 0", balance)
		}

		tenant.monthEnd(t)
		details := tenant.statementDetails(t, switchAt)
		if tenant.egressOn(t, switchAt) != 30 || details["settled_with_statement_cents"] != float64(30) || details["paid_from_balance_cents"] != float64(30) {
			t.Fatalf("prepaid statement = %v GiB, details %v; want the 30 GiB at 0.01 settled from the balance with it", tenant.egressOn(t, switchAt), details)
		}
		if balance := tenant.balance(t); balance != -30 {
			t.Fatalf("balance after month end = %d, want -30: the 30 GiB of the prepaid phase paid once", balance)
		}
	})

	t.Run("to postpaid", func(t *testing.T) {
		db := startPurserUsageRealPG(t)
		seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
		tenant := newPhaseTenant(t, db, "prepaid", periodStart, periodEnd, 1000)
		tenant.receive(t, egressReport(tenant.id, strings.Repeat("6", 64), periodStart.Add(2*time.Hour), 200))
		// A prepaid report received before the switch is rated, but settled
		// only after the switch committed.
		unsettled := egressReport(tenant.id, strings.Repeat("7", 64), periodStart.Add(3*time.Hour), 50)
		unsettledAccepted, unsettledModel, err := tenant.jobs.receiveUsageSummary(ctx, unsettled, "kafka-test")
		if err != nil || unsettledModel != "prepaid" {
			t.Fatalf("receive = %q %v", unsettledModel, err)
		}
		closing, err := PreparePrepaidPhaseClose(ctx, db, tenant.jobs.logger, tenant.svc, tenant.id)
		if err != nil {
			t.Fatal(err)
		}
		// Received after the rating, before the switch commits: settled from
		// the balance as prepaid.
		if model := tenant.receive(t, egressReport(tenant.id, strings.Repeat("8", 64), now.Truncate(5*time.Minute).Add(-5*time.Minute), 40)); model != "prepaid" {
			t.Fatalf("report before the switch committed processed as %s, want prepaid", model)
		}
		if balance := tenant.balance(t); balance != 1000-200-50-40 {
			t.Fatalf("balance before the switch = %d, want %d", balance, 1000-200-50-40)
		}
		if closed, err := tenant.commitSwitchToPostpaid(closing); err != nil || !closed {
			t.Fatalf("switch = closed %v err %v", closed, err)
		}
		switchAt, _ := subscriptionPeriod(t, db, tenant.id)
		tenant.process(t, unsettled, unsettledAccepted, unsettledModel)
		details := tenant.statementDetails(t, periodStart)
		// The settlement of the 40 GiB took the unsettled 50 GiB too, as it
		// rates the period cumulatively; the statement returns the 40 GiB of
		// the postpaid phase.
		if tenant.egressOn(t, periodStart) != 250 || details["paid_from_balance_cents"] != float64(250) || details["settled_with_statement_cents"] != float64(-40) {
			t.Fatalf("closing statement = %v GiB, details %v; want 250 GiB paid from the balance, the 40 GiB of the postpaid phase returned",
				tenant.egressOn(t, periodStart), details)
		}
		if balance := tenant.balance(t); balance != 1000-250 {
			t.Fatalf("balance after the switch and the late settlement = %d, want %d: the prepaid phase's 250 GiB at 0.01, once", balance, 1000-250)
		}

		tenant.monthEnd(t)
		invoice := readInvoice(t, db, tenant.id, switchAt)
		if gib := tenant.egressOn(t, switchAt); gib != 40 || invoice.metered != "0.80" {
			t.Fatalf("postpaid invoice = %v GiB metered %s, want the 40 GiB received after the switch's rating at 0.02", gib, invoice.metered)
		}
	})
}

// A usage correction belongs to the phase in force when it reached Purser,
// like a usage record: a correction whose window straddles a switch is
// billed by the closing invoice of the phase it arrived in and nowhere else.
func TestUsageCorrectionStraddlingASwitchIsBilledOnce_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	periodStart := now.Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	tenant := newPhaseTenant(t, db, "postpaid", periodStart, periodEnd, 0)
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("9", 64), periodStart.Add(2*time.Hour), 100))
	correction := egressReport(tenant.id, strings.Repeat("a", 64), periodStart.Add(4*time.Hour), 0)
	correction.Meters = nil
	correction.UsageAdjustments = []models.UsageAdjustment{{
		SourceSystem: "periscope.projection_divergences", SourceID: "straddling-correction", UsageType: "egress_gb",
		Unit: "gibibyte", ClusterID: phaseCluster, DeltaValue: 10,
		PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(time.Hour), Reason: "projection_divergence",
	}}
	tenant.receive(t, correction)

	switchAt := tenant.switchToPrepaid(t)
	closing := readInvoice(t, db, tenant.id, periodStart)
	if closing.metered != "2.20" {
		t.Fatalf("closing invoice metered = %s, want 2.20: 100 GiB and the 10 GiB correction at 0.02", closing.metered)
	}
	var appliedTo string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(applied_invoice_id::text, '') FROM purser.usage_adjustments WHERE source_id = 'straddling-correction'`).Scan(&appliedTo); err != nil {
		t.Fatal(err)
	}
	if appliedTo != closing.id {
		t.Fatalf("correction applied to %q, want the closing invoice %s", appliedTo, closing.id)
	}

	tenant.receive(t, egressReport(tenant.id, strings.Repeat("b", 64), switchAt.Truncate(5*time.Minute).Add(5*time.Minute), 30))
	if balance := tenant.balance(t); balance != -30 {
		t.Fatalf("balance after prepaid usage = %d, want -30: 30 GiB at 0.01, without the correction the closing invoice billed", balance)
	}
	tenant.monthEnd(t)
	if gib := tenant.egressOn(t, switchAt); gib != 30 {
		t.Fatalf("prepaid statement egress = %v GiB, want 30", gib)
	}
	if balance := tenant.balance(t); balance != -30 {
		t.Fatalf("balance after month end = %d, want -30", balance)
	}
}

// A tenant moved from postpaid to prepaid and back within one period pays
// the postpaid tier's base fee for the time it was postpaid, and nothing for
// the prepaid stretch between: the closing invoice and the month-end invoice
// of the rest of the period each pay their own share of the period.
//
// The switches happen at the database clock, so the period is placed against
// it: a 72.00 fee over a 30-day period of 2,592,000 s is one cent per 360 s,
// and the period starts 10 d 2 min 45 s = 864,165 s before the clock is read.
// A switch s seconds later sits at (864,165 + s) / 360 = 2400.458 + s/360
// cents of the period, 2400 while s < 15: the closing invoice pays 24.00, the
// prepaid stretch between the two switches 0.00, and the month-end invoice
// the remaining 7200 - 2400 cents, 48.00. A share split 30 s later than the
// switch would round to 2401 and charge 24.01 and 47.99.
func TestRoundTripPaysThePostpaidBaseFeeOnlyForItsOwnTime_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedCompleteMetering(t, db, now.Truncate(time.Hour).AddDate(0, -2, 0), now.Truncate(time.Hour).AddDate(0, 2, 0))
	var clock time.Time
	if err := db.QueryRowContext(ctx, `SELECT NOW()`).Scan(&clock); err != nil {
		t.Fatal(err)
	}
	clock = clock.UTC()
	periodStart := clock.Add(-(10*24*time.Hour + 2*time.Minute + 45*time.Second))
	periodEnd := periodStart.Add(30 * 24 * time.Hour)
	tenant := newPhaseTenant(t, db, "postpaid", periodStart, periodEnd, 0)
	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET base_price = 72.00 WHERE id = $1`, tenant.supporterID); err != nil {
		t.Fatal(err)
	}

	toPrepaid := tenant.switchToPrepaid(t)
	toPostpaid := tenant.switchToPostpaid(t)
	if drift := toPostpaid.Sub(clock); drift >= 15*time.Second {
		t.Fatalf("the switches ran %s after the clock was read; the amounts below hold for less than 15 s", drift)
	}
	tenant.monthEnd(t)

	closing := readInvoice(t, db, tenant.id, periodStart)
	rest := readInvoice(t, db, tenant.id, toPostpaid)
	if closing.base != "24.00" || rest.base != "48.00" || rest.status == "draft" {
		t.Fatalf("base fees = closing %s, rest of the period %s (%s); want 24.00 and 48.00 (switches at %s and %s)",
			closing.base, rest.base, rest.status, toPrepaid, toPostpaid)
	}
	prepaidStretch := decimal.RequireFromString("0.00")
	paid := decimal.RequireFromString(closing.base).Add(decimal.RequireFromString(rest.base))
	if !paid.Add(prepaidStretch).Equal(decimal.RequireFromString("72.00")) {
		t.Fatalf("postpaid base fees %s plus the prepaid stretch's share %s = %s, want exactly the period's 72.00",
			paid, prepaidStretch, paid.Add(prepaidStretch))
	}
}

// A period closed early and the period after it start in the same month.
// Each billing document holds its invoice credit under its own key, so the
// next period's draft neither takes nor returns the credit the closed
// invoice holds.
func TestEarlyClosedPeriodKeepsItsInvoiceCreditFromTheNextDraft_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	periodStart := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	closeAt := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), closeAt.AddDate(0, 1, 0))
	tenant := newPhaseTenant(t, db, "postpaid", periodStart, periodStart.AddDate(0, 1, 0), 1000)
	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET base_price = 0 WHERE id = $1`, tenant.supporterID); err != nil {
		t.Fatal(err)
	}
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("c", 64), periodStart.Add(24*time.Hour), 1000))
	if _, err := tenant.jobs.CloseAdvanceBilledPeriod(ctx, tenant.id, closeAt); err != nil {
		t.Fatalf("close the period early: %v", err)
	}
	closed := readInvoice(t, db, tenant.id, periodStart)
	if closed.status != "pending" || closed.credit != "10.00" || tenant.balance(t) != 0 {
		t.Fatalf("early-closed invoice = status %s credit %s, balance %d; want the whole 10.00 balance as its credit", closed.status, closed.credit, tenant.balance(t))
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions SET billing_period_start = $2, billing_period_end = $3, next_billing_date = $3
		WHERE tenant_id = $1`, tenant.id, closeAt, closeAt.AddDate(0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("d", 64), closeAt.Add(time.Hour), 1))
	draft := readInvoice(t, db, tenant.id, closeAt)
	if draft.status != "draft" || draft.credit != "0.00" {
		t.Fatalf("next period's draft = status %s credit %s, want a draft without credit: the balance is empty", draft.status, draft.credit)
	}
	if balance := tenant.balance(t); balance != 0 {
		t.Fatalf("balance after the next draft = %d, want 0: the early-closed invoice's credit stays with it", balance)
	}
	var held int64
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(-amount_cents), 0) FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = 'invoice_credit' AND description IN ('Invoice credit: 2026-03', 'Invoice credit returned: 2026-03')`,
		tenant.id).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 1000 {
		t.Fatalf("credit held under the early-closed invoice's key = %d, want 1000", held)
	}
}

// A tenant billed its base fee in advance that switches to prepaid gets the
// unused share of that period's base fee back on its prepaid balance once:
// from the switch the prepaid tier runs, and the prepaid statement charges
// its own base fee for that time.
func TestSwitchToPrepaidReturnsTheUnusedAdvanceBaseFee_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	f := newTierChangeFixture(t, "30.00", firstBaseFeePaid)
	ctx := context.Background()
	paygID := uuid.NewString()
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid, metering_enabled)
		VALUES ($1, 'payg', 'Pay As You Go', 0, 'EUR', 0, true, true)`, paygID); err != nil {
		t.Fatal(err)
	}
	first := f.firstBaseFee(t)
	closing, err := PreparePostpaidPhaseClose(ctx, f.db, f.jobs.logger, f.service, f.tenantID)
	if err != nil {
		t.Fatalf("PreparePostpaidPhaseClose: %v", err)
	}
	if closed, commitErr := commitSwitchToPrepaid(ctx, f.db, closing, f.tenantID, paygID); commitErr != nil || !closed {
		t.Fatalf("switch = closed %v err %v", closed, commitErr)
	}
	switchAt, _ := subscriptionPeriod(t, f.db, f.tenantID)
	whole := f.end.Sub(f.start).Microseconds()
	remaining := f.end.Sub(switchAt).Microseconds()
	wantCents := decimal.NewFromInt(3000).Mul(decimal.NewFromInt(remaining)).Div(decimal.NewFromInt(whole)).Round(0).IntPart()

	var credited, rows int64
	if err = f.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount_cents), 0), count(*) FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = 'base_fee_unused_share' AND reference_id = $2::uuid`, f.tenantID, first.id).Scan(&credited, &rows); err != nil {
		t.Fatal(err)
	}
	var balance int64
	if err = f.db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = $2`, f.tenantID, billing.LedgerCurrency).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || credited != wantCents || balance != wantCents {
		t.Fatalf("unused base fee returned = %d cents in %d ledger rows, balance %d; want %d once", credited, rows, balance, wantCents)
	}
	if _, err = purserdb.New(f.db).FindOverlappedBaseFeeInvoice(ctx, purserdb.FindOverlappedBaseFeeInvoiceParams{
		TenantID: f.tenantID, PeriodStart: switchAt.Add(time.Hour),
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a later base-fee invoice of the period still finds the returned one to credit (err %v)", err)
	}
	invoice := readInvoice(t, f.db, f.tenantID, f.start)
	if invoice.base != "0.00" || invoice.details[closesPostpaidPhaseKey] != true {
		t.Fatalf("closing invoice base = %s, want 0.00: the base-fee invoice charged the period's base fee", invoice.base)
	}
}

// A Mollie-collected subscription's draft runs from the Mollie-anchored
// period start, which can differ from the stored period start. A switch to
// prepaid closes that same period: the draft becomes the closing invoice and
// no draft stays open.
func TestSwitchOfAMollieAnchoredSubscriptionFinalizesItsDraft_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	anchorEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 20)
	anchorStart := anchorEnd.AddDate(0, -1, 0)
	storedStart := anchorStart.AddDate(0, 0, -5)
	tenant := newPhaseTenant(t, db, "postpaid", storedStart, storedStart.AddDate(0, 1, 0), 0)
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET payment_method = 'mollie', mollie_subscription_id = 'sub_phase_anchor', mollie_next_payment_date = $2
		WHERE tenant_id = $1`, tenant.id, anchorEnd); err != nil {
		t.Fatal(err)
	}
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("e", 64), anchorStart.Add(time.Hour), 100))
	draft := readInvoice(t, db, tenant.id, anchorStart)
	if draft.status != "draft" {
		t.Fatalf("draft of the Mollie-anchored period = %s", draft.status)
	}

	switchAt := tenant.switchToPrepaid(t)
	invoice := readInvoice(t, db, tenant.id, anchorStart)
	if invoice.id != draft.id || invoice.status == "draft" || !invoice.periodEnd.Equal(switchAt) || invoice.metered != "2.00" {
		t.Fatalf("closing invoice = id %s (draft %s) status %s period end %s metered %s; want the draft finalized to the switch at %s with 100 GiB at 0.02",
			invoice.id, draft.id, invoice.status, invoice.periodEnd, invoice.metered, switchAt)
	}
	var open, documents int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE status IN ('draft', 'manual_review')), count(*)
		FROM purser.billing_invoices WHERE tenant_id = $1 AND base_fee_period_start IS NULL`, tenant.id).Scan(&open, &documents); err != nil {
		t.Fatal(err)
	}
	if open != 0 || documents != 1 {
		t.Fatalf("after the switch: %d open drafts of %d documents, want only the closing invoice", open, documents)
	}
	_, prepaidEnd := subscriptionPeriod(t, db, tenant.id)
	if !prepaidEnd.Equal(anchorEnd) {
		t.Fatalf("prepaid period ends %s, want the Mollie-anchored end %s", prepaidEnd, anchorEnd)
	}
}

// A switch closes its phase with the open draft that overlaps the phase also
// when the draft starts elsewhere, such as at a Mollie anchor that moved after
// the draft was written. The draft becomes the closing invoice and none stays
// open. The prepaid credit the draft held under its own period start's key
// returns to the balance once, and the closing invoice takes its credit under
// the key of the phase's start.
func TestSwitchClosesItsPhaseWithAnOverlappingDraftStartingElsewhere_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	draftEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 20)
	draftStart := draftEnd.AddDate(0, -1, 0)
	// The moved anchor's period starts on the 2nd of the month before the
	// draft's, so the two documents hold credit under different months' keys.
	phaseEnd := time.Date(draftStart.Year(), draftStart.Month(), 2, 0, 0, 0, 0, time.UTC)
	phaseStart := phaseEnd.AddDate(0, -1, 0)
	storedStart := phaseStart.AddDate(0, -1, 0)
	tenant := newPhaseTenant(t, db, "postpaid", storedStart, phaseStart, 200)
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET payment_method = 'mollie', mollie_subscription_id = 'sub_phase_moved', mollie_next_payment_date = $2
		WHERE tenant_id = $1`, tenant.id, draftEnd); err != nil {
		t.Fatal(err)
	}
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("f", 64), draftStart.Add(time.Hour), 100))
	draft := readInvoice(t, db, tenant.id, draftStart)
	if draft.status != "draft" || draft.credit != "0.00" || tenant.balance(t) != 200 {
		t.Fatalf("draft of the anchored period = status %s credit %s, balance %d; want a draft of 100 GiB at 0.02 leaving the 2.00 on the balance", draft.status, draft.credit, tenant.balance(t))
	}
	// The draft holds the whole balance as credit under its own key.
	holdDraftCredit(t, db, tenant.id, draftStart, 200)

	if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET mollie_next_payment_date = $2 WHERE tenant_id = $1`, tenant.id, phaseEnd); err != nil {
		t.Fatal(err)
	}
	tenant.switchToPrepaid(t)
	closing := readInvoice(t, db, tenant.id, phaseStart)
	if closing.id != draft.id || closing.status == "draft" || closing.metered != "2.00" || closing.credit != "2.00" {
		t.Fatalf("closing invoice = id %s (draft %s) status %s metered %s credit %s; want the draft finalized with 100 GiB at 0.02 and the 2.00 credit",
			closing.id, draft.id, closing.status, closing.metered, closing.credit)
	}
	var open int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM purser.billing_invoices
		WHERE tenant_id = $1 AND base_fee_period_start IS NULL AND status IN ('draft', 'manual_review')`, tenant.id).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("after the switch: %d open drafts, want none", open)
	}

	credit := func(description string) (cents int64, rows int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(amount_cents), 0), count(*) FROM purser.balance_transactions
			WHERE tenant_id = $1 AND reference_type = 'invoice_credit' AND description = $2`, tenant.id, description).Scan(&cents, &rows); err != nil {
			t.Fatal(err)
		}
		return cents, rows
	}
	if cents, rows := credit(invoiceCreditDescription(draftStart)); cents != -200 || rows != 1 {
		t.Fatalf("credit the draft took under its key = %d cents in %d rows, want -200 in 1", cents, rows)
	}
	if cents, rows := credit(invoiceCreditReturnedDescription(draftStart)); cents != 200 || rows != 1 {
		t.Fatalf("credit returned under the draft's key = %d cents in %d rows, want 200 once", cents, rows)
	}
	if cents, rows := credit(invoiceCreditDescription(phaseStart)); cents != -200 || rows != 1 {
		t.Fatalf("credit the closing invoice holds under the phase's key = %d cents in %d rows, want -200 in 1", cents, rows)
	}
}

// A prepaid settlement takes the subscription row before the balance, as a
// switch does. A settlement that starts while a switch to postpaid holds the
// row waits for the switch, then finds the tenant postpaid and leaves the
// balance alone: the closing statement settled the report with the rest of
// the prepaid phase, once.
func TestPrepaidSettlementWaitsForASwitchClosingItsPhase_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	periodStart := now.Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	tenant := newPhaseTenant(t, db, "prepaid", periodStart, periodEnd, 1000)
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("f", 64), periodStart.Add(2*time.Hour), 200))
	unsettled := egressReport(tenant.id, strings.Repeat("0", 64), periodStart.Add(3*time.Hour), 50)
	accepted, model, err := tenant.jobs.receiveUsageSummary(ctx, unsettled, "kafka-test")
	if err != nil || model != "prepaid" {
		t.Fatalf("receive = %q %v", model, err)
	}
	closing, err := PreparePrepaidPhaseClose(ctx, db, tenant.jobs.logger, tenant.svc, tenant.id)
	if err != nil {
		t.Fatal(err)
	}

	switchTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = switchTx.Rollback() }()
	if _, err = switchTx.ExecContext(ctx, `SELECT 1 FROM purser.tenant_subscriptions WHERE tenant_id = $1 FOR UPDATE`, tenant.id); err != nil {
		t.Fatal(err)
	}
	if closed, commitErr := closing.CommitTx(ctx, switchTx); commitErr != nil || !closed {
		t.Fatalf("close the prepaid phase = closed %v err %v", closed, commitErr)
	}
	if _, err = switchTx.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions SET billing_model = 'postpaid', tier_id = $2, updated_at = NOW()
		WHERE tenant_id = $1`, tenant.id, tenant.supporterID); err != nil {
		t.Fatal(err)
	}
	settled := make(chan error, 1)
	go func() { settled <- tenant.jobs.processPrepaidUsage(ctx, unsettled, accepted) }()
	waitForUsageLockWaiter(t, db)
	select {
	case settleErr := <-settled:
		t.Fatalf("the settlement finished (%v) while a switch closing its phase held the subscription row", settleErr)
	default:
	}
	if err = switchTx.Commit(); err != nil {
		t.Fatal(err)
	}
	closing.Log()
	if settleErr := <-settled; settleErr != nil {
		t.Fatalf("settlement after the switch committed: %v", settleErr)
	}

	details := tenant.statementDetails(t, periodStart)
	if tenant.egressOn(t, periodStart) != 250 || details["paid_from_balance_cents"] != float64(250) || details["settled_with_statement_cents"] != float64(50) {
		t.Fatalf("closing statement = %v GiB, details %v; want 250 GiB paid from the balance, the unsettled 50 GiB settled with it",
			tenant.egressOn(t, periodStart), details)
	}
	var settlements int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.prepaid_usage_settlements WHERE report_id = $1`, unsettled.ReportID).Scan(&settlements); err != nil {
		t.Fatal(err)
	}
	if balance := tenant.balance(t); balance != 1000-250 || settlements != 0 {
		t.Fatalf("balance = %d with %d settlements of the report, want %d and none: the statement paid its 50 GiB",
			balance, settlements, 1000-250)
	}
}
