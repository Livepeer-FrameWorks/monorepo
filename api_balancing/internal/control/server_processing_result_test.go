package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessProcessingJobResult_NilDB(t *testing.T) {
	_, _, _ = setupArtifactTestDeps(t)
	db = nil
	logger := logging.NewLogger()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "completed",
	}, "node-1", logger)
	// should not panic
}

func TestProcessProcessingJobProgress_ChapterFinalizeUsesChapterLedger(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectQuery("UPDATE foghorn.processing_jobs").
		WithArgs("chapter-finalize-chapter-1", int32(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("UPDATE foghorn.dvr_chapters c").
		WithArgs("chapter-1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "tenant_id"}).
			AddRow("chapter-artifact-hash", "5eed517e-ba5e-da7a-517e-ba5eda7a0001"))

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{
		JobId:       "chapter-finalize-chapter-1",
		ProgressPct: 42,
	}, logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Completed_NoOutput(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'completed'").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "completed",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Completed_WithOutputMeta(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'completed'").
		WithArgs("job-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:   "job-1",
		Status:  "completed",
		Outputs: map[string]string{"resolution": "1080p"},
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Completed_RegistersProcessedOutput(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("(?s)UPDATE foghorn.processing_jobs.*SET status = 'completed'.*progress = 100").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Lookup artifact hash
	mock.ExpectQuery("SELECT a\\.artifact_hash.*FROM foghorn.processing_jobs").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-hash", "vod", "tenant-1", "", "", "s3://old/upload.avi", "avi", int64(0), int64(0)))

	// Update artifact format + size_bytes + reset sync while retaining the
	// old source URL until the replacement upload is durably synced.
	mock.ExpectExec("(?s)UPDATE foghorn.artifacts.*SET format.*size_bytes.*artifact_type IN \\('clip', 'vod'\\).*sync_status = 'pending'.*storage_location = 'local'").
		WithArgs("mp4", "art-hash", int64(5000), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:           "job-1",
		Status:          "completed",
		OutputPath:      "/data/processed/output.mp4",
		OutputSizeBytes: 5000,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	if len(repo.originArtifactCalls) != 1 {
		t.Fatalf("expected 1 RegisterOriginArtifact, got %d", len(repo.originArtifactCalls))
	}
	call := repo.originArtifactCalls[0]
	if call.Hash != "art-hash" || call.NodeID != "node-1" || call.Path != "/data/processed/output.mp4" || call.Size != 5000 || !call.Complete {
		t.Fatalf("unexpected call: %+v", call)
	}
	repo.mu.Unlock()

	// Check in-memory state
	sm := state.DefaultManager()
	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			for _, a := range n.Artifacts {
				if a.ClipHash == "art-hash" {
					found = true
					if a.Format != "mp4" {
						t.Fatalf("expected format=mp4, got %s", a.Format)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("processed artifact not found in in-memory state")
	}
}

// A result that arrives after the clip was deleted (the ready-claim matches 0
// rows because the artifact is in a terminal state) must skip output
// registration and in-memory state, so a deleted clip is never resurrected.
func TestProcessProcessingJobResult_Completed_SkipsRegistrationWhenArtifactTerminal(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("(?s)UPDATE foghorn.processing_jobs.*SET status = 'completed'.*progress = 100").
		WithArgs("job-del", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery("SELECT a\\.artifact_hash.*FROM foghorn.processing_jobs").
		WithArgs("job-del").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-deleted", "clip", "tenant-1", "", "", "", "avi", int64(0), int64(0)))

	// Guarded ready-claim matches no row (artifact deleted/failed/etc): 0 rows
	// affected. The handler must return here without any side effects.
	mock.ExpectExec("(?s)UPDATE foghorn.artifacts.*SET format.*size_bytes.*sync_status = 'pending'.*storage_location = 'local'").
		WithArgs("mp4", "art-deleted", int64(5000), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:           "job-del",
		Status:          "completed",
		OutputPath:      "/data/processed/output.mp4",
		OutputSizeBytes: 5000,
	}, "node-del", logger)

	// No further DB calls (no projection/lifecycle); exactly the three above.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// No origin registration for a terminal artifact.
	repo.mu.Lock()
	if len(repo.originArtifactCalls) != 0 {
		t.Fatalf("expected no RegisterOriginArtifact for terminal artifact, got %d", len(repo.originArtifactCalls))
	}
	repo.mu.Unlock()

	// Not added to in-memory node state either.
	for _, n := range state.DefaultManager().GetAllNodesSnapshot().Nodes {
		if n.NodeID != "node-del" {
			continue
		}
		for _, a := range n.Artifacts {
			if a.ClipHash == "art-deleted" {
				t.Fatal("terminal artifact should not be added to in-memory state")
			}
		}
	}
}

// A clip whose measured output is shorter than the requested span (live
// buffer didn't reach back far enough) still completes: the artifact goes
// ready with the ACTUAL duration recorded, not failed and not the requested
// length.
func TestProcessProcessingJobResult_Completed_PartialClipRecordsActualDuration(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("(?s)UPDATE foghorn.processing_jobs.*SET status = 'completed'.*progress = 100").
		WithArgs("job-partial", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Requested span: 60s (unix 100 → 160). Actual output: 40.792s.
	mock.ExpectQuery("SELECT a\\.artifact_hash.*source_start_unix.*source_stop_unix.*FROM foghorn.processing_jobs").
		WithArgs("job-partial").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-partial", "clip", "tenant-1", "", "", "", "mkv", int64(100), int64(160)))

	mock.ExpectExec("(?s)UPDATE foghorn.artifacts.*duration_seconds = CASE WHEN \\$4::bigint > 0.*status = CASE WHEN artifact_type IN \\('clip', 'vod'\\) THEN 'ready'").
		WithArgs("mkv", "art-partial", int64(23625909), int64(40792)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mediaDurationMs := int64(40792)
	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:           "job-partial",
		Status:          "completed",
		OutputPath:      "/data/clips/stream/art-partial.mkv",
		OutputSizeBytes: 23625909,
		MediaDurationMs: &mediaDurationMs,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Completed_DoesNotDeleteOldS3UploadBeforeReplacementSync(t *testing.T) {
	mock, s3Mock, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery("SELECT a\\.artifact_hash").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-hash", "vod", "tenant-1", "", "", "s3://bucket/old/upload.avi", "avi", int64(0), int64(0)))

	mock.ExpectExec("UPDATE foghorn.artifacts").
		WithArgs("mp4", "art-hash", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:      "job-1",
		Status:     "completed",
		OutputPath: "/data/output.mp4",
	}, "node-1", logger)

	time.Sleep(20 * time.Millisecond)
	s3Mock.mu.Lock()
	if len(s3Mock.deleteByURLCalls) != 0 {
		t.Fatalf("expected no DeleteByURL before replacement sync, got %d", len(s3Mock.deleteByURLCalls))
	}
	s3Mock.mu.Unlock()
}

func TestProcessProcessingJobResult_Completed_SetsS3URLToNull(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec("UPDATE foghorn.processing_jobs").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery("SELECT a\\.artifact_hash").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-hash", "vod", "tenant-1", "", "", "", "avi", int64(0), int64(0)))

	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status = 'pending'.*storage_location = 'local'").
		WithArgs("mp4", "art-hash", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:      "job-1",
		Status:     "completed",
		OutputPath: "/data/output.mp4",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Failed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectQuery("SELECT a.artifact_hash").
		WithArgs("job-fail").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'.*error_message").
		WithArgs("job-fail", "ffmpeg crashed").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-fail",
		Status: "failed",
		Error:  "ffmpeg crashed",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_Failed_MarksClipArtifactFailed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectQuery("SELECT a\\.artifact_hash.*stream_id.*stream_internal_name.*FROM foghorn.processing_jobs").
		WithArgs("job-clip-fail").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("art-clip", "clip", "tenant-1", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "stream-int"))
	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'").
		WithArgs("job-clip-fail", "output duration short").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts.*SET status = 'failed'").
		WithArgs("art-clip", "output duration short").
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-clip-fail",
		Status: "failed",
		Error:  "output duration short",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessProcessingJobResult_CallsHandler(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	var handlerCalled bool
	prevHandler := onProcessingJobResult
	onProcessingJobResult = func(_ context.Context, jobID, status string, _ map[string]string, _ string) {
		handlerCalled = true
		if jobID != "job-1" || status != "completed" {
			t.Fatalf("unexpected handler args: jobID=%s status=%s", jobID, status)
		}
	}
	t.Cleanup(func() { onProcessingJobResult = prevHandler })

	mock.ExpectExec("UPDATE foghorn.processing_jobs").
		WithArgs("job-1", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "completed",
	}, "node-1", logger)

	if !handlerCalled {
		t.Fatal("onProcessingJobResult handler was not called")
	}
}

func TestProcessProcessingJobResult_UnknownStatus(t *testing.T) {
	_, _, _ = setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "unknown_status",
	}, "node-1", logger)
	// should not panic, should just log and return
}
