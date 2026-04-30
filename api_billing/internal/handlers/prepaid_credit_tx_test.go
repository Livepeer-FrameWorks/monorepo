package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"frameworks/pkg/billing"
	"frameworks/pkg/logging"
)

// TestDeductPrepaidBalanceForCreditTx_FreshDeduction verifies the happy path:
// the helper inserts/locks the balance row, then INSERTs the ledger row first
// (idempotency gate), then UPDATEs the balance.
func TestDeductPrepaidBalanceForCreditTx_FreshDeduction(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	currency := billing.DefaultCurrency()
	tenantID := "tenant-1"
	ref := "ref-1"

	mock.ExpectBegin()
	tx, err := mockDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(5000)))
	// Ledger insert FIRST — idempotency gate before balance mutation.
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.prepaid_balances SET balance_cents`).
		WithArgs(int64(4000), tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 1))

	newBalance, applied, duplicate, err := jm.deductPrepaidBalanceForCreditTx(context.Background(), tx, tenantID, 1000, "credit", &ref)
	if err != nil {
		t.Fatalf("deductPrepaidBalanceForCreditTx: %v", err)
	}
	if duplicate {
		t.Errorf("duplicate = true, want false on fresh deduction")
	}
	if newBalance != 4000 {
		t.Errorf("newBalance = %d, want 4000", newBalance)
	}
	if applied != 1000 {
		t.Errorf("applied = %d, want 1000 (request fits under balance)", applied)
	}
}

// TestDeductPrepaidBalanceForCreditTx_CapsAgainstLockedBalance verifies the
// race-safety fix: the caller's requestCents may be a stale snapshot, so the
// helper caps the deduction against the row-locked current balance and
// returns the actually-applied amount.
func TestDeductPrepaidBalanceForCreditTx_CapsAgainstLockedBalance(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	currency := billing.DefaultCurrency()
	tenantID := "tenant-1"
	ref := "ref-cap"

	mock.ExpectBegin()
	tx, err := mockDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Locked balance is 1500, but caller requests 5000 (stale snapshot).
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(1500)))
	// Ledger insert FIRST — idempotency gate.
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WithArgs(tenantID, int64(-1500), int64(0), "stale-request", ref, "invoice_credit").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.prepaid_balances SET balance_cents`).
		WithArgs(int64(0), tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 1))

	newBalance, applied, duplicate, err := jm.deductPrepaidBalanceForCreditTx(context.Background(), tx, tenantID, 5000, "stale-request", &ref)
	if err != nil {
		t.Fatalf("deductPrepaidBalanceForCreditTx: %v", err)
	}
	if duplicate {
		t.Errorf("duplicate = true, want false")
	}
	if applied != 1500 {
		t.Errorf("applied = %d, want 1500 (capped to locked balance, not 5000)", applied)
	}
	if newBalance != 0 {
		t.Errorf("newBalance = %d, want 0", newBalance)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestDeductPrepaidBalanceForCreditTx_DuplicateNoOps verifies idempotency:
// the ledger insert is the gate, so a racing/duplicate request hits 23505 on
// INSERT before any balance UPDATE happens. Helper returns duplicate=true with
// the historic amount and the balance is untouched.
func TestDeductPrepaidBalanceForCreditTx_DuplicateNoOps(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	currency := billing.DefaultCurrency()
	tenantID := "tenant-1"
	ref := "ref-2"

	mock.ExpectBegin()
	tx, err := mockDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(8000)))
	// Ledger INSERT hits the unique-violation idempotency gate.
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnError(&pq.Error{Code: "23505"})
	// Helper looks up the historic ledger row to surface the prior amount.
	mock.ExpectQuery(`SELECT amount_cents FROM purser\.balance_transactions`).
		WithArgs(tenantID, "invoice_credit", ref).
		WillReturnRows(sqlmock.NewRows([]string{"amount_cents"}).AddRow(int64(-5000)))
	// Crucially, NO UPDATE on prepaid_balances expected — balance stays at 8000.

	newBalance, applied, duplicate, err := jm.deductPrepaidBalanceForCreditTx(context.Background(), tx, tenantID, 1000, "credit", &ref)
	if err != nil {
		t.Fatalf("deductPrepaidBalanceForCreditTx: %v", err)
	}
	if !duplicate {
		t.Errorf("duplicate = false, want true on idempotency hit")
	}
	if applied != 5000 {
		t.Errorf("applied = %d, want 5000 (historic credit amount, not request)", applied)
	}
	if newBalance != 8000 {
		t.Errorf("newBalance = %d, want 8000 (untouched on duplicate)", newBalance)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestDeductPrepaidBalanceForUsageMicro_AccumulatesSubCent verifies the carry
// column. Two sub-cent deductions back-to-back add up to one whole cent —
// the structural fix for "fractional cents truncated to zero".
func TestDeductPrepaidBalanceForUsageMicro_AccumulatesSubCent(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	currency := billing.DefaultCurrency()
	tenantID := "tenant-1"
	refID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-1"))

	// First call: 6000 micro-cents (= 0.6 cent). Carries 6000, deducts 0 cents.
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT balance_cents, balance_remainder_micro FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents", "balance_remainder_micro"}).AddRow(int64(10000), int64(0)))
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.prepaid_balances\s+SET balance_cents`).
		WithArgs(int64(10000), int64(6000), tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	prev1, new1, applied1, err := jm.deductPrepaidBalanceForUsageMicro(context.Background(), tenantID, 6000, "first", refID)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !applied1 || prev1 != 10000 || new1 != 10000 {
		t.Errorf("first call: prev=%d new=%d applied=%v, want prev=10000 new=10000 applied=true", prev1, new1, applied1)
	}

	// Second call: another 6000 micro-cents. Carry was 6000, total 12000 → deduct 1 cent, carry 2000.
	refID2 := uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-2"))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT balance_cents, balance_remainder_micro FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents", "balance_remainder_micro"}).AddRow(int64(10000), int64(6000)))
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.prepaid_balances\s+SET balance_cents`).
		WithArgs(int64(9999), int64(2000), tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, new2, applied2, err := jm.deductPrepaidBalanceForUsageMicro(context.Background(), tenantID, 6000, "second", refID2)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !applied2 || new2 != 9999 {
		t.Errorf("second call: new=%d applied=%v, want new=9999 applied=true (sub-cents must accumulate)", new2, applied2)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestDeductPrepaidBalanceForUsageMicro_DuplicateReferenceNoOps verifies the
// reference_id idempotency: duplicate calls don't re-debit the balance.
func TestDeductPrepaidBalanceForUsageMicro_DuplicateReferenceNoOps(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	currency := billing.DefaultCurrency()
	tenantID := "tenant-1"
	refID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("dup"))

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT balance_cents, balance_remainder_micro FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents", "balance_remainder_micro"}).AddRow(int64(10000), int64(0)))
	// Idempotency: ON CONFLICT DO NOTHING returns 0 rows affected.
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	prev, newBal, applied, err := jm.deductPrepaidBalanceForUsageMicro(context.Background(), tenantID, 50000, "dup-call", refID)
	if err != nil {
		t.Fatalf("deductPrepaidBalanceForUsageMicro: %v", err)
	}
	if applied {
		t.Errorf("applied = true, want false on duplicate reference")
	}
	if prev != 10000 || newBal != 10000 {
		t.Errorf("balance changed on duplicate: prev=%d new=%d, want both 10000", prev, newBal)
	}
}
