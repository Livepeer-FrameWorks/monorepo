package control

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A PUSH_INPUT_CLOSE ends the active session (event-time fenced) and queues its offline
// effects, but leaves the session's recording running: Mist keeps the buffer for its resume
// window, so a reconnect continues the recording and Mist's buffer unload ends it otherwise.
// No artifacts row is claimed and no DVRStop is sent.
func TestFinalizeIngestSessionClose_EndsSessionAndLeavesRecordingRunning(t *testing.T) {
	mock := withMockDB(t)
	ensureRegistry(t)
	fakeStream := &fakeControlStream{}
	registry.mu.Lock()
	registry.conns["storage-1"] = &conn{stream: fakeStream}
	registry.mu.Unlock()

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE foghorn.ingest_sessions\s+SET ended_at = NOW.*'push_input_close'.*started_at_unix_millis <= \$1.*RETURNING id`).
		WithArgs(int64(9000), "tenant-a", "node-1", int64(1234), "live+s1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "start_trigger_uuid", "ingest_cluster_id", "stream_id"}).AddRow("gen-1", "trigger-uuid-x", "demo-media", ""))
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(2)))
	mock.ExpectExec(`INSERT INTO foghorn.ingest_offline_effects`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := FinalizeIngestSessionClose(context.Background(), "tenant-a", "node-1", 1234, 9000, "live+s1", logging.NewLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.EndedSessionID != "gen-1" || res.ClaimToken != "trigger-uuid-x" || res.ClusterID != "demo-media" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (the close must not claim a DVR stop): %v", err)
	}
	for _, m := range fakeStream.sent {
		if ds := m.GetDvrStopRequest(); ds != nil {
			t.Fatalf("the close sent DVRStop for %s; the recording must keep running", ds.GetDvrHash())
		}
	}
}

// A fenced/already-ended close ends nothing, claims nothing (no artifacts query), and
// returns an empty result — idempotent.
func TestFinalizeIngestSessionClose_FencedReturnsEmpty(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE foghorn.ingest_sessions.*RETURNING id`).
		WithArgs(int64(500), "tenant-a", "node-1", int64(1234), "live+s1").
		WillReturnError(sql.ErrNoRows)
	// No active session ended → record a close-before-insert tombstone so a late rewrite is denied.
	mock.ExpectExec(`INSERT INTO foghorn.ingest_close_tombstones`).
		WithArgs("tenant-a", "node-1", int64(1234), "live+s1", int64(500)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := FinalizeIngestSessionClose(context.Background(), "tenant-a", "node-1", 1234, 500, "live+s1", logging.NewLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.EndedSessionID != "" || res.ClaimToken != "" {
		t.Fatalf("a fenced close must return an empty result, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Missing identity fails closed with no DB call.
func TestFinalizeIngestSessionClose_MissingIdentityErrors(t *testing.T) {
	_ = withMockDB(t)
	if _, err := FinalizeIngestSessionClose(context.Background(), "", "node-1", 1234, 1, "live+s1", logging.NewLogger()); err == nil {
		t.Fatal("missing tenant must error")
	}
	if _, err := FinalizeIngestSessionClose(context.Background(), "tenant-a", "node-1", 0, 1, "live+s1", logging.NewLogger()); err == nil {
		t.Fatal("missing PID must error")
	}
}

func TestFinalizeIngestSessionClose_NoDatabaseErrors(t *testing.T) {
	previous := GetDB()
	SetDB(nil)
	t.Cleanup(func() { SetDB(previous) })

	if _, err := FinalizeIngestSessionClose(context.Background(), "tenant-a", "node-1", 1234, 1, "live+s1", logging.NewLogger()); err == nil {
		t.Fatal("unconfigured database must fail the durable close")
	}
}
