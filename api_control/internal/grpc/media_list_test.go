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
