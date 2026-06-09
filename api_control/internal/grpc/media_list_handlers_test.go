package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
)

var mediaTS = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// vodRows mirrors the 17-column projection shared by GetVodAsset and
// ListVodAssets.
func vodRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "vod_hash", "playback_id", "stream_id", "origin_type", "origin_id",
		"title", "description", "filename", "content_type",
		"size_bytes", "retention_until", "retention_source", "created_at", "updated_at",
		"storage_cluster", "has_thumbnails",
	})
}

// storageArtifactRows mirrors the 20-column UNION projection ListStorageArtifacts scans.
func storageArtifactRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"kind", "id", "artifact_hash", "playback_id", "stream_id", "stream_title", "title", "secondary_label",
		"size_bytes", "status", "storage_location", "is_frozen", "created_at", "updated_at", "expires_at",
		"retention_source", "origin_type", "origin_id", "storage_cluster_id", "has_thumbnails",
	})
}

func TestGetVodAsset(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.GetVodAsset(context.Background(), &sharedpb.GetVodAssetRequest{ArtifactHash: "h1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("WHERE vod_hash").
			WithArgs("h1", "t1").
			WillReturnError(sql.ErrNoRows)
		_, err := s.GetVodAsset(ctxAs("u1", "t1", "owner"), &sharedpb.GetVodAssetRequest{ArtifactHash: "h1"})
		wantCode(t, err, codes.NotFound)
	})

	// The query is tenant-scoped (WHERE ... AND tenant_id = $2); the test
	// asserts the bound tenant comes from ctx, not the request.
	t.Run("happy_maps_metadata", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("WHERE vod_hash").
			WithArgs("h1", "t1").
			WillReturnRows(vodRows().AddRow(
				"id-1", "h1", "pb-1", "", "", "",
				"My VOD", nil, "movie.mp4", "video/mp4",
				int64(4096), nil, "tenant_default", mediaTS, mediaTS,
				nil, false))
		asset, err := s.GetVodAsset(ctxAs("u1", "t1", "owner"), &sharedpb.GetVodAssetRequest{ArtifactHash: "h1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if asset.GetArtifactHash() != "h1" || asset.GetTitle() != "My VOD" || asset.GetFilename() != "movie.mp4" {
			t.Errorf("unexpected mapping: %+v", asset)
		}
		if asset.GetSizeBytes() != 4096 {
			t.Errorf("SizeBytes = %d, want 4096", asset.GetSizeBytes())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestListVodAssets(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListVodAssets(context.Background(), &sharedpb.ListVodAssetsRequest{})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("happy_lists_library", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT COUNT").
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(2)))
		mock.ExpectQuery("SELECT id, vod_hash").
			WithArgs("t1").
			WillReturnRows(vodRows().
				AddRow("id-1", "h1", "pb1", "", "", "", "A", nil, "a.mp4", "video/mp4", int64(1), nil, "", mediaTS, mediaTS, nil, false).
				AddRow("id-2", "h2", "pb2", "", "", "", "B", nil, "b.mp4", "video/mp4", int64(2), nil, "", mediaTS, mediaTS, nil, false))

		resp, err := s.ListVodAssets(ctxAs("u1", "t1", "owner"), &sharedpb.ListVodAssetsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetAssets()) != 2 {
			t.Fatalf("got %d assets, want 2", len(resp.GetAssets()))
		}
		if resp.GetPagination().GetTotalCount() != 2 {
			t.Errorf("TotalCount = %d, want 2", resp.GetPagination().GetTotalCount())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestListStorageArtifacts(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStorageArtifacts(context.Background(), &commodorepb.ListStorageArtifactsRequest{})
		wantCode(t, err, codes.Unauthenticated)
	})

	// A caller may pass tenant_id, but it must equal the ctx tenant or be
	// rejected — a client cannot list another tenant's storage.
	t.Run("tenant_mismatch_denied", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"),
			&commodorepb.ListStorageArtifactsRequest{TenantId: "other-tenant"})
		wantCode(t, err, codes.PermissionDenied)
	})

	t.Run("happy_lists_union_of_kinds", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("COUNT").
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(2)))
		// default limit 25 → fetches limit+1 (26) with offset 0
		mock.ExpectQuery("ORDER BY").
			WithArgs("t1", 26, 0).
			WillReturnRows(storageArtifactRows().
				AddRow("vod", "id-1", "h1", "pb1", "", "", "Movie", "movie.mp4", int64(10), "registry", nil, nil, mediaTS, mediaTS, nil, "", "", "", "", false).
				AddRow("clip", "id-2", "h2", "pb2", "", "", "Clip", "highlight", int64(5), "registry", nil, nil, mediaTS, mediaTS, nil, "", "", "", "", false))

		resp, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"), &commodorepb.ListStorageArtifactsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetArtifacts()) != 2 {
			t.Fatalf("got %d artifacts, want 2", len(resp.GetArtifacts()))
		}
		if resp.GetArtifacts()[0].GetKind() != "vod" || resp.GetArtifacts()[1].GetKind() != "clip" {
			t.Errorf("unexpected kinds: %+v", resp.GetArtifacts())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// SQL-injection guard: a sort field outside the whitelist must collapse to
	// created_at, never reach the ORDER BY verbatim. The expectation matches
	// only if the ORDER BY uses created_at — a leaked field would fail it.
	t.Run("sort_field_injection_falls_back_to_created_at", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("COUNT").
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(0)))
		mock.ExpectQuery("ORDER BY created_at").
			WithArgs("t1", 26, 0).
			WillReturnRows(storageArtifactRows())

		_, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"),
			&commodorepb.ListStorageArtifactsRequest{SortField: "title; DROP TABLE commodore.clips"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}
