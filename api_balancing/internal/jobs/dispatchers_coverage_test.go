package jobs

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// seedProcessingNode makes a single node the only viable routing target for a
// video_transcode job: alive, processing-capable, with unbounded class capacity.
// Unique IDs per test avoid cross-test state contamination.
func seedProcessingNode(t *testing.T, sm *state.StreamStateManager, nodeID string) {
	t.Helper()
	sm.TouchNode(nodeID, true)
	setNodeProcessing(sm, nodeID, true, 0, 0)
}

// TestDispatchJobRoutesThenDispatchFails locks the dispatch decision on the
// happy routing path: a job whose class matches an alive, processing-capable
// node IS routed there (route succeeds), but with no live control-stream
// connection in the registry the gRPC send fails — and the dispatcher must NOT
// leave the job stranded in 'dispatched'. It reverts the job to 'queued' and
// projects the clip artifact back to 'queued' with the dispatch-failed reason.
// This is the only path that exercises full param assembly (SourceURL branch,
// output_profiles, source_params merge) before the send.
func TestDispatchJobRoutesThenDispatchFails(t *testing.T) {
	restore := control.SetupTestRegistry("", nil) // registry exists, no conns -> ErrNotConnected
	defer restore()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedProcessingNode(t, sm, "dispatch-node-1")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Routing succeeds -> SendProcessingJob hits an empty registry -> dispatch
	// fails -> revert + markArtifactQueued. No 'processing' UPDATE must run.
	mock.ExpectExec("UPDATE foghorn.processing_jobs\\s+SET status = 'queued', processing_node_id = NULL").
		WithArgs("job-route-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-route-1", "tenant-route", "queued").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(context.Background(), &processingJob{
		JobID:          "job-route-1",
		TenantID:       "tenant-route",
		ArtifactHash:   sql.NullString{String: "hash-route-1", Valid: true},
		ArtifactType:   sql.NullString{String: "clip", Valid: true},
		JobType:        "process",
		OutputProfiles: sql.NullString{String: "720p,480p", Valid: true},
		InputCodec:     sql.NullString{String: "h264", Valid: true},
		// SourceURL set so the dispatcher skips S3 presign (which needs a
		// configured s3Client) and goes straight to param assembly + send.
		SourceURL:    sql.NullString{String: "https://origin.example/source.mp4", Valid: true},
		SourceParams: sql.NullString{String: `{"source_kind":"upload","extra":"v"}`, Valid: true},
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchJobInvalidSourceParamsReverts locks the validation decision: a job
// whose source_params is not valid JSON is rejected BEFORE any send — the job
// reverts to 'queued' and the artifact is projected back with the
// "invalid source params" reason. A malformed params blob must never be silently
// dispatched with empty params.
func TestDispatchJobInvalidSourceParamsReverts(t *testing.T) {
	restore := control.SetupTestRegistry("", nil)
	defer restore()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedProcessingNode(t, sm, "dispatch-node-2")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE foghorn.processing_jobs\\s+SET status = 'queued', processing_node_id = NULL").
		WithArgs("job-bad-params").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-bad-params", "tenant-bp", "queued").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(context.Background(), &processingJob{
		JobID:        "job-bad-params",
		TenantID:     "tenant-bp",
		ArtifactHash: sql.NullString{String: "hash-bad-params", Valid: true},
		ArtifactType: sql.NullString{String: "vod", Valid: true},
		JobType:      "process",
		SourceURL:    sql.NullString{String: "https://origin.example/s.mp4", Valid: true},
		SourceParams: sql.NullString{String: `{not valid json`, Valid: true},
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchJobPresignFailureReverts locks the S3-source branch: a job with no
// explicit SourceURL falls through to presigning its S3 artifact bytes. With no
// s3Client configured in the control package, the presign fails and the job must
// revert to 'queued' with the "presign failed" reason rather than dispatching a
// job that points at nothing.
func TestDispatchJobPresignFailureReverts(t *testing.T) {
	restore := control.SetupTestRegistry("", nil)
	defer restore()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedProcessingNode(t, sm, "dispatch-node-3")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE foghorn.processing_jobs\\s+SET status = 'queued', processing_node_id = NULL").
		WithArgs("job-presign").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-presign", "tenant-ps", "queued").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.dispatchJob(context.Background(), &processingJob{
		JobID:        "job-presign",
		TenantID:     "tenant-ps",
		ArtifactHash: sql.NullString{String: "hash-presign", Valid: true},
		ArtifactType: sql.NullString{String: "vod", Valid: true},
		JobType:      "process",
		// No SourceURL -> S3 presign path; s3Client is nil in control -> error.
		S3URL: sql.NullString{String: "s3://bucket/tenant/hash/index.mp4", Valid: true},
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverStaleRequeueOnlyPath locks the non-terminal recovery decision: when
// the requeue CTE returns rows (stale dispatched/processing jobs still within
// the retry budget) and the fail CTE returns nothing, the pass requeues and
// fires NO artifact-failure UPDATE and NO onJobExhausted callback. Recoverable
// jobs must not be failed.
func TestRecoverStaleRequeueOnlyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Requeue CTE affects rows (jobs bumped back to queued).
	mock.ExpectExec("WITH requeued AS").
		WillReturnResult(sqlmock.NewResult(0, 3))
	// Fail CTE returns no exhausted jobs.
	mock.ExpectQuery("WITH failed AS").
		WillReturnRows(sqlmock.NewRows([]string{
			"job_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name",
		}))

	var exhaustedCalls int
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.SetJobExhaustedHandler(func(_ context.Context, _, _ string) { exhaustedCalls++ })

	d.recoverStale()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if exhaustedCalls != 0 {
		t.Fatalf("onJobExhausted fired %d times on requeue-only pass; want 0", exhaustedCalls)
	}
}

// TestMarkArtifactStatusSkipsNonClipVod locks the artifact-projection guard:
// markArtifactStatus only projects job status onto clip/vod artifacts. A 'dvr'
// (or any other) artifact type is a no-op — no UPDATE is issued, so the DVR
// segment-ledger state of record is never clobbered by the processing-job
// projection. sqlmock with zero expectations fails if any query fires.
func TestMarkArtifactStatusSkipsNonClipVod(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.markArtifactQueued(context.Background(), &processingJob{
		JobID:        "job-dvr",
		TenantID:     "tenant-dvr",
		ArtifactHash: sql.NullString{String: "hash-dvr", Valid: true},
		ArtifactType: sql.NullString{String: "dvr", Valid: true},
	}, "should not project")

	// No UPDATE expected. ExpectationsWereMet passes only if nothing ran.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMarkArtifactStatusFiltersByTenant locks tenant isolation on the artifact
// projection UPDATE: the clip/vod status projection carries both artifact_hash
// AND tenant_id as bind args, so a processing-job status can never bleed onto
// another tenant's artifact that happens to share a hash collision.
func TestMarkArtifactStatusFiltersByTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("hash-vod", "tenant-iso", "processing").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
	d.markArtifactProcessing(context.Background(), &processingJob{
		JobID:        "job-iso",
		TenantID:     "tenant-iso",
		ArtifactHash: sql.NullString{String: "hash-vod", Valid: true},
		ArtifactType: sql.NullString{String: "vod", Valid: true},
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestPurgeStaleNodeRowsReportsCount locks the orphan-node reap decision: the
// sweep deletes only orphaned artifact_nodes past the 7-day grace window and
// reports the affected count. A positive RowsAffected drives the operator-
// visible "purged" log; this asserts the sweep runs its DELETE and reads the
// count without error.
func TestPurgeStaleNodeRowsReportsCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec("DELETE FROM foghorn.artifact_nodes").
		WillReturnResult(sqlmock.NewResult(0, 5))

	j := NewPurgeDeletedJob(PurgeDeletedConfig{DB: db, Logger: logging.NewLogger()})
	j.purgeStaleNodeRows(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
