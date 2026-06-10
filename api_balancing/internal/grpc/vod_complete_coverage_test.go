package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// completeVodS3Stub is a VOD S3 seam whose CompleteMultipartUpload actually
// records its inputs (the production fakeVodS3Client panics on it). Named with
// the -VodComplete suffix to avoid colliding with the other grpc test agent.
type completeVodS3Stub struct {
	completeErr   error
	completeKey   string
	completeUpID  string
	completeParts []storage.CompletedPart
	s3URL         string
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

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(1024), "user-1"))
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'failed'`).
		WillReturnResult(sqlmock.NewResult(1, 1))

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

	mock.ExpectQuery(`SELECT v\.artifact_hash, v\.s3_key, a\.size_bytes, a\.user_id`).
		WithArgs("up-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "s3_key", "size_bytes", "user_id"}).
			AddRow("hash-1", "vod/t1/hash-1/video.mp4", int64(2048), "user-1"))
	// Advance: status -> processing, storage_location -> s3.
	mock.ExpectExec(`UPDATE foghorn\.artifacts\s+SET status = 'processing'`).
		WithArgs("hash-1", "s3://bucket/vod/t1/hash-1/video.mp4").
		WillReturnResult(sqlmock.NewResult(1, 1))
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
