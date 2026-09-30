//go:build schema_verify

package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

// A prepaid tenant moved to a postpaid tier keeps its balance as invoice
// credit. The credit the period's draft holds must follow the draft: when an
// operator grant lowers the base fee after a draft already took credit, the
// credit the draft no longer uses goes back to the balance, and the invoice
// records exactly what it uses.
func TestInvoiceDraftPrepaidCreditFollowsTheDraft_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID, subscriptionID := uuid.NewString(), uuid.NewString()
	paygID, supporterID := uuid.NewString(), uuid.NewString()
	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(t, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, is_default_prepaid)
		VALUES ($1, 'payg', 'Pay As You Go', 0, 'EUR', 0, true), ($2, 'supporter', 'Supporter', 79.00, 'EUR', 2, false)`, paygID, supporterID)
	exec(t, `INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end, presentment_currency)
		VALUES ($1, $2, $3, 'active', 'prepaid', 'billing@example.com', $4, $5, 'EUR')`, subscriptionID, tenantID, paygID, periodStart, periodEnd)
	exec(t, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 1000, 'EUR')`, tenantID)
	// The operator moves the tenant to a postpaid tier; the balance stays as
	// invoice credit.
	exec(t, `UPDATE purser.tenant_subscriptions SET tier_id = $2, billing_model = 'postpaid' WHERE tenant_id = $1`, tenantID, supporterID)

	logger := logging.NewLogger()
	jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger}}
	type state struct {
		balance         int64
		amount, credit  string
		heldCredit      int64
		ledgerEntries   int
		returnedEntries int
	}
	draft := func(t *testing.T) state {
		t.Helper()
		if err := jm.updateInvoiceDraft(ctx, tenantID); err != nil {
			t.Fatalf("updateInvoiceDraft: %v", err)
		}
		var s state
		if err := db.QueryRowContext(ctx, `
			SELECT (SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1),
			       amount::text, prepaid_credit_applied::text,
			       (SELECT COALESCE(SUM(-amount_cents), 0) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit'),
			       (SELECT count(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit'),
			       (SELECT count(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_type = 'invoice_credit'
			           AND description = 'Invoice credit returned: 2026-09' AND amount_cents > 0)
			FROM purser.billing_invoices WHERE tenant_id = $1 AND period_start = $2 AND status = 'draft'
		`, tenantID, periodStart).Scan(&s.balance, &s.amount, &s.credit, &s.heldCredit, &s.ledgerEntries, &s.returnedEntries); err != nil {
			t.Fatalf("read draft: %v", err)
		}
		if s.balance+s.heldCredit != 1000 {
			t.Fatalf("balance %d + credit held by the draft %d = %d, want the tenant's 1000 cents", s.balance, s.heldCredit, s.balance+s.heldCredit)
		}
		return s
	}
	setGrantBase := func(t *testing.T, base string) {
		t.Helper()
		exec(t, `INSERT INTO purser.subscription_operator_grants (subscription_id, base_price, collection, reason)
			VALUES ($1, $2::numeric, 'invoice', 'staging partner')
			ON CONFLICT (subscription_id) DO UPDATE SET base_price = EXCLUDED.base_price`, subscriptionID, base)
	}

	// Usage arrives while the tier's 79.00 base fee is in force: the draft
	// takes the whole balance as credit.
	if s := draft(t); s.balance != 0 || s.amount != "69.00" || s.credit != "10.00" || s.heldCredit != 1000 {
		t.Fatalf("draft on the 79.00 tier = %+v, want 10.00 credit, 69.00 due, balance 0", s)
	}

	// The operator grant sets the base fee to zero; the next draft is 0.00 and
	// uses no credit, so all of it returns.
	setGrantBase(t, "0")
	if s := draft(t); s.balance != 1000 || s.amount != "0.00" || s.credit != "0.00" || s.heldCredit != 0 || s.returnedEntries != 1 {
		t.Fatalf("draft after a zero base-fee grant = %+v, want no credit, 0.00 due, balance 1000 with one return row", s)
	}

	// A draft that repeats the zero total moves nothing.
	if s := draft(t); s.balance != 1000 || s.credit != "0.00" || s.ledgerEntries != 2 {
		t.Fatalf("repeated zero draft = %+v, want balance 1000 and no new ledger rows", s)
	}

	// A 5.00 grant takes exactly 5.00.
	setGrantBase(t, "5.00")
	if s := draft(t); s.balance != 500 || s.amount != "0.00" || s.credit != "5.00" || s.heldCredit != 500 {
		t.Fatalf("draft after a 5.00 grant = %+v, want 5.00 credit, 0.00 due, balance 500", s)
	}

	// With the grant revoked the draft grows back to 79.00 and takes the rest
	// of the balance, although an earlier debit had the same amounts.
	exec(t, `DELETE FROM purser.subscription_operator_grants WHERE subscription_id = $1`, subscriptionID)
	if s := draft(t); s.balance != 0 || s.amount != "69.00" || s.credit != "10.00" || s.heldCredit != 1000 {
		t.Fatalf("draft after the grant is revoked = %+v, want 10.00 credit, 69.00 due, balance 0", s)
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
	var amount, credit string
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1), amount::text, prepaid_credit_applied::text
		FROM purser.billing_invoices WHERE tenant_id = $1 AND status = 'draft'`, tenantID).Scan(&balance, &amount, &credit); err != nil {
		t.Fatal(err)
	}
	if balance != 1000 || amount != "79.00" || credit != "0.00" {
		t.Fatalf("after the return balance %d, draft amount %s credit %s; want 1000, 79.00, 0.00", balance, amount, credit)
	}
}
