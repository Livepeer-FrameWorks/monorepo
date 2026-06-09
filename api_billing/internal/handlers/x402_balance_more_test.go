package handlers

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestGetCurrentBalance(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	h := &X402Handler{db: db, logger: logging.NewLogger()}

	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs("tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(2500)))

	bal, err := h.getCurrentBalance(context.Background(), "tenant-1", "EUR")
	if err != nil {
		t.Fatalf("getCurrentBalance: %v", err)
	}
	if bal != 2500 {
		t.Fatalf("balance = %d, want 2500", bal)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetCurrentBalanceNoRowIsZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	h := &X402Handler{db: db, logger: logging.NewLogger()}

	// Absent balance row means "no balance yet", which is 0 (not an error).
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs("tenant-1", "EUR").
		WillReturnError(sql.ErrNoRows)

	bal, err := h.getCurrentBalance(context.Background(), "tenant-1", "EUR")
	if err != nil || bal != 0 {
		t.Fatalf("absent balance should be (0,nil), got (%d,%v)", bal, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreditPrepaidBalanceTxIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	h := &X402Handler{db: db, logger: logging.NewLogger()}

	mock.ExpectBegin()
	// A settlement already recorded for this nonce returns its prior
	// balance_after and short-circuits — no second credit applied.
	mock.ExpectQuery(`SELECT balance_after_cents FROM purser\.balance_transactions`).
		WithArgs("tenant-1", "nonce-1").
		WillReturnRows(sqlmock.NewRows([]string{"balance_after_cents"}).AddRow(int64(9000)))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	bal, err := h.creditPrepaidBalanceTx(context.Background(), tx, "tenant-1", 500, "nonce-1", "0xabcdef0123456789aa", "x402 topup")
	if err != nil {
		t.Fatalf("creditPrepaidBalanceTx: %v", err)
	}
	if bal != 9000 {
		t.Fatalf("idempotent path should return prior balance 9000, got %d", bal)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreditPrepaidBalanceTxNewCredit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	h := &X402Handler{db: db, logger: logging.NewLogger()}

	mock.ExpectBegin()
	// No prior settlement for this nonce.
	mock.ExpectQuery(`SELECT balance_after_cents FROM purser\.balance_transactions`).
		WithArgs("tenant-1", "nonce-2").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances\s+WHERE tenant_id = \$1 AND currency = \$2\s+FOR UPDATE`).
		WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(1000)))
	// New balance = current (1000) + amount (500).
	mock.ExpectExec(`UPDATE purser\.prepaid_balances\s+SET balance_cents = \$1`).
		WithArgs(int64(1500), "tenant-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Ledger row links to the nonce; description carries a truncated tx hash.
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(500), int64(1500), sqlmock.AnyArg(), "nonce-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	bal, err := h.creditPrepaidBalanceTx(context.Background(), tx, "tenant-1", 500, "nonce-2", "0xabcdef0123456789aa", "x402 topup")
	if err != nil {
		t.Fatalf("creditPrepaidBalanceTx: %v", err)
	}
	if bal != 1500 {
		t.Fatalf("new balance = %d, want 1500", bal)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
