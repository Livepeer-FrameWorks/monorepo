//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

// seedClosingStatement writes the statement a switch from prepaid to postpaid
// writes for the prepaid phase [start, end).
func seedClosingStatement(t *testing.T, db *sql.DB, tenantID string, start, end time.Time) {
	t.Helper()
	details, err := json.Marshal(map[string]any{"statement": PrepaidStatementDetails{ClosesPrepaidPhase: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = purserdb.New(db).InsertPrepaidStatement(context.Background(), purserdb.InsertPrepaidStatementParams{
		TenantID: tenantID, FinalizedAt: end, BaseAmount: "0", MeteredAmount: "0", GrossMeteredAmount: "0",
		UsageDetails: details, PeriodStart: start, PeriodEnd: end,
	}); err != nil {
		t.Fatalf("seed closing statement %s..%s: %v", start, end, err)
	}
}

func subscriptionPeriod(t *testing.T, db *sql.DB, tenantID string) (time.Time, time.Time) {
	t.Helper()
	var start, end time.Time
	if err := db.QueryRowContext(context.Background(), `
		SELECT billing_period_start, billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	return start.UTC(), end.UTC()
}

// A tenant moved from prepaid to postpaid mid-month and back to prepaid keeps
// the rest of the month as its period. Its month end closes that rest with a
// statement and starts the next calendar month, not a period as short as the
// rest of the month was.
func TestPrepaidRoundTripMonthEndStartsTheNextMonth_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	ctx := context.Background()
	monthStart, monthEnd := prepaidStatementPeriod()
	logger := logging.NewLogger()

	for _, tc := range []struct {
		name string
		// switches are the times the tenant moved from prepaid to postpaid;
		// it moved back to prepaid after each.
		switches []time.Duration
	}{
		{name: "one round trip", switches: []time.Duration{monthEnd.Sub(monthStart) - 7*time.Hour - 23*time.Minute - 22*time.Second}},
		{name: "two round trips", switches: []time.Duration{10 * 24 * time.Hour, 20*24*time.Hour + 2*time.Hour + 45*time.Minute}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := startPurserUsageRealPG(t)
			seedCompleteMetering(t, db, monthStart.AddDate(0, -1, 0), monthEnd.AddDate(0, 1, 0))
			jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger}}
			tenantID := seedPrepaidTenant(t, db, monthStart, monthEnd, 1000)
			phaseStart := monthStart
			for _, offset := range tc.switches {
				switchAt := monthStart.Add(offset)
				seedClosingStatement(t, db, tenantID, phaseStart, switchAt)
				phaseStart = switchAt
			}
			if _, err := db.ExecContext(ctx, `
				UPDATE purser.tenant_subscriptions
				SET billing_period_start = $2, billing_period_end = $3, next_billing_date = $3, billing_model = 'prepaid'
				WHERE tenant_id = $1`, tenantID, phaseStart, monthEnd); err != nil {
				t.Fatal(err)
			}

			jm.generateMonthlyInvoices(ctx)

			var statementEnd time.Time
			if err := db.QueryRowContext(ctx, `
				SELECT period_end FROM purser.billing_invoices
				WHERE tenant_id = $1 AND period_start = $2 AND document_kind = 'prepaid_statement'`, tenantID, phaseStart).Scan(&statementEnd); err != nil {
				t.Fatalf("read the statement of the rest of the month: %v", err)
			}
			if !statementEnd.Equal(monthEnd) {
				t.Fatalf("statement of the rest of the month ends %s, want %s", statementEnd, monthEnd)
			}
			nextStart, nextEnd := subscriptionPeriod(t, db, tenantID)
			if !nextStart.Equal(monthEnd) || !nextEnd.Equal(monthEnd.AddDate(0, 1, 0)) {
				t.Fatalf("period after month end = %s..%s, want the next calendar month %s..%s",
					nextStart, nextEnd, monthEnd, monthEnd.AddDate(0, 1, 0))
			}
		})
	}
}

// A postpaid period that a switch from prepaid started mid-month is the rest
// of the month: the month end invoices it and starts the next calendar month,
// also after more than one switch in the month.
func TestPostpaidMonthEndAfterPrepaidSwitchesStartsTheNextMonth_RealPG(t *testing.T) {
	appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	monthStart, monthEnd := prepaidStatementPeriod()
	tenantID, tierID := uuid.NewString(), uuid.NewString()
	firstSwitch := monthStart.Add(10 * 24 * time.Hour)
	secondSwitch := monthStart.Add(20*24*time.Hour + 2*time.Hour + 45*time.Minute)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, tier_level, metering_enabled)
		VALUES ($1, $2, 'Free', 0, 'EUR', 1, false)`, tierID, "free-"+tierID[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email,
			billing_period_start, billing_period_end, next_billing_date, presentment_currency)
		VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', $3, $4, $4, 'EUR')`, tenantID, tierID, secondSwitch, monthEnd); err != nil {
		t.Fatal(err)
	}
	seedClosingStatement(t, db, tenantID, monthStart, firstSwitch)
	seedClosingStatement(t, db, tenantID, firstSwitch, secondSwitch)
	logger := logging.NewLogger()
	jm := &JobManager{db: db, logger: logger, billing: &Service{db: db, logger: logger}}

	jm.generateMonthlyInvoices(ctx)

	var invoices int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM purser.billing_invoices
		WHERE tenant_id = $1 AND period_start = $2 AND period_end = $3 AND document_kind = 'invoice' AND status <> 'draft'`,
		tenantID, secondSwitch, monthEnd).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if invoices != 1 {
		t.Fatalf("finalized invoices for %s..%s = %d, want 1", secondSwitch, monthEnd, invoices)
	}
	nextStart, nextEnd := subscriptionPeriod(t, db, tenantID)
	if !nextStart.Equal(monthEnd) || !nextEnd.Equal(monthEnd.AddDate(0, 1, 0)) {
		t.Fatalf("period after month end = %s..%s, want the next calendar month %s..%s",
			nextStart, nextEnd, monthEnd, monthEnd.AddDate(0, 1, 0))
	}
}
