package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
)

func clipRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "clip_hash", "playback_id", "stream_id", "title", "description",
		"start_time", "duration", "clip_mode", "requested_params",
		"size_bytes", "retention_until", "retention_source", "created_at", "updated_at",
		"thumbnail_cluster", "has_thumbnails",
	})
}

func TestGetClips(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.GetClips(context.Background(), &sharedpb.GetClipsRequest{})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("happy_path_projects_and_converts_ms_to_seconds", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		mock.ExpectQuery("SELECT COUNT").
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		// has_thumbnails=true but clusterURLs resolver is nil → ThumbnailAssets
		// must be nil rather than panicking.
		mock.ExpectQuery("FROM commodore.clips").
			WithArgs("t1").
			WillReturnRows(clipRows().AddRow(
				"id1", "hash1", "pb1", "s1", "Title", "desc",
				int64(10000), int64(30000), "precise", nil,
				int64(2048), nil, "manual", now, now,
				"cluster-a", true))

		resp, err := s.GetClips(ctxAs("u1", "t1", "owner"), &sharedpb.GetClipsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetClips()) != 1 {
			t.Fatalf("clips = %d, want 1", len(resp.GetClips()))
		}
		c := resp.GetClips()[0]
		// ms → seconds conversion is a real contract for the API surface.
		if c.GetStartTime() != 10 || c.GetDuration() != 30 {
			t.Errorf("start/duration = %d/%d, want 10/30 (seconds)", c.GetStartTime(), c.GetDuration())
		}
		if c.GetStatus() != "registry" {
			t.Errorf("status = %q, want registry", c.GetStatus())
		}
		if c.GetThumbnailAssets() != nil {
			t.Error("expected nil ThumbnailAssets when clusterURLs resolver is unset")
		}
		if resp.GetPagination().GetTotalCount() != 1 || resp.GetPagination().GetHasNextPage() {
			t.Errorf("unexpected pagination: %+v", resp.GetPagination())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("stream_filter_binds_extra_arg", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		// Both the count and the page query must carry the stream filter arg.
		mock.ExpectQuery("SELECT COUNT").
			WithArgs("t1", "s1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery("FROM commodore.clips").
			WithArgs("t1", "s1").
			WillReturnRows(clipRows())

		streamID := "s1"
		resp, err := s.GetClips(ctxAs("u1", "t1", "owner"), &sharedpb.GetClipsRequest{StreamId: &streamID})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetClips()) != 0 {
			t.Errorf("clips = %d, want 0", len(resp.GetClips()))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestGetClip(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.GetClip(context.Background(), &sharedpb.GetClipRequest{ClipHash: "h"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("empty_clip_hash", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.GetClip(ctxAs("u1", "t1", "owner"), &sharedpb.GetClipRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.clips").
			WithArgs("t1", "h").
			WillReturnError(sql.ErrNoRows)
		_, err := s.GetClip(ctxAs("u1", "t1", "owner"), &sharedpb.GetClipRequest{ClipHash: "h"})
		wantCode(t, err, codes.NotFound)
	})

	t.Run("happy_path", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		mock.ExpectQuery("FROM commodore.clips").
			WithArgs("t1", "hash1").
			WillReturnRows(clipRows().AddRow(
				"id1", "hash1", "pb1", "s1", "Title", "desc",
				int64(5000), int64(60000), "precise", nil,
				int64(2048), nil, "manual", now, now,
				"cluster-a", false))
		resp, err := s.GetClip(ctxAs("u1", "t1", "owner"), &sharedpb.GetClipRequest{ClipHash: "hash1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetClipHash() != "hash1" || resp.GetStartTime() != 5 || resp.GetDuration() != 60 {
			t.Errorf("unexpected clip: %+v", resp)
		}
	})
}

func dvrRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "dvr_hash", "playback_id", "internal_name", "stream_id", "title",
		"size_bytes", "retention_until", "retention_source", "created_at", "updated_at",
		"thumbnail_cluster", "has_thumbnails", "active_ingest_cluster",
	})
}

func TestListDVRRequests(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListDVRRequests(context.Background(), &sharedpb.ListDVRRecordingsRequest{})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("happy_path_projects_recording", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		mock.ExpectQuery("SELECT COUNT").
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery("FROM commodore.dvr_recordings").
			WithArgs("t1").
			WillReturnRows(dvrRows().AddRow(
				"id1", "dvr1", "pb1", "live+stream1", "s1", "My Recording",
				int64(4096), nil, "policy", now, now,
				"cluster-a", true, "cluster-a"))

		resp, err := s.ListDVRRequests(ctxAs("u1", "t1", "owner"), &sharedpb.ListDVRRecordingsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetDvrRecordings()) != 1 {
			t.Fatalf("recordings = %d, want 1", len(resp.GetDvrRecordings()))
		}
		rec := resp.GetDvrRecordings()[0]
		if rec.GetDvrHash() != "dvr1" || rec.GetInternalName() != "live+stream1" || rec.GetTitle() != "My Recording" {
			t.Errorf("unexpected recording: %+v", rec)
		}
		if rec.GetThumbnailAssets() != nil {
			t.Error("expected nil ThumbnailAssets when clusterURLs resolver is unset")
		}
		if resp.GetPagination().GetTotalCount() != 1 {
			t.Errorf("total = %d, want 1", resp.GetPagination().GetTotalCount())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}
