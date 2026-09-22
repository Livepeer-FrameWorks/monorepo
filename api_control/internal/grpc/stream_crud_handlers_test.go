package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestStreamFromConfigRowManagedSource(t *testing.T) {
	s := &CommodoreServer{}
	stream, err := s.streamFromConfigRow(commodoredb.StreamConfigRow{
		ID: "s1", InternalName: "loop", StreamKey: "unused-secret", PlaybackID: "pb1",
		Title: "Platform loop", IngestMode: "mist_native",
		CreatedAt:                sql.NullTime{Time: fixedTS, Valid: true},
		UpdatedAt:                sql.NullTime{Time: fixedTS, Valid: true},
		ManagedSourceKind:        sql.NullString{String: "playlist", Valid: true},
		ManagedAlwaysOn:          true,
		ManagedPlacementCount:    sql.NullInt32{Int32: 1, Valid: true},
		ManagedAllowedClusterIDs: []string{"media-eu"},
	})
	if err != nil {
		t.Fatalf("streamFromConfigRow: %v", err)
	}
	if stream.GetIngestMode() != "mist_native" || stream.GetPullSource() != nil {
		t.Fatalf("unexpected source mode mapping: %+v", stream)
	}
	managed := stream.GetManagedSource()
	if managed == nil || managed.GetSourceKind() != "playlist" || !managed.GetAlwaysOn() {
		t.Fatalf("managed source = %+v", managed)
	}
	if managed.GetPlacementCount() != 1 || len(managed.GetAllowedClusterIds()) != 1 || managed.GetAllowedClusterIds()[0] != "media-eu" {
		t.Fatalf("managed placement summary = %+v", managed)
	}
}

var fixedTS = time.Unix(1700000000, 0).UTC()

// streamListCols mirrors the stream projection used by list and point reads.
// ListStreams (push stream → pull-source columns NULL).
func pushListRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "internal_name", "stream_key", "playback_id", "title", "description",
		"is_recording_enabled", "created_at", "updated_at", "ingest_mode",
		"source_uri_enc", "enabled", "pull_allowed_cluster_ids",
		"managed_source_kind", "managed_always_on", "managed_placement_count",
		"managed_allowed_cluster_ids", "active_ingest_cluster_id",
		"dvr_chapter_mode", "dvr_chapter_interval_seconds",
		"dvr_retention_days_override", "clip_retention_days_override", "monitoring_enabled",
	})
}

// pushFullRow mirrors the projection queryStream reads after an UpdateStream commit.
func pushFullRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "internal_name", "stream_key", "playback_id", "title", "description",
		"is_recording_enabled", "created_at", "updated_at", "ingest_mode",
		"source_uri_enc", "enabled", "pull_allowed_cluster_ids",
		"managed_source_kind", "managed_always_on", "managed_placement_count",
		"managed_allowed_cluster_ids", "active_ingest_cluster_id",
		"dvr_chapter_mode", "dvr_chapter_interval_seconds",
		"dvr_retention_days_override", "clip_retention_days_override", "monitoring_enabled",
	})
}

// expectStreamPlacementRead mocks the batched own-policy read that attaches
// source locations to stream reads. rows nil means no stream has own rules.
func expectStreamPlacementRead(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	if rows == nil {
		rows = sqlmock.NewRows([]string{"stream_id", "revision", "policy_payload"})
	}
	mock.ExpectQuery("ListStreamMediaPlacementPolicies").WillReturnRows(rows)
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
		_, err := s.CreateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.CreateStreamRequest{
			Title: "x", IngestMode: "carrier-pigeon",
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("pull_without_source_uri_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.CreateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.CreateStreamRequest{
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
			WithArgs(testTenantID, "u1", "My Stream").
			WillReturnRows(sqlmock.NewRows([]string{"stream_id", "stream_key", "playback_id", "internal_name"}).
				AddRow("s1", "key-1", "pb-1", "live+abc"))
		expectDualEventInsert(mock, "stream.created", eventStreamCreated)
		mock.ExpectCommit()

		resp, err := s.CreateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.CreateStreamRequest{Title: "My Stream"})
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
			WithArgs(testTenantID, "u1", "Untitled Stream").
			WillReturnRows(sqlmock.NewRows([]string{"stream_id", "stream_key", "playback_id", "internal_name"}).
				AddRow("s1", "key-1", "pb-1", "live+abc"))
		expectDualEventInsert(mock, "stream.created", eventStreamCreated)
		mock.ExpectCommit()

		if _, err := s.CreateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.CreateStreamRequest{}); err != nil {
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
		_, err := s.UpdateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.UpdateStreamRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", testTenantID).
			WillReturnError(sql.ErrNoRows)
		_, err := s.UpdateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.UpdateStreamRequest{
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
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		_, err := s.UpdateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.UpdateStreamRequest{
			StreamId: "s1", IngestMode: proto.String("pull"),
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("title_update_happy_path", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE commodore.streams SET").
			WithArgs(
				true, "New Title", false, nil, false, false,
				false, nil, false, nil, false, nil,
				"s1", "u1", testTenantID,
			).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDualEventInsert(mock, "stream.updated", eventStreamUpdated)
		mock.ExpectCommit()
		// Trailing queryStream re-read.
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(pushFullRow().AddRow(
				"s1", "live+abc", "key-1", "pb-1", "New Title", nil,
				false, fixedTS, fixedTS, "push",
				nil, nil, "{}", nil, false, nil, "{}", nil,
				nil, nil, nil, nil, nil))
		expectStreamPlacementRead(mock, nil)

		stream, err := s.UpdateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.UpdateStreamRequest{
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

	// A serialization abort replays the update in a fresh transaction, and the change event is emitted once, after
	// the commit, naming the fields of the committed attempt.
	t.Run("title_update_replays_serialization_failure", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		updateArgs := []driver.Value{
			true, "New Title", false, nil, false, false,
			false, nil, false, nil, false, nil,
			"s1", "u1", testTenantID,
		}
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE commodore.streams SET").WithArgs(updateArgs...).
			WillReturnError(serializationFailure())
		mock.ExpectRollback()
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE commodore.streams SET").WithArgs(updateArgs...).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDualEventInsert(mock, "stream.updated", eventStreamUpdated)
		mock.ExpectCommit()
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(pushFullRow().AddRow(
				"s1", "live+abc", "key-1", "pb-1", "New Title", nil,
				false, fixedTS, fixedTS, "push",
				nil, nil, "{}", nil, false, nil, "{}", nil,
				nil, nil, nil, nil, nil))
		expectStreamPlacementRead(mock, nil)

		stream, err := s.UpdateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.UpdateStreamRequest{
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
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(pushFullRow().AddRow(
				"s1", "live+abc", "key-1", "pb-1", "Title", nil,
				false, fixedTS, fixedTS, "push",
				nil, nil, "{}", nil, false, nil, "{}", nil,
				nil, nil, nil, nil, nil))
		expectStreamPlacementRead(mock, nil)

		if _, err := s.UpdateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.UpdateStreamRequest{StreamId: "s1"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	t.Run("monitoring_update_writes_nullable_toggle", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("ingest_mode, is_recording_enabled").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "ingest_mode", "is_recording_enabled"}).
				AddRow("live+abc", "push", false))
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE commodore.streams SET").
			WithArgs(
				false, "", false, nil, false, false,
				false, nil, false, nil, true, false,
				"s1", "u1", testTenantID,
			).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDualEventInsert(mock, "stream.updated", eventStreamUpdated)
		mock.ExpectCommit()
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(pushFullRow().AddRow(
				"s1", "live+abc", "key-1", "pb-1", "Title", nil,
				false, fixedTS, fixedTS, "push",
				nil, nil, "{}", nil, false, nil, "{}", nil,
				nil, nil, nil, nil, false))
		expectStreamPlacementRead(mock, nil)

		monitoring := commodorepb.MonitoringToggle_MONITORING_TOGGLE_OFF
		stream, err := s.UpdateStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.UpdateStreamRequest{
			StreamId:   "s1",
			Monitoring: &monitoring,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stream.GetMonitoring() != commodorepb.MonitoringToggle_MONITORING_TOGGLE_OFF {
			t.Fatalf("Monitoring=%v want OFF", stream.GetMonitoring())
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
			WithArgs("s1", "u1", testTenantID).
			WillReturnError(sql.ErrNoRows)
		_, err := s.DeleteStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.DeleteStreamRequest{StreamId: "s1"})
		wantCode(t, err, codes.NotFound)
	})

	// Two-phase delete: phase 1 SOFT-deletes the stream (deleted_at) + drops keys + enqueues the outbox, in one tx.
	// nil quartermaster skips both the clip-cleanup branch AND the phase-2 Foghorn delivery, so the deletion is not
	// finalized here — the RPC returns deletion_pending and the stream row is NOT hard-deleted.
	t.Run("soft_deletes_and_returns_pending", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT internal_name, title FROM commodore.streams").
			WithArgs("s1", "u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"internal_name", "title"}).AddRow("live+abc", "My Stream"))
		mock.ExpectBegin()
		mock.ExpectExec("DELETE FROM commodore.stream_keys").
			WithArgs("s1", testTenantID).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE commodore.streams SET deleted_at").
			WithArgs("s1", testTenantID).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO commodore.media_placement_policies").
			WithArgs(testTenantID, "s1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("INSERT INTO commodore.stream_cleanup_outbox").
			WithArgs("s1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"stream_id"}).AddRow("s1"))
		mock.ExpectCommit()
		// nil QM ⇒ resolveFoghornForTenant errors ⇒ no finalize, no hard-delete, and NO terminal stream_deleted
		// event (it is emitted only on actual finalization, never at soft-delete time).

		resp, err := s.DeleteStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.DeleteStreamRequest{StreamId: "s1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetStreamId() != "s1" || resp.GetStreamTitle() != "My Stream" {
			t.Errorf("resp = (%s,%s), want (s1, My Stream)", resp.GetStreamId(), resp.GetStreamTitle())
		}
		if resp.GetDeletionStatus() != "deletion_pending" {
			t.Errorf("deletion_status = %q, want deletion_pending (Foghorn not reachable, not finalized)", resp.GetDeletionStatus())
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
			WithArgs("u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(2)))
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("u1", testTenantID, false, "", int32(51)).
			WillReturnRows(pushListRow().
				AddRow("s1", "live+a", "k1", "pb1", "First", nil, false, fixedTS, fixedTS, "push", nil, nil, "{}", nil, false, nil, "{}", nil, nil, nil, nil, nil, nil).
				AddRow("s2", "live+b", "k2", "pb2", "Second", "desc", true, fixedTS, fixedTS, "push", nil, nil, "{}", nil, false, nil, "{}", nil, nil, nil, nil, nil, nil))
		restricted, err := proto.Marshal(&placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
			Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"edge-a"}, NodeIds: []string{"node-1"}}}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		expectStreamPlacementRead(mock, sqlmock.NewRows([]string{"stream_id", "revision", "policy_payload"}).AddRow("s1", int64(1), restricted))

		resp, err := s.ListStreams(ctxAs("u1", testTenantID, "owner"), &commodorepb.ListStreamsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetStreams()) != 2 {
			t.Fatalf("got %d streams, want 2", len(resp.GetStreams()))
		}
		if resp.GetStreams()[0].GetStreamId() != "s1" || resp.GetStreams()[1].GetTitle() != "Second" {
			t.Errorf("unexpected mapping: %+v", resp.GetStreams())
		}
		first, second := resp.GetStreams()[0].GetSourceLocation(), resp.GetStreams()[1].GetSourceLocation()
		if first.GetMode() != commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED || len(first.GetClusters()) != 1 ||
			first.GetClusters()[0].GetClusterId() != "edge-a" || len(first.GetClusters()[0].GetNodeIds()) != 1 {
			t.Errorf("stored source location read back as %+v", first)
		}
		if second.GetMode() != commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_ANY {
			t.Errorf("stream without own rules reported %+v", second)
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
			WithArgs("u1", testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(0)))
		mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
			WithArgs("u1", testTenantID, false, "", int32(51)).
			WillReturnRows(pushListRow())

		resp, err := s.ListStreams(ctxAs("u1", testTenantID, "owner"), &commodorepb.ListStreamsRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetStreams()) != 0 {
			t.Errorf("got %d streams, want 0", len(resp.GetStreams()))
		}
	})
}

func TestListStreamMonitoringMapsNullableToggle(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery("SELECT id::text AS stream_id, internal_name, monitoring_enabled").
		WithArgs(testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "internal_name", "monitoring_enabled"}).
			AddRow("s1", "live+a", nil).
			AddRow("s2", "live+b", true).
			AddRow("s3", "live+c", false))

	resp, err := s.ListStreamMonitoring(serviceCtx(), &commodorepb.ListStreamMonitoringRequest{TenantId: testTenantID})
	if err != nil {
		t.Fatalf("ListStreamMonitoring: %v", err)
	}
	got := resp.GetStreams()
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	want := []commodorepb.MonitoringToggle{
		commodorepb.MonitoringToggle_MONITORING_TOGGLE_INHERIT,
		commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON,
		commodorepb.MonitoringToggle_MONITORING_TOGGLE_OFF,
	}
	for i := range want {
		if got[i].GetMonitoringToggle() != want[i] {
			t.Fatalf("row %d toggle=%v want %v", i, got[i].GetMonitoringToggle(), want[i])
		}
	}
	if got[0].GetInternalName() != "live+a" {
		t.Fatalf("InternalName=%q want live+a", got[0].GetInternalName())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}
