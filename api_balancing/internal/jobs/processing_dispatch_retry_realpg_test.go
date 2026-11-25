//go:build schema_verify

package jobs

import (
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestDispatchFailureEmitsOwnQueueReason_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const job = "7a900000-0000-4000-8000-000000000501"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id)
		VALUES('dispatch-reason',$1::uuid,'vod','queued','dispatch-cluster')`, r9Tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs
		(job_id,tenant_id,artifact_hash,job_type,status,processing_node_id,wait_reason,memory_wait_ms,memory_wait_started_at,wait_started_at)
		VALUES($1::uuid,$2::uuid,'dispatch-reason','transcode','dispatched','worker','memory',90000,NOW()-INTERVAL '2 seconds',NOW()-INTERVAL '2 seconds')`, job, r9Tenant); err != nil {
		t.Fatal(err)
	}
	if err := foghorndb.New(conn).RevertProcessingJobToQueued(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	var retries int
	var retainsBackoff, clearedNode bool
	var memoryWait int64
	if err := conn.QueryRow(`SELECT status,wait_reason,retry_count,
		next_attempt_at>NOW() AND next_attempt_at<=NOW()+INTERVAL '20 seconds',
		processing_node_id IS NULL,memory_wait_ms FROM foghorn.processing_jobs
		WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant).
		Scan(&status, &reason, &retries, &retainsBackoff, &clearedNode, &memoryWait); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || reason != "dispatch_retry" || retries != 0 || !retainsBackoff || !clearedNode || memoryWait < 90000 {
		t.Fatalf("reverted dispatch: status=%s reason=%s retries=%d backoff=%v cleared_node=%v memory_wait=%d; want dispatch_retry with backoff, no retry cost, and preserved memory standing",
			status, reason, retries, retainsBackoff, clearedNode, memoryWait)
	}
}
