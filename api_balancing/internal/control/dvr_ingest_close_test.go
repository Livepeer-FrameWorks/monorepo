package control

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A PUSH_INPUT_CLOSE atomically ends the active session (event-time fenced) AND claims
// the stop obligation for its bound DVR in ONE transaction, then reports the DVR to stop.
func TestFinalizeIngestSessionClose_EndsAndClaimsStop(t *testing.T) {
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "start_trigger_uuid", "ingest_cluster_id"}).AddRow("gen-1", "trigger-uuid-x", "demo-media"))
	mock.ExpectQuery(`UPDATE foghorn.artifacts.*'"stop_pending"'.*ingest_generation = \$1::uuid.*RETURNING`).
		WithArgs("gen-1", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).AddRow("dvr-h", "storage-1"))
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(2)))
	mock.ExpectExec(`INSERT INTO foghorn.ingest_offline_effects`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := FinalizeIngestSessionClose(context.Background(), "tenant-a", "node-1", 1234, 9000, "live+s1", logging.NewLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.EndedSessionID != "gen-1" || res.DVRHash != "dvr-h" || res.StorageNodeID != "storage-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	stopped := false
	for _, m := range fakeStream.sent {
		if ds := m.GetDvrStopRequest(); ds != nil && ds.GetDvrHash() == "dvr-h" {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("expected a best-effort DVRStop for dvr-h, got %d messages", len(fakeStream.sent))
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
	if res.EndedSessionID != "" || res.DVRHash != "" {
		t.Fatalf("a fenced close must return an empty result, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A session that ended but has no active bound DVR commits the session end and returns
// its id with no DVR to stop.
func TestFinalizeIngestSessionClose_EndedNoDVR(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE foghorn.ingest_sessions.*RETURNING id`).
		WithArgs(int64(9000), "tenant-a", "node-1", int64(1234), "live+s1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "start_trigger_uuid", "ingest_cluster_id"}).AddRow("gen-1", "trigger-uuid-x", "demo-media"))
	// No active bound DVR: the multi-row claim returns an empty set (not ErrNoRows).
	mock.ExpectQuery(`UPDATE foghorn.artifacts.*RETURNING`).
		WithArgs("gen-1", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(2)))
	mock.ExpectExec(`INSERT INTO foghorn.ingest_offline_effects`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := FinalizeIngestSessionClose(context.Background(), "tenant-a", "node-1", 1234, 9000, "live+s1", logging.NewLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.EndedSessionID != "gen-1" || res.DVRHash != "" || res.StorageNodeID != "" {
		t.Fatalf("expected session ended with no DVR, got %+v", res)
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
