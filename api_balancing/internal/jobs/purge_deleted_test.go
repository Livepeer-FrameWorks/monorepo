package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/api_balancing/internal/artifacts"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// fakeS3 implements artifacts.S3Client for purge tests (AbortMultipartUpload is retained to assert the stale-upload
// sweep never calls it directly — the aborting saga owns S3 teardown).
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
	// A production cell always carries a local backend fingerprint; the deleted-sweep now claims ONLY rows recorded on
	// it (NULL is never claimed by cluster). Reap-seed rows therefore carry backend_id = "backend-eu".
	cleaner := &artifacts.Cleaner{LocalCluster: "platform-eu", S3: fake, LocalBackendID: "backend-eu"}
	j := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:           db,
		Logger:       logging.NewLogger(),
		RetentionAge: 30 * 24 * time.Hour,
		Cleaner:      cleaner,
	})
	return j, mock, func() { _ = db.Close() }
}

// newPurgeJobBackend is newPurgeJob with an explicit local backend fingerprint on the cleaner, so the stale-upload
// ownership fence (recorded backend_id == local) passes and the deleted-sweep binds (retention, backendID).
func newPurgeJobBackend(t *testing.T, fake *fakeS3, backendID string) (*PurgeDeletedJob, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	cleaner := &artifacts.Cleaner{LocalCluster: "platform-eu", S3: fake, LocalBackendID: backendID}
	j := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:           db,
		Logger:       logging.NewLogger(),
		RetentionAge: 30 * 24 * time.Hour,
		Cleaner:      cleaner,
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

// expectMarkFailedDeleted matches markFailedArtifactsDeleted, which runs at the top of the
// bytes+rows sweep (after the nil-cleaner guard) to transition purgeable 'failed' rows to
// 'deleted' so the reconciler projects their catalog deletion before they are reaped.
func expectMarkFailedDeleted(mock sqlmock.Sqlmock) {
	mock.ExpectExec("status = 'failed'").
		WithArgs("720h0m0s").
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectThumbnailCleanup matches the thumbnail control-row deletes the purge runs (BEFORE the S3 prefix sweep and
// the artifact row) so a hard-deleted artifact never strands its thumbnail pointer/assignment rows. Both deletes
// run in ONE transaction (atomic; no half-deleted control state a racing publisher could observe).
func expectThumbnailCleanup(mock sqlmock.Sqlmock, tenantID, hash string) {
	// Route S3 deletion by the thumbnail's own destination cluster + recorded backend-local fact: read them first
	// (no rows → local sweep).
	mock.ExpectQuery("SELECT destination_cluster, COALESCE\\(backend_id").
		WithArgs(tenantID, hash).
		WillReturnRows(sqlmock.NewRows([]string{"destination_cluster", "backend_id", "bool_or"}))
	mock.ExpectBegin()
	// Before deleting the control rows, enqueue the deterministic object keys for every attempt (reconstruct SELECT;
	// no rows here → nothing enqueued) so a late-promoted object is still swept.
	mock.ExpectQuery("FROM foghorn.thumbnail_task_assignment a\\s+JOIN foghorn.thumbnail_task_object o").
		WithArgs(tenantID, hash).
		WillReturnRows(sqlmock.NewRows([]string{"attempt_id", "version", "file_name"}))
	mock.ExpectExec("DELETE FROM foghorn.thumbnail_active_pointer").
		WithArgs(tenantID, hash).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM foghorn.thumbnail_task_assignment").
		WithArgs(tenantID, hash).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
}

func TestPurge_ClipUsesFormatColumnFromRow(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}).
		AddRow("clip-1", "clip", "tenant-a", "stream-x", "webm", "", "", "", "", "", "", "", false, "backend-eu", "deleted")
	expectMarkFailedDeleted(mock)
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(rows)
	expectThumbnailCleanup(mock, "tenant-a", "clip-1")
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("clip-1", "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deleteCalls) != 2 || fake.deleteCalls[0] != "clips/tenant-a/stream-x/clip-1.webm" || fake.deleteCalls[1] != "clips/tenant-a/stream-x/clip-1.webm.dtsh" {
		t.Errorf("deleteCalls = %v", fake.deleteCalls)
	}
	if len(fake.deletePrefixCalls) != 1 || fake.deletePrefixCalls[0] != "thumbnails/clip-1/" {
		t.Errorf("thumbnail prefix must be swept: deletePrefixCalls = %v", fake.deletePrefixCalls)
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

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}).
		AddRow("dvr-1", "dvr", "tenant-a", "stream-x", "", "", "", "", "", "", "", "", false, "backend-eu", "deleted")
	expectMarkFailedDeleted(mock)
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(rows)
	expectThumbnailCleanup(mock, "tenant-a", "dvr-1")
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("dvr-1", "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	// The DVR prefix AND the thumbnail prefix are both swept (order: main bytes, then thumbnails).
	if len(fake.deletePrefixCalls) != 2 || fake.deletePrefixCalls[0] != "dvr/tenant-a/stream-x/dvr-1" || fake.deletePrefixCalls[1] != "thumbnails/dvr-1/" {
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

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}).
		AddRow("vod-1", "vod", "tenant-a", "", "", "", "", "vod/tenant-a/vod-1/movie.mp4", "", "", "", "", false, "backend-eu", "deleted")
	expectMarkFailedDeleted(mock)
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(rows)
	expectThumbnailCleanup(mock, "tenant-a", "vod-1")
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("vod-1", "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deleteCalls) != 2 || fake.deleteCalls[0] != "vod/tenant-a/vod-1/movie.mp4" || fake.deleteCalls[1] != "vod/tenant-a/vod-1/movie.mp4.dtsh" {
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

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}).
		AddRow("clip-1", "clip", "tenant-a", "stream-x", "mp4", "", "", "", "", "", "", "", false, "backend-eu", "deleted")
	expectMarkFailedDeleted(mock)
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(rows)
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

	rows := sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}).
		AddRow("vod-2", "vod", "tenant-a", "", "", "", "", "", "", "", "", "", false, "backend-eu", "deleted")
	expectMarkFailedDeleted(mock)
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(rows)
	expectThumbnailCleanup(mock, "tenant-a", "vod-2")
	mock.ExpectExec("DELETE FROM foghorn.artifacts").WithArgs("vod-2", "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deleteCalls) != 0 {
		t.Errorf("S3 must not be called when target is missing, got %v", fake.deleteCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_FailedRowMarkedDeletedBeforeReap(t *testing.T) {
	// A purgeable 'failed' row is NOT reaped directly — it is first transitioned to 'deleted' so the
	// reconciler projects a catalog deletion for it (no stranded phantom "Failed" library asset).
	// This pass only marks it; the deleted sweep returns no rows (the flipped row is not yet
	// catalog-acked), so no bytes/row are freed this cycle.
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)

	// The failed→deleted transition MUST run.
	expectMarkFailedDeleted(mock)
	// The deleted sweep (coverage-gated) returns nothing for the just-marked row.
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(
		sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}),
	)
	// NO ExpectExec for DELETE FROM foghorn.artifacts — nothing is reaped this cycle.

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.deleteCalls) != 0 || len(fake.deletePrefixCalls) != 0 {
		t.Errorf("no S3 cleanup expected before catalog coverage; got delete=%v prefix=%v", fake.deleteCalls, fake.deletePrefixCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v (failed row must be marked deleted, not reaped)", err)
	}
}

// TestPurge_CoverageGateInSelect asserts the deleted sweep's SELECT carries the catalog-coverage
// gate, so a row whose deletion the catalog hasn't yet acked is never returned for reaping.
func TestPurge_CoverageGateInSelect(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	expectStaleUploadingNoRows(mock)
	expectMarkFailedDeleted(mock)
	// The expectation matches only if the SELECT contains the coverage gate.
	mock.ExpectQuery(`(?s)catalog_synced_rev >= a.catalog_revision.*LIMIT`).
		WithArgs("720h0m0s", "backend-eu").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}))
	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v (coverage gate must be present in the deleted sweep)", err)
	}
}

// While cross-cluster deletion is disabled (the default), the deleted sweep carries the remote-exclusion
// clause bound to the local cluster, so remote-owned rows are never selected and cannot starve local reaping.
func TestPurge_ExcludesRemoteRowsWhenDeleteDisabled(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake) // LocalCluster="platform-eu", crossClusterDeleteEnabled=false
	defer closeDB()

	expectStaleUploadingNoRows(mock)
	expectMarkFailedDeleted(mock)
	// The sweep claims ONLY rows recorded on this cell's store (backend_id = $2), ordered oldest-first. A remote-store
	// row (different backend_id) is never selected, so it can't be reaped here nor starve local reaping.
	mock.ExpectQuery(`(?s)a.backend_id = \$2.*ORDER BY a.updated_at`).
		WithArgs("720h0m0s", "backend-eu").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}))
	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v (remote rows must be excluded while delete is disabled)", err)
	}
}

// A cell filters purge claims to its OWN store: backend_id = this store's fingerprint ($2). A foreign-backend row
// (another cell's store) is never claimed, and a NULL (unattributed) row is never claimed by cluster — legacy local
// rows are attributed once at boot, so a remaining NULL is retained. This prevents hard-deleting a row after a no-op
// DeleteObject against the wrong S3 (which would orphan the real bytes).
func TestPurge_BackendAffinityFiltersForeignRows(t *testing.T) {
	fake := &fakeS3{}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	j := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:           db,
		Logger:       logging.NewLogger(),
		RetentionAge: 30 * 24 * time.Hour,
		Cleaner:      &artifacts.Cleaner{LocalCluster: "platform-eu", LocalBackendID: "backend-eu", S3: fake},
	})

	expectStaleUploadingNoRows(mock)
	expectMarkFailedDeleted(mock)
	mock.ExpectQuery(`(?s)a.backend_id = \$2.*ORDER BY a.updated_at`).
		WithArgs("720h0m0s", "backend-eu").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}))
	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v (claims must be backend-affine: local store OR local-cluster legacy)", err)
	}
}

// With cross-cluster deletion enabled, all rows are eligible: the sweep binds only the retention interval (no
// $2 local-cluster filter).
func TestPurge_ReapsAllWhenDeleteEnabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	fake := &fakeS3{}
	j := NewPurgeDeletedJob(PurgeDeletedConfig{
		DB:                      db,
		Logger:                  logging.NewLogger(),
		RetentionAge:            30 * 24 * time.Hour,
		Cleaner:                 &artifacts.Cleaner{LocalCluster: "platform-eu", S3: fake},
		AllowCrossClusterDelete: true,
	})

	expectStaleUploadingNoRows(mock)
	expectMarkFailedDeleted(mock)
	// Only the retention interval is bound — no $2 filter.
	mock.ExpectQuery("FROM foghorn.artifacts a").
		WithArgs("720h0m0s").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}))
	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
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
	j, mock, closeDB := newPurgeJobBackend(t, fake, "backend-eu")
	defer closeDB()

	// The stale row is owned by this cell (recorded backend_id == the cleaner's local fingerprint), so the ownership
	// fence passes and the row is CLAIMED 'uploading' -> 'aborting' (tenant-scoped). The purge does NOT touch S3 — the
	// AbortingVodRecoveryJob owns the idempotent multipart teardown — so aborting before the guarded claim can't race a
	// concurrent CompleteVodUpload.
	staleRows := sqlmock.NewRows([]string{"artifact_hash", "tenant_id", "storage_cluster_id", "origin_cluster_id", "backend_id"}).
		AddRow("vod-stale", "tenant-a", "platform-eu", "", "backend-eu")
	mock.ExpectQuery("FROM foghorn.artifacts a").WillReturnRows(staleRows)
	mock.ExpectExec(`UPDATE foghorn\.artifacts SET status = 'aborting'.*status = 'uploading'`).
		WithArgs("vod-stale", "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))

	// No rows for the deleted sweep.
	expectMarkFailedDeleted(mock)
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(
		sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}),
	)

	expectStaleNodeRowsCleanup(mock)

	j.purge()

	if len(fake.abortCalls) != 0 {
		t.Fatalf("purge must NOT abort S3 directly (the aborting saga does); got %v", fake.abortCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectations: %v", err)
	}
}

func TestPurge_RemoteStaleUploadingSkipped(t *testing.T) {
	fake := &fakeS3{}
	j, mock, closeDB := newPurgeJob(t, fake)
	defer closeDB()

	staleRows := sqlmock.NewRows([]string{"artifact_hash", "tenant_id", "storage_cluster_id", "origin_cluster_id", "backend_id"}).
		AddRow("vod-remote", "tenant-a", "us-east", "us-east", "")
	mock.ExpectQuery("FROM foghorn.artifacts a").WillReturnRows(staleRows)
	// No abort, no claim — remote rows skip+log.

	expectMarkFailedDeleted(mock)
	mock.ExpectQuery("FROM foghorn.artifacts a").WithArgs("720h0m0s", "backend-eu").WillReturnRows(
		sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_internal_name", "format", "storage_cluster_id", "origin_cluster_id", "s3_key", "s3_url", "sync_object_key", "active_object_key", "active_dtsh_key", "durable_backend_local", "backend_id", "status"}),
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
