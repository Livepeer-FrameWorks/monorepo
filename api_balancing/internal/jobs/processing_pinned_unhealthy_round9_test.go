package jobs

import (
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A connected node whose latest heartbeat reported a failed Mist poll still
// holds the DVR bytes; a queued pinned clip must wait, not fail terminally.
func TestRVPinnedSourceOnConnectedUnhealthyNodeWaits(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	touchInCluster(sm, "src", routeTestCluster)
	sm.TouchNode("src", false) // heartbeat now; one Mist metrics poll failed
	const tenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	mock.ExpectExec(deferUnroutablePattern).WillReturnResult(sqlmock.NewResult(0, 1))
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(t.Context(), &processingJob{JobID: "job-pinned", TenantID: tenant, OriginCluster: routeTestCluster, PreferredNode: sql.NullString{String: "src", Valid: true}, SourceParams: sql.NullString{String: `{"source_kind":"dvr_rolling"}`, Valid: true}})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pinned dvr_rolling clip on a connected node was not deferred (terminal fail path taken?): %v", err)
	}
}
