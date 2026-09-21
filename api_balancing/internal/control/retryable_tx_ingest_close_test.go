package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/lib/pq"
)

// A serialization failure while claiming the DVR stop replays the whole close
// transaction; the best-effort DVRStop is dispatched once, after the commit.
func TestFinalizeIngestSessionCloseReplaysSerializationFailureAndStopsOnce(t *testing.T) {
	mock := withMockDB(t)
	ensureRegistry(t)
	fakeStream := &fakeControlStream{}
	registry.mu.Lock()
	registry.conns["storage-1"] = &conn{stream: fakeStream}
	registry.mu.Unlock()

	expectEndAndClaim := func() *sqlmock.ExpectedQuery {
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`UPDATE foghorn.ingest_sessions\s+SET ended_at = NOW.*RETURNING id`).
			WithArgs(int64(9000), "tenant-a", "node-1", int64(1234), "live+s1").
			WillReturnRows(sqlmock.NewRows([]string{"id", "start_trigger_uuid", "ingest_cluster_id"}).AddRow("gen-1", "trigger-uuid-x", "demo-media"))
		return mock.ExpectQuery(`UPDATE foghorn.artifacts.*'"stop_pending"'.*ingest_generation = \$1::uuid.*RETURNING`).
			WithArgs("gen-1", "tenant-a")
	}
	expectEndAndClaim().WillReturnError(&pq.Error{Code: "40001", Message: "restart transaction"})
	mock.ExpectRollback()
	expectEndAndClaim().WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).AddRow("dvr-h", "storage-1"))
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
	stops := 0
	for _, m := range fakeStream.sent {
		if ds := m.GetDvrStopRequest(); ds != nil && ds.GetDvrHash() == "dvr-h" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("DVRStop sends = %d, want exactly 1 after the replayed commit", stops)
	}
}
