package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeVodS3Client struct {
	parts       []storage.UploadedPart
	uploadParts []storage.UploadPart
	listErr     error
	listUpID    string
	createID    string
	abortKey    string
	abortUpID   string
	abortErr    error
	abortCalls  int
}

func (f *fakeVodS3Client) ListUploadedParts(_ context.Context, _, uploadID string) ([]storage.UploadedPart, error) {
	f.listUpID = uploadID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.parts, nil
}

func (f *fakeVodS3Client) CreateMultipartUpload(context.Context, string, string) (string, error) {
	if f.createID == "" {
		return "up-1", nil
	}
	return f.createID, nil
}
func (f *fakeVodS3Client) GeneratePresignedUploadParts(string, string, int, time.Duration) ([]storage.UploadPart, error) {
	if len(f.uploadParts) > 0 {
		return f.uploadParts, nil
	}
	return []storage.UploadPart{{PartNumber: 1, PresignedURL: "https://s3.example/part/1"}}, nil
}
func (f *fakeVodS3Client) CompleteMultipartUpload(context.Context, string, string, []storage.CompletedPart) error {
	panic("not used")
}
func (f *fakeVodS3Client) AbortMultipartUpload(_ context.Context, key, uploadID string) error {
	f.abortCalls++
	f.abortKey = key
	f.abortUpID = uploadID
	return f.abortErr
}
func (f *fakeVodS3Client) Exists(context.Context, string) (bool, error) { return false, nil }
func (f *fakeVodS3Client) BuildVodS3Key(string, string, string) string {
	return "vod/t1/hash/video.mp4"
}
func (f *fakeVodS3Client) BuildS3URL(string) string             { return "s3://bucket/vod/t1/hash/video.mp4" }
func (f *fakeVodS3Client) Delete(context.Context, string) error { panic("not used") }
func (f *fakeVodS3Client) PutObject(context.Context, string, []byte, string) error {
	panic("not used")
}
func (f *fakeVodS3Client) GeneratePresignedGET(string, time.Duration) (string, error) {
	return "https://example.com/presigned", nil
}

func newStatusServer(t *testing.T, s3 *fakeVodS3Client) (*FoghornGRPCServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, s3, nil)
	return srv, mock, func() { _ = db.Close() }
}

const statusSelect = `SELECT v.artifact_hash, COALESCE\(v.s3_key, ''\), a.status,
	       a.error_message, a.retention_until, v.upload_expires_at, v.total_parts`

func statusRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"artifact_hash", "s3_key", "status", "error_message", "retention_until", "upload_expires_at", "total_parts",
	})
}

func TestGetVodUploadStatus_RequiresTenantAndUploadID(t *testing.T) {
	srv, _, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	_, err := srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{UploadId: "u1"})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing tenant, got %s", got)
	}
	_, err = srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{TenantId: "t1"})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing upload_id, got %s", got)
	}
}

func TestCreateVodUpload_MetadataFailureAbortsMultipartUpload(t *testing.T) {
	s3 := &fakeVodS3Client{
		createID:    "up-1",
		uploadParts: []storage.UploadPart{{PartNumber: 1, PresignedURL: "https://s3.example/part/1"}},
	}
	srv, mock, cleanup := newStatusServer(t, s3)
	defer cleanup()

	// Idempotency pre-check: a Commodore-minted vod_hash is looked up first so a
	// retry re-signs an existing multipart instead of creating a second. No prior
	// upload here, so it returns no rows and the create proceeds.
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WillReturnError(sql.ErrNoRows)
	// Artifact + metadata + REQUESTED event are one atomic transaction now. When the vod_metadata
	// INSERT fails the whole tx rolls back and the S3 multipart upload is aborted — no half-written
	// rows, no orphaned upload.
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO foghorn\.artifacts`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.vod_metadata`).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	internalName := "vod-test"
	vodHash := "hash-1"
	_, err := srv.CreateVodUpload(context.Background(), &sharedpb.CreateVodUploadRequest{
		TenantId:     "00000000-0000-0000-0000-000000000001",
		UserId:       "00000000-0000-0000-0000-000000000002",
		Filename:     "video.mp4",
		SizeBytes:    1024,
		VodHash:      &vodHash,
		InternalName: &internalName,
	})
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected Internal for metadata failure, got %s", got)
	}
	if s3.abortUpID != "up-1" {
		t.Fatalf("expected multipart upload to be aborted, got upload_id %q", s3.abortUpID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The command-ledger 'accepted' row is written FIRST — before any fallible precheck — so
// a precheck failure records 'rejected' rather than leaving a missing command. Here
// CreateVodUpload carries a Commodore request_id; the accept INSERT + identity/status read
// run before the idempotency probe, and when that probe errors the deferred finalizer
// CAS-flips the still-'accepted' row to 'rejected'. The strict expectation order proves
// the accept precedes the precheck.
func TestCreateVodUpload_AcceptedBeforePrecheckFailureRecordsRejected(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	// 1) Accept is recorded first (INSERT ... ON CONFLICT DO NOTHING + identity/status read).
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_creation_commands`).
		WithArgs("req-vod", "t1", "vod", "vh1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \(tenant_id`).
		WithArgs("req-vod", "t1", "vod", "vh1").
		WillReturnRows(sqlmock.NewRows([]string{"identity_ok", "status"}).AddRow(true, "accepted"))
	// 2) A later precheck (the idempotency probe) fails.
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).
		WithArgs("vh1", "t1").
		WillReturnError(sql.ErrConnDone)
	// 3) The deferred finalizer CAS-rejects the still-'accepted' row — no missing command.
	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("req-vod", "t1", "vod", "vh1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	internalName := "vod-test"
	vodHash := "vh1"
	requestID := "req-vod"
	_, err := srv.CreateVodUpload(context.Background(), &sharedpb.CreateVodUploadRequest{
		TenantId:     "t1",
		Filename:     "video.mp4",
		SizeBytes:    1024,
		VodHash:      &vodHash,
		InternalName: &internalName,
		RequestId:    &requestID,
	})
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected Internal for the failed precheck, got %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetVodUploadStatus_NotFoundForWrongTenant(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "wrong-tenant").
		WillReturnError(sql.ErrNoRows)

	_, err := srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{
		TenantId: "wrong-tenant",
		UploadId: "up-1",
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("expected NotFound for wrong tenant, got %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetVodUploadStatus_TerminalStateSkipsS3(t *testing.T) {
	s3 := &fakeVodS3Client{parts: []storage.UploadedPart{{PartNumber: 1, ETag: "should-not-be-called"}}}
	srv, mock, cleanup := newStatusServer(t, s3)
	defer cleanup()

	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "t1").
		WillReturnRows(statusRows().AddRow("hash-1", "vod/t1/hash-1/hash-1.mp4", "ready",
			nil, time.Now().Add(30*24*time.Hour), nil, nil))

	resp, err := srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != sharedpb.VodStatus_VOD_STATUS_READY {
		t.Fatalf("expected READY, got %v", resp.State)
	}
	if s3.listUpID != "" {
		t.Fatal("ListUploadedParts should not be called for terminal state")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMapArtifactStatusToVodStatus_TerminalAliases(t *testing.T) {
	for _, status := range []string{"completed", "complete", "done", "ready", "synced"} {
		if got := mapArtifactStatusToVodStatus(status); got != sharedpb.VodStatus_VOD_STATUS_READY {
			t.Fatalf("expected %q to map to READY, got %v", status, got)
		}
	}
}

func TestGetVodUploadStatus_FailedStateReturnsErrorCode(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "t1").
		WillReturnRows(statusRows().AddRow("hash-1", "vod/t1/hash-1/hash-1.mp4", "failed",
			"transcode crashed", nil, nil, nil))

	resp, err := srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.LastErrorCode != "processing_failed" {
		t.Fatalf("expected processing_failed, got %q", resp.LastErrorCode)
	}
}

func TestGetVodUploadStatus_ProcessingSkipsExpiryAndS3(t *testing.T) {
	s3 := &fakeVodS3Client{parts: []storage.UploadedPart{{PartNumber: 1, ETag: "should-not-be-called"}}}
	srv, mock, cleanup := newStatusServer(t, s3)
	defer cleanup()

	expired := time.Now().Add(-1 * time.Hour)
	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "t1").
		WillReturnRows(statusRows().AddRow("hash-1", "vod/t1/hash-1/hash-1.mp4", "processing",
			nil, nil, expired, 5))

	resp, err := srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != sharedpb.VodStatus_VOD_STATUS_PROCESSING {
		t.Fatalf("expected PROCESSING, got %v", resp.State)
	}
	if s3.listUpID != "" {
		t.Fatal("ListUploadedParts should not be called for processing state")
	}
}

func TestGetVodUploadStatus_ExpiredSession(t *testing.T) {
	s3 := &fakeVodS3Client{parts: []storage.UploadedPart{{PartNumber: 1}}}
	srv, mock, cleanup := newStatusServer(t, s3)
	defer cleanup()

	expired := time.Now().Add(-1 * time.Hour)
	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "t1").
		WillReturnRows(statusRows().AddRow("hash-1", "key", "uploading", nil, nil, expired, 5))

	resp, err := srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != sharedpb.VodStatus_VOD_STATUS_EXPIRED {
		t.Fatalf("expected EXPIRED, got %v", resp.State)
	}
	if s3.listUpID != "" {
		t.Fatal("ListUploadedParts should not run for expired session")
	}
}

func TestGetVodUploadStatus_LiveReconciliation(t *testing.T) {
	s3 := &fakeVodS3Client{
		parts: []storage.UploadedPart{
			{PartNumber: 1, ETag: "et-1", SizeBytes: 1024},
			{PartNumber: 3, ETag: "et-3", SizeBytes: 1024},
		},
	}
	srv, mock, cleanup := newStatusServer(t, s3)
	defer cleanup()

	future := time.Now().Add(2 * time.Hour)
	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "t1").
		WillReturnRows(statusRows().AddRow("hash-1", "vod/t1/hash-1/hash-1.mp4", "uploading",
			nil, nil, future, 4))

	resp, err := srv.GetVodUploadStatus(context.Background(), &sharedpb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != sharedpb.VodStatus_VOD_STATUS_UPLOADING {
		t.Fatalf("expected UPLOADING, got %v", resp.State)
	}
	if len(resp.UploadedParts) != 2 {
		t.Fatalf("expected 2 uploaded parts, got %d", len(resp.UploadedParts))
	}
	wantMissing := map[int32]struct{}{2: {}, 4: {}}
	if len(resp.MissingParts) != 2 {
		t.Fatalf("expected 2 missing parts, got %v", resp.MissingParts)
	}
	for _, m := range resp.MissingParts {
		if _, ok := wantMissing[m]; !ok {
			t.Fatalf("unexpected missing part %d", m)
		}
	}
	if s3.listUpID != "up-1" {
		t.Fatal("ListUploadedParts not called")
	}
}
