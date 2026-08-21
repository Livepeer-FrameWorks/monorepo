package stripe

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestEnqueueMeterEvents_ManualReviewIsHardHold(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnqueueMeterEvents(context.Background(), tx, "not-evaluated", "not-evaluated", "manual_review"); err != nil {
		t.Fatalf("EnqueueMeterEvents: %v", err)
	}
	mock.ExpectCommit()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestEnqueueMeterEvents_RejectsInvalidBoundaryIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnqueueMeterEvents(context.Background(), tx, "invalid", uuid.NewString(), "pending"); err == nil {
		t.Fatal("invalid invoice UUID accepted")
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestEnqueueMeterEvents_UsesTypedSetBasedQuery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	invoiceID := uuid.New()
	tenantID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.stripe_meter_events_outbox`).
		WithArgs(tenantID, invoiceID).
		WillReturnResult(sqlmock.NewResult(0, 2))
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnqueueMeterEvents(context.Background(), tx, invoiceID.String(), tenantID.String(), "pending"); err != nil {
		t.Fatalf("EnqueueMeterEvents: %v", err)
	}
	mock.ExpectCommit()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
