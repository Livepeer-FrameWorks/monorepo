package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestHandlePrepaidCheckoutCompletedRejectsTenantMismatch(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() {
		db = nil
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, tenant_id FROM purser.pending_topups").
		WithArgs("topup-123").
		WillReturnRows(sqlmock.NewRows([]string{"status", "tenant_id"}).AddRow("pending", "tenant-a"))
	mock.ExpectRollback()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-1", "tenant-b", "topup-123", 1500, "EUR", ProviderStripe); err == nil {
		t.Fatal("expected error")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandlePrepaidCheckoutCompletedSkipsAlreadyProcessed(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() {
		db = nil
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, tenant_id FROM purser.pending_topups").
		WithArgs("topup-456").
		WillReturnRows(sqlmock.NewRows([]string{"status", "tenant_id"}).AddRow("completed", "tenant-a"))
	mock.ExpectRollback()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-2", "tenant-a", "topup-456", 1500, "USD", ProviderStripe); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandlePrepaidCheckoutCompletedRequiresTenantAndTopup(t *testing.T) {
	logger = logrus.New()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-3", "", "", 1500, "USD", ProviderStripe); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
