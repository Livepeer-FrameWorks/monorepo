package control

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A first PUSH_REWRITE for a connection mints a fresh ingest session: new trigger UUID, no active
// incumbent for the stream, plain insert. The advisory lock is now STREAM-scoped.
func TestCreateIngestSession_MintsNew(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	// 1. Trigger-UUID lookup — new UUID.
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).
		WithArgs("tenant-a", "node-1", "uuid-1").
		WillReturnError(sql.ErrNoRows)
	// 2. Stream-incumbent lookup — no active publisher for this stream.
	mock.ExpectQuery(`stream_internal_name = \$2 AND ended_at IS NULL\s+FOR UPDATE`).
		WithArgs("tenant-a", "live+s1").
		WillReturnError(sql.ErrNoRows)
	// 2b. Close-before-insert tombstone — none.
	mock.ExpectQuery(`ingest_close_tombstones`).
		WithArgs("tenant-a", "node-1", int64(1234), "live+s1", int64(1000)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	// 3. Mint.
	mock.ExpectQuery(`INSERT INTO foghorn.ingest_sessions`).
		WithArgs("tenant-a", "node-1", "live+s1", int64(1234), "uuid-1", int64(1000), nil, "demo-media").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sess-1"))
	mock.ExpectCommit()

	id, outcome, err := CreateIngestSession(context.Background(), "tenant-a", "node-1", "live+s1", 1234, "uuid-1", 1000, nil, "demo-media", logging.NewLogger())
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("id=%q outcome=%v err=%v", id, outcome, err)
	}
	if id != "sess-1" {
		t.Fatalf("id = %q, want sess-1", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A duplicate PUSH_REWRITE for the SAME connection (same trigger UUID, still open) returns the
// existing session id and inserts nothing (idempotent).
func TestCreateIngestSession_DuplicateIsIdempotent(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).
		WithArgs("tenant-a", "node-1", "uuid-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "stream_internal_name", "ended", "connector_pid"}).AddRow("sess-1", "live+s1", false, int64(1234)))
	mock.ExpectCommit()

	id, outcome, err := CreateIngestSession(context.Background(), "tenant-a", "node-1", "live+s1", 1234, "uuid-1", 2000, nil, "demo-media", logging.NewLogger())
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("id=%q outcome=%v err=%v", id, outcome, err)
	}
	if id != "sess-1" {
		t.Fatalf("id = %q, want the existing sess-1", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A different active publisher already holds the stream → the DB stream authority REJECTS the push
// (duplicate), no insert.
func TestCreateIngestSession_RejectsDuplicatePublisher(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).
		WithArgs("tenant-a", "node-2", "uuid-2").
		WillReturnError(sql.ErrNoRows)
	// Incumbent is a DIFFERENT node holding the stream → reject.
	mock.ExpectQuery(`stream_internal_name = \$2 AND ended_at IS NULL\s+FOR UPDATE`).
		WithArgs("tenant-a", "live+s1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "node_id", "connector_pid", "started_at_unix_millis"}).AddRow("sess-incumbent", "node-1", int64(999), int64(1000)))
	mock.ExpectCommit()

	id, outcome, err := CreateIngestSession(context.Background(), "tenant-a", "node-2", "live+s1", 1234, "uuid-2", 5000, nil, "demo-media", logging.NewLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != IngestSessionRejectedDuplicate {
		t.Fatalf("a different active publisher must be rejected as duplicate, got outcome=%v id=%q", outcome, id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A PID the OS reused on the SAME node for a NEWER connector (new UUID, later start) supersedes the
// stale still-active incumbent: end it, claim its orphaned DVR stop, mint fresh.
func TestCreateIngestSession_PidReuseEndsStaleAndMintsFresh(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).
		WithArgs("tenant-a", "node-1", "uuid-new").
		WillReturnError(sql.ErrNoRows)
	// Incumbent is the SAME (node, PID) with an OLDER start → PID-reuse supersede.
	mock.ExpectQuery(`stream_internal_name = \$2 AND ended_at IS NULL\s+FOR UPDATE`).
		WithArgs("tenant-a", "live+s1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "node_id", "connector_pid", "started_at_unix_millis"}).AddRow("sess-old", "node-1", int64(1234), int64(1000)))
	mock.ExpectExec(`UPDATE foghorn.ingest_sessions\s+SET ended_at = NOW.*'superseded_pid_reuse'`).
		WithArgs(int64(5000), "sess-old").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`UPDATE foghorn.artifacts.*ingest_generation = \$1::uuid.*RETURNING`).
		WithArgs("sess-old", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))
	// 2b. Close-before-insert tombstone — none for the reused connector.
	mock.ExpectQuery(`ingest_close_tombstones`).
		WithArgs("tenant-a", "node-1", int64(1234), "live+s1", int64(5000)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO foghorn.ingest_sessions`).
		WithArgs("tenant-a", "node-1", "live+s1", int64(1234), "uuid-new", int64(5000), nil, "demo-media").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sess-new"))
	mock.ExpectCommit()

	id, outcome, err := CreateIngestSession(context.Background(), "tenant-a", "node-1", "live+s1", 1234, "uuid-new", 5000, nil, "demo-media", logging.NewLogger())
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("id=%q outcome=%v err=%v", id, outcome, err)
	}
	if id != "sess-new" {
		t.Fatalf("id = %q, want the fresh sess-new", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Missing required identity fails closed with no DB call.
func TestCreateIngestSession_MissingIdentityErrors(t *testing.T) {
	_ = withMockDB(t)
	if _, _, err := CreateIngestSession(context.Background(), "", "node-1", "live+s1", 1234, "u", 1, nil, "demo-media", logging.NewLogger()); err == nil {
		t.Fatal("missing tenant must error")
	}
	if _, _, err := CreateIngestSession(context.Background(), "tenant-a", "node-1", "live+s1", 0, "u", 1, nil, "demo-media", logging.NewLogger()); err == nil {
		t.Fatal("missing PID must error")
	}
}
