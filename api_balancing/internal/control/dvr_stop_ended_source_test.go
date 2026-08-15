package control

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func withMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() {
		db = prevDB
		mockDB.Close()
	})
	return mock
}

// DVR finalization is node-local (source-session-scoped) and tenant-scoped: on a
// source node's STREAM_END the active DVR bound to that node's session (matched by
// dvr_start_dispatch.source_node_id) is claimed to stop_pending+stopping in one
// guarded atomic UPDATE that ignores terminal rows, then a best-effort stop is
// dispatched by the descriptor's node_id — regardless of current stream ownership.
func TestStopDVRForEndedSource_ClaimsObligationAndSends(t *testing.T) {
	mock := withMockDB(t)
	ensureRegistry(t)
	fakeStream := &fakeControlStream{}
	registry.mu.Lock()
	registry.conns["storage-1"] = &conn{stream: fakeStream}
	registry.mu.Unlock()

	mock.ExpectQuery(`UPDATE foghorn.artifacts.*'"stop_pending"'.*status = 'stopping'.*source_node_id' = \$2.*tenant_id::text = \$3.*RETURNING`).
		WithArgs("live+s1", "node-A", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).AddRow("dvr-h", "storage-1"))

	if err := StopDVRForEndedSource("live+s1", "tenant-a", "node-A", logging.NewLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
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
		t.Fatalf("expected a DVRStop for dvr-h sent to storage-1, got %d messages", len(fakeStream.sent))
	}
}

// STREAM_END means the whole stream drained, so EVERY active recording on the ending node
// is stopped — not just the newest. Same-node sessions can overlap when a prior session's
// PUSH_INPUT_CLOSE was lost; a LIMIT 1 would strand the older writer forever.
func TestStopDVRForEndedSource_StopsAllOverlappingSessions(t *testing.T) {
	mock := withMockDB(t)
	ensureRegistry(t)
	fakeStream := &fakeControlStream{}
	registry.mu.Lock()
	registry.conns["storage-1"] = &conn{stream: fakeStream}
	registry.mu.Unlock()

	mock.ExpectQuery(`UPDATE foghorn.artifacts.*source_node_id' = \$2.*RETURNING`).
		WithArgs("live+s1", "node-A", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).
			AddRow("dvr-old", "storage-1").
			AddRow("dvr-new", "storage-1"))

	if err := StopDVRForEndedSource("live+s1", "tenant-a", "node-A", logging.NewLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	stopped := map[string]bool{}
	for _, m := range fakeStream.sent {
		if ds := m.GetDvrStopRequest(); ds != nil {
			stopped[ds.GetDvrHash()] = true
		}
	}
	if !stopped["dvr-old"] || !stopped["dvr-new"] {
		t.Fatalf("both overlapping recordings must be stopped, got %v", stopped)
	}
}

// No active DVR for this source session (never started, already terminal, or an
// early STREAM_END before the async insert): the guarded UPDATE matches 0 rows and
// the call is a no-op that sends nothing.
func TestStopDVRForEndedSource_NoActiveRowIsNoop(t *testing.T) {
	mock := withMockDB(t)
	ensureRegistry(t)
	fakeStream := &fakeControlStream{}
	registry.mu.Lock()
	registry.conns["storage-1"] = &conn{stream: fakeStream}
	registry.mu.Unlock()

	// No active rows: the multi-row claim returns an empty result set (not ErrNoRows).
	mock.ExpectQuery(`UPDATE foghorn.artifacts.*RETURNING`).
		WithArgs("live+s1", "node-A", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))

	if err := StopDVRForEndedSource("live+s1", "tenant-a", "node-A", logging.NewLogger()); err != nil {
		t.Fatalf("no active row must be a no-op, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	for _, m := range fakeStream.sent {
		if m.GetDvrStopRequest() != nil {
			t.Fatal("no stop must be sent when there is no active DVR")
		}
	}
}

// A claim failure (transient outage) is RETURNED so the durable STREAM_END gets a
// negative ack; a silent swallow would truncate the WAL and lose the stop.
func TestStopDVRForEndedSource_ClaimErrorIsReturned(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectQuery(`UPDATE foghorn.artifacts.*RETURNING`).
		WithArgs("live+s1", "node-A", "tenant-a").
		WillReturnError(errors.New("connection reset"))

	if err := StopDVRForEndedSource("live+s1", "tenant-a", "node-A", logging.NewLogger()); err == nil {
		t.Fatal("a claim failure must be returned, not swallowed")
	}
}

// Missing required scope fails CLOSED (returns an error), never a silent success
// that would positively ack the durable STREAM_END. A nil DB (unconfigured/test
// process, never the case at runtime) is a genuine no-op — the runtime
// "database unavailable" case is the claim query failing, covered above.
func TestStopDVRForEndedSource_FailsClosedOnMissingScope(t *testing.T) {
	mock := withMockDB(t)
	if err := StopDVRForEndedSource("live+s1", "", "node-A", logging.NewLogger()); err == nil {
		t.Fatal("missing tenant must return an error")
	}
	if err := StopDVRForEndedSource("live+s1", "tenant-a", "", logging.NewLogger()); err == nil {
		t.Fatal("missing source node must return an error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no DB call may run for a scope error: %v", err)
	}

	prevDB := db
	db = nil
	t.Cleanup(func() { db = prevDB })
	if err := StopDVRForEndedSource("live+s1", "tenant-a", "node-A", logging.NewLogger()); err != nil {
		t.Fatalf("a nil DB (unconfigured) must be a no-op, got %v", err)
	}
}
