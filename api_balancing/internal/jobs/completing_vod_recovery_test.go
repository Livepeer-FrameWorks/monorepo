package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// recoveryS3Stub implements CompletingVodRecoveryS3 with scripted CompleteMultipartUpload/Exists results.
type recoveryS3Stub struct {
	completeErr   error
	completeCalls int
	existsResult  bool
	existsErr     error
	existsCalls   int
}

func (s *recoveryS3Stub) CompleteMultipartUpload(context.Context, string, string, []storage.CompletedPart) error {
	s.completeCalls++
	return s.completeErr
}
func (s *recoveryS3Stub) Exists(context.Context, string) (bool, error) {
	s.existsCalls++
	return s.existsResult, s.existsErr
}
func (s *recoveryS3Stub) BuildS3URL(key string) string { return "s3://bucket/" + key }

func newRecoveryJob(t *testing.T, s3 CompletingVodRecoveryS3) (*CompletingVodRecoveryJob, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	j := NewCompletingVodRecoveryJob(CompletingVodRecoveryConfig{
		DB:             db,
		S3:             s3,
		Logger:         logging.NewLogger(),
		LocalBackendID: "backend-x", // matches the scan rows' backend_id so the ownership fence passes
	})
	return j, mock, func() { _ = db.Close() }
}

// recoveryScanRows builds a scan row with NO completion descriptor (legacy row): reconcileOne falls to
// the existence-probe path.
func recoveryScanRows(pastGrace bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"artifact_hash", "tenant_id", "user_id", "size_bytes", "s3_key", "s3_upload_id", "processes_json", "backend_id", "vod_completion_descriptor", "past_fail_grace",
	}).AddRow("hash-1", "t1", "user-1", int64(2048), "vod/t1/hash-1/video.mp4", "up-1", "", "backend-x", "", pastGrace)
}

// recoveryScanRowsWithDescriptor builds a scan row carrying a durable completion descriptor, so
// reconcileOne RETRIES CompleteMultipartUpload before deciding.
func recoveryScanRowsWithDescriptor(pastGrace bool) *sqlmock.Rows {
	descriptor := `{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"etag-1"}]}`
	return sqlmock.NewRows([]string{
		"artifact_hash", "tenant_id", "user_id", "size_bytes", "s3_key", "s3_upload_id", "processes_json", "backend_id", "vod_completion_descriptor", "past_fail_grace",
	}).AddRow("hash-1", "t1", "user-1", int64(2048), "vod/t1/hash-1/video.mp4", "up-1", "", "backend-x", descriptor, pastGrace)
}

// A stranded 'completing' row whose object is PRESENT converges to 'processing' (+ PROCESSING lifecycle
// event) and dispatches the processing job (idempotently).
func TestCompletingVodRecovery_ConvergesPresentObjectToProcessing(t *testing.T) {
	s3 := &recoveryS3Stub{existsResult: true}
	j, mock, cleanup := newRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(recoveryScanRows(false))
	// Converge to processing, insert the processing job, and emit the event — all in ONE tx, so the
	// row can never end up 'processing' without its job. The clip->queued UPDATE is a no-op on a VOD
	// row (guarded artifact_type='clip') but still executes.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'processing'`).
		WithArgs("s3://bucket/vod/t1/hash-1/video.mp4", "hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("hash-1", "process").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn\.processing_jobs`).
		WithArgs("hash-1", "process").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO foghorn\.processing_jobs`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'queued'`).
		WithArgs("hash-1", "t1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	j.reconcile()

	if s3.existsCalls != 1 {
		t.Fatalf("expected exactly one Exists probe, got %d", s3.existsCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An object confirmed ABSENT past the grace period is marked 'failed' (+ FAILED lifecycle event). No
// processing job is dispatched.
func TestCompletingVodRecovery_MarksAbsentPastGraceFailed(t *testing.T) {
	s3 := &recoveryS3Stub{existsResult: false}
	j, mock, cleanup := newRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(recoveryScanRows(true)) // past_fail_grace = true
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'failed'`).
		WithArgs(sqlmock.AnyArg(), "hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	j.reconcile()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An object absent but WITHIN the grace period is left 'completing' (no write) — S3 may still be
// finalizing it.
func TestCompletingVodRecovery_LeavesAbsentWithinGraceCompleting(t *testing.T) {
	s3 := &recoveryS3Stub{existsResult: false}
	j, mock, cleanup := newRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(recoveryScanRows(false)) // past_fail_grace = false
	// No UPDATE expected — the row is left 'completing'.

	j.reconcile()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An Exists probe error leaves the row 'completing' (no write) for the next pass.
func TestCompletingVodRecovery_ProbeErrorLeavesCompleting(t *testing.T) {
	s3 := &recoveryS3Stub{existsErr: errors.New("s3 unreachable")}
	j, mock, cleanup := newRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(recoveryScanRows(true))

	j.reconcile()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// With a persisted descriptor, the recovery job RETRIES CompleteMultipartUpload; a successful retry
// (a crash before the client's original completion call) converges the row to 'processing' without
// any existence probe.
func TestCompletingVodRecovery_DescriptorRetryCompletesConverges(t *testing.T) {
	s3 := &recoveryS3Stub{completeErr: nil}
	j, mock, cleanup := newRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(recoveryScanRowsWithDescriptor(false))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'processing'`).
		WithArgs("s3://bucket/vod/t1/hash-1/video.mp4", "hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("hash-1", "process").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn\.processing_jobs`).
		WithArgs("hash-1", "process").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO foghorn\.processing_jobs`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'queued'`).
		WithArgs("hash-1", "t1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	j.reconcile()

	if s3.completeCalls != 1 {
		t.Fatalf("expected exactly one CompleteMultipartUpload retry, got %d", s3.completeCalls)
	}
	if s3.existsCalls != 0 {
		t.Fatalf("expected no existence probe after a successful completion retry, got %d", s3.existsCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// With a persisted descriptor, if the completion retry errors but the object is ALREADY present
// (a prior attempt completed it), the job converges rather than failing — never fail while the object
// exists.
func TestCompletingVodRecovery_DescriptorRetryErrorButObjectPresentConverges(t *testing.T) {
	s3 := &recoveryS3Stub{completeErr: errors.New("NoSuchUpload: upload does not exist"), existsResult: true}
	j, mock, cleanup := newRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(recoveryScanRowsWithDescriptor(true))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'processing'`).
		WithArgs("s3://bucket/vod/t1/hash-1/video.mp4", "hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("hash-1", "process").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT job_id\s+FROM foghorn\.processing_jobs`).
		WithArgs("hash-1", "process").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO foghorn\.processing_jobs`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'queued'`).
		WithArgs("hash-1", "t1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	j.reconcile()

	if s3.completeCalls != 1 || s3.existsCalls != 1 {
		t.Fatalf("expected one completion retry and one existence probe, got complete=%d exists=%d", s3.completeCalls, s3.existsCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
