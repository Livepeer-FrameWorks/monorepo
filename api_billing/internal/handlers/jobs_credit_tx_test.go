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
			WithArgs(newBalance, tenantID, billing.LedgerCurrency).
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
			WithArgs(newBalance, tenantID, billing.LedgerCurrency).
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

// reconcileInvoicePrepaidCreditTx makes the period's invoice credit follow
// its target: it debits only the missing amount when the target grows, moves
// nothing when the credit already matches, and returns the excess to the
// balance when the target shrinks.
func TestReconcileInvoicePrepaidCreditTx(t *testing.T) {
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	const tenantID = "tenant-1"

	run := func(t *testing.T, gross int64, expect func(sqlmock.Sqlmock)) invoiceCreditChange {
		t.Helper()
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		mock.ExpectBegin()
		expect(mock)
		mock.ExpectCommit()
		tx, err := mockDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		change, err := reconcileInvoicePrepaidCreditTx(context.Background(), tx, tenantID, periodStart, gross)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
		return change
	}
	lockBalance := func(mock sqlmock.Sqlmock, balance int64) {
		mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
			WithArgs(tenantID, billing.LedgerCurrency).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(balance))
	}
	// heldCredit expects the period's credit key lookup (the first document
	// of April holds the month's key) and its held credit.
	heldCredit := func(mock sqlmock.Sqlmock, held, entries int64) {
		mock.ExpectQuery(`-- name: UsageDocumentStartsEarlierInMonth`).
			WithArgs(tenantID, periodStart, periodStart).
			WillReturnRows(sqlmock.NewRows([]string{"earlier"}).AddRow(false))
		mock.ExpectQuery(`SELECT COALESCE\(SUM\(-amount_cents\), 0\)`).
			WithArgs(tenantID, "Invoice credit: 2026-04", "Invoice credit returned: 2026-04").
			WillReturnRows(sqlmock.NewRows([]string{"applied_cents", "entries"}).AddRow(held, entries))
	}

	t.Run("credit matching the invoice moves nothing", func(t *testing.T) {
		change := run(t, 100, func(mock sqlmock.Sqlmock) {
			lockBalance(mock, 500)
			heldCredit(mock, 100, 1)
		})
		if change != (invoiceCreditChange{PreviousCents: 100, AppliedCents: 100, BalanceCents: 500}) {
			t.Fatalf("change = %+v, want 100 held and unchanged", change)
		}
	})

	t.Run("deducts only the missing delta", func(t *testing.T) {
		const gross, alreadyApplied, balance = int64(150), int64(2), int64(1000)
		const delta = gross - alreadyApplied
		change := run(t, gross, func(mock sqlmock.Sqlmock) {
			lockBalance(mock, balance)
			heldCredit(mock, alreadyApplied, 1)
			mock.ExpectExec("INSERT INTO purser.prepaid_balances").WillReturnResult(sqlmock.NewResult(0, 1))
			lockBalance(mock, balance)
			mock.ExpectExec("INSERT INTO purser.balance_transactions").
				WithArgs(tenantID, -delta, balance-delta, "Invoice credit: 2026-04", sqlmock.AnyArg(), "invoice_credit").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE purser.prepaid_balances").
				WithArgs(balance-delta, tenantID, billing.LedgerCurrency).
				WillReturnResult(sqlmock.NewResult(0, 1))
		})
		if change != (invoiceCreditChange{PreviousCents: alreadyApplied, AppliedCents: gross, BalanceCents: balance - delta}) {
			t.Fatalf("change = %+v, want %d applied", change, gross)
		}
	})

	t.Run("returns credit the invoice no longer uses", func(t *testing.T) {
		change := run(t, 0, func(mock sqlmock.Sqlmock) {
			lockBalance(mock, 0)
			heldCredit(mock, 1000, 1)
			mock.ExpectExec("INSERT INTO purser.balance_transactions").
				WithArgs(tenantID, int64(1000), int64(1000), "Invoice credit returned: 2026-04", sqlmock.AnyArg(), "invoice_credit").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE purser.prepaid_balances").
				WithArgs(int64(1000), tenantID, billing.LedgerCurrency).
				WillReturnResult(sqlmock.NewResult(0, 1))
		})
		if change != (invoiceCreditChange{PreviousCents: 1000, AppliedCents: 0, BalanceCents: 1000}) {
			t.Fatalf("change = %+v, want all 1000 returned", change)
		}
	})

	t.Run("tenant without a prepaid balance holds no credit", func(t *testing.T) {
		change := run(t, 500, func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
				WithArgs(tenantID, billing.LedgerCurrency).
				WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}))
		})
		if change != (invoiceCreditChange{}) {
			t.Fatalf("change = %+v, want none", change)
		}
	})
}
