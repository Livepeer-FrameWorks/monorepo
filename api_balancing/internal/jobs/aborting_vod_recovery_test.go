package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// abortRecoveryS3Stub implements AbortingVodRecoveryS3 with a scripted AbortMultipartUpload result.
type abortRecoveryS3Stub struct {
	abortErr   error
	abortCalls int
}

func (s *abortRecoveryS3Stub) AbortMultipartUpload(context.Context, string, string) error {
	s.abortCalls++
	return s.abortErr
}

// smithyAPIErrorStub implements smithy.APIError so the abort classifier can be exercised against a typed
// provider error whose CODE (not message text) decides whether the upload is gone.
type smithyAPIErrorStub struct {
	code    string
	message string
}

func (e *smithyAPIErrorStub) Error() string                 { return e.code + ": " + e.message }
func (e *smithyAPIErrorStub) ErrorCode() string             { return e.code }
func (e *smithyAPIErrorStub) ErrorMessage() string          { return e.message }
func (e *smithyAPIErrorStub) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func newAbortRecoveryJob(t *testing.T, s3 AbortingVodRecoveryS3) (*AbortingVodRecoveryJob, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	j := NewAbortingVodRecoveryJob(AbortingVodRecoveryConfig{
		DB:             db,
		S3:             s3,
		Logger:         logging.NewLogger(),
		LocalBackendID: "backend-x", // matches the scan rows' backend_id so the ownership fence passes
	})
	return j, mock, func() { _ = db.Close() }
}

func abortRecoveryScanRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"artifact_hash", "tenant_id", "user_id", "s3_key", "s3_upload_id", "backend_id",
	}).AddRow("hash-1", "t1", "user-1", "vod/t1/hash-1/video.mp4", "up-1", "backend-x")
}

// A stranded 'aborting' row whose multipart upload aborts cleanly converges to 'deleted' (metadata
// deleted + DELETED lifecycle event) in ONE transaction.
func TestAbortingVodRecovery_ConvergesAbortedToDeleted(t *testing.T) {
	s3 := &abortRecoveryS3Stub{}
	j, mock, cleanup := newAbortRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(abortRecoveryScanRows())
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'deleted'.*status = 'aborting'`).
		WithArgs("hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM foghorn\.vod_metadata`).
		WithArgs("hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	j.reconcile()

	if s3.abortCalls != 1 {
		t.Fatalf("expected exactly one AbortMultipartUpload re-run, got %d", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A TYPED NoSuchUpload result (a prior attempt already tore the upload down), as aws-sdk-go-v2 surfaces
// it wrapped with %w, is an idempotent success: the row converges to 'deleted'.
func TestAbortingVodRecovery_NoSuchUploadConverges(t *testing.T) {
	s3 := &abortRecoveryS3Stub{abortErr: fmt.Errorf("operation error S3: AbortMultipartUpload: %w", &types.NoSuchUpload{})}
	j, mock, cleanup := newAbortRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(abortRecoveryScanRows())
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'deleted'.*status = 'aborting'`).
		WithArgs("hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM foghorn\.vod_metadata`).
		WithArgs("hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	j.reconcile()

	if s3.abortCalls != 1 {
		t.Fatalf("expected exactly one AbortMultipartUpload re-run, got %d", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A genuine (non-NoSuchUpload) abort error leaves the row 'aborting' for a later pass — no metadata
// delete, no DELETED event.
func TestAbortingVodRecovery_AbortErrorLeavesAborting(t *testing.T) {
	s3 := &abortRecoveryS3Stub{abortErr: errors.New("s3 unreachable")}
	j, mock, cleanup := newAbortRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(abortRecoveryScanRows())
	// No tx expected — the abort errored, so the row is left 'aborting'.

	j.reconcile()

	if s3.abortCalls != 1 {
		t.Fatalf("expected exactly one AbortMultipartUpload attempt, got %d", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An unrelated TYPED provider error (e.g. AccessDenied / NoSuchBucket) whose MESSAGE contains "does not
// exist" is NOT a gone upload: the multipart may still exist, so the row must stay 'aborting' — no
// metadata delete, no DELETED event. Classification keys on the error code, never the message text.
func TestAbortingVodRecovery_UnrelatedSmithyErrorLeavesAborting(t *testing.T) {
	s3 := &abortRecoveryS3Stub{abortErr: fmt.Errorf("operation error S3: AbortMultipartUpload: %w",
		&smithyAPIErrorStub{code: "AccessDenied", message: "the specified bucket does not exist"})}
	j, mock, cleanup := newAbortRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(abortRecoveryScanRows())
	// No tx expected — an unclassified S3 error leaves the row 'aborting'.

	j.reconcile()

	if s3.abortCalls != 1 {
		t.Fatalf("expected exactly one AbortMultipartUpload attempt, got %d", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An UNTYPED error whose text merely contains "does not exist" carries no SDK type signal, so it must
// NOT converge the abort to 'deleted' — the row stays 'aborting' for a later pass.
func TestAbortingVodRecovery_UntypedDoesNotExistLeavesAborting(t *testing.T) {
	s3 := &abortRecoveryS3Stub{abortErr: errors.New("the specified multipart upload does not exist")}
	j, mock, cleanup := newAbortRecoveryJob(t, s3)
	defer cleanup()

	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+JOIN foghorn\.vod_metadata`).
		WillReturnRows(abortRecoveryScanRows())
	// No tx expected — an untyped "does not exist" error is not a proven NoSuchUpload; row stays 'aborting'.

	j.reconcile()

	if s3.abortCalls != 1 {
		t.Fatalf("expected exactly one AbortMultipartUpload attempt, got %d", s3.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
