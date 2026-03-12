package control

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessFreezeComplete_Success(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 'local'.*sync_status = 'synced'.*s3_url").
		WithArgs("s3://bucket/clip.mp4", "hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processFreezeComplete(context.Background(), &pb.FreezeComplete{
		RequestId: "req-1",
		AssetHash: "hash-1",
		Status:    "success",
		S3Url:     "s3://bucket/clip.mp4",
		SizeBytes: 1024,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessFreezeComplete_Success_EmptyS3URL(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 'local'.*sync_status = 'synced'").
		WithArgs("", "hash-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processFreezeComplete(context.Background(), &pb.FreezeComplete{
		AssetHash: "hash-1",
		Status:    "success",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessFreezeComplete_Failure_RevertsToLocal(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*SET storage_location = 'local'.*sync_status = 'failed'").
		WithArgs("upload timed out", "hash-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Lookup artifact type for S3 cleanup
	mock.ExpectQuery("SELECT artifact_type.*FROM foghorn.artifacts").
		WithArgs("hash-2").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_internal_name", "tenant_id"}).
			AddRow("clip", "stream-1", "tenant-1"))

	processFreezeComplete(context.Background(), &pb.FreezeComplete{
		AssetHash: "hash-2",
		Status:    "failed",
		Error:     "upload timed out",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// S3 cleanup runs in goroutine — give it a moment
	time.Sleep(50 * time.Millisecond)
	s3Mock.mu.Lock()
	if len(s3Mock.deletePrefixCalls) != 1 {
		t.Fatalf("expected 1 DeletePrefix call, got %d", len(s3Mock.deletePrefixCalls))
	}
	s3Mock.mu.Unlock()
}

func TestProcessFreezeComplete_Failure_DVR_CleanupPrefix(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'failed'").
		WithArgs("", "dvr-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery("SELECT artifact_type").
		WithArgs("dvr-hash").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_internal_name", "tenant_id"}).
			AddRow("dvr", "stream-dvr", "tenant-dvr"))

	processFreezeComplete(context.Background(), &pb.FreezeComplete{
		AssetHash: "dvr-hash",
		Status:    "failed",
	}, "node-1", logger)

	time.Sleep(50 * time.Millisecond)
	s3Mock.mu.Lock()
	if len(s3Mock.deletePrefixCalls) != 1 {
		t.Fatalf("expected 1 DeletePrefix for DVR, got %d", len(s3Mock.deletePrefixCalls))
	}
	prefix := s3Mock.deletePrefixCalls[0]
	s3Mock.mu.Unlock()
	if prefix != "tenant-dvr/stream-dvr/dvr/dvr-hash" {
		t.Fatalf("unexpected DVR prefix: %s", prefix)
	}
}

func TestProcessFreezeComplete_Failure_NoTenant_SkipsCleanup(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("err", "hash-3").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery("SELECT artifact_type").
		WithArgs("hash-3").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_internal_name", "tenant_id"}).
			AddRow("clip", "", ""))

	processFreezeComplete(context.Background(), &pb.FreezeComplete{
		AssetHash: "hash-3",
		Status:    "failed",
		Error:     "err",
	}, "node-1", logger)

	time.Sleep(50 * time.Millisecond)
	s3Mock.mu.Lock()
	if len(s3Mock.deletePrefixCalls) != 0 {
		t.Fatal("should not call DeletePrefix when tenantID is empty")
	}
	s3Mock.mu.Unlock()
}

func TestProcessFreezeComplete_Failure_NilS3Client_SkipsCleanup(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	s3Client = nil // override
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("err", "hash-4").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processFreezeComplete(context.Background(), &pb.FreezeComplete{
		AssetHash: "hash-4",
		Status:    "failed",
		Error:     "err",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
