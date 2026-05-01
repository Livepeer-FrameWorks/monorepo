package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/storage"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
	f.abortKey = key
	f.abortUpID = uploadID
	return nil
}
func (f *fakeVodS3Client) BuildVodS3Key(string, string, string) string {
	return "vod/t1/hash/video.mp4"
}
func (f *fakeVodS3Client) BuildS3URL(string) string             { return "s3://bucket/vod/t1/hash/video.mp4" }
func (f *fakeVodS3Client) Delete(context.Context, string) error { panic("not used") }

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

	_, err := srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{UploadId: "u1"})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing tenant, got %s", got)
	}
	_, err = srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{TenantId: "t1"})
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

	mock.ExpectExec(`INSERT INTO foghorn\.artifacts`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.vod_metadata`).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectExec(`UPDATE foghorn\.artifacts`).
		WillReturnResult(sqlmock.NewResult(1, 1))

	internalName := "vod-test"
	vodHash := "hash-1"
	_, err := srv.CreateVodUpload(context.Background(), &pb.CreateVodUploadRequest{
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

func TestGetVodUploadStatus_NotFoundForWrongTenant(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "wrong-tenant").
		WillReturnError(sql.ErrNoRows)

	_, err := srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{
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

	resp, err := srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != pb.VodStatus_VOD_STATUS_READY {
		t.Fatalf("expected READY, got %v", resp.State)
	}
	if s3.listUpID != "" {
		t.Fatal("ListUploadedParts should not be called for terminal state")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetVodUploadStatus_FailedStateReturnsErrorCode(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectQuery(statusSelect).
		WithArgs("up-1", "t1").
		WillReturnRows(statusRows().AddRow("hash-1", "vod/t1/hash-1/hash-1.mp4", "failed",
			"transcode crashed", nil, nil, nil))

	resp, err := srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{
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

	resp, err := srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != pb.VodStatus_VOD_STATUS_PROCESSING {
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

	resp, err := srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != pb.VodStatus_VOD_STATUS_EXPIRED {
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

	resp, err := srv.GetVodUploadStatus(context.Background(), &pb.GetVodUploadStatusRequest{
		TenantId: "t1",
		UploadId: "up-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State != pb.VodStatus_VOD_STATUS_UPLOADING {
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
