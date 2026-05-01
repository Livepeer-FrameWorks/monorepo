package operator

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

// TestManualReviewIsHardHold verifies that an invoice in manual_review status
// produces zero ledger writes — no SELECT, no INSERT — so a held invoice
// cannot leak partial accruals.
func TestManualReviewIsHardHold(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := ComputeAndPersistCredits(context.Background(), tx, "inv-1", "manual_review"); err != nil {
		t.Fatalf("ComputeAndPersistCredits: %v", err)
	}
	mock.ExpectCommit()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestNoMarketplaceLinesNoLedgerRows verifies that an invoice with only
// platform_official / tenant_private lines (which are filtered out at the
// SELECT level) results in zero INSERTs.
func TestNoMarketplaceLinesNoLedgerRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("inv-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := ComputeAndPersistCredits(context.Background(), tx, "inv-1", "pending"); err != nil {
		t.Fatalf("ComputeAndPersistCredits: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestMarketplaceLineProducesAccrualHeld covers the default path: a
// marketplace line at $5.00 with the default 2000bps fee produces one
// accrual row with platform_fee_cents=100, payable_cents=400, status='held'
// (the operator hasn't been vetted yet — pre-launch posture).
func TestMarketplaceLineProducesAccrualHeld(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	owner := uuid.New()
	lineID := uuid.New()
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("inv-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}).AddRow(lineID.String(), "operator-eu-1", owner.String(), int64(400), int64(100), "EUR", periodStart, periodEnd))
	mock.ExpectQuery(`FROM purser\.cluster_owners`).
		WithArgs(owner).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO purser\.operator_credit_ledger`).
		WithArgs(lineID, owner, "operator-eu-1", "inv-1",
			periodStart, periodEnd, "EUR",
			int64(500), int64(100), int64(400), "held").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := ComputeAndPersistCredits(context.Background(), tx, "inv-1", "pending"); err != nil {
		t.Fatalf("ComputeAndPersistCredits: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestMarketplaceLineApprovedOperatorAccrues verifies that when the
// operator is approved AND payout-eligible, the new accrual is 'accruing'
// (counted toward payout) instead of 'held'.
func TestMarketplaceLineApprovedOperatorAccrues(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	owner := uuid.New()
	lineID := uuid.New()
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("inv-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}).AddRow(lineID.String(), "operator-eu-1", owner.String(), int64(400), int64(100), "EUR", periodStart, periodEnd))
	mock.ExpectQuery(`FROM purser\.cluster_owners`).
		WithArgs(owner).
		WillReturnRows(sqlmock.NewRows([]string{"status", "payout_eligible"}).AddRow("approved", true))
	mock.ExpectExec(`INSERT INTO purser\.operator_credit_ledger`).
		WithArgs(lineID, owner, "operator-eu-1", "inv-1",
			periodStart, periodEnd, "EUR",
			int64(500), int64(100), int64(400), "accruing").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := ComputeAndPersistCredits(context.Background(), tx, "inv-1", "pending"); err != nil {
		t.Fatalf("ComputeAndPersistCredits: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestSuspendedOperatorStaysHeld verifies that a suspended operator still
// gets an audit row but at status='held' so payout cannot accidentally
// fire.
func TestSuspendedOperatorStaysHeld(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	owner := uuid.New()
	lineID := uuid.New()
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("inv-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}).AddRow(lineID.String(), "operator-eu-1", owner.String(), int64(400), int64(100), "EUR", periodStart, periodEnd))
	mock.ExpectQuery(`FROM purser\.cluster_owners`).
		WithArgs(owner).
		WillReturnRows(sqlmock.NewRows([]string{"status", "payout_eligible"}).AddRow("suspended", true))
	mock.ExpectExec(`INSERT INTO purser\.operator_credit_ledger`).
		WithArgs(lineID, owner, "operator-eu-1", "inv-1",
			periodStart, periodEnd, "EUR",
			int64(500), int64(100), int64(400), "held").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := ComputeAndPersistCredits(context.Background(), tx, "inv-1", "pending"); err != nil {
		t.Fatalf("ComputeAndPersistCredits: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestStampedLineSplitIsUsed verifies that invoice-line audit columns are
// the source of truth for the ledger split. The writer computes these values
// during rating so invoice presentation and operator revenue agree.
func TestStampedLineSplitIsUsed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	owner := uuid.New()
	lineID := uuid.New()
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("inv-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}).AddRow(lineID.String(), "operator-eu-1", owner.String(), int64(750), int64(250), "EUR", periodStart, periodEnd))
	mock.ExpectQuery(`FROM purser\.cluster_owners`).
		WithArgs(owner).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO purser\.operator_credit_ledger`).
		WithArgs(lineID, owner, "operator-eu-1", "inv-1",
			periodStart, periodEnd, "EUR",
			int64(1000), int64(250), int64(750), "held").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := ComputeAndPersistCredits(context.Background(), tx, "inv-1", "pending"); err != nil {
		t.Fatalf("ComputeAndPersistCredits: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPersistStripeSubscriptionCredit covers the monthly Stripe subscription
// path: a paid monthly invoice produces one accrual sourced from
// stripe_subscription, idempotent on stripe_invoice_id. Status defaults to
// held until the operator is approved+payout_eligible.
func TestPersistStripeSubscriptionCredit(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	owner := uuid.New()
	stripeInvoiceID := "in_1ABC"
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.platform_fee_policy`).
		WithArgs(owner, "cluster_monthly").
		WillReturnRows(sqlmock.NewRows([]string{"fee_basis_points"}).AddRow(2000))
	mock.ExpectQuery(`FROM purser\.cluster_owners`).
		WithArgs(owner).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO purser\.operator_credit_ledger`).
		WithArgs(stripeInvoiceID, owner, "operator-eu-1",
			periodStart, periodEnd, "EUR",
			int64(4900), int64(980), int64(3920), "held").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := PersistStripeSubscriptionCredit(context.Background(), tx,
		stripeInvoiceID, owner, "operator-eu-1", "EUR", 4900,
		periodStart, periodEnd, "cluster_monthly"); err != nil {
		t.Fatalf("PersistStripeSubscriptionCredit: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestDollarStringToCents covers the edge cases for the cents converter.
func TestDollarStringToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"5.00", 500},
		{"10", 1000},
		{"0.99", 99},
		{"100.5", 10050},
		{"1234.56", 123456},
		{"-5.00", -500},
		{"0", 0},
		{"0.00", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := dollarStringToCents(tc.in)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Errorf("%q → %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
