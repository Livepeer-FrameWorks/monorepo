package grpc

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
)

var mediaTS = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// storageArtifactRows mirrors the 26-column UNION projection ListStorageArtifacts scans.
func storageArtifactRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"kind", "id", "artifact_hash", "playback_id", "stream_id", "stream_title", "title", "secondary_label",
		"size_bytes", "status", "storage_location", "created_at", "updated_at", "expires_at",
		"retention_source", "origin_type", "origin_id", "storage_cluster_id", "has_thumbnails", "duration_ms", "tracks",
		"sync_status", "is_synced", "is_finalized", "description", "error_message", "thumbnail_serving_cluster",
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
		mock.ExpectQuery("GROUP BY kind").
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}).AddRow("vod", int32(1)).AddRow("clip", int32(1)))
		// default limit 25 → fetches limit+1 (26) with offset 0
		mock.ExpectQuery("ORDER BY").
			WithArgs("t1", 26, 0).
			WillReturnRows(storageArtifactRows().
				AddRow("vod", "id-1", "h1", "pb1", "", "", "Movie", "movie.mp4", int64(10), "processing", nil, mediaTS, mediaTS, nil, "", "", "", "", false, nil, nil, nil, nil, nil, "Launch recording", "transcode failed", "").
				AddRow("clip", "id-2", "h2", "pb2", "", "", "Clip", "highlight", int64(5), "ready", "s3", mediaTS, mediaTS, nil, "", "", "", "", false, int64(60000), `[{"type":"video","codec":"h264","resolution":"1920x1080"}]`, "synced", true, true, "", "", "cluster-a"))

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
		// description + error_message project onto the catalog row.
		if resp.GetArtifacts()[0].GetDescription() != "Launch recording" || resp.GetArtifacts()[0].GetErrorMessage() != "transcode failed" {
			t.Errorf("description/error_message not projected: %+v", resp.GetArtifacts()[0])
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// Batch exact-hash lookup (Top Assets enrichment): a single query filters
	// artifact_hash = ANY($2), replacing the former per-asset RPC fan-out.
	t.Run("batch_hashes_uses_any_filter", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		hashes := []string{"h1", "h2"}
		mock.ExpectQuery(`(?s)COUNT.*artifact_hash = ANY`).
			WithArgs("t1", pq.Array(hashes)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(2)))
		mock.ExpectQuery(`(?s)GROUP BY kind`).
			WithArgs("t1", pq.Array(hashes)).
			WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}).AddRow("vod", int32(2)))
		// Limit=2 → fetches limit+1 (3); batch filter arg precedes the limit/offset args.
		mock.ExpectQuery(`(?s)artifact_hash = ANY.*ORDER BY`).
			WithArgs("t1", pq.Array(hashes), 3, 0).
			WillReturnRows(storageArtifactRows().
				AddRow("vod", "id-1", "h1", "pb1", "", "", "Movie", "movie.mp4", int64(10), "ready", "s3", mediaTS, mediaTS, nil, "", "", "", "", false, nil, nil, "synced", true, false, "", "", "").
				AddRow("vod", "id-2", "h2", "pb2", "", "", "Movie2", "movie2.mp4", int64(10), "ready", "s3", mediaTS, mediaTS, nil, "", "", "", "", false, nil, nil, "synced", true, false, "", "", ""))

		resp, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"),
			&commodorepb.ListStorageArtifactsRequest{ArtifactHashes: hashes, Limit: 2})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetArtifacts()) != 2 {
			t.Fatalf("got %d artifacts, want 2", len(resp.GetArtifacts()))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A batch request that cleans to nothing (all blank/duplicate) must match NOTHING — the
	// WHERE carries a FALSE guard, never falling through to an unfiltered tenant scan.
	t.Run("batch_hashes_all_blank_matches_nothing", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery(`(?s)COUNT.*FALSE`).
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(0)))
		mock.ExpectQuery(`(?s)GROUP BY kind`).
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}))
		mock.ExpectQuery(`(?s)FALSE.*ORDER BY`).
			WithArgs("t1", 26, 0).
			WillReturnRows(storageArtifactRows())

		resp, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"),
			&commodorepb.ListStorageArtifactsRequest{ArtifactHashes: []string{" ", "", "   "}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetArtifacts()) != 0 {
			t.Fatalf("blank batch must return nothing, got %d", len(resp.GetArtifacts()))
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
		mock.ExpectQuery("GROUP BY kind").
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}))
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

	// Deleted catalog rows are physically removed (the tombstone marker is the sole deletion record),
	// so the library list — which no longer carries any in-row deletion filter — never returns them:
	// an absent row is simply not in the UNION. The tenant-scoped list queries still run cleanly.
	t.Run("excludes_deleted_rows", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM`).
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(0)))
		mock.ExpectQuery(`(?s)GROUP BY kind`).
			WithArgs("t1").
			WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}))
		mock.ExpectQuery(`(?s)FROM commodore.vod_assets v.*ORDER BY`).
			WithArgs("t1", 26, 0).
			WillReturnRows(storageArtifactRows())

		_, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"), &commodorepb.ListStorageArtifactsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// An unknown kind must be rejected outright — otherwise a request whose kinds ALL normalize
	// away would fall through to an unfiltered scan and leak every kind.
	t.Run("unknown_kind_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"),
			&commodorepb.ListStorageArtifactsRequest{Kinds: []string{"bogus"}})
		wantCode(t, err, codes.InvalidArgument)
	})

	// A batch of 101–500 exact hashes must return them ALL — the exact-hash path overrides the
	// generic page clamp (100) so enrichment never receives a silently-truncated page.
	t.Run("batch_over_page_clamp_returns_all", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		hashes := make([]string, 0, 150)
		for i := 0; i < 150; i++ {
			hashes = append(hashes, "h"+strconv.Itoa(i))
		}
		mock.ExpectQuery(`(?s)COUNT.*artifact_hash = ANY`).
			WithArgs("t1", pq.Array(hashes)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(150)))
		mock.ExpectQuery(`(?s)GROUP BY kind`).
			WithArgs("t1", pq.Array(hashes)).
			WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}).AddRow("vod", int32(150)))
		// limit overridden to len(batch)=150 → fetches limit+1 (151), NOT the clamped 101.
		mock.ExpectQuery(`(?s)artifact_hash = ANY.*ORDER BY`).
			WithArgs("t1", pq.Array(hashes), 151, 0).
			WillReturnRows(storageArtifactRows())

		_, err := s.ListStorageArtifacts(ctxAs("u1", "t1", "owner"),
			&commodorepb.ListStorageArtifactsRequest{ArtifactHashes: hashes})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}
