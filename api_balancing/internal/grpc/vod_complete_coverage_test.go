package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testStubBackendID is the backend fingerprint that s.localBackendID() computes for the VOD S3 stubs (whose
// BackendDescriptor returns this exact tuple). Recorded backend_id in mocks must equal it so the ownership fence
// (recorded == local) passes for owned rows; a different value models a foreign backend.
var testStubBackendID = control.BackendFingerprint("s3", "test-bucket", "https://s3.example", "eu-central", "prod")

// completeVodS3Stub is a VOD S3 seam whose CompleteMultipartUpload actually
// records its inputs (the production fakeVodS3Client panics on it). Named with
// the -VodComplete suffix to avoid colliding with the other grpc test agent.
type completeVodS3Stub struct {
	completeErr   error
	completeKey   string
	completeUpID  string
	completeParts []storage.CompletedPart
	s3URL         string
	existsResult  bool
	existsErr     error
	existsCalls   int
}

func (f *completeVodS3Stub) Exists(context.Context, string) (bool, error) {
	f.existsCalls++
	return f.existsResult, f.existsErr
}

func (f *completeVodS3Stub) ListUploadedParts(context.Context, string, string) ([]storage.UploadedPart, error) {
	return nil, nil
}
func (f *completeVodS3Stub) CreateMultipartUpload(context.Context, string, string) (string, error) {
	return "up-1", nil
}
func (f *completeVodS3Stub) GeneratePresignedUploadParts(string, string, int, time.Duration) ([]storage.UploadPart, error) {
	return nil, nil
}
func (f *completeVodS3Stub) CompleteMultipartUpload(_ context.Context, key, uploadID string, parts []storage.CompletedPart) error {
	f.completeKey = key
	f.completeUpID = uploadID
	f.completeParts = parts
	return f.completeErr
}
func (f *completeVodS3Stub) AbortMultipartUpload(context.Context, string, string) error { return nil }
func (f *completeVodS3Stub) BuildVodS3Key(string, string, string) string {
	return "vod/t1/hash/video.mp4"
}
func (f *completeVodS3Stub) BuildS3URL(string) string {
	if f.s3URL != "" {
		return f.s3URL
	}
	return "s3://bucket/vod/t1/hash-1/video.mp4"
}
func (f *completeVodS3Stub) Delete(context.Context, string) error { return nil }
func (f *completeVodS3Stub) PutObject(context.Context, string, []byte, string) error {
	return nil
}
func (f *completeVodS3Stub) GeneratePresignedGET(string, time.Duration) (string, error) {
	return "https://example.com/presigned", nil
}

// BackendDescriptor gives the stub a non-empty local backend fingerprint so CompleteVodUpload's I2 verify (the recorded
// backend must be present) actually runs in tests rather than being skipped for a descriptor-less store.
func (f *completeVodS3Stub) BackendDescriptor() (bucket, endpoint, region, prefix string) {
	return "test-bucket", "https://s3.example", "eu-central", "prod"
}

// expectVodCompletionContractLoad stubs the authoritative-contract SELECT that CompleteVodUpload issues
// after the claim: it returns the persisted vod_completion_descriptor (the ordered part set + upload id),
// processes_json the RPC must use instead of the retry request's parts/spec, and the backend_id recorded
// when the upload was created (verified, never reconstructed).
func expectVodCompletionContractLoad(mock sqlmock.Sqlmock, hash, descriptorJSON string) {
	mock.ExpectQuery(`SELECT COALESCE\(v.vod_completion_descriptor::text`).
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"vod_completion_descriptor", "processes_json", "backend_id"}).
			AddRow(descriptorJSON, "", testStubBackendID))
}

func newCompleteVodServer(t *testing.T, s3 *completeVodS3Stub) (*FoghornGRPCServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	srv := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, s3, nil)
	return srv, mock, func() { _ = db.Close() }
}

// Invariant: CompleteVodUpload rejects requests with no upload_id and no parts
// BEFORE touching S3 or the DB. These are the input-contract guards.
func TestCompleteVodUpload_ValidationGuards(t *testing.T) {
	srv, _, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	_, err := srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		Parts: []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "et-1"}},
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("missing upload_id: expected InvalidArgument, got %s", got)
	}

	_, err = srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		UploadId: "up-1",
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("missing parts: expected InvalidArgument, got %s", got)
	}
}

// Invariant: when no S3 client is configured the RPC fails closed with
// FailedPrecondition rather than nil-panicking on the multipart complete.
func TestCompleteVodUpload_NoS3ClientFailsClosed(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	srv := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	_, err = srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		UploadId: "up-1",
		Parts:    []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "et-1"}},
	})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition with no S3, got %s", got)
	}
}

// Invariant: the upload lookup is tenant-scoped — a tenant that does not own the
// upload_id sees NotFound, and the SELECT carries the claimed tenant_id arg.
func TestCompleteVodUpload_NotFoundForWrongTenant(t *testing.T) {
	srv, mock, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id`).
		WithArgs("up-1", "wrong-tenant").
		WillReturnError(sql.ErrNoRows)

	_, err := srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		TenantId: "wrong-tenant",
		UploadId: "up-1",
		Parts:    []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "et-1"}},
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("expected NotFound for wrong tenant, got %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: a failed S3 CompleteMultipartUpload transitions the artifact to
// 'failed' (the UPDATE ... SET status='failed') and surfaces Internal, rather
// than advancing the artifact to processing.
func TestCompleteVodUpload_S3FailureMarksArtifactFailed(t *testing.T) {
	s3 := &completeVodS3Stub{completeErr: errors.New("boom")}
	srv, mock, cleanup := newCompleteVodServer(t, s3)
	defer cleanup()

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(1024), "user-1", "uploading"))
	// The 'uploading' row is claimed to 'completing' AND the processing spec + multipart completion
	// descriptor are persisted ATOMICALLY in ONE tx before the external S3 call (fail-closed claim).
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts AS a\s+SET status = 'completing'`).
		WithArgs("hash-1", "t1", "up-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.vod_metadata\s+SET processes_json`).
		WithArgs("", sqlmock.AnyArg(), "hash-1", "up-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// The claimed row's authoritative completion contract is loaded and used for the S3 completion.
	expectVodCompletionContractLoad(mock, "hash-1", `{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"et-1"}]}`)
	// A non-NoSuchUpload S3 error is a genuine failure: the guarded FAILED transition (tenant-scoped)
	// and its lifecycle event commit in ONE tx.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts AS a\s+SET status = 'failed'.*WHERE artifact_hash = \$2\s+AND tenant_id = \$3::uuid\s+AND status NOT IN`).
		WithArgs("S3 upload failed: boom", "hash-1", "t1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err := srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		TenantId: "t1",
		UploadId: "up-1",
		Parts:    []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "et-1"}},
	})
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected Internal on S3 failure, got %s", got)
	}
	if s3.completeKey != "vod/t1/hash-1/video.mp4" || s3.completeUpID != "up-1" {
		t.Fatalf("CompleteMultipartUpload got wrong key/upload_id: %q / %q", s3.completeKey, s3.completeUpID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: the multipart-complete happy path forwards the proto parts to S3,
// flips the artifact to status='processing' (storage_location='s3'), and returns
// the asset. vodPipeline is nil in tests, so no processing-job INSERT is queued
// and pipelineFailed stays false — the asset comes back as PROCESSING.
func TestCompleteVodUpload_HappyPathTransitionsToProcessing(t *testing.T) {
	s3 := &completeVodS3Stub{s3URL: "s3://bucket/vod/t1/hash-1/video.mp4"}
	srv, mock, cleanup := newCompleteVodServer(t, s3)
	defer cleanup()

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1", "uploading"))
	// Claim 'uploading'->'completing' AND persist the processing spec + multipart completion descriptor
	// ATOMICALLY in ONE tx before the external S3 CompleteMultipartUpload call.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts AS a\s+SET status = 'completing'`).
		WithArgs("hash-1", "t1", "up-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.vod_metadata\s+SET processes_json`).
		WithArgs("", sqlmock.AnyArg(), "hash-1", "up-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Load the authoritative contract: its ordered parts (1,2) are what reach S3, not the request's.
	expectVodCompletionContractLoad(mock, "hash-1", `{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"et-1"},{"part_number":2,"etag":"et-2"}]}`)
	// Advance: status 'completing' -> 'processing' (tenant-scoped), storage_location -> s3, committed
	// atomically with the processing job AND the PROCESSING lifecycle outbox row.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts AS a\s+SET status = 'processing'`).
		WithArgs("s3://bucket/vod/t1/hash-1/video.mp4", "hash-1", "t1", "up-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
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
	// lookupCompletedUploadAsset -> getVodAssetInfo SELECT (20 columns).
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
		Parts: []*sharedpb.VodCompletedPart{
			{PartNumber: 1, Etag: "et-1"},
			{PartNumber: 2, Etag: "et-2"},
		},
	})
	if err != nil {
		t.Fatalf("happy path: unexpected error: %v", err)
	}
	if resp.GetAsset().GetArtifactHash() != "hash-1" {
		t.Fatalf("expected artifact hash-1, got %q", resp.GetAsset().GetArtifactHash())
	}
	if resp.GetAsset().GetStatus() != sharedpb.VodStatus_VOD_STATUS_PROCESSING {
		t.Fatalf("expected PROCESSING asset, got %v", resp.GetAsset().GetStatus())
	}
	// The proto parts must reach S3 unmodified, in order.
	if len(s3.completeParts) != 2 || s3.completeParts[0].PartNumber != 1 || s3.completeParts[0].ETag != "et-1" {
		t.Fatalf("S3 did not receive parts faithfully: %+v", s3.completeParts)
	}
	if s3.completeParts[1].PartNumber != 2 || s3.completeParts[1].ETag != "et-2" {
		t.Fatalf("S3 second part mismatch: %+v", s3.completeParts[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Reconciliation invariant: a retry where the multipart already completed on a prior attempt
// (S3 returns NoSuchUpload) but the durable PG transition never committed must NOT record 'failed'.
// The row is already claimed ('completing'); with the final object present (Exists=true) the RPC
// converges idempotently to 'processing' by running ONLY the durable transition — it never re-runs
// multipart completion and never marks failed.
func TestCompleteVodUpload_NoSuchUploadWithObjectConvergesToProcessing(t *testing.T) {
	s3 := &completeVodS3Stub{
		// A genuine SDK NoSuchUpload arrives TYPED (aws-sdk-go-v2 wraps it with %w); the classifier keys
		// on the type, not the message text.
		completeErr:  fmt.Errorf("operation error S3: CompleteMultipartUpload: %w", &types.NoSuchUpload{}),
		existsResult: true,
		s3URL:        "s3://bucket/vod/t1/hash-1/video.mp4",
	}
	srv, mock, cleanup := newCompleteVodServer(t, s3)
	defer cleanup()

	// The prior attempt already claimed the row to 'completing' AND persisted the spec+descriptor
	// atomically; this retry finds it 'completing', so neither the claim nor the persist runs again.
	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1", "completing"))
	// An already-'completing' retry loads the persisted contract (no re-claim) and uses ITS parts/upload-id.
	expectVodCompletionContractLoad(mock, "hash-1", `{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"et-1"}]}`)
	// S3 CompleteMultipartUpload returns NoSuchUpload; Exists=true, so we skip re-completion and run
	// the durable 'completing' -> 'processing' transition + processing job + PROCESSING event atomically.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.artifacts AS a\s+SET status = 'processing'`).
		WithArgs("s3://bucket/vod/t1/hash-1/video.mp4", "hash-1", "t1", "up-1").
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
		t.Fatalf("reconciliation should converge, got error: %v", err)
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

// Idempotency invariant: a retry that finds the artifact already 'processing' returns the asset
// without calling S3 again or re-running the durable transition. This is the converged terminal of
// the reconciliation flow — the second call must be a pure no-op against storage.
func TestCompleteVodUpload_RetryAlreadyProcessingIsNoOp(t *testing.T) {
	s3 := &completeVodS3Stub{s3URL: "s3://bucket/vod/t1/hash-1/video.mp4"}
	srv, mock, cleanup := newCompleteVodServer(t, s3)
	defer cleanup()

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1", "processing"))
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
		t.Fatalf("already-processing retry should be a no-op, got error: %v", err)
	}
	if resp.GetAsset().GetStatus() != sharedpb.VodStatus_VOD_STATUS_PROCESSING {
		t.Fatalf("expected PROCESSING, got %v", resp.GetAsset().GetStatus())
	}
	if s3.completeKey != "" || s3.existsCalls != 0 {
		t.Fatalf("S3 must not be touched on an already-processing retry: completeKey=%q existsCalls=%d", s3.completeKey, s3.existsCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant: a retry arriving while the row is already 'completing' whose
// part set DIVERGES from the persisted completion contract is REJECTED with FailedPrecondition — the RPC
// must never complete a different multipart part set than the first claim persisted. No S3 completion and
// no state transition run.
func TestCompleteVodUpload_CompletingRetryDivergentPartsRejected(t *testing.T) {
	s3 := &completeVodS3Stub{}
	srv, mock, cleanup := newCompleteVodServer(t, s3)
	defer cleanup()

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1", "completing"))
	// Persisted contract has part 1 = "et-1"; the retry below claims part 1 = "DIVERGENT".
	expectVodCompletionContractLoad(mock, "hash-1", `{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"et-1"}]}`)
	// No further expectations: a divergent retry must be rejected before any S3 call or state write.

	_, err := srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
		TenantId: "t1",
		UploadId: "up-1",
		Parts:    []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "DIVERGENT"}},
	})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for divergent retry, got %s (err=%v)", got, err)
	}
	if s3.completeKey != "" || s3.existsCalls != 0 {
		t.Fatalf("S3 must not be touched on a rejected divergent retry: completeKey=%q existsCalls=%d", s3.completeKey, s3.existsCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant (I2): completion FENCES on exact backend ownership recorded when the upload was CREATED — recorded
// backend_id must EQUAL this cell's local store. A missing (unattributed) OR mismatched (foreign) backend is refused
// BEFORE the irreversible S3 completion or any state transition. Presence alone is NOT enough; only exact equality
// lets a foreign object never be finalized on the current store (which cleanup would then refuse to delete, leaking it).
func TestCompleteVodUpload_BackendOwnershipFence(t *testing.T) {
	cases := []struct {
		name     string
		recorded string
	}{
		{"missing (unattributed)", ""},
		{"mismatch (foreign backend)", "some-other-cell-backend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s3 := &completeVodS3Stub{} // BackendDescriptor => localBackendID() is this cell's real fingerprint
			srv, mock, cleanup := newCompleteVodServer(t, s3)
			defer cleanup()

			mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id, a\.status`).
				WithArgs("up-1", "t1").
				WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id", "status"}).
					AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1", "completing"))
			mock.ExpectQuery(`SELECT COALESCE\(v.vod_completion_descriptor::text`).
				WithArgs("hash-1").
				WillReturnRows(sqlmock.NewRows([]string{"vod_completion_descriptor", "processes_json", "backend_id"}).
					AddRow(`{"s3_key":"vod/t1/hash-1/video.mp4","upload_id":"up-1","parts":[{"part_number":1,"etag":"et-1"}]}`, "", tc.recorded))
			// No further expectations: the RPC must fail closed before any S3 completion or state write.

			_, err := srv.CompleteVodUpload(context.Background(), &sharedpb.CompleteVodUploadRequest{
				TenantId: "t1",
				UploadId: "up-1",
				Parts:    []*sharedpb.VodCompletedPart{{PartNumber: 1, Etag: "et-1"}},
			})
			if got := status.Code(err); got != codes.Internal {
				t.Fatalf("expected Internal for %s, got %s (err=%v)", tc.name, got, err)
			}
			if s3.completeKey != "" || s3.existsCalls != 0 {
				t.Fatalf("S3 must not be touched on a fenced row: completeKey=%q existsCalls=%d", s3.completeKey, s3.existsCalls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// --- DVR chapter RPC validation (pre-control-access arms) ---

// Invariant: RetrieveDVRChapter rejects empty dvr_artifact_id and a degenerate
// window (end_ms <= start_ms) before any control-plane access.
func TestRetrieveDVRChapter_ValidationGuards(t *testing.T) {
	srv, _, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	_, err := srv.RetrieveDVRChapter(context.Background(), &foghorncontrolpb.RetrieveDVRChapterRequest{
		StartMs: 0,
		EndMs:   1000,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("missing dvr_artifact_id: expected InvalidArgument, got %s", got)
	}

	_, err = srv.RetrieveDVRChapter(context.Background(), &foghorncontrolpb.RetrieveDVRChapterRequest{
		DvrArtifactId: "dvr-1",
		StartMs:       2000,
		EndMs:         1000,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("end_ms <= start_ms: expected InvalidArgument, got %s", got)
	}
}

// Invariant: an unrecognized chapter mode is rejected with InvalidArgument
// before the policy/registry is ever consulted.
func TestRetrieveDVRChapter_RejectsUnknownMode(t *testing.T) {
	srv, _, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	_, err := srv.RetrieveDVRChapter(context.Background(), &foghorncontrolpb.RetrieveDVRChapterRequest{
		DvrArtifactId: "dvr-1",
		Mode:          "garbage_mode",
		StartMs:       0,
		EndMs:         1000,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for bad mode, got %s", got)
	}
}

// Invariant: fixed_interval below the 1h automatic-chapter floor is rejected
// before tenant assertion / chapter lookup. This is the pre-control arm: with a
// concrete (non-empty) mode there is no ReadDVRChapterPolicy call, and the
// interval guard fires ahead of assertChapterTenant.
func TestRetrieveDVRChapter_FixedIntervalBelowFloorRejected(t *testing.T) {
	srv, _, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	_, err := srv.RetrieveDVRChapter(context.Background(), &foghorncontrolpb.RetrieveDVRChapterRequest{
		DvrArtifactId:   "dvr-1",
		Mode:            control.ChapterModeFixedInterval,
		IntervalSeconds: 600, // 10m < 3600s floor
		StartMs:         0,
		EndMs:           1000,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for sub-floor interval, got %s", got)
	}
}

// Invariant: ListDVRChapters rejects empty dvr_artifact_id before tenant
// assertion / chapter enumeration.
func TestListDVRChapters_RequiresArtifactID(t *testing.T) {
	srv, _, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	_, err := srv.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("missing dvr_artifact_id: expected InvalidArgument, got %s", got)
	}
}

// Invariant: fixed_interval listing requires a positive interval_seconds at or
// above the automatic-chapter floor. Both guards fire after the tenant assertion
// passes (empty tenant_id is the internal-caller bypass) but before any
// control-plane range/enumeration call. We pin both arms.
func TestListDVRChapters_FixedIntervalGuards(t *testing.T) {
	srv, _, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	// interval_seconds <= 0
	_, err := srv.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{
		DvrArtifactId:   "dvr-1",
		Mode:            control.ChapterModeFixedInterval,
		IntervalSeconds: 0,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("interval<=0: expected InvalidArgument, got %s", got)
	}

	// 0 < interval_seconds < floor
	_, err = srv.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{
		DvrArtifactId:   "dvr-1",
		Mode:            control.ChapterModeFixedInterval,
		IntervalSeconds: 60,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("interval below floor: expected InvalidArgument, got %s", got)
	}
}

// Invariant: assertChapterTenant denies a caller whose claimed tenant_id does
// not match the artifact's owner (PermissionDenied), and reports NotFound when
// the artifact row is absent. Empty tenant_id is the documented internal bypass.
// This guard runs ahead of any chapter enumeration in ListDVRChapters.
func TestListDVRChapters_TenantGuard(t *testing.T) {
	srv, mock, cleanup := newCompleteVodServer(t, &completeVodS3Stub{})
	defer cleanup()

	// Wrong tenant -> PermissionDenied (artifact owned by someone else).
	mock.ExpectQuery(`SELECT tenant_id::text FROM foghorn\.artifacts WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("owner-tenant"))

	_, err := srv.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{
		DvrArtifactId: "dvr-1",
		TenantId:      "intruder-tenant",
	})
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for tenant mismatch, got %s", got)
	}

	// Missing artifact -> NotFound.
	mock.ExpectQuery(`SELECT tenant_id::text FROM foghorn\.artifacts WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
		WithArgs("dvr-missing").
		WillReturnError(sql.ErrNoRows)

	_, err = srv.ListDVRChapters(context.Background(), &foghorncontrolpb.ListDVRChaptersRequest{
		DvrArtifactId: "dvr-missing",
		TenantId:      "any-tenant",
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("expected NotFound for missing artifact, got %s", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
