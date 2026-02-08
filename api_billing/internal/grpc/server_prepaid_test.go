package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"frameworks/pkg/logging"
)

func TestRecordBalanceTransaction_DuplicateReferenceReturnsExisting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := &PurserServer{
		db:     db,
		logger: logging.NewLogger(),
	}

	tenantID := uuid.New().String()
	currency := "USD"
	amountCents := int64(-500)
	txType := "usage"
	description := "usage summary"
	referenceID := uuid.New().String()
	referenceType := "usage_summary"

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(sqlmock.AnyArg(), tenantID, amountCents, txType, description, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(&pq.Error{Code: "23505"})

	createdAt := time.Now().Add(-1 * time.Minute)
	rows := sqlmock.NewRows([]string{
		"id",
		"tenant_id",
		"amount_cents",
		"balance_after_cents",
		"transaction_type",
		"description",
		"reference_id",
		"reference_type",
		"created_at",
	}).AddRow(
		"existing-id",
		tenantID,
		amountCents,
		int64(2500),
		txType,
		description,
		referenceID,
		referenceType,
		createdAt,
	)

	mock.ExpectQuery("SELECT id, tenant_id, amount_cents, balance_after_cents").
		WithArgs(tenantID, referenceType, referenceID).
		WillReturnRows(rows)
	mock.ExpectRollback()

	txn, err := server.recordBalanceTransaction(
		context.Background(),
		tenantID,
		currency,
		amountCents,
		txType,
		description,
		&referenceID,
		&referenceType,
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if txn == nil {
		t.Fatal("expected transaction, got nil")
	}
	if txn.Id != "existing-id" {
		t.Fatalf("expected existing transaction id, got %s", txn.Id)
	}
	if txn.BalanceAfterCents != 2500 {
		t.Fatalf("expected balance after 2500, got %d", txn.BalanceAfterCents)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
