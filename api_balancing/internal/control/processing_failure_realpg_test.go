//go:build schema_verify

package control

import (
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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
