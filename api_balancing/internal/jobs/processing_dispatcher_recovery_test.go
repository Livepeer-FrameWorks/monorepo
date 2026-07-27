package jobs

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func newRecoveryDispatcher(t *testing.T, db *sql.DB) *ProcessingDispatcher {
	t.Helper()
	return NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
}

// TestRecoverStaleFailsExhaustedArtifacts pins the terminal half of stale recovery: each
// exhausted candidate is failed in its OWN transaction — the job-failed CTE, the artifact-failed
// UPDATE, and the failure lifecycle outbox insert all commit together (BEGIN…COMMIT per job), so
// a failed job is never left with a live artifact.
func TestRecoverStaleFailsExhaustedArtifacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 1. requeue CTE (jobs still under max retries) — nothing requeued here.
	mock.ExpectExec("WITH requeued AS").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// 2. candidate enumeration (read-only) returns the exhausted job ids.
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn.processing_jobs`).
		WillReturnRows(sqlmock.NewRows([]string{"job_id"}).AddRow("job-clip").AddRow("job-vod"))

	// 3. job-clip: atomic fail tx.
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH failed AS`).
		WithArgs("job-clip", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "error_message"}).
			AddRow("hash-clip", "clip", "tenant-1", "stream-1", "live+demo", "max retries exceeded"))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-clip", "max retries exceeded", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 4. job-vod: atomic fail tx.
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH failed AS`).
		WithArgs("job-vod", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "error_message"}).
			AddRow("hash-vod", "vod", "tenant-2", "", "", "max retries exceeded"))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-vod", "max retries exceeded", "tenant-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	d := newRecoveryDispatcher(t, db)
	d.recoverStale()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverStaleRequeueErrorAborts confirms the requeue failure is terminal
// for the pass: if the first CTE errors, the worker logs and returns without
// running the fail sweep, so a transient DB error can never be misread as
// "nothing to fail".
func TestRecoverStaleRequeueErrorAborts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("WITH requeued AS").
		WillReturnError(sql.ErrConnDone)
	// No ExpectQuery for the fail CTE — it must not run.

	newRecoveryDispatcher(t, db).recoverStale()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRevertToQueuedDoesNotBumpRetryCount pins the contract that distinguishes a
// dispatch-time revert from the retry sweep: reverting clears the node and
// returns the job to 'queued' WITHOUT incrementing retry_count, so a job that
// never reached a node is not penalized toward its retry budget.
func TestRevertToQueuedDoesNotBumpRetryCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The revert statement sets queued + clears node; it must not touch retry_count.
	mock.ExpectExec("UPDATE foghorn.processing_jobs\\s+SET status = 'queued', processing_node_id = NULL").
		WithArgs("job-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	newRecoveryDispatcher(t, db).revertToQueued(context.Background(), "job-1")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
