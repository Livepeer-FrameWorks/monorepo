package triggers

import (
	"database/sql"
	"database/sql/driver"
	"sync"
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
)

// installIngestSessionMintMock wires a mock control DB that satisfies the synchronous
// ingest-session mint handlePushRewrite now performs on every accepted push (fail-closed:
// with no DB the push is denied). Tests that exercise the accept path inject this rather
// than relying on a nil-DB shortcut. Expects the mint's advisory lock + FOR UPDATE lookup
// (no existing session) + insert + commit; args are matched loosely (any).
func installIngestSessionMintMock(t *testing.T) {
	t.Helper()
	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() { control.SetDB(prev); dbMock.Close() })

	// PUSH_REWRITE resolves the connection's existing session BEFORE choosing a
	// cluster to claim (an open session's cluster outranks the node's current
	// registration). No rows = a new connection.
	mock.ExpectQuery(`tenant_id = \$1::uuid AND node_id = \$2 AND start_trigger_uuid = \$3 AND ended_at IS NULL`).WillReturnError(sql.ErrNoRows)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).WillReturnError(sql.ErrNoRows)           // new trigger UUID
	mock.ExpectQuery(`stream_internal_name = \$2 AND ended_at IS NULL`).WillReturnError(sql.ErrNoRows) // no stream incumbent
	mock.ExpectQuery(`ingest_close_tombstones`).                                                       // no close-before-insert tombstone
														WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO foghorn.ingest_sessions`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("11111111-1111-1111-1111-111111111111"))
	mock.ExpectCommit()

	// After the mint, handlePushRewrite projects the source only if this session is STILL the active
	// one under the stream advisory lock. The session keeps the allocated revision so a retry projects
	// the same ordered transition, and projection confirmation makes the pending→active handoff durable.
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists", "source_revision", "projection_state"}).AddRow(true, nil, "pending"))
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(1)))
	mock.ExpectExec(`UPDATE foghorn\.ingest_sessions[\s\S]*SET source_revision`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// The confirmation transaction: pending→active plus the durable admission-effect obligation,
	// atomically (one obligation per generation; the worker owns the once-only effects).
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.ingest_sessions[\s\S]*SET projection_state = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.ingest_admission_effects`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
}

// installIngestSessionMintThenAbortMock stops before projection and expects the caller to retire
// the committed pending generation. It models a synchronous post-mint gate failure and therefore
// discriminates the cleanup contract from the normal projection path.
func installIngestSessionMintThenAbortMock(t *testing.T) {
	t.Helper()
	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() {
		control.SetDB(prev)
		if expectationsErr := mock.ExpectationsWereMet(); expectationsErr != nil {
			t.Errorf("pending-session cleanup SQL: %v", expectationsErr)
		}
		_ = dbMock.Close()
	})

	mock.ExpectQuery(`tenant_id = \$1::uuid AND node_id = \$2 AND start_trigger_uuid = \$3 AND ended_at IS NULL`).WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`stream_internal_name = \$2 AND ended_at IS NULL`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`ingest_close_tombstones`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO foghorn.ingest_sessions`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("11111111-1111-1111-1111-111111111111"))
	mock.ExpectCommit()

	// AbortPendingIngestSession: end the exact pending generation and durably order its no-op
	// offline projection so a stale local registry transition cannot later resurrect it.
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`UPDATE foghorn\.ingest_sessions[\s\S]*ended_reason\s+=\s+'projection_failed'`).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "start_trigger_uuid"}).AddRow("edge-node-1", "test-trigger-uuid"))
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(1)))
	mock.ExpectExec(`INSERT INTO foghorn\.ingest_offline_effects`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
}

// installIngestSessionEndedMock wires a mock control DB where the mint's ended-session idempotency
// lookup FINDS an already-closed session for this exact trigger — so CreateIngestSession returns
// IngestSessionAlreadyEnded and handlePushRewrite must DENY the push (a connector whose own
// PUSH_INPUT_CLOSE already won the race must never be re-admitted or run admission side effects).
func installIngestSessionEndedMock(t *testing.T) {
	t.Helper()
	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() { control.SetDB(prev); dbMock.Close() })

	// PUSH_REWRITE resolves the connection's existing session BEFORE choosing a
	// cluster to claim (an open session's cluster outranks the node's current
	// registration). No rows = a new connection.
	mock.ExpectQuery(`tenant_id = \$1::uuid AND node_id = \$2 AND start_trigger_uuid = \$3 AND ended_at IS NULL`).WillReturnError(sql.ErrNoRows)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	// The trigger-UUID lookup finds a row for this exact trigger that is already ENDED.
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "stream_internal_name", "ended", "connector_pid"}).AddRow("11111111-1111-1111-1111-111111111111", "stream-a", true, int64(4242)))
	mock.ExpectCommit()
}

// installIngestSessionResumedMock wires a mock control DB for a PUSH_REWRITE RETRY of an
// already-admitted, already-projected session (the blocking trigger's first accept response was
// lost). CreateIngestSession finds the open row for this exact trigger UUID and returns it as an
// idempotent Active; ProjectSourceIfCurrent's probe reports projection_state='active', so the
// projection resolves as RESUMED — the identical accept, with the once-only effects owned by the
// durable obligation the FIRST confirmation inserted (nothing enqueued or re-run on the retry).
func installIngestSessionResumedMock(t *testing.T, internalName string, connectorPID int64) {
	t.Helper()
	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() { control.SetDB(prev); dbMock.Close() })

	const sessionID = "22222222-2222-2222-2222-222222222222"

	// The pre-claim session lookup finds the open session and pins its cluster.
	mock.ExpectQuery(`tenant_id = \$1::uuid AND node_id = \$2 AND start_trigger_uuid = \$3 AND ended_at IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"ingest_cluster_id"}).AddRow("demo-media"))

	// CreateIngestSession resolves the same trigger UUID to the still-open row (idempotent retry);
	// the row's full identity (stream AND connector PID) must match the caller's.
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "stream_internal_name", "ended", "connector_pid"}).AddRow(sessionID, internalName, false, connectorPID))
	mock.ExpectCommit()

	// ProjectSourceIfCurrent: the session is current AND already confirmed → resumed.
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists", "source_revision", "projection_state"}).AddRow(true, int64(7), "active"))
	mock.ExpectCommit()
}

// byteArgRecorder captures a []byte SQL argument (e.g. the obligation's serialized Decklog
// trigger) so a test can assert on exactly what was persisted.
type byteArgRecorder struct {
	mu sync.Mutex
	v  []byte
}

func (r *byteArgRecorder) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	if !ok {
		return v == nil
	}
	r.mu.Lock()
	r.v = append([]byte(nil), b...)
	r.mu.Unlock()
	return true
}

func (r *byteArgRecorder) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.v
}

// installIngestSessionMintMockCaptureDecklog is installIngestSessionMintMock with the obligation
// INSERT's decklog_trigger argument captured, so the test can drive the worker leg with the exact
// persisted bytes.
func installIngestSessionMintMockCaptureDecklog(t *testing.T) *byteArgRecorder {
	t.Helper()
	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() { control.SetDB(prev); dbMock.Close() })

	mock.ExpectQuery(`tenant_id = \$1::uuid AND node_id = \$2 AND start_trigger_uuid = \$3 AND ended_at IS NULL`).WillReturnError(sql.ErrNoRows)

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`start_trigger_uuid = \$3\s+FOR UPDATE`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`stream_internal_name = \$2 AND ended_at IS NULL`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`ingest_close_tombstones`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO foghorn.ingest_sessions`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("11111111-1111-1111-1111-111111111111"))
	mock.ExpectCommit()

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists", "source_revision", "projection_state"}).AddRow(true, nil, "pending"))
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(1)))
	mock.ExpectExec(`UPDATE foghorn\.ingest_sessions[\s\S]*SET source_revision`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	recorder := &byteArgRecorder{}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE foghorn\.ingest_sessions[\s\S]*SET projection_state = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.ingest_admission_effects`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), recorder,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	return recorder
}
