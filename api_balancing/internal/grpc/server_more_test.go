package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// buildVodAssetInfo maps a database row's sql.Null* columns onto a VodAssetInfo
// proto. The invariant under test: an invalid (NULL) column must leave the
// corresponding optional proto field nil — never a zero value — so clients can
// distinguish "unset" from "zero". A valid column sets the pointer.
func TestBuildVodAssetInfoNullFieldsStayUnset(t *testing.T) {
	createdAt := time.Unix(1700000000, 0).UTC()
	updatedAt := time.Unix(1700000100, 0).UTC()

	asset := buildVodAssetInfo(
		"id-1", "hash-1", "uploading", "central", "movie.mp4", "Title", "Desc",
		sql.NullInt64{}, sql.NullInt32{}, sql.NullString{}, sql.NullString{}, sql.NullString{},
		sql.NullInt32{}, sql.NullString{}, sql.NullString{}, sql.NullString{},
		createdAt, updatedAt, sql.NullTime{},
	)

	if asset.Id != "id-1" || asset.ArtifactHash != "hash-1" || asset.Filename != "movie.mp4" {
		t.Fatalf("scalar fields not copied through: %+v", asset)
	}
	if asset.Status != sharedpb.VodStatus_VOD_STATUS_UPLOADING {
		t.Fatalf("status = %v, want UPLOADING", asset.Status)
	}
	if asset.SizeBytes != nil || asset.DurationMs != nil || asset.Resolution != nil ||
		asset.VideoCodec != nil || asset.AudioCodec != nil || asset.BitrateKbps != nil ||
		asset.S3UploadId != nil || asset.S3Key != nil || asset.ErrorMessage != nil ||
		asset.ExpiresAt != nil {
		t.Fatalf("NULL columns leaked into proto as non-nil pointers: %+v", asset)
	}
	if asset.CreatedAt == nil || !asset.CreatedAt.AsTime().Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", asset.CreatedAt, createdAt)
	}
}

func TestBuildVodAssetInfoValidFieldsSetPointers(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	expires := time.Unix(1700009999, 0).UTC()

	asset := buildVodAssetInfo(
		"id-2", "hash-2", "ready", "edge", "clip.mp4", "T", "D",
		sql.NullInt64{Int64: 4096, Valid: true},
		sql.NullInt32{Int32: 12000, Valid: true},
		sql.NullString{String: "1920x1080", Valid: true},
		sql.NullString{String: "h264", Valid: true},
		sql.NullString{String: "aac", Valid: true},
		sql.NullInt32{Int32: 6000, Valid: true},
		sql.NullString{String: "upload-1", Valid: true},
		sql.NullString{String: "key/1", Valid: true},
		sql.NullString{String: "boom", Valid: true},
		now, now, sql.NullTime{Time: expires, Valid: true},
	)

	if asset.Status != sharedpb.VodStatus_VOD_STATUS_READY {
		t.Fatalf("status = %v, want READY", asset.Status)
	}
	if asset.SizeBytes == nil || *asset.SizeBytes != 4096 {
		t.Fatalf("size_bytes = %v, want 4096", asset.SizeBytes)
	}
	if asset.DurationMs == nil || *asset.DurationMs != 12000 {
		t.Fatalf("duration_ms = %v, want 12000", asset.DurationMs)
	}
	if asset.Resolution == nil || *asset.Resolution != "1920x1080" {
		t.Fatalf("resolution = %v", asset.Resolution)
	}
	if asset.VideoCodec == nil || *asset.VideoCodec != "h264" ||
		asset.AudioCodec == nil || *asset.AudioCodec != "aac" {
		t.Fatalf("codecs = %v / %v", asset.VideoCodec, asset.AudioCodec)
	}
	if asset.BitrateKbps == nil || *asset.BitrateKbps != 6000 {
		t.Fatalf("bitrate = %v", asset.BitrateKbps)
	}
	if asset.S3UploadId == nil || *asset.S3UploadId != "upload-1" ||
		asset.S3Key == nil || *asset.S3Key != "key/1" {
		t.Fatalf("s3 fields = %v / %v", asset.S3UploadId, asset.S3Key)
	}
	if asset.ErrorMessage == nil || *asset.ErrorMessage != "boom" {
		t.Fatalf("error_message = %v", asset.ErrorMessage)
	}
	if asset.ExpiresAt == nil || !asset.ExpiresAt.AsTime().Equal(expires) {
		t.Fatalf("expires_at = %v, want %v", asset.ExpiresAt, expires)
	}
}

func TestBuildVodAssetInfoStatusMapping(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	cases := []struct {
		statusStr string
		want      sharedpb.VodStatus
	}{
		{"uploading", sharedpb.VodStatus_VOD_STATUS_UPLOADING},
		{"processing", sharedpb.VodStatus_VOD_STATUS_PROCESSING},
		{"completed", sharedpb.VodStatus_VOD_STATUS_READY},
		{"complete", sharedpb.VodStatus_VOD_STATUS_READY},
		{"done", sharedpb.VodStatus_VOD_STATUS_READY},
		{"ready", sharedpb.VodStatus_VOD_STATUS_READY},
		{"synced", sharedpb.VodStatus_VOD_STATUS_READY},
		{"failed", sharedpb.VodStatus_VOD_STATUS_FAILED},
		{"deleted", sharedpb.VodStatus_VOD_STATUS_DELETED},
		{"nonsense", sharedpb.VodStatus_VOD_STATUS_UNSPECIFIED},
		{"", sharedpb.VodStatus_VOD_STATUS_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run(tc.statusStr, func(t *testing.T) {
			asset := buildVodAssetInfo(
				"id", "hash", tc.statusStr, "loc", "f", "t", "d",
				sql.NullInt64{}, sql.NullInt32{}, sql.NullString{}, sql.NullString{}, sql.NullString{},
				sql.NullInt32{}, sql.NullString{}, sql.NullString{}, sql.NullString{},
				now, now, sql.NullTime{},
			)
			if asset.Status != tc.want {
				t.Fatalf("status %q -> %v, want %v", tc.statusStr, asset.Status, tc.want)
			}
		})
	}
}

// vodAssetColumns mirrors the SELECT column order that scanVodAsset/scanVodAssetRow
// expect. If the production query changes, the scan offset would silently shift —
// this test pins the contract.
var vodAssetColumns = []string{
	"id", "artifact_hash", "status", "size_bytes",
	"storage_location", "s3_url", "error_message",
	"created_at", "updated_at", "expires_at",
	"filename", "title", "description",
	"duration_ms", "resolution", "video_codec", "audio_codec", "bitrate_kbps",
	"s3_upload_id", "s3_key",
}

func TestScanVodAssetWithNullOptionalColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Unix(1700000000, 0).UTC()
	mockRows := sqlmock.NewRows(vodAssetColumns).AddRow(
		"vod-1", "hash-1", "ready", nil,
		"central", "s3://b/k", nil,
		now, now, nil,
		"f.mp4", "Title", "Desc",
		nil, nil, nil, nil, nil,
		nil, nil,
	)
	mock.ExpectQuery(`SELECT`).WillReturnRows(mockRows)

	rows, err := db.QueryContext(context.Background(), "SELECT ...")
	if err != nil {
		t.Fatalf("db.Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected one row")
	}

	s := &FoghornGRPCServer{}
	asset, err := s.scanVodAsset(rows)
	if err != nil {
		t.Fatalf("scanVodAsset: %v", err)
	}
	if asset.Id != "vod-1" || asset.Status != sharedpb.VodStatus_VOD_STATUS_READY {
		t.Fatalf("unexpected asset: %+v", asset)
	}
	if asset.SizeBytes != nil || asset.DurationMs != nil || asset.ErrorMessage != nil {
		t.Fatalf("NULL columns leaked: %+v", asset)
	}
}

// latestPlayableChapterForDVR returns the playback_id of the newest playable
// chapter for a DVR artifact, or "" with no error when none exists.
func TestLatestPlayableChapterForDVR(t *testing.T) {
	t.Run("nil dispatch returns empty", func(t *testing.T) {
		s := &FoghornGRPCServer{}
		pid, err := s.latestPlayableChapterForDVR(context.Background(), nil)
		if err != nil || pid != "" {
			t.Fatalf("got (%q, %v), want (\"\", nil)", pid, err)
		}
	})

	t.Run("empty hash returns empty without querying", func(t *testing.T) {
		s := &FoghornGRPCServer{}
		pid, err := s.latestPlayableChapterForDVR(context.Background(), &control.DVRArtifactDispatch{})
		if err != nil || pid != "" {
			t.Fatalf("got (%q, %v), want (\"\", nil)", pid, err)
		}
	})

	t.Run("returns playback id", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM foghorn.dvr_chapters`).
			WithArgs("dvr-hash").
			WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("pb-123"))

		s := &FoghornGRPCServer{db: db, logger: logging.NewLogger()}
		pid, err := s.latestPlayableChapterForDVR(context.Background(), &control.DVRArtifactDispatch{DVRHash: "dvr-hash"})
		if err != nil || pid != "pb-123" {
			t.Fatalf("got (%q, %v), want (pb-123, nil)", pid, err)
		}
	})

	t.Run("no rows returns empty no error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM foghorn.dvr_chapters`).
			WithArgs("dvr-hash").
			WillReturnError(sql.ErrNoRows)

		s := &FoghornGRPCServer{db: db, logger: logging.NewLogger()}
		pid, err := s.latestPlayableChapterForDVR(context.Background(), &control.DVRArtifactDispatch{DVRHash: "dvr-hash"})
		if err != nil || pid != "" {
			t.Fatalf("got (%q, %v), want (\"\", nil)", pid, err)
		}
	})

	t.Run("query error propagates", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM foghorn.dvr_chapters`).
			WithArgs("dvr-hash").
			WillReturnError(errors.New("db down"))

		s := &FoghornGRPCServer{db: db, logger: logging.NewLogger()}
		_, err = s.latestPlayableChapterForDVR(context.Background(), &control.DVRArtifactDispatch{DVRHash: "dvr-hash"})
		if err == nil {
			t.Fatal("expected error to propagate")
		}
	})
}

// assertChapterTenant is a cross-tenant isolation gate for DVR chapter access.
func TestAssertChapterTenant(t *testing.T) {
	t.Run("empty claimed tenant is an internal caller, allowed", func(t *testing.T) {
		s := &FoghornGRPCServer{}
		if err := s.assertChapterTenant(context.Background(), "hash", ""); err != nil {
			t.Fatalf("empty tenant should be allowed, got %v", err)
		}
	})

	t.Run("missing artifact returns NotFound", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM foghorn.artifacts`).
			WithArgs("hash").
			WillReturnError(sql.ErrNoRows)

		s := &FoghornGRPCServer{db: db, logger: logging.NewLogger()}
		err = s.assertChapterTenant(context.Background(), "hash", "tenant-a")
		if status.Code(err) != codes.NotFound {
			t.Fatalf("got %v, want NotFound", err)
		}
	})

	t.Run("tenant mismatch returns PermissionDenied", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM foghorn.artifacts`).
			WithArgs("hash").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-b"))

		s := &FoghornGRPCServer{db: db, logger: logging.NewLogger()}
		err = s.assertChapterTenant(context.Background(), "hash", "tenant-a")
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("got %v, want PermissionDenied", err)
		}
	})

	t.Run("matching tenant is allowed", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM foghorn.artifacts`).
			WithArgs("hash").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-a"))

		s := &FoghornGRPCServer{db: db, logger: logging.NewLogger()}
		if err := s.assertChapterTenant(context.Background(), "hash", "tenant-a"); err != nil {
			t.Fatalf("matching tenant should be allowed, got %v", err)
		}
	})
}

// Pending DVR-stop signals are one-shot: registering then consuming returns
// true and clears the entry; a second consume returns false. With no Redis
// store wired, this exercises the in-memory map path.
func TestPendingDVRStopInMemoryRoundTrip(t *testing.T) {
	s := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	if s.consumePendingDVRStop("") {
		t.Fatal("empty name must never be a registered stop")
	}
	if s.consumePendingDVRStop("live+x") {
		t.Fatal("unregistered name must return false")
	}

	s.RegisterPendingDVRStop("") // no-op, must not panic or register
	if s.consumePendingDVRStop("") {
		t.Fatal("empty registration must remain a no-op")
	}

	s.RegisterPendingDVRStop("live+x")
	if !s.consumePendingDVRStop("live+x") {
		t.Fatal("registered stop must be consumed once")
	}
	if s.consumePendingDVRStop("live+x") {
		t.Fatal("stop must be one-shot: second consume returns false")
	}
}

func TestArtifactSessionName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"abc123", "vod+abc123"},
		{"  abc123  ", "vod+abc123"},
		{"vod+abc123", "vod+abc123"},
		{"live+x", "live+x"},
		{"pull+y", "pull+y"},
	}
	for _, tc := range cases {
		if got := artifactSessionName(tc.in); got != tc.want {
			t.Errorf("artifactSessionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
