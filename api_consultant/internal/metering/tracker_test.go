package metering

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUsageTrackerPersistsTenantUsage(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tracker := NewUsageTracker(UsageTrackerConfig{
		DB:        db,
		Model:     "gpt-test",
		ClusterID: "skipper",
	})

	tracker.RecordLLMCall("tenant-a", 10, 5)

	mock.ExpectExec("INSERT INTO skipper\\.skipper_usage").WithArgs(
		"tenant-a",
		"llm_call",
		1,
		10,
		5,
		sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	tracker.Flush(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUsageTrackerRetriesFailedPersistence(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tracker := NewUsageTracker(UsageTrackerConfig{
		DB:        db,
		Model:     "gpt-test",
		ClusterID: "skipper",
	})

	tracker.RecordLLMCall("tenant-a", 10, 5)

	mock.ExpectExec("INSERT INTO skipper\\.skipper_usage").WithArgs(
		"tenant-a",
		"llm_call",
		1,
		10,
		5,
		sqlmock.AnyArg(),
	).WillReturnError(sqlmock.ErrCancelled)

	tracker.Flush(context.Background())

	mock.ExpectExec("INSERT INTO skipper\\.skipper_usage").WithArgs(
		"tenant-a",
		"llm_call",
		1,
		10,
		5,
		sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	tracker.Flush(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
