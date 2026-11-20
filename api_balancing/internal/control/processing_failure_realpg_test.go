//go:build schema_verify

package control

import (
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const (
	processingFailureTenant = "7a1e0000-0000-4000-8000-0000000000f1"
	processingFailureJob    = "7a1e0000-0000-4000-8000-0000000000f2"
	processingFailureHash   = "importfailhash000000000000000001"
)

func seedActiveImportJob(t *testing.T, conn *sql.DB) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status)
		VALUES ($1, 'vod', $2::uuid, 'processing')`, processingFailureHash, processingFailureTenant); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, processing_node_id)
		VALUES ($1::uuid, $2::uuid, $3, 'transcode', 'processing', 'edge-1')`, processingFailureJob, processingFailureTenant, processingFailureHash); err != nil {
		t.Fatalf("seed job: %v", err)
	}
}

// A URL import whose source answers 404 reaches FAILED with upload.failed
// naming the unavailable source, through the same result path a Helmsman
// "failed" report takes; a report from a node that does not hold the job is
// refused and leaves it active.
func TestProcessingFailureReachesFailed_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	seedActiveImportJob(t, conn)
	logger := logging.NewLogger()

	foreign := &ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "failed", Error: "source unavailable: 404 Not Found"}
	processProcessingJobResult(foreign, "edge-2", logger)
	var status string
	if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, processingFailureJob).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "processing" {
		t.Fatalf("foreign node failure changed job status to %q", status)
	}

	processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "failed", Error: "source unavailable: 404 Not Found"}, "edge-1", logger)

	var jobErr, artifactStatus string
	if err := conn.QueryRow(`SELECT status, COALESCE(error_message, '') FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, processingFailureJob).Scan(&status, &jobErr); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1`, processingFailureHash).Scan(&artifactStatus); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || jobErr != "source unavailable: 404 Not Found" || artifactStatus != "failed" {
		t.Fatalf("job=%q (%q) artifact=%q, want failed/failed", status, jobErr, artifactStatus)
	}

	rows := domainRows(t, conn, "upload.failed")
	if len(rows) != 1 {
		t.Fatalf("upload.failed rows = %d, want 1", len(rows))
	}
	_, msg, err := events.Decode("upload.failed", rows[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	failed, ok := msg.(*publicv1.UploadFailed)
	if !ok {
		t.Fatalf("upload.failed payload is %T", msg)
	}
	if failed.GetReason() != publicv1.MediaFailureReason_MEDIA_FAILURE_REASON_SOURCE_UNAVAILABLE {
		t.Fatalf("upload.failed reason = %s, want SOURCE_UNAVAILABLE", failed.GetReason())
	}
}

// A job retried after a failed attempt and then completed reports no error: the
// requeue's reason described the attempt the completion superseded.
func TestCompletedProcessingJobClearsRetryReason_RealPG(t *testing.T) {
	conn := startRealPG(t)
	seedActiveImportJob(t, conn)
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET retry_count = 1,
		error_message = 'recording validation failed: PROCESS_TRACKS_CHANGED' WHERE job_id = $1::uuid`, processingFailureJob); err != nil {
		t.Fatal(err)
	}
	if err := foghorndb.New(conn).CompleteProcessingJob(t.Context(), foghorndb.CompleteProcessingJobParams{JobID: processingFailureJob}); err != nil {
		t.Fatal(err)
	}
	var status string
	var jobErr sql.NullString
	var retries int
	if err := conn.QueryRow(`SELECT status, error_message, retry_count FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, processingFailureJob).Scan(&status, &jobErr, &retries); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || jobErr.Valid || retries != 1 {
		t.Fatalf("job = %q error=%v retries=%d, want completed with no error and the retry kept", status, jobErr, retries)
	}
}

func TestMemoryDeferralSurvivesTenMinutesAndCompletes_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	seedActiveImportJob(t, conn)
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET retry_count=$3, started_at=NOW()-INTERVAL '10 minutes' WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant, ProcessingMaxRetries); err != nil {
		t.Fatal(err)
	}
	logger := logging.NewLogger()
	for attempt := 0; attempt < 5; attempt++ {
		if err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "retryable", Error: "waiting for processing memory", Outputs: map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryPressure}}, "edge-1", logger); err != nil {
			t.Fatal(err)
		}
		var job, artifact string
		var retries int
		var delayed bool
		if err := conn.QueryRow(`SELECT j.status,a.status,j.retry_count,COALESCE(j.next_attempt_at>NOW(),false) FROM foghorn.processing_jobs j JOIN foghorn.artifacts a ON a.artifact_hash=j.artifact_hash AND a.tenant_id=j.tenant_id WHERE j.job_id=$1::uuid AND j.tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&job, &artifact, &retries, &delayed); err != nil {
			t.Fatal(err)
		}
		if job != "queued" || artifact != "queued" || retries != ProcessingMaxRetries || !delayed {
			t.Fatalf("capacity wait consumed retry or failed valid upload after ten minutes: job=%s artifact=%s retries=%d delayed=%v", job, artifact, retries, delayed)
		}
		if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET status='processing',processing_node_id='edge-1',next_attempt_at=NULL WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant); err != nil {
			t.Fatal(err)
		}
	}
	duration := int64(60000)
	if err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "completed", OutputPath: "/storage/vod/queued.mkv", OutputSizeBytes: 1024, MediaDurationMs: &duration, Outputs: map[string]string{"video_codec": "H264", "width": "3840", "height": "2160", "duration_ms": "60000"}}, "edge-1", logger); err != nil {
		t.Fatal(err)
	}
	var job, artifact string
	if err := conn.QueryRow(`SELECT j.status,a.status FROM foghorn.processing_jobs j JOIN foghorn.artifacts a ON a.artifact_hash=j.artifact_hash AND a.tenant_id=j.tenant_id WHERE j.job_id=$1::uuid AND j.tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&job, &artifact); err != nil {
		t.Fatal(err)
	}
	if job != "completed" || artifact != "ready" {
		t.Fatalf("deferred upload did not reach READY: job=%s artifact=%s", job, artifact)
	}
}

// Lease heartbeats refresh updated_at but not progress_advanced_at; only an
// increased percentage or media position moves the watchdog clock.
func TestProcessingProgressWatchdogClock_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	seedActiveImportJob(t, conn)
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET progress = 10, progress_last_ms = 5000,
		progress_advanced_at = NOW() - INTERVAL '1 hour', updated_at = NOW() - INTERVAL '1 hour' WHERE job_id = $1::uuid`, processingFailureJob); err != nil {
		t.Fatal(err)
	}
	logger := logging.NewLogger()
	readClock := func() (advancedOld, updatedOld bool) {
		t.Helper()
		if err := conn.QueryRow(`SELECT progress_advanced_at < NOW() - INTERVAL '30 minutes', updated_at < NOW() - INTERVAL '30 minutes'
			FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, processingFailureJob).Scan(&advancedOld, &updatedOld); err != nil {
			t.Fatal(err)
		}
		return advancedOld, updatedOld
	}

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: processingFailureJob, ProgressPct: 10, LastMs: 5000}, "edge-1", logger)
	if advancedOld, updatedOld := readClock(); !advancedOld || updatedOld {
		t.Fatalf("heartbeat without progress: advanced_old=%t updated_old=%t, want true/false", advancedOld, updatedOld)
	}

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: processingFailureJob, ProgressPct: 10, LastMs: 9000}, "edge-1", logger)
	if advancedOld, _ := readClock(); advancedOld {
		t.Fatal("media position advance did not move progress_advanced_at")
	}
}

// An attempt whose source read stalled in storage or upstream is requeued even
// with the retry budget spent: it backs off by as long as the run of stalls has
// lasted, is not dispatched before then, and the run ends when an attempt's
// media advances. A run that outlasts the no-progress window fails the upload as
// timed out.
func TestSourceStallRetriesOutsideRetryBudget_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	seedActiveImportJob(t, conn)
	logger := logging.NewLogger()
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET retry_count = $2, started_at = NOW() - INTERVAL '45 seconds'
		WHERE job_id = $1::uuid`, processingFailureJob, ProcessingMaxRetries); err != nil {
		t.Fatal(err)
	}
	stall := func(node string) {
		t.Helper()
		processProcessingJobResult(&ipcpb.ProcessingJobResult{
			JobId: processingFailureJob, Status: "retryable",
			Error:   "processing stream did not boot (source read failed: upstream did not answer before the reader gave up)",
			Outputs: map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseSourceStall},
		}, node, logger)
	}
	type jobState struct {
		status, artifact     string
		retries              int
		stalledSinceAgo      sql.NullFloat64
		nextAttemptInSeconds sql.NullFloat64
	}
	read := func() jobState {
		t.Helper()
		var s jobState
		if err := conn.QueryRow(`SELECT j.status, a.status, j.retry_count,
				EXTRACT(EPOCH FROM (NOW() - j.source_stalled_since))::float8,
				EXTRACT(EPOCH FROM (j.next_attempt_at - NOW()))::float8
			FROM foghorn.processing_jobs j JOIN foghorn.artifacts a ON a.artifact_hash = j.artifact_hash
			WHERE j.job_id = $1::uuid`, processingFailureJob).Scan(&s.status, &s.artifact, &s.retries, &s.stalledSinceAgo, &s.nextAttemptInSeconds); err != nil {
			t.Fatal(err)
		}
		return s
	}

	stall("edge-1")
	s := read()
	if s.status != "queued" || s.artifact != "queued" || s.retries != ProcessingMaxRetries {
		t.Fatalf("stalled attempt with the retry budget spent: job=%q artifact=%q retries=%d, want queued/queued/%d", s.status, s.artifact, s.retries, ProcessingMaxRetries)
	}
	if !s.stalledSinceAgo.Valid || s.stalledSinceAgo.Float64 < 40 || s.stalledSinceAgo.Float64 > 60 {
		t.Fatalf("source_stalled_since %v s ago, want the stalled attempt's start (45 s ago)", s.stalledSinceAgo)
	}
	if !s.nextAttemptInSeconds.Valid || s.nextAttemptInSeconds.Float64 < 40 || s.nextAttemptInSeconds.Float64 > 60 {
		t.Fatalf("next attempt in %v s, want the 45 s the stall has lasted", s.nextAttemptInSeconds)
	}
	claimed, err := foghorndb.New(conn).ClaimQueuedProcessingJobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("a job inside its stall backoff was dispatched: %v", claimed)
	}
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET next_attempt_at = NOW() - INTERVAL '1 second' WHERE job_id = $1::uuid`, processingFailureJob); err != nil {
		t.Fatal(err)
	}
	claimed, err = foghorndb.New(conn).ClaimQueuedProcessingJobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].JobID != processingFailureJob {
		t.Fatalf("a job past its stall backoff was not dispatched: %v", claimed)
	}

	// The next attempt boots and its media advances: the run of stalls ends.
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET status = 'processing', processing_node_id = 'edge-1', started_at = NOW()
		WHERE job_id = $1::uuid`, processingFailureJob); err != nil {
		t.Fatal(err)
	}
	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: processingFailureJob, ProgressPct: 5, LastMs: 2000}, "edge-1", logger)
	if s := read(); s.stalledSinceAgo.Valid {
		t.Fatalf("media progress left source_stalled_since %v s ago, want it cleared", s.stalledSinceAgo.Float64)
	}

	// A new run of stalls that has outlasted the no-progress window fails the job.
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET source_stalled_since = NOW() - $2::interval WHERE job_id = $1::uuid`,
		processingFailureJob, (ProcessingProgressStallTimeout + time.Minute).String()); err != nil {
		t.Fatal(err)
	}
	stall("edge-1")
	if s := read(); s.status != "failed" || s.artifact != "failed" {
		t.Fatalf("stall past the window: job=%q artifact=%q, want failed/failed", s.status, s.artifact)
	}
	rows := domainRows(t, conn, "upload.failed")
	if len(rows) != 1 {
		t.Fatalf("upload.failed rows = %d, want 1", len(rows))
	}
	_, msg, err := events.Decode("upload.failed", rows[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if reason := msg.(*publicv1.UploadFailed).GetReason(); reason != publicv1.MediaFailureReason_MEDIA_FAILURE_REASON_TIMED_OUT {
		t.Fatalf("upload.failed reason = %s, want TIMED_OUT", reason)
	}
}

func TestProcessingMemoryRequirementAndBoundedWait_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	seedActiveImportJob(t, conn)
	policy := `[{"process":"AV","codec":"H264","resolution":"0x2160"}]`
	result := &ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "retryable", Error: "node too small", Outputs: map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryNodeTooSmall, mist.ProcessingRequiredMemoryBytes: "2147483648", mist.ProcessingMemoryPolicy: policy}}
	if err := processProcessingJobResult(result, "edge-1", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	var required int64
	var stored, status string
	if err := conn.QueryRow(`SELECT required_memory_bytes,processes_json,status FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&required, &stored, &status); err != nil {
		t.Fatal(err)
	}
	if required != 2147483648 || stored != policy || status != "queued" {
		t.Fatalf("memory placement lost on requeue: %d %s %s", required, stored, status)
	}
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET status='processing',processing_node_id='edge-1',capacity_wait_started_at=NOW()-INTERVAL '25 minutes' WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant); err != nil {
		t.Fatal(err)
	}
	if err := processProcessingJobResult(result, "edge-1", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	var artifact string
	if err := conn.QueryRow(`SELECT j.status,a.status FROM foghorn.processing_jobs j JOIN foghorn.artifacts a ON a.artifact_hash=j.artifact_hash AND a.tenant_id=j.tenant_id WHERE j.job_id=$1::uuid AND j.tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&status, &artifact); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || artifact != "failed" {
		t.Fatalf("capacity waited forever: %s/%s", status, artifact)
	}
	if got := len(domainRows(t, conn, "upload.failed")); got != 1 {
		t.Fatalf("failure events: %d", got)
	}
}

func TestNeverFitVODCapacityFailure_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	seedActiveImportJob(t, conn)
	if err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "retryable", Error: "4K processing workload cannot fit the cluster's largest node", Outputs: map[string]string{mist.ProcessingResultRetryCause: "insufficient_memory"}}, "edge-1", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	var job, artifact string
	if err := conn.QueryRow(`SELECT j.status,a.status FROM foghorn.processing_jobs j JOIN foghorn.artifacts a ON a.artifact_hash=j.artifact_hash AND a.tenant_id=j.tenant_id WHERE j.job_id=$1::uuid AND j.tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&job, &artifact); err != nil {
		t.Fatal(err)
	}
	if job != "failed" || artifact != "failed" {
		t.Fatalf("never-fit 4K VOD was requeued: job=%s artifact=%s", job, artifact)
	}
}

func TestProcessingMemoryReportsCannotCrossTenant_RealPG(t *testing.T) {
	for _, status := range []string{"retryable", "cache_update", "completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			conn := startRealPG(t)
			previous := db
			SetDB(conn)
			t.Cleanup(func() { SetDB(previous) })
			seedActiveImportJob(t, conn)
			var beforePolicy string
			var beforeBytes int64
			if err := conn.QueryRow(`SELECT COALESCE(processes_json,''),required_memory_bytes FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&beforePolicy, &beforeBytes); err != nil {
				t.Fatal(err)
			}
			result := &ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: status, Error: "waiting for memory", Outputs: map[string]string{
				"tenant_id":                        "7a1e0000-0000-4000-8000-000000000099",
				"artifact_hash":                    processingFailureHash,
				"processes_json":                   `[{"process":"AV","codec":"H264"}]`,
				mist.ProcessingResultRetryCause:    mist.ProcessingRetryCauseMemoryPressure,
				mist.ProcessingRequiredMemoryBytes: "2147483648",
			}}
			if err := processProcessingJobResult(result, "edge-1", logging.NewLogger()); err != nil {
				t.Fatal(err)
			}
			var jobStatus, afterPolicy string
			var afterBytes int64
			if err := conn.QueryRow(`SELECT status,COALESCE(processes_json,''),required_memory_bytes FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&jobStatus, &afterPolicy, &afterBytes); err != nil {
				t.Fatal(err)
			}
			if jobStatus != "processing" || afterPolicy != beforePolicy || afterBytes != beforeBytes {
				t.Fatalf("mismatched tenant mutated processing job: status=%s memory=%d policy=%s", jobStatus, afterBytes, afterPolicy)
			}
		})
	}
}
