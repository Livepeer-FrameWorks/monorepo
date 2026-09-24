//go:build schema_verify

package jobs

import (
	"testing"
	"time"
)

// A job whose lease heartbeat is fresh but whose media position has not moved
// for longer than the watchdog window is failed by stale recovery, with its
// artifact and upload.failed; a job that is advancing is left running.
func TestProcessingProgressWatchdogFailsHeartbeatingStall_RealPG(t *testing.T) {
	// The cutoffs bind to TIMESTAMP (without time zone) columns, which drop a
	// parameter's offset; production hosts run in UTC, so the test does too.
	prevLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = prevLocal })
	conn := startRealPGForCleanup(t)
	const tenant = "7a1e0000-0000-4000-8000-0000000000e1"
	seed := func(jobID, hash, advancedAgo string) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status)
			VALUES ($1, 'vod', $2::uuid, 'processing')`, hash, tenant); err != nil {
			t.Fatalf("seed artifact %s: %v", hash, err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs
			(job_id, tenant_id, artifact_hash, job_type, status, processing_node_id, updated_at, progress_advanced_at)
			VALUES ($1::uuid, $2::uuid, $3, 'transcode', 'processing', 'edge-1', NOW(), NOW() - $4::interval)`,
			jobID, tenant, hash, advancedAgo); err != nil {
			t.Fatalf("seed job %s: %v", jobID, err)
		}
	}
	const stalledJob, advancingJob = "7a1e0000-0000-4000-8000-0000000000e2", "7a1e0000-0000-4000-8000-0000000000e3"
	seed(stalledJob, "watchdogstalled00000000000000001", "45 minutes")
	seed(advancingJob, "watchdogadvancing000000000000001", "1 minute")

	newRecoveryDispatcher(t, conn).recoverStale()

	status := func(jobID string) (string, string) {
		t.Helper()
		var s, msg string
		if err := conn.QueryRow(`SELECT status, COALESCE(error_message, '') FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, jobID).Scan(&s, &msg); err != nil {
			t.Fatal(err)
		}
		return s, msg
	}
	if s, msg := status(stalledJob); s != "failed" || msg != "no processing progress within the watchdog window" {
		t.Fatalf("stalled job = %q (%q), want failed by the watchdog", s, msg)
	}
	if s, _ := status(advancingJob); s != "processing" {
		t.Fatalf("advancing job = %q, want processing", s)
	}
	var artifactStatus string
	if err := conn.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = 'watchdogstalled00000000000000001'`).Scan(&artifactStatus); err != nil {
		t.Fatal(err)
	}
	if artifactStatus != "failed" {
		t.Fatalf("stalled artifact = %q, want failed", artifactStatus)
	}
	var uploadFailed int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.domain_event_outbox WHERE event_type = 'upload.failed'`).Scan(&uploadFailed); err != nil {
		t.Fatal(err)
	}
	if uploadFailed != 1 {
		t.Fatalf("upload.failed events = %d, want 1", uploadFailed)
	}
}
