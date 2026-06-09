package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

var fixedTS = time.Unix(1700000000, 0).UTC()

// streamListCols mirrors the 16-column projection scanStream reads in
// ListStreams (push stream → pull-source columns NULL).
func pushListRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "internal_name", "stream_key", "playback_id", "title", "description",
		"is_recording_enabled", "created_at", "updated_at", "ingest_mode",
		"source_uri_enc", "enabled", "allowed_cluster_ids", "active_ingest_cluster_id",
		"dvr_retention_days_override", "clip_retention_days_override",
	})
}

// pushFullRow mirrors the 18-column projection queryStream reads after an
// UpdateStream commit.
func pushFullRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "internal_name", "stream_key", "playback_id", "title", "description",
		"is_recording_enabled", "created_at", "updated_at", "ingest_mode",
		"source_uri_enc", "enabled", "allowed_cluster_ids", "active_ingest_cluster_id",
		"dvr_chapter_mode", "dvr_chapter_interval_seconds",
		"dvr_retention_days_override", "clip_retention_days_override",
	})
}

func TestCreateStream(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.CreateStream(context.Background(), &commodorepb.CreateStreamRequest{Title: "x"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("unsupported_ingest_mode_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.CreateStream(ctxAs("u1", "t1", "owner"), &commodorepb.CreateStreamRequest{
			Title: "x", IngestMode: "carrier-pigeon",
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("pull_without_source_uri_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.CreateStream(ctxAs("u1", "t1", "owner"), &commodorepb.CreateStreamRequest{
			Title: "x", IngestMode: "pull",
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	// Push happy path: nil purserClient makes isTenantSuspended fail-open, so
	// the handler proceeds through create_user_stream → commit → outbox emit.
	t.Run("push_happy_path_creates_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("create_user_stream").
			WithArgs("t1", "u1", "My Stream").
			WillReturnRows(sqlmock.NewRows([]string{"stream_id", "stream_key", "playback_id", "internal_name"}).
				AddRow("s1", "key-1", "pb-1", "live+abc"))
		mock.ExpectCommit()
		expectOutboxInsert(mock)

		resp, err := s.CreateStream(ctxAs("u1", "t1", "owner"), &commodorepb.CreateStreamRequest{Title: "My Stream"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetId() != "s1" || resp.GetStreamKey() != "key-1" || resp.GetPlaybackId() != "pb-1" {
			t.Errorf("response ids = (%s,%s,%s), want (s1,key-1,pb-1)", resp.GetId(), resp.GetStreamKey(), resp.GetPlaybackId())
		}
		if resp.GetIngestMode() != "push" {
			t.Errorf("IngestMode = %q, want push", resp.GetIngestMode())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// Title defaulting: an empty title must become "Untitled Stream" before it
	// reaches create_user_stream.
	t.Run("empty_title_defaults", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("create_user_stream").
			WithArgs("t1", "u1", "Untitled Stream").
			WillReturnRows(sqlmock.NewRows([]string{"stream_id", "stream_key", "playback_id", "internal_name"}).
				AddRow("s1", "key-1", "pb-1", "live+abc"))
		mock.ExpectCommit()
		expectOutboxInsert(mock)

		if _, err := s.CreateStream(ctxAs("u1", "t1", "owner"), &commodorepb.CreateStreamRequest{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestUpdateStream(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.UpdateStream(context.Background(), &commodorepb.UpdateStreamRequest{StreamId: "s1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("missing_stream_id", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.UpdateStream(ctxAs("u1", "t1", "owner"), &commodorepb.UpdateStreamRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", "t1").
			WillReturnError(sql.ErrNoRows)
		_, err := s.UpdateStream(ctxAs("u1", "t1", "owner"), &commodorepb.UpdateStreamRequest{
			StreamId: "s1", Name: proto.String("new"),
		})
		wantCode(t, err, codes.NotFound)
	})

	// ingest_mode is immutable post-creation: a change attempt must be rejected
	// before any write, regardless of the stored mode.
	t.Run("ingest_mode_change_rejected", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		_, err := s.UpdateStream(ctxAs("u1", "t1", "owner"), &commodorepb.UpdateStreamRequest{
			StreamId: "s1", IngestMode: proto.String("pull"),
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("title_update_happy_path", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE commodore.streams SET").
			WithArgs("New Title", "s1", "u1", "t1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		expectOutboxInsert(mock)
		// Trailing queryStream re-read.
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(pushFullRow().AddRow(
				"s1", "live+abc", "key-1", "pb-1", "New Title", nil,
				false, fixedTS, fixedTS, "push",
				nil, nil, "{}", nil,
				nil, nil, nil, nil))

		stream, err := s.UpdateStream(ctxAs("u1", "t1", "owner"), &commodorepb.UpdateStreamRequest{
			StreamId: "s1", Name: proto.String("New Title"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stream.GetTitle() != "New Title" {
			t.Errorf("Title = %q, want New Title", stream.GetTitle())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A no-op update (no fields, no pull source) short-circuits to a plain
	// re-read with no transaction/outbox.
	t.Run("noop_update_reads_through", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(pushFullRow().AddRow(
				"s1", "live+abc", "key-1", "pb-1", "Title", nil,
				false, fixedTS, fixedTS, "push",
				nil, nil, "{}", nil,
				nil, nil, nil, nil))

		if _, err := s.UpdateStream(ctxAs("u1", "t1", "owner"), &commodorepb.UpdateStreamRequest{StreamId: "s1"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestDeleteStream(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.DeleteStream(context.Background(), &commodorepb.DeleteStreamRequest{StreamId: "s1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT internal_name, title FROM commodore.streams").
			WithArgs("s1", "u1", "t1").
			WillReturnError(sql.ErrNoRows)
		_, err := s.DeleteStream(ctxAs("u1", "t1", "owner"), &commodorepb.DeleteStreamRequest{StreamId: "s1"})
		wantCode(t, err, codes.NotFound)
	})

	// Happy delete: nil quartermaster skips the foghorn clip-cleanup branch, so
	// the path is the keys+stream delete tx followed by the outbox emit.
	t.Run("happy_deletes_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT internal_name, title FROM commodore.streams").
			WithArgs("s1", "u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "title"}).AddRow("live+abc", "My Stream"))
		mock.ExpectBegin()
		mock.ExpectExec("DELETE FROM commodore.stream_keys").
			WithArgs("s1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("DELETE FROM commodore.streams").
			WithArgs("s1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		expectOutboxInsert(mock)

		resp, err := s.DeleteStream(ctxAs("u1", "t1", "owner"), &commodorepb.DeleteStreamRequest{StreamId: "s1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetStreamId() != "s1" || resp.GetStreamTitle() != "My Stream" {
			t.Errorf("resp = (%s,%s), want (s1, My Stream)", resp.GetStreamId(), resp.GetStreamTitle())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestListStreams(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListStreams(context.Background(), &commodorepb.ListStreamsRequest{})
		wantCode(t, err, codes.Unauthenticated)
	})

	// Happy path: count + page query, tenant-scoped, mapping two push rows
	// through scanStream. quartermaster nil → no origin-region enrichment.
	t.Run("happy_lists_tenant_streams", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("COUNT").
			WithArgs("u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(2)))
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("u1", "t1").
			WillReturnRows(pushListRow().
				AddRow("s1", "live+a", "k1", "pb1", "First", nil, false, fixedTS, fixedTS, "push", nil, nil, "{}", nil, nil, nil).
				AddRow("s2", "live+b", "k2", "pb2", "Second", "desc", true, fixedTS, fixedTS, "push", nil, nil, "{}", nil, nil, nil))

		resp, err := s.ListStreams(ctxAs("u1", "t1", "owner"), &commodorepb.ListStreamsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetStreams()) != 2 {
			t.Fatalf("got %d streams, want 2", len(resp.GetStreams()))
		}
		if resp.GetStreams()[0].GetStreamId() != "s1" || resp.GetStreams()[1].GetTitle() != "Second" {
			t.Errorf("unexpected mapping: %+v", resp.GetStreams())
		}
		if resp.GetPagination().GetTotalCount() != 2 {
			t.Errorf("TotalCount = %d, want 2", resp.GetPagination().GetTotalCount())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	t.Run("empty_result", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("COUNT").
			WithArgs("u1", "t1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(0)))
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("u1", "t1").
			WillReturnRows(pushListRow())

		resp, err := s.ListStreams(ctxAs("u1", "t1", "owner"), &commodorepb.ListStreamsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetStreams()) != 0 {
			t.Errorf("got %d streams, want 0", len(resp.GetStreams()))
		}
	})
}
