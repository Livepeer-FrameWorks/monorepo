//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

// A serialization failure on the last statement of the invoice transaction makes generateMonthlyInvoices replay the
// whole closure after prepaid credit, the invoice, its lines, and the email outbox row were written once. The replay
// must commit exactly one invoice with the same derived amounts, deduct prepaid credit once, and advance the
// subscription by one period.
func TestMonthlyInvoiceReplaysAfterSerializationFailure_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID, tierID := uuid.NewString(), uuid.NewString()
	periodStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, metering_enabled)
			VALUES ($1, $2, 'Invoice replay', 20.00, 'EUR', false)`, []any{tierID, "invoice-replay-" + tierID}},
		{`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_period_start, billing_period_end)
			VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', $3, $4)`, []any{tenantID, tierID, periodStart, periodEnd}},
		{`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 500, 'EUR')`, []any{tenantID}},
		// nextval survives the rollback, so the fault fires on the first attempt only and the replay proceeds.
		{`CREATE SEQUENCE public.invoice_replay_fault`, nil},
		{`CREATE FUNCTION public.invoice_replay_fault() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF nextval('public.invoice_replay_fault') = 1 THEN
					RAISE EXCEPTION 'injected serialization failure' USING ERRCODE = '40001';
				END IF;
				RETURN NEW;
			END $$`, nil},
		{`CREATE TRIGGER invoice_replay_fault AFTER UPDATE OF billing_period_end ON purser.tenant_subscriptions
			FOR EACH ROW EXECUTE FUNCTION public.invoice_replay_fault()`, nil},
	} {
		if _, err := db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement.sql)
		}
	}

	logger := logging.NewLogger()
	jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger}}
	jm.generateMonthlyInvoices(ctx)

	var attempts int64
	if err := db.QueryRowContext(ctx, `SELECT last_value FROM public.invoice_replay_fault`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("invoice transaction reached the subscription advance %d times, want the failed attempt and one replay", attempts)
	}

	var invoices int
	var amount, base, credit, status, usageJSON string
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) OVER (), amount::text, base_amount::text, prepaid_credit_applied::text, status, usage_details::text
		FROM purser.billing_invoices WHERE tenant_id = $1`, tenantID).Scan(&invoices, &amount, &base, &credit, &status, &usageJSON); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	if invoices != 1 || amount != "15.00" || base != "20.00" || credit != "5.00" || status != "pending" {
		t.Fatalf("invoice = count %d amount %s base %s credit %s status %s, want one pending invoice of 15.00 after 5.00 credit on 20.00",
			invoices, amount, base, credit, status)
	}
	var usage map[string]any
	if err := json.Unmarshal([]byte(usageJSON), &usage); err != nil {
		t.Fatal(err)
	}
	if _, collection := usage["collection"]; collection {
		t.Fatalf("usage details carry a collection decision without a collection provider: %s", usageJSON)
	}

	var balance int64
	var creditTransactions int
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1),
		       (SELECT count(*) FROM purser.balance_transactions WHERE tenant_id = $1)`, tenantID).Scan(&balance, &creditTransactions); err != nil {
		t.Fatal(err)
	}
	if balance != 0 || creditTransactions != 1 {
		t.Fatalf("prepaid balance %d with %d transactions, want the 500 cent credit deducted once", balance, creditTransactions)
	}

	var lines, emails int
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM purser.invoice_line_items WHERE tenant_id = $1),
		       (SELECT count(*) FROM purser.invoice_email_outbox WHERE tenant_id = $1)`, tenantID).Scan(&lines, &emails); err != nil {
		t.Fatal(err)
	}
	if lines != 1 || emails != 1 {
		t.Fatalf("invoice has %d line items and %d queued emails, want the base line and one email", lines, emails)
	}

	var nextStart, nextEnd time.Time
	if err := db.QueryRowContext(ctx, `SELECT billing_period_start, billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&nextStart, &nextEnd); err != nil {
		t.Fatal(err)
	}
	if !nextStart.Equal(periodEnd) || !nextEnd.Equal(periodEnd.Add(periodEnd.Sub(periodStart))) {
		t.Fatalf("subscription period advanced to %s..%s, want one period from %s", nextStart, nextEnd, periodEnd)
	}
}
