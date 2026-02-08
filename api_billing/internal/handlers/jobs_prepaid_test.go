package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"frameworks/pkg/billing"
	"frameworks/pkg/logging"
)

func TestDeductPrepaidBalanceForUsage_AppliesAndLocksBalance(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{
		db:     mockDB,
		logger: logging.NewLogger(),
	}

	ctx := context.Background()
	tenantID := uuid.New().String()
	amountCents := int64(500)
	description := "usage charge"
	referenceID := uuid.New()
	currency := billing.DefaultCurrency()
	currentBalance := int64(1000)
	newBalance := currentBalance - amountCents

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT balance_cents.*FOR UPDATE`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(currentBalance))
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(tenantID, -amountCents, newBalance, description, referenceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(newBalance, tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	previous, updated, applied, err := jm.deductPrepaidBalanceForUsage(ctx, tenantID, amountCents, description, referenceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("expected deduction to apply")
	}
	if previous != currentBalance {
		t.Fatalf("expected previous balance %d, got %d", currentBalance, previous)
	}
	if updated != newBalance {
		t.Fatalf("expected new balance %d, got %d", newBalance, updated)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestDeductPrepaidBalanceForUsage_DuplicateSummaryNoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{
		db:     mockDB,
		logger: logging.NewLogger(),
	}

	ctx := context.Background()
	tenantID := uuid.New().String()
	amountCents := int64(500)
	description := "usage charge"
	referenceID := uuid.New()
	currency := billing.DefaultCurrency()
	currentBalance := int64(1000)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT balance_cents.*FOR UPDATE`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(currentBalance))
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(tenantID, -amountCents, currentBalance-amountCents, description, referenceID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	previous, updated, applied, err := jm.deductPrepaidBalanceForUsage(ctx, tenantID, amountCents, description, referenceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Fatal("expected duplicate deduction to be skipped")
	}
	if previous != currentBalance || updated != currentBalance {
		t.Fatalf("expected balances to remain %d, got %d/%d", currentBalance, previous, updated)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
