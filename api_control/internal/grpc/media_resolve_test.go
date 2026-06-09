package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
)

// The Resolve*Hash/Resolve*ID handlers encode a deliberate contract: a missing
// artifact is NOT an error — it returns Found=false with a nil error so callers
// (analytics enrichment, playback auth) can distinguish "no such artifact" from
// "lookup failed". A genuine DB failure is a separate Internal error. These
// tests pin both halves.

func TestResolveClipHash(t *testing.T) {
	t.Run("empty_hash", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ResolveClipHash(context.Background(), &commodorepb.ResolveClipHashRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_returns_found_false_no_error", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.clips").
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)
		resp, err := s.ResolveClipHash(context.Background(), &commodorepb.ResolveClipHashRequest{ClipHash: "missing"})
		if err != nil {
			t.Fatalf("expected nil error for missing clip, got %v", err)
		}
		if resp.GetFound() {
			t.Error("expected Found=false")
		}
	})

	t.Run("db_error_is_internal", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.clips").
			WithArgs("h").
			WillReturnError(errors.New("connection reset"))
		_, err := s.ResolveClipHash(context.Background(), &commodorepb.ResolveClipHashRequest{ClipHash: "h"})
		wantCode(t, err, codes.Internal)
	})

	t.Run("happy_path_projects_fields", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.clips").
			WithArgs("clip-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"tenant_id", "user_id", "stream_id", "title", "description",
				"start_time", "duration", "clip_mode", "stream_internal_name",
				"playback_id", "internal_name", "origin_cluster_id",
			}).AddRow("t1", "u1", "s1", "My Clip", "desc",
				int64(10), int64(30), "precise", "live+stream1",
				"pb-clip", "clip_internal", "cluster-a"))

		resp, err := s.ResolveClipHash(context.Background(), &commodorepb.ResolveClipHashRequest{ClipHash: "clip-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() {
			t.Fatal("expected Found=true")
		}
		if resp.GetTenantId() != "t1" || resp.GetStreamId() != "s1" || resp.GetStreamInternalName() != "live+stream1" {
			t.Errorf("unexpected projection: %+v", resp)
		}
		if resp.GetPlaybackId() != "pb-clip" || resp.GetInternalName() != "clip_internal" {
			t.Errorf("artifact identifiers not projected: %+v", resp)
		}
		if resp.GetOriginClusterId() != "cluster-a" {
			t.Errorf("origin cluster = %q, want cluster-a", resp.GetOriginClusterId())
		}
	})
}

func TestResolveDVRHash(t *testing.T) {
	t.Run("empty_hash", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ResolveDVRHash(context.Background(), &commodorepb.ResolveDVRHashRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_returns_found_false_no_error", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.dvr_recordings").
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)
		resp, err := s.ResolveDVRHash(context.Background(), &commodorepb.ResolveDVRHashRequest{DvrHash: "missing"})
		if err != nil {
			t.Fatalf("expected nil error for missing DVR, got %v", err)
		}
		if resp.GetFound() {
			t.Error("expected Found=false")
		}
	})

	t.Run("happy_path_projects_fields", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.dvr_recordings").
			WithArgs("dvr-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"tenant_id", "user_id", "stream_id", "stream_internal_name",
				"playback_id", "internal_name", "origin_cluster_id",
			}).AddRow("t1", "u1", "s1", "live+stream1", "pb-dvr", "dvr_internal", "cluster-a"))

		resp, err := s.ResolveDVRHash(context.Background(), &commodorepb.ResolveDVRHashRequest{DvrHash: "dvr-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetTenantId() != "t1" || resp.GetStreamInternalName() != "live+stream1" {
			t.Errorf("unexpected projection: %+v", resp)
		}
		if resp.GetInternalName() != "dvr_internal" {
			t.Errorf("artifact internal name = %q, want dvr_internal", resp.GetInternalName())
		}
	})
}

func TestResolveVodID(t *testing.T) {
	t.Run("empty_id", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ResolveVodID(context.Background(), &commodorepb.ResolveVodIDRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found_returns_found_false_no_error", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.vod_assets").
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)
		resp, err := s.ResolveVodID(context.Background(), &commodorepb.ResolveVodIDRequest{VodId: "missing"})
		if err != nil {
			t.Fatalf("expected nil error for missing VOD, got %v", err)
		}
		if resp.GetFound() {
			t.Error("expected Found=false")
		}
	})

	t.Run("happy_path_projects_fields", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.vod_assets").
			WithArgs("vod-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"tenant_id", "user_id", "vod_hash", "playback_id", "internal_name",
			}).AddRow("t1", "u1", "vodhash", "pb-vod", "vod_internal"))

		resp, err := s.ResolveVodID(context.Background(), &commodorepb.ResolveVodIDRequest{VodId: "vod-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetVodHash() != "vodhash" || resp.GetInternalName() != "vod_internal" {
			t.Errorf("unexpected projection: %+v", resp)
		}
	})
}
