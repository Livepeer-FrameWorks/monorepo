package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/lib/pq"
)

// A serialization failure while ending the session replays the whole close transaction;
// the replayed commit ends the session once and sends no DVRStop.
func TestFinalizeIngestSessionCloseReplaysSerializationFailure(t *testing.T) {
	mock := withMockDB(t)
	ensureRegistry(t)
	const streamID = "22222222-2222-4222-8222-222222222222"
	fakeStream := &fakeControlStream{}
	registry.mu.Lock()
	registry.conns["storage-1"] = &conn{stream: fakeStream}
	registry.mu.Unlock()

	expectEnd := func() *sqlmock.ExpectedQuery {
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		return mock.ExpectQuery(`UPDATE foghorn.ingest_sessions\s+SET ended_at = NOW.*RETURNING id`).
			WithArgs(int64(9000), mockTenantUUID, "node-1", int64(1234), "live+s1")
	}
	expectEnd().WillReturnError(&pq.Error{Code: "40001", Message: "restart transaction"})
	mock.ExpectRollback()
	expectEnd().WillReturnRows(sqlmock.NewRows([]string{"id", "start_trigger_uuid", "ingest_cluster_id", "stream_id"}).AddRow("gen-1", "trigger-uuid-x", "demo-media", streamID))
	expectDomainEventInsert(mock, "stream.idle", streamID)
	mock.ExpectQuery(`INSERT INTO foghorn.source_projection_revision_counter`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(2)))
	mock.ExpectExec(`INSERT INTO foghorn.ingest_offline_effects`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := FinalizeIngestSessionClose(context.Background(), mockTenantUUID, "node-1", 1234, 9000, "live+s1", logging.NewLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.EndedSessionID != "gen-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	for _, m := range fakeStream.sent {
		if ds := m.GetDvrStopRequest(); ds != nil {
			t.Fatalf("the close sent DVRStop for %s", ds.GetDvrHash())
		}
	}
}
