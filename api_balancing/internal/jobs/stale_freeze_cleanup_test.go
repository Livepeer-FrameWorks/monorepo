package jobs

import (
	"fmt"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStaleFreezeCleanup_Defaults(t *testing.T) {
	j := NewStaleFreezeCleanupJob(StaleFreezeCleanupConfig{Logger: logging.NewLogger()})
	if j.interval != 1*time.Minute {
		t.Fatalf("expected default interval 1m, got %v", j.interval)
	}
	if j.staleAfter != 30*time.Minute {
		t.Fatalf("expected default staleAfter 30m, got %v", j.staleAfter)
	}
}

func TestStaleFreezeCleanup_Custom(t *testing.T) {
	j := NewStaleFreezeCleanupJob(StaleFreezeCleanupConfig{
		Logger:     logging.NewLogger(),
		Interval:   5 * time.Second,
		StaleAfter: 2 * time.Minute,
	})
	if j.interval != 5*time.Second {
		t.Fatalf("expected interval 5s, got %v", j.interval)
	}
	if j.staleAfter != 2*time.Minute {
		t.Fatalf("expected staleAfter 2m, got %v", j.staleAfter)
	}
}

func TestStaleFreezeCleanup_ResetsToLocalPending(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &StaleFreezeCleanupJob{
		db:         mockDB,
		logger:     logging.NewLogger(),
		staleAfter: 30 * time.Minute,
	}

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 'local'.*sync_status = 'pending'.*WHERE storage_location = 'freezing'").
		WithArgs(int64(1800)).
		WillReturnResult(sqlmock.NewResult(0, 3))

	j.cleanup()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleFreezeCleanup_QueryError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &StaleFreezeCleanupJob{
		db:         mockDB,
		logger:     logging.NewLogger(),
		staleAfter: 30 * time.Minute,
	}

	mock.ExpectExec("UPDATE foghorn.artifacts").WillReturnError(fmt.Errorf("db error"))

	j.cleanup() // should not panic

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleFreezeCleanup_ZeroDuration_ClampsToOne(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &StaleFreezeCleanupJob{
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
