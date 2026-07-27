package control

import (
	"database/sql"
	"testing"

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
		WithArgs("chapter-finalize-chapter-1", int32(42), "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("UPDATE foghorn.dvr_chapters c").
		WithArgs("chapter-1", "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "tenant_id"}).
			AddRow("chapter-artifact-hash", "5eed517e-ba5e-da7a-517e-ba5eda7a0001"))

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{
		JobId:       "chapter-finalize-chapter-1",
		ProgressPct: 42,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A "completed" result with no output path is malformed: it must NOT bless the job as
// completed (which would strand the artifact "processing" forever). It is failed atomically
// instead — BEGIN → lock+resolve FOR UPDATE OF pj → mark job failed → COMMIT. Here the job
// has no artifact row, so only the job flips.
func TestProcessProcessingJobResult_Completed_NoOutputFailsJob(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("processing", "node-1", "", "", "", "", ""))
	mock.ExpectExec(`UPDATE foghorn.processing_jobs\s+SET status = 'failed'`).
		WithArgs("job-1", "completed processing result carried no output path").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-1",
		Status: "completed",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A cache_update result only mutates config for a job assigned to the REPORTING node (processing_node_id =
// reporting). A foreign node's cache_update matches 0 rows and is dropped — it cannot rewrite another node's
// job config by naming the artifact hash.
func TestProcessProcessingJobResult_CacheUpdate_BindsToAssignedNode(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectExec(`UPDATE foghorn.processing_jobs\s+SET processes_json`).
		WithArgs("job-x", "art-1", "[]", "attacker-node").
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows: not this node's job

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:   "job-x",
		Status:  "cache_update",
		Outputs: map[string]string{"artifact_hash": "art-1", "processes_json": "[]"},
	}, "attacker-node", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A completion reported by a node OTHER than the one the job was dispatched to is rejected: the reporting
// node would otherwise be recorded as the artifact origin. Nothing past the lock read is written or committed.
func TestProcessProcessingJobResult_Completed_RejectsForeignNode(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, COALESCE.*FROM foghorn.processing_jobs WHERE job_id = \$1 FOR UPDATE`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id"}).AddRow("processing", "assigned-node"))
	mock.ExpectRollback()

	// Reporting node "attacker-node" != assigned "assigned-node": no readiness/complete write, just rollback.
	processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: "job-1", Status: "completed", OutputPath: "/data/out.mp4"}, "attacker-node", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failure reported by a node other than the assigned one is rejected before any job/artifact write.
func TestFailProcessingJob_RejectsForeignNode(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("processing", "assigned-node", "art-clip", "clip", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "", ""))
	mock.ExpectRollback()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: "job-1", Status: "failed", Error: "boom"}, "attacker-node", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A non-active job (cancelled/deleted → status 'failed', or a duplicate already-'completed'
// result) is a no-op: the lock read shows it inactive and NOTHING is written or committed.
func TestProcessProcessingJobResult_Completed_NonActiveJobIsNoop(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, COALESCE.*FROM foghorn.processing_jobs WHERE job_id = \$1 FOR UPDATE`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id"}).AddRow("failed", ""))
	mock.ExpectRollback()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: "job-1", Status: "completed", OutputPath: "/data/out.mp4"}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Boundary: a readiness-write failure ROLLS BACK — the job is NOT marked completed, so stale
// recovery retries. No job-complete UPDATE, no COMMIT.
func TestProcessProcessingJobResult_Completed_ReadinessFailureRollsBack(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, COALESCE.*FROM foghorn.processing_jobs WHERE job_id = \$1 FOR UPDATE`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id"}).AddRow("processing", "node-1"))
	mock.ExpectQuery(`FROM foghorn.processing_jobs pj\s+JOIN foghorn.artifacts a`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-1", "vod", "tenant-1", "", "", "", "mp4", int64(0), int64(0)))
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET format`).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:           "job-1",
		Status:          "completed",
		OutputPath:      "/data/out.mp4",
		OutputSizeBytes: 10,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Deleted mid-processing: the readiness UPDATE guard matches 0 rows. The job STILL commits as
// completed (it did complete) but no publication side effects occur.
func TestProcessProcessingJobResult_Completed_DeletedMidProcessingStillCompletes(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, COALESCE.*FROM foghorn.processing_jobs WHERE job_id = \$1 FOR UPDATE`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id"}).AddRow("processing", "node-1"))
	mock.ExpectQuery(`FROM foghorn.processing_jobs pj\s+JOIN foghorn.artifacts a`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-1", "vod", "tenant-1", "", "", "", "mp4", int64(0), int64(0)))
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET format`).
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows: deleted/failed mid-processing
	mock.ExpectExec(`UPDATE foghorn.processing_jobs\s+SET status = 'completed'`).
		WithArgs("job-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:           "job-1",
		Status:          "completed",
		OutputPath:      "/data/out.mp4",
		OutputSizeBytes: 10,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A GENUINELY missing artifact row (the JOIN lookup returns ErrNoRows — the row is gone, not
// merely soft-deleted) must NOT be acknowledged as completed. The tx rolls back with no
// job-complete UPDATE and no COMMIT, so stale recovery retries.
func TestProcessProcessingJobResult_Completed_MissingArtifactRollsBack(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, COALESCE.*FROM foghorn.processing_jobs WHERE job_id = \$1 FOR UPDATE`).
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id"}).AddRow("processing", "node-1"))
	mock.ExpectQuery(`FROM foghorn.processing_jobs pj\s+JOIN foghorn.artifacts a`).
		WithArgs("job-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:           "job-1",
		Status:          "completed",
		OutputPath:      "/data/out.mp4",
		OutputSizeBytes: 10,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Full happy path for a clip completion: the whole terminal transition — readiness UPDATE,
// origin registration (+ its node-copy outbox event), clip lifecycle outbox enqueue, and the
// job-completed UPDATE — commits as ONE transaction, with the job marked completed LAST.
func TestProcessProcessingJobResult_Completed_ClipFullSuccess(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	tenant := "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	streamID := "5eed517e-ba5e-da7a-517e-ba5eda7a0002"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, COALESCE.*FROM foghorn.processing_jobs WHERE job_id = \$1 FOR UPDATE`).
		WithArgs("job-clip-ok").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id"}).AddRow("processing", "node-1"))
	mock.ExpectQuery(`FROM foghorn.processing_jobs pj\s+JOIN foghorn.artifacts a`).
		WithArgs("job-clip-ok").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name", "s3_url", "format", "req_start", "req_stop"}).
			AddRow("art-clip", "clip", tenant, streamID, "live+demo", "", "", int64(0), int64(0)))
	// Readiness claim.
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET format`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// RegisterOriginArtifactTx: prior-read (none) → origin upsert (freshly inserted, complete).
	mock.ExpectQuery(`SELECT role, is_complete, is_orphaned FROM foghorn.artifact_nodes`).
		WithArgs("art-clip", "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO foghorn.artifact_nodes`).
		WillReturnRows(sqlmock.NewRows([]string{"inserted", "is_complete"}).AddRow(true, true))
	// emitPresentTx → GAINED: resolve tenant → mint version → stamp last_emitted_version →
	// enqueue the node-copy outbox event.
	mock.ExpectQuery(`SELECT tenant_id::text FROM foghorn.artifacts WHERE artifact_hash`).
		WithArgs("art-clip").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenant))
	mock.ExpectQuery(`SELECT nextval\('foghorn.artifact_node_copy_version_seq'\)`).
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(1)))
	mock.ExpectExec(`UPDATE foghorn.artifact_nodes SET last_emitted_version`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1)) // node-copy event
	// Clip lifecycle enqueue (same tx).
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Job marked completed LAST, then commit.
	mock.ExpectExec(`UPDATE foghorn.processing_jobs\s+SET status = 'completed'`).
		WithArgs("job-clip-ok", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:           "job-clip-ok",
		Status:          "completed",
		OutputPath:      "/data/clip.mp4",
		OutputSizeBytes: 2048,
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failed result with no artifact row still flips the job to failed atomically:
// BEGIN → lock+resolve FOR UPDATE OF pj (no artifact) → job failed → COMMIT.
func TestProcessProcessingJobResult_Failed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-fail").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("processing", "node-1", "", "", "", "", ""))
	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'.*error_message").
		WithArgs("job-fail", "ffmpeg crashed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-fail",
		Status: "failed",
		Error:  "ffmpeg crashed",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failed clip: the job-failed UPDATE, the artifact-failed UPDATE, and the failure lifecycle
// outbox insert all commit as ONE transaction — a failed job is never left with an artifact
// still "processing".
func TestProcessProcessingJobResult_Failed_MarksClipArtifactFailed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-clip-fail").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("processing", "node-1", "art-clip", "clip", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "5eed517e-ba5e-da7a-517e-ba5eda7a0002", "stream-int"))
	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'").
		WithArgs("job-clip-fail", "output duration short").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts.*SET status = 'failed'").
		WithArgs("art-clip", "output duration short", "5eed517e-ba5e-da7a-517e-ba5eda7a0001").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-clip-fail",
		Status: "failed",
		Error:  "output duration short",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// If the artifact is already terminal (concurrently ready/deleted/expired), the guarded UPDATE
// affects 0 rows — the job still fails, but NO false FAILED lifecycle event is emitted.
func TestProcessProcessingJobResult_Failed_NoFalseLifecycleWhenArtifactTerminal(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-clip-fail").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("processing", "node-1", "art-clip", "clip", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "", ""))
	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'").
		WithArgs("job-clip-fail", "output duration short").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Artifact already terminal → 0 rows; NO outbox INSERT follows.
	mock.ExpectExec("UPDATE foghorn.artifacts.*SET status = 'failed'").
		WithArgs("art-clip", "output duration short", "5eed517e-ba5e-da7a-517e-ba5eda7a0001").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-clip-fail",
		Status: "failed",
		Error:  "output duration short",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failed VOD job flips both the job and the vod artifact and enqueues the vod failure
// lifecycle in one tx.
func TestProcessProcessingJobResult_Failed_MarksVodArtifactFailed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-vod-fail").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("processing", "node-1", "art-vod", "vod", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "", ""))
	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'").
		WithArgs("job-vod-fail", "transcode exploded").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts.*SET status = 'failed'").
		WithArgs("art-vod", "transcode exploded", "5eed517e-ba5e-da7a-517e-ba5eda7a0001").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.artifact_event_outbox").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-vod-fail",
		Status: "failed",
		Error:  "transcode exploded",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failure for a non-active job (already completed/cancelled/duplicate) is a no-op: the
// FOR UPDATE lock shows it inactive and NOTHING is written or committed.
func TestProcessProcessingJobResult_Failed_NonActiveJobIsNoop(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-done").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("completed", "", "art-x", "vod", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "", ""))
	mock.ExpectRollback()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-done",
		Status: "failed",
		Error:  "late failure after completion",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A transient error while marking the artifact failed ROLLS BACK the whole failure tx — the
// job is NOT left failed with an unfailed artifact; stale recovery retries.
func TestProcessProcessingJobResult_Failed_ArtifactWriteRollsBack(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	logger := logging.NewLogger()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pj.status.*FROM foghorn.processing_jobs pj\s+LEFT JOIN foghorn.artifacts a`).
		WithArgs("job-vod-fail").
		WillReturnRows(sqlmock.NewRows([]string{"status", "processing_node_id", "artifact_hash", "artifact_type", "tenant_id", "stream_id", "stream_internal_name"}).
			AddRow("processing", "node-1", "art-vod", "vod", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "", ""))
	mock.ExpectExec("UPDATE foghorn.processing_jobs.*SET status = 'failed'").
		WithArgs("job-vod-fail", "boom").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn.artifacts.*SET status = 'failed'").
		WithArgs("art-vod", "boom", "5eed517e-ba5e-da7a-517e-ba5eda7a0001").
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId:  "job-vod-fail",
		Status: "failed",
		Error:  "boom",
	}, "node-1", logger)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingSpeedFromOutputs(t *testing.T) {
	// No telemetry keys at all.
	sp, wallMs := processingSpeedFromOutputs(map[string]string{})
	if sp != nil || wallMs != nil {
		t.Fatalf("empty outputs should yield nil,nil; got %v,%v", sp, wallMs)
	}

	// Wall time without speed stats (no sampler data, no Mist stats).
	sp, wallMs = processingSpeedFromOutputs(map[string]string{"processing_wall_ms": "63000"})
	if sp != nil {
		t.Fatalf("no speed_source should yield nil stats, got %+v", sp)
	}
	if wallMs == nil || *wallMs != 63000 {
		t.Fatalf("wallMs = %v, want 63000", wallMs)
	}

	// Full telemetry round-trip.
	sp, wallMs = processingSpeedFromOutputs(map[string]string{
		"processing_wall_ms": "63000",
		"speed_source":       "mist",
		"speed_ticks":        "40",
		"speed_min_x":        "1.00",
		"speed_avg_x":        "6.50",
		"speed_max_x":        "24.00",
		"hard_slow_ticks":    "3",
		"regular_slow_ticks": "2",
		"ramp_ups":           "8",
		"lockout_ticks":      "10",
		"stale_hold_ticks":   "12",
		"drain_ms":           "30000",
	})
	if wallMs == nil || *wallMs != 63000 {
		t.Fatalf("wallMs = %v, want 63000", wallMs)
	}
	if sp == nil || sp.GetTicks() != 40 || sp.GetSpeedAvg() != 6.5 || sp.GetSpeedMax() != 24 {
		t.Fatalf("speed stats mismatch: %+v", sp)
	}
	if sp.GetHardSlowTicks() != 3 || sp.GetStaleHoldTicks() != 12 || sp.GetLockoutTicks() != 10 {
		t.Fatalf("verdict counters mismatch: %+v", sp)
	}
	if sp.DrainMs == nil || sp.GetDrainMs() != 30000 {
		t.Fatalf("drain_ms mismatch: %+v", sp)
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
