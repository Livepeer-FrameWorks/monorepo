package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

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
	mock.ExpectQuery(`INSERT INTO purser\.balance_transactions`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(500), "topup", sqlmock.AnyArg(), "nonce-1", "x402_payment", nil, nil, nil, nil, sqlmock.AnyArg()).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id, tenant_id, amount_cents, balance_after_cents`).
		WithArgs("tenant-1", "x402_payment", "nonce-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "amount_cents", "balance_after_cents", "transaction_type",
			"description", "reference_id", "reference_type", "created_at",
		}).AddRow(
			"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222",
			int64(500), int64(9000), "topup", "x402 topup", "33333333-3333-3333-3333-333333333333",
			"x402_payment", time.Now(),
		))
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
	mock.ExpectQuery(`INSERT INTO purser\.balance_transactions`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", int64(500), "topup", sqlmock.AnyArg(), "nonce-2", "x402_payment", nil, nil, nil, nil, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("11111111-1111-1111-1111-111111111111"))
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs("tenant-1", "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`UPDATE purser\.prepaid_balances`).
		WithArgs(int64(500), "tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(1500)))
	mock.ExpectExec(`UPDATE purser\.balance_transactions`).
		WithArgs(int64(1500), sqlmock.AnyArg()).
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
