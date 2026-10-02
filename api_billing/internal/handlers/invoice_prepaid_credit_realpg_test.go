//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

// holdDraftCredit makes the tenant's open draft starting at periodStart hold
// cents of prepaid invoice credit under its key, so a test can check that the
// credit returns or moves to the finalized invoice exactly once.
func holdDraftCredit(t *testing.T, db *sql.DB, tenantID string, periodStart time.Time, cents int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	change, err := reconcileInvoicePrepaidCreditTx(ctx, tx, tenantID, periodStart, cents)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("hold draft credit: %v", err)
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE purser.billing_invoices SET prepaid_credit_applied = $3::numeric / 100, amount = GREATEST(amount - $3::numeric / 100, 0)
		WHERE tenant_id = $1 AND period_start = $2 AND status = 'draft'`, tenantID, periodStart, change.AppliedCents); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if change.AppliedCents != cents {
		t.Fatalf("draft holds %d cents of credit, want %d", change.AppliedCents, cents)
	}
}

// invoiceCreditLedger is the prepaid balance and the invoice credit its
// ledger rows hold for the tenant.
func invoiceCreditLedger(t *testing.T, db *sql.DB, tenantID string) (balance, held int64, rows int) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `
		SELECT (SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1),
		       (SELECT COALESCE(SUM(-amount_cents), 0) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit'),
		       (SELECT count(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit')`,
		tenantID).Scan(&balance, &held, &rows); err != nil {
		t.Fatal(err)
	}
	return balance, held, rows
}

// A postpaid tenant's prepaid balance stays on the balance while its period
// runs: the draft states the gross amount and holds no credit, so the balance
// the tenant sees is true for the whole postpaid stretch. The invoice takes
// the credit once, when it is finalized. A draft that holds credit returns it
// on its next refresh.
func TestPostpaidPrepaidCreditIsTakenWhenTheInvoiceIsFinalized_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	periodStart := time.Now().UTC().Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	periodEnd := periodStart.AddDate(0, 1, 0)
	seedCompleteMetering(t, db, periodStart.AddDate(0, -1, 0), periodEnd.AddDate(0, 1, 0))
	tenant := newPhaseTenant(t, db, "postpaid", periodStart, periodEnd, 1000)

	// 79.00 base fee and 100 GiB at 0.02.
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("1", 64), periodStart.Add(time.Hour), 100))
	draft := readInvoice(t, db, tenant.id, periodStart)
	balance, held, rows := invoiceCreditLedger(t, db, tenant.id)
	if draft.status != "draft" || draft.credit != "0.00" || draft.amount != "81.00" || balance != 1000 || held != 0 || rows != 0 {
		t.Fatalf("draft = status %s credit %s amount %s, balance %d, credit held %d in %d rows; want a draft of 81.00 holding no credit and the whole 1000 on the balance",
			draft.status, draft.credit, draft.amount, balance, held, rows)
	}

	holdDraftCredit(t, db, tenant.id, periodStart, 1000)
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("2", 64), periodStart.Add(2*time.Hour), 60))
	draft = readInvoice(t, db, tenant.id, periodStart)
	balance, held, rows = invoiceCreditLedger(t, db, tenant.id)
	if draft.credit != "0.00" || draft.amount != "82.20" || balance != 1000 || held != 0 || rows != 2 {
		t.Fatalf("refreshed draft = credit %s amount %s, balance %d, credit held %d in %d rows; want the held 1000 returned once and a draft of 82.20 without credit",
			draft.credit, draft.amount, balance, held, rows)
	}

	tenant.monthEnd(t)
	invoice := readInvoice(t, db, tenant.id, periodStart)
	balance, held, rows = invoiceCreditLedger(t, db, tenant.id)
	if invoice.id != draft.id || invoice.status == "draft" || invoice.credit != "10.00" || invoice.amount != "72.20" ||
		balance != 0 || held != 1000 || rows != 3 {
		t.Fatalf("finalized invoice = id %s (draft %s) status %s credit %s amount %s, balance %d, credit held %d in %d rows; "+
			"want the draft finalized with the whole 10.00 balance as credit, 72.20 due, taken in one ledger row",
			invoice.id, draft.id, invoice.status, invoice.credit, invoice.amount, balance, held, rows)
	}

	// The next period's draft leaves the empty balance and the finalized
	// invoice's credit alone.
	nextStart, _ := subscriptionPeriod(t, db, tenant.id)
	tenant.receive(t, egressReport(tenant.id, strings.Repeat("3", 64), nextStart.Add(time.Hour), 10))
	next := readInvoice(t, db, tenant.id, nextStart)
	balance, held, rows = invoiceCreditLedger(t, db, tenant.id)
	if next.status != "draft" || next.credit != "0.00" || balance != 0 || held != 1000 || rows != 3 {
		t.Fatalf("next draft = status %s credit %s, balance %d, credit held %d in %d rows; want no credit moved", next.status, next.credit, balance, held, rows)
	}
}

// Moving a tenant back to prepaid returns the credit its open draft holds and
// clears it from the draft.
func TestReturnOpenInvoicePrepaidCredit_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID, supporterID := uuid.NewString(), uuid.NewString()
	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level)
			VALUES ($1, 'supporter', 'Supporter', 79.00, 'EUR', 2)`, []any{supporterID}},
		{`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end, presentment_currency)
			VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', $3, $4, 'EUR')`, []any{tenantID, supporterID, periodStart, periodEnd}},
		{`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 1000, 'EUR')`, []any{tenantID}},
	} {
		if _, err := db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement.sql)
		}
	}
	logger := logging.NewLogger()
	jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger}}
	if err := jm.updateInvoiceDraft(ctx, tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft: %v", err)
	}
	holdDraftCredit(t, db, tenantID, periodStart, 1000)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	returned, err := ReturnOpenInvoicePrepaidCreditTx(ctx, tx, tenantID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("ReturnOpenInvoicePrepaidCreditTx: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(returned) != 1 || returned[0].ReturnedCents != 1000 || returned[0].BalanceCents != 1000 || !returned[0].PeriodStart.Equal(periodStart) {
		t.Fatalf("returned = %+v, want the draft's 1000 cents for 2026-09", returned)
	}

	var balance int64
	var credit string
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1), prepaid_credit_applied::text
		FROM purser.billing_invoices WHERE tenant_id = $1 AND status = 'draft'`, tenantID).Scan(&balance, &credit); err != nil {
		t.Fatal(err)
	}
	if balance != 1000 || credit != "0.00" {
		t.Fatalf("after the return balance %d, draft credit %s; want 1000, 0.00", balance, credit)
	}
}
