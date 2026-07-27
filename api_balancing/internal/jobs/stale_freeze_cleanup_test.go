package jobs

import (
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

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

	// The reset + the durable staging enqueue commit as ONE transaction. The CTE RETURNS the OLD (pre-clear)
	// attempt id + canonical key so the abandoned attempt's staging object is enqueued for deletion.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*sync_status = 'failed'.*sync_request_id = NULL.*sync_node_id = NULL.*RETURNING").
		WithArgs(int64(1800)).
		WillReturnRows(sqlmock.NewRows([]string{"canonical_key", "attempt_id"}).
			AddRow("tenant-1/clips/hash.mp4", "att-1").
			AddRow("", "")) // a row with no key/attempt is skipped
	// The non-empty row enqueues its staging (main + .dtsh) AND its published candidate (main + .dtsh); the
	// empty row is skipped.
	k, a := "tenant-1/clips/hash.mp4", "att-1"
	for _, key := range []string{
		control.FreezeStagingKey(k, a),
		control.FreezeStagingKey(k+".dtsh", a),
		control.FreezePublishKey(k, a),
		control.FreezePublishDtshKey(k, a),
	} {
		mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
			WithArgs(key).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

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

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts").WillReturnError(fmt.Errorf("db error"))
	mock.ExpectRollback()

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

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts").
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"canonical_key", "attempt_id"}))
	mock.ExpectCommit()

	j.cleanup()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
