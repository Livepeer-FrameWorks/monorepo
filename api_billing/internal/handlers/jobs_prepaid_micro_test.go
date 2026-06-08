package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// deductPrepaidBalanceForUsageMicro carries a sub-cent residual across calls so
// a stream of per-event deductions below €0.01 eventually crosses a whole-cent
// boundary instead of truncating to zero (revenue-leak prevention). These tests
// pin the residual arithmetic and the idempotency gate — the parts that would
// silently lose or double-count money if they regressed.
func TestDeductPrepaidBalanceForUsageMicro(t *testing.T) {
	currency := billing.DefaultCurrency()

	t.Run("residual accumulates and commits whole cents on boundary cross", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		tenantID := uuid.New().String()
		ref := uuid.New()
		const currentBalance = int64(1000)
		const currentRemainder = int64(9000) // micro-cents already carried
		// amountMicro 2000 -> total 11000 -> 1 cent applied, 1000 carried.
		const amountMicro = int64(2000)
		const wantApplied = int64(1)
		const wantNewBalance = currentBalance - wantApplied // 999
		const wantNewRemainder = int64(1000)

		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").
			WithArgs(tenantID, currency).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents, balance_remainder_micro.*FOR UPDATE`).
			WithArgs(tenantID, currency).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents", "balance_remainder_micro"}).AddRow(currentBalance, currentRemainder))
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WithArgs(tenantID, -wantApplied, wantNewBalance, "usage", ref).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE purser.prepaid_balances").
			WithArgs(wantNewBalance, wantNewRemainder, tenantID, currency).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		prev, newBal, applied, err := jm.deductPrepaidBalanceForUsageMicro(context.Background(), tenantID, amountMicro, "usage", ref)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !applied {
			t.Fatal("expected applied=true")
		}
		if prev != currentBalance || newBal != wantNewBalance {
			t.Fatalf("balances = (%d,%d), want (%d,%d)", prev, newBal, currentBalance, wantNewBalance)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("negative correction normalizes residual into [0, microPerCent)", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		tenantID := uuid.New().String()
		ref := uuid.New()
		const currentBalance = int64(1000)
		const currentRemainder = int64(0)
		// amountMicro -5000 -> total -5000 -> trunc 0, rem -5000 -> normalize to
		// applied -1, rem +5000. A credit of 1 cent, residual stays non-negative.
		const amountMicro = int64(-5000)
		const wantApplied = int64(-1)
		const wantNewBalance = currentBalance - wantApplied // 1001
		const wantNewRemainder = int64(5000)

		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").
			WithArgs(tenantID, currency).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents, balance_remainder_micro.*FOR UPDATE`).
			WithArgs(tenantID, currency).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents", "balance_remainder_micro"}).AddRow(currentBalance, currentRemainder))
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WithArgs(tenantID, -wantApplied, wantNewBalance, "credit correction", ref).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE purser.prepaid_balances").
			WithArgs(wantNewBalance, wantNewRemainder, tenantID, currency).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		prev, newBal, applied, err := jm.deductPrepaidBalanceForUsageMicro(context.Background(), tenantID, amountMicro, "credit correction", ref)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !applied {
			t.Fatal("expected applied=true")
		}
		if prev != currentBalance || newBal != wantNewBalance {
			t.Fatalf("balances = (%d,%d), want (%d,%d)", prev, newBal, currentBalance, wantNewBalance)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("duplicate usage summary is an idempotent no-op", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()

		tenantID := uuid.New().String()
		ref := uuid.New()
		const currentBalance = int64(1000)
		const currentRemainder = int64(3000)
		const amountMicro = int64(20000)

		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO purser.prepaid_balances").
			WithArgs(tenantID, currency).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT balance_cents, balance_remainder_micro.*FOR UPDATE`).
			WithArgs(tenantID, currency).
			WillReturnRows(sqlmock.NewRows([]string{"balance_cents", "balance_remainder_micro"}).AddRow(currentBalance, currentRemainder))
		// ON CONFLICT DO NOTHING -> 0 rows affected. No UPDATE must follow.
		mock.ExpectExec("INSERT INTO purser.balance_transactions").
			WithArgs(tenantID, -int64(2), currentBalance-int64(2), "usage", ref).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()

		jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
		prev, newBal, applied, err := jm.deductPrepaidBalanceForUsageMicro(context.Background(), tenantID, amountMicro, "usage", ref)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applied {
			t.Fatal("expected applied=false for duplicate")
		}
		if prev != currentBalance || newBal != currentBalance {
			t.Fatalf("balances = (%d,%d), want both %d (unchanged)", prev, newBal, currentBalance)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
