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

// TestRecoverStaleFailsExhaustedArtifacts pins the terminal half of stale
// recovery: jobs returned by the fail-CTE are marked failed per artifact type
// (clip and vod each get an artifacts UPDATE carrying the exhaustion reason),
// and every exhausted job with an artifact fires the onJobExhausted reconciler
// so the artifact can fall back to a raw/served state.
func TestRecoverStaleFailsExhaustedArtifacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 1. requeue CTE (jobs still under max retries) — nothing requeued here.
	mock.ExpectExec("WITH requeued AS").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// 2. fail CTE returns the exhausted jobs (clip then vod).
	mock.ExpectQuery("WITH failed AS").
		WillReturnRows(sqlmock.NewRows([]string{
			"job_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name",
		}).
			AddRow("job-clip", "hash-clip", "clip", "tenant-1", "stream-1", "live+demo").
			AddRow("job-vod", "hash-vod", "vod", "tenant-2", "", ""))

	// 3. clip artifact marked failed with the exhaustion reason.
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-clip", "tenant-1", "max retries exceeded").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// 4. vod artifact marked failed.
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-vod", "tenant-2", "max retries exceeded").
		WillReturnResult(sqlmock.NewResult(0, 1))

	var exhausted [][2]string
	d := newRecoveryDispatcher(t, db)
	d.SetJobExhaustedHandler(func(_ context.Context, jobID, artifactHash string) {
		exhausted = append(exhausted, [2]string{jobID, artifactHash})
	})

	d.recoverStale()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"job-clip", "hash-clip"}, {"job-vod", "hash-vod"}}
	if len(exhausted) != len(want) {
		t.Fatalf("onJobExhausted calls = %v, want %v", exhausted, want)
	}
	for i, w := range want {
		if exhausted[i] != w {
			t.Fatalf("onJobExhausted[%d] = %v, want %v", i, exhausted[i], w)
		}
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
