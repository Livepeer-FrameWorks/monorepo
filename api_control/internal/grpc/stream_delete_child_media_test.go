package grpc

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// expectStreamDeletePhaseOne queues the phase-1 transaction of DeleteStream: drop keys, soft-delete, retain the
// placement policy and enqueue the durable stream-cleanup obligation.
func expectStreamDeletePhaseOne(mock sqlmock.Sqlmock, streamID string) {
	mock.ExpectQuery("SELECT internal_name, title FROM commodore.streams").
		WithArgs(streamID, "u1", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"internal_name", "title"}).AddRow("live+abc", "My Stream"))
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM commodore.stream_keys").
		WithArgs(streamID, testTenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE commodore.streams SET deleted_at").
		WithArgs(streamID, testTenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO commodore.media_placement_policies").
		WithArgs(testTenantID, streamID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO commodore.stream_cleanup_outbox").
		WithArgs(streamID, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"stream_id"}).AddRow(streamID))
	mock.ExpectCommit()
}

// Staging rc16: deleting a stream that owned 22 clips ran every child delete inside the API call (one Foghorn
// DeleteClip after another, each with its own S3 deletes), so the call outlived the client's 10 s timeout even
// though the deletion was durably accepted in phase 1. The call must return promptly with deletion_pending and
// leave the child cascade to the durable stream-cleanup worker.
func TestDeleteStream_ManyChildClipsReturnsPromptlyAsPending(t *testing.T) {
	const streamID = "s1"
	const clipCount = 22
	const perChildDelete = 300 * time.Millisecond

	s, mock, done := newMockServer(t)
	defer done()
	mock.MatchExpectationsInOrder(false)

	expectStreamDeletePhaseOne(mock, streamID)
	mock.ExpectQuery("SELECT COALESCE\\(thumbnail_serving_cluster_ids").
		WithArgs(streamID, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"thumbnail_serving_cluster_ids"}).AddRow("{cluster-eu}"))
	clips := sqlmock.NewRows([]string{"clip_hash", "origin_cluster_id"})
	for i := 0; i < clipCount; i++ {
		clips.AddRow("clip-"+string(rune('a'+i)), "cluster-eu")
	}
	mock.ExpectQuery("FROM commodore.clips WHERE stream_id").WithArgs(streamID, testTenantID).WillReturnRows(clips)
	mock.ExpectQuery("FROM commodore.dvr_recordings WHERE stream_id").WithArgs(streamID, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"dvr_hash", "origin_cluster_id"}))
	mock.ExpectExec("UPDATE commodore.stream_cleanup_outbox").WillReturnResult(sqlmock.NewResult(0, 1))

	var kicks atomic.Int32
	s.streamCleanupKickFn = func() { kicks.Add(1) }
	s.streamThumbnailDeleteFn = func(context.Context, string, string, string) error { return nil }
	var childDeletes atomic.Int32
	s.childArtifactDeleteFn = func(ctx context.Context, _, _, _, _ string) error {
		childDeletes.Add(1)
		select {
		case <-time.After(perChildDelete):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	start := time.Now()
	resp, err := s.DeleteStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.DeleteStreamRequest{StreamId: streamID})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DeleteStream: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("DeleteStream took %s for a stream with %d clips (%d child deletes ran inside the call); it must return promptly and leave the cascade to the cleanup worker",
			elapsed, clipCount, childDeletes.Load())
	}
	if got := resp.GetDeletionStatus(); got != "deletion_pending" {
		t.Fatalf("deletion_status = %q, want deletion_pending while child media remains", got)
	}
	if kicks.Load() != 1 {
		t.Fatalf("stream cleanup worker kicked %d times, want 1 so the cascade starts without waiting for the poll", kicks.Load())
	}
}

// A stream without child media still finalizes inside the call once every owning cell acked the thumbnail tombstone.
func TestDeleteStream_NoChildMediaFinalizesInline(t *testing.T) {
	const streamID = "s1"
	s, mock, done := newMockServer(t)
	defer done()

	expectStreamDeletePhaseOne(mock, streamID)
	mock.ExpectQuery("SELECT COALESCE\\(thumbnail_serving_cluster_ids").
		WithArgs(streamID, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"thumbnail_serving_cluster_ids"}).AddRow("{cluster-eu}"))
	mock.ExpectQuery("FROM commodore.clips WHERE stream_id").WithArgs(streamID, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"clip_hash", "origin_cluster_id"}))
	mock.ExpectQuery("FROM commodore.dvr_recordings WHERE stream_id").WithArgs(streamID, testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"dvr_hash", "origin_cluster_id"}))
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE commodore.stream_cleanup_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(testTenantID))
	mock.ExpectQuery("SELECT COALESCE\\(user_id").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO commodore.media_placement_policies").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM commodore.streams").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	s.streamThumbnailDeleteFn = func(context.Context, string, string, string) error { return nil }
	s.streamCleanupKickFn = func() { t.Error("a stream without child media must not need the cleanup worker") }

	resp, err := s.DeleteStream(ctxAs("u1", testTenantID, "owner"), &commodorepb.DeleteStreamRequest{StreamId: streamID})
	if err != nil {
		t.Fatalf("DeleteStream: %v", err)
	}
	if got := resp.GetDeletionStatus(); got != "deleted" {
		t.Fatalf("deletion_status = %q, want deleted", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
