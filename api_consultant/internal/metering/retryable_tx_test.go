package metering

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestClaimPendingReplaysWholeTransactionOnSerializationFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tracker := NewUsageTracker(UsageTrackerConfig{DB: db, Model: "gpt-test"})
	columns := []string{"id", "tenant_id", "event_type", "event_count", "tokens_input", "tokens_output", "model", "provider", "created_at"}
	createdAt := time.Unix(1_700_000_000, 0).UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("FROM skipper\\.skipper_usage").WillReturnRows(sqlmock.NewRows(columns).
		AddRow("usage-1", "tenant-a", "llm_call", 1, 10, 5, "gpt-test", "openai", createdAt))
	mock.ExpectExec("UPDATE skipper\\.skipper_usage").
		WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery("FROM skipper\\.skipper_usage").WillReturnRows(sqlmock.NewRows(columns).
		AddRow("usage-1", "tenant-a", "llm_call", 1, 10, 5, "gpt-test", "openai", createdAt))
	mock.ExpectExec("UPDATE skipper\\.skipper_usage").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	rows, err := tracker.claimPending(context.Background())
	if err != nil {
		t.Fatalf("claimPending: %v", err)
	}
	if len(rows) != 1 || rows[0].id != "usage-1" {
		t.Fatalf("expected the replay to yield exactly one claimed row, got %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
