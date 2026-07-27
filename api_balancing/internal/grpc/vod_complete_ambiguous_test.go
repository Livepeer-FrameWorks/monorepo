package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// smithyAPIErrorStub implements smithy.APIError so the classifier can be exercised against a typed
// server (5xx) vs client (4xx) fault without a live AWS endpoint.
type smithyAPIErrorStub struct {
	code  string
	fault smithy.ErrorFault
}

func (e *smithyAPIErrorStub) Error() string                 { return e.code }
func (e *smithyAPIErrorStub) ErrorCode() string             { return e.code }
func (e *smithyAPIErrorStub) ErrorMessage() string          { return e.code }
func (e *smithyAPIErrorStub) ErrorFault() smithy.ErrorFault { return e.fault }

// An AMBIGUOUS S3 completion error (timeout / connection reset / 5xx) — where
// S3 may have committed the object despite the client seeing an error — must trigger the Exists
// reconciliation, NOT an immediate 'failed'. With Exists=true the RPC converges idempotently to
// 'processing' by running ONLY the durable transition; it never re-completes and never marks failed.
func TestCompleteVodUpload_AmbiguousErrorWithObjectConvergesToProcessing(t *testing.T) {
	s3 := &completeVodS3Stub{
		completeErr:  errors.New("operation error S3: CompleteMultipartUpload, read tcp 10.0.0.1: connection reset by peer"),
		existsResult: true,
		s3URL:        "s3://bucket/vod/t1/hash-1/video.mp4",
	}
	srv, mock, cleanup := newCompleteVodServer(t, s3)
	defer cleanup()

	// Prior attempt already claimed 'completing' AND persisted spec+descriptor atomically; this retry
	// finds it 'completing', so neither the claim nor the persist runs again.
	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1", "completing"))
	// An already-'completing' retry loads the persisted contract and uses ITS parts/upload-id.
	expectVodCompletionContractLoad(mock, "hash-1", `{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"et-1"}]}`)
	// Ambiguous S3 error + Exists=true -> skip re-completion, run 'completing' -> 'processing' + job + event.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'processing'`).
		WithArgs("hash-1", "s3://bucket/vod/t1/hash-1/video.mp4", "t1", "up-1").
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
	mock.ExpectQuery(`FROM foghorn\.artifacts a\s+LEFT JOIN foghorn\.vod_metadata`).
		WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "artifact_hash", "status", "size_bytes",
			"storage_location", "s3_url", "error_message",
			"created_at", "updated_at", "retention_until",
			"filename", "title", "description",
			"duration_ms", "resolution", "video_codec", "audio_codec", "bitrate_kbps",
			"s3_upload_id", "s3_key",
		}).AddRow(
			"hash-1", "hash-1", "processing", int64(2048),
			"s3", "s3://bucket/vod/t1/hash-1/video.mp4", "",
			time.Now(), time.Now(), nil,
			"video.mp4", "Video", "",
			nil, nil, nil, nil, nil,
			"up-1", "vod/t1/hash-1/video.mp4",
		))

	resp, err := srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		TenantId: "t1",
		UploadId: "up-1",
		Parts:    []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "et-1"}},
	})
	if err != nil {
		t.Fatalf("ambiguous+present should converge, got error: %v", err)
	}
	if resp.GetAsset().GetStatus() != sharedpb.VodStatus_VOD_STATUS_PROCESSING {
		t.Fatalf("expected PROCESSING after convergence, got %v", resp.GetAsset().GetStatus())
	}
	if s3.existsCalls != 1 {
		t.Fatalf("expected exactly one Exists probe, got %d", s3.existsCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An AMBIGUOUS S3 completion error with the object ABSENT must LEAVE the row
// 'completing' (for later reconciliation by retry or the recovery scan) — it must NOT mark 'failed',
// because S3 may still be finalizing the object. The RPC returns Internal and issues NO failed UPDATE.
func TestCompleteVodUpload_AmbiguousErrorObjectAbsentLeavesCompleting(t *testing.T) {
	s3 := &completeVodS3Stub{
		completeErr:  errors.New("operation error S3: CompleteMultipartUpload, RequestTimeout: i/o timeout"),
		existsResult: false,
	}
	srv, mock, cleanup := newCompleteVodServer(t, s3)
	defer cleanup()

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1", "completing"))
	// An already-'completing' retry loads the persisted contract before completing.
	expectVodCompletionContractLoad(mock, "hash-1", `{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"et-1"}]}`)
	// No UPDATE to 'failed' and no processing transition: the row stays 'completing'. sqlmock has no
	// further expectations, so any stray write would fail the test.

	_, err := srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		TenantId: "t1",
		UploadId: "up-1",
		Parts:    []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "et-1"}},
	})
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected Internal (left completing), got %s (err=%v)", got, err)
	}
	if s3.existsCalls != 1 {
		t.Fatalf("expected exactly one Exists probe, got %d", s3.existsCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The completion-error classifier keys on error TYPE, not string, so a definite
// client fault marks failed, NoSuchUpload/ambiguous reconcile via Exists.
func TestClassifyS3CompletionError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want s3CompletionClass
	}{
		{"nil defensive", nil, s3CompletionDefiniteFailure},
		{"typed NoSuchUpload", &types.NoSuchUpload{}, s3CompletionMaybeCompleted},
		{"smithy NoSuchUpload code", &smithyAPIErrorStub{code: "NoSuchUpload", fault: smithy.FaultClient}, s3CompletionMaybeCompleted},
		// Untyped string that merely contains "NoSuchUpload"/"does not exist" carries no SDK type signal,
		// so it is NOT reconciled as a gone upload — classification is type-based, never by substring.
		{"untyped NoSuchUpload string", errors.New("NoSuchUpload: does not exist"), s3CompletionDefiniteFailure},
		{"context deadline", context.DeadlineExceeded, s3CompletionAmbiguous},
		{"connection reset string", errors.New("read tcp: connection reset by peer"), s3CompletionAmbiguous},
		{"i/o timeout string", errors.New("RequestTimeout: i/o timeout"), s3CompletionAmbiguous},
		{"smithy 5xx server fault", &smithyAPIErrorStub{code: "InternalError", fault: smithy.FaultServer}, s3CompletionAmbiguous},
		{"smithy 4xx client fault", &smithyAPIErrorStub{code: "AccessDenied", fault: smithy.FaultClient}, s3CompletionDefiniteFailure},
		// Unrelated smithy client error whose message contains "does not exist" is a definite failure, not
		// a gone upload — the code (NoSuchBucket), not the message text, drives the decision.
		{"smithy NoSuchBucket does-not-exist message", &smithyAPIErrorStub{code: "NoSuchBucket", fault: smithy.FaultClient}, s3CompletionDefiniteFailure},
		{"opaque permanent", errors.New("EntityTooLarge: your proposed upload exceeds the maximum"), s3CompletionDefiniteFailure},
	}
	for _, tc := range cases {
		if got := classifyS3CompletionError(tc.err); got != tc.want {
			t.Errorf("%s: classify = %d, want %d", tc.name, got, tc.want)
		}
	}
}
