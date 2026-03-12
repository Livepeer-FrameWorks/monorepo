package jobs

import (
	"fmt"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStaleDefrostCleanup_Defaults(t *testing.T) {
	j := NewStaleDefrostCleanupJob(StaleDefrostCleanupConfig{Logger: logging.NewLogger()})
	if j.interval != 1*time.Minute {
		t.Fatalf("expected default interval 1m, got %v", j.interval)
	}
	if j.staleAfter != 10*time.Minute {
		t.Fatalf("expected default staleAfter 10m, got %v", j.staleAfter)
	}
}

func TestStaleDefrostCleanup_Custom(t *testing.T) {
	j := NewStaleDefrostCleanupJob(StaleDefrostCleanupConfig{
		Logger:     logging.NewLogger(),
		Interval:   3 * time.Second,
		StaleAfter: 5 * time.Minute,
	})
	if j.interval != 3*time.Second {
		t.Fatalf("expected interval 3s, got %v", j.interval)
	}
	if j.staleAfter != 5*time.Minute {
		t.Fatalf("expected staleAfter 5m, got %v", j.staleAfter)
	}
}

func TestStaleDefrostCleanup_ResetsToS3(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &StaleDefrostCleanupJob{
		db:         mockDB,
		logger:     logging.NewLogger(),
		staleAfter: 10 * time.Minute,
	}

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 's3'.*defrost_node_id = NULL.*WHERE storage_location = 'defrosting'").
		WithArgs(int64(600)).
		WillReturnResult(sqlmock.NewResult(0, 2))

	j.cleanup()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleDefrostCleanup_QueryError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &StaleDefrostCleanupJob{
		db:         mockDB,
		logger:     logging.NewLogger(),
		staleAfter: 10 * time.Minute,
	}

	mock.ExpectExec("UPDATE foghorn.artifacts").WillReturnError(fmt.Errorf("connection lost"))

	j.cleanup() // should not panic

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleDefrostCleanup_ZeroDuration_ClampsToOne(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &StaleDefrostCleanupJob{
		db:         mockDB,
		logger:     logging.NewLogger(),
		staleAfter: 0,
	}

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs(int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	j.cleanup()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
