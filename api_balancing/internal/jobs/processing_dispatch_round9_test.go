package jobs

import (
	"context"
	"database/sql"
	"frameworks/api_balancing/internal/state"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestR9FairnessRefreshFailureDoesNotStopDispatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("UPDATE foghorn.processing_node_reservations").WillReturnError(sql.ErrConnDone)
	mock.ExpectQuery("WITH claimed AS").WillReturnRows(sqlmock.NewRows([]string{"job_id"}))
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.fairnessEnabled = true
	d.dispatch()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestR9DispatchLoadsInventoryOncePerCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	oldNodes, oldAccess := registeredProcessingNodes, clusterAccessibleForTenant
	calls := map[string]int{}
	registeredProcessingNodes = func(_ context.Context, id string) ([]string, error) { calls[id]++; return nil, nil }
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { registeredProcessingNodes = oldNodes; clusterAccessibleForTenant = oldAccess })
	rows := sqlmock.NewRows([]string{"job_id", "tenant_id", "artifact_hash", "artifact_type", "job_type", "input_codec", "output_profiles", "status", "retry_count", "s3_url", "source_url", "source_params", "preferred_node_id", "processes_json", "internal_name", "stream_id", "stream_internal_name", "durable_backend_local", "origin_cluster_id", "required_memory_bytes", "memory_floor_bytes", "wait_reason", "wait_started_at", "source_probe", "created_at", "upload_created_at"})
	for i, cluster := range []string{"a", "a", "b"} {
		id := []string{"job-a1", "job-a2", "job-b1"}[i]
		rows.AddRow(id, "tenant", nil, "", "process", nil, nil, "dispatched", 0, nil, nil, nil, nil, nil, nil, "", nil, false, cluster, 0, 0, nil, nil, nil, nil, nil)
	}
	mock.ExpectQuery("WITH claimed AS").WillReturnRows(rows)
	for _, id := range []string{"job-a1", "job-a2", "job-b1"} {
		mock.ExpectExec(deferUnroutablePattern).WithArgs(unroutableDeferralSeconds{}, "cluster_unavailable", id, "tenant").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatch()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if calls["a"] != 1 || calls["b"] != 1 {
		t.Fatalf("per-tick inventory calls: %v", calls)
	}
}

func TestR9GonePinnedSourceFailsAtDispatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	const tenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj`).WithArgs("job-pinned", tenant).
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).AddRow("dispatched", "", "", "", tenant, "", ""))
	mock.ExpectExec(`UPDATE foghorn.processing_jobs SET status = 'failed'`).WithArgs("job-pinned", "source unavailable: pinned source node is gone", tenant).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(t.Context(), &processingJob{JobID: "job-pinned", TenantID: tenant, PreferredNode: sql.NullString{String: "gone", Valid: true}, SourceParams: sql.NullString{String: `{"source_kind":"live"}`, Valid: true}})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
