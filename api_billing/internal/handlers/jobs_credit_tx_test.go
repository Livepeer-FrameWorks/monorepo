package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// deductPrepaidBalanceForCreditTx must (1) cap the deduction at the row-locked
// balance — requestCents is a ceiling, not a guarantee — and (2) treat a 23505
// on the ledger insert as an idempotent duplicate, returning the historic
// amount WITHOUT mutating the balance. Both are money-correctness invariants.
func TestDeductPrepaidBalanceForCreditTx(t *testing.T) {
	const refType = "invoice_credit"

	t.Run("applies requested amount when balance is sufficient", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		const tenantID = "tenant-1"
		ref := "ref-1"
		const balance = int64(1000)
		const request = int64(300)
		const newBalance = balance - request

		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents.*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(balance))
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WithArgs(tenantID, -request, newBalance, "credit", ref, refType).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE purser.prepaid_balances").
			WithArgs(newBalance, tenantID, billing.DefaultCurrency()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		tx, err := mockDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		gotBalance, applied, dup, err := jm.deductPrepaidBalanceForCreditTx(context.Background(), tx, tenantID, request, "credit", &ref)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dup {
			t.Fatal("did not expect duplicate")
		}
		if applied != request || gotBalance != newBalance {
			t.Fatalf("applied=%d balance=%d, want applied=%d balance=%d", applied, gotBalance, request, newBalance)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("caps deduction at the locked balance", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		const tenantID = "tenant-1"
		ref := "ref-2"
		const balance = int64(1000)
		const request = int64(5000) // exceeds balance; must cap to 1000
		const newBalance = int64(0)

		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents.*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(balance))
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WithArgs(tenantID, -balance, newBalance, "credit", ref, refType).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE purser.prepaid_balances").
			WithArgs(newBalance, tenantID, billing.DefaultCurrency()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		tx, err := mockDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		_, applied, _, err := jm.deductPrepaidBalanceForCreditTx(context.Background(), tx, tenantID, request, "credit", &ref)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applied != balance {
			t.Fatalf("applied=%d, want capped to balance %d", applied, balance)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("zero-or-negative applied returns early without inserting", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		const tenantID = "tenant-1"
		ref := "ref-3"

		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents.*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(0)))
		mock.ExpectCommit()

		tx, err := mockDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		gotBalance, applied, dup, err := jm.deductPrepaidBalanceForCreditTx(context.Background(), tx, tenantID, 100, "credit", &ref)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applied != 0 || dup || gotBalance != 0 {
			t.Fatalf("got balance=%d applied=%d dup=%v, want 0/0/false", gotBalance, applied, dup)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("duplicate ledger row returns historic amount and leaves balance untouched", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		const tenantID = "tenant-1"
		ref := "ref-dup"
		const balance = int64(1000)
		const historic = int64(-250) // stored amount_cents (negative = debit)

		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents.*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(balance))
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WillReturnError(&pq.Error{Code: "23505"})
		mock.ExpectQuery("SELECT amount_cents FROM purser.balance_transactions").
			WithArgs(tenantID, refType, ref).
			WillReturnRows(sqlmock.NewRows([]string{"amount_cents"}).AddRow(historic))
		mock.ExpectCommit()

		tx, err := mockDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		gotBalance, applied, dup, err := jm.deductPrepaidBalanceForCreditTx(context.Background(), tx, tenantID, 250, "credit", &ref)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !dup {
			t.Fatal("expected duplicate=true")
		}
		if gotBalance != balance {
			t.Fatalf("balance=%d, want untouched %d", gotBalance, balance)
		}
		if applied != -historic { // -(-250) = 250
			t.Fatalf("applied=%d, want %d (historic restated positive)", applied, -historic)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

// applyInvoicePrepaidCreditTx is delta-based: it brings the applied invoice
// credit UP TO grossCents, never re-charging credit already applied for the
// same tenant/period. The short-circuit (already >= gross) must issue no
// further deduction.
func TestApplyInvoicePrepaidCreditTx(t *testing.T) {
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	t.Run("already fully applied issues no further deduction", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		const tenantID = "tenant-1"
		const gross = int64(100)

		mock.ExpectBegin()
		mock.ExpectQuery("SELECT COALESCE\\(SUM\\(-amount_cents\\), 0\\)").
			WillReturnRows(sqlmock.NewRows([]string{"applied"}).AddRow(int64(100)))
		mock.ExpectCommit()

		tx, err := mockDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		applied, err := jm.applyInvoicePrepaidCreditTx(context.Background(), tx, tenantID, periodStart, gross)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applied != 100 {
			t.Fatalf("applied=%d, want 100 (unchanged, no deduction)", applied)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("deducts only the missing delta", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		const tenantID = "tenant-1"
		const gross = int64(150)
		const alreadyApplied = int64(2)
		const delta = gross - alreadyApplied // 148
		const balance = int64(1000)
		const newBalance = balance - delta

		mock.ExpectBegin()
		mock.ExpectQuery("SELECT COALESCE\\(SUM\\(-amount_cents\\), 0\\)").
			WillReturnRows(sqlmock.NewRows([]string{"applied"}).AddRow(alreadyApplied))
		// nested deductPrepaidBalanceForCreditTx for the 148-cent delta
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents.*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(balance))
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WithArgs(tenantID, -delta, newBalance, sqlmock.AnyArg(), sqlmock.AnyArg(), "invoice_credit").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE purser.prepaid_balances").
			WithArgs(newBalance, tenantID, billing.DefaultCurrency()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		tx, err := mockDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		applied, err := jm.applyInvoicePrepaidCreditTx(context.Background(), tx, tenantID, periodStart, gross)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applied != gross {
			t.Fatalf("applied=%d, want %d (already + delta)", applied, gross)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
