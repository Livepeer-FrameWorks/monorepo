package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/pkg/logging"
)

// fakeS3 implements artifacts.S3Client (and UploadAborter) for purge tests.
type fakeS3 struct {
	deleteCalls       []string
	deletePrefixCalls []string
	abortCalls        []abortCall
	deleteErr         error
	deletePrefixErr   error
	abortErr          error
}

type abortCall struct {
	key, uploadID string
}

func (f *fakeS3) Delete(_ context.Context, key string) error {
	f.deleteCalls = append(f.deleteCalls, key)
	return f.deleteErr
}
func (f *fakeS3) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.deletePrefixCalls = append(f.deletePrefixCalls, prefix)
	return 0, f.deletePrefixErr
}
func (f *fakeS3) ParseS3URL(s3URL string) (string, error) {
	const scheme = "s3://"
	if len(s3URL) < len(scheme) || s3URL[:len(scheme)] != scheme {
		return "", errors.New("not an s3 url")
	}
	rest := s3URL[len(scheme):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[i+1:], nil
		}
	}
	return "", errors.New("no key")
}
func (f *fakeS3) AbortMultipartUpload(_ context.Context, key, uploadID string) error {
	f.abortCalls = append(f.abortCalls, abortCall{key, uploadID})
	return f.abortErr
}

func newPurgeJob(t *testing.T, fake *fakeS3) (*PurgeDeletedJob, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	cleaner := &artifacts.Cleaner{LocalCluster: "platform-eu", S3: fake}
	j := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:           db,
		Logger:       logging.NewLogger(),
		RetentionAge: 30 * 24 * time.Hour,
		Cleaner:      cleaner,
		S3Aborter:    fake,
	})
	return j, mock, func() { _ = db.Close() }
}

// expectStaleUploadingNoRows lets purgeStaleUploadingVODs run without
// matching rows in tests that don't focus on the upload sweep.
func expectStaleUploadingNoRows(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("FROM foghorn.artifacts a").
		WithArgs(). // no args
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_upload_id"}))
}

// expectStaleNodeRowsCleanup matches purgeStaleNodeRows.
func expectStaleNodeRowsCleanup(mock sqlmock.Sqlmock) {
	mock.ExpectExec("DELETE FROM foghorn.artifact_nodes").
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestPurge_ClipUsesFormatColumnFromRow(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}).
		AddRow("clip-1", "clip", "tenant-a", "stream-x", "webm", "", "", "", "", "deleted")
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(rows)
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("clip-1").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deleteCalls) != 1 || fake.deleteCalls[0] != "clips/tenant-a/stream-x/clip-1.webm" {
		t.Errorf("deleteCalls = %v", fake.deleteCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_DVRDeletesPrefix(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}).
		AddRow("dvr-1", "dvr", "tenant-a", "stream-x", "", "", "", "", "", "deleted")
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(rows)
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("dvr-1").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deletePrefixCalls) != 1 || fake.deletePrefixCalls[0] != "dvr/tenant-a/stream-x/dvr-1" {
		t.Errorf("deletePrefixCalls = %v", fake.deletePrefixCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_VODUsesS3KeyFromMetadataJoin(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}).
		AddRow("vod-1", "vod", "tenant-a", "", "", "", "", "vod/tenant-a/vod-1/movie.mp4", "", "deleted")
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(rows)
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("vod-1").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deleteCalls) != 1 || fake.deleteCalls[0] != "vod/tenant-a/vod-1/movie.mp4" {
		t.Errorf("deleteCalls = %v", fake.deleteCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_S3FailureKeepsRow(t *testing.T) {
	fake := &fakeS3{deleteErr: errors.New("503 throttled")}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}).
		AddRow("clip-1", "clip", "tenant-a", "stream-x", "mp4", "", "", "", "", "deleted")
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(rows)
	// NO ExpectExec for DELETE FROM foghorn.artifacts — must not be called.
	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v (DELETE must not run on S3 failure)", err)
	}
}

func TestPurge_MissingTargetOnDeletedDropsRow(t *testing.T) {
	// A user-deleted VOD without vod_metadata.s3_key has nothing for us
	// to free in S3 (no derivable target). Drop the DB row so it doesn't
	// accumulate forever.
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}).
		AddRow("vod-2", "vod", "tenant-a", "", "", "", "", "", "", "deleted")
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(rows)
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("vod-2").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deleteCalls) != 0 {
		t.Errorf("S3 must not be called when target is missing, got %v", fake.deleteCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_MissingTargetOnFailedKeepsRow(t *testing.T) {
	// A failed VOD freeze may have left partial bytes at the deterministic
	// prefix even without vod_metadata.s3_key. We keep the row so an
	// operator (or a future repair sweep) can investigate; never drop
	// retry state when bytes might still exist.
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}).
		AddRow("vod-3", "vod", "tenant-a", "", "", "", "", "", "", "failed")
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(rows)
	// NO ExpectExec for DELETE FROM foghorn.artifacts — must not be called.

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v (failed+missing-target must not drop the row)", err)
	}
}

func TestPurge_NilCleanerSkipsBytesAndRowSweep(t *testing.T) {
	// Without a cleaner we can't guarantee S3 cleanup; never hard-delete
	// rows that may still hold bytes (locally or remotely).
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	j := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:     db,
		Logger: logging.NewLogger(),
		// Cleaner intentionally nil.
	})

	expectStaleUploadingNoRows(mock)
	// No SELECT for the main bytes+rows sweep — skipped.
	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_StaleUploadingAborts(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	staleRows := sqlmock.NewRows([]string{"artifact_hash", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_upload_id"}).
		AddRow("vod-stale", "platform-eu", "", "vod/tenant-a/vod-stale/movie.mp4", "upload-id-1")
	mock.ExpectQuery("FROM foghorn.artifacts a").WillReturnRows(staleRows)
	mock.ExpectExec("UPDATE foghorn.artifacts SET status").WithArgs("vod-stale").WillReturnResult(sqlmock.NewResult(0, 1))

	// No rows for the deleted/failed sweep.
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(
		sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}),
	)

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.abortCalls) != 1 {
		t.Fatalf("abortCalls = %v, want 1", fake.abortCalls)
	}
	if fake.abortCalls[0].key != "vod/tenant-a/vod-stale/movie.mp4" || fake.abortCalls[0].uploadID != "upload-id-1" {
		t.Errorf("abort call = %+v", fake.abortCalls[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_RemoteStaleUploadingSkipped(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	staleRows := sqlmock.NewRows([]string{"artifact_hash", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_upload_id"}).
		AddRow("vod-remote", "us-east", "us-east", "vod/tenant-a/vod-remote/m.mp4", "upload-id-2")
	mock.ExpectQuery("FROM foghorn.artifacts a").WillReturnRows(staleRows)
	// No abort, no UPDATE — remote rows skip+log.

	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s").WillReturnRows(
		sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "status"}),
	)
	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.abortCalls) != 0 {
		t.Errorf("remote upload must not abort locally; got %v", fake.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}
