//go:build schema_verify

package jobs

import (
	"context"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const r9Tenant = "7a900000-0000-4000-8000-000000000001"

// Claim fairness: jobs that dispatch reverts without a deferral (input not
// locally backed, presign failure, dispatch failure) must not hold the head
// of the claim forever and starve every younger job.
func TestR9RevertedJobsDoNotStarveYoungerJob_RealPG(t *testing.T) {
	assertRevertedJobsDoNotStarveYoungerJob(t, false)
}

func TestRevertedJobsDoNotStarveYoungerJobDuringCapacityChurn_RealPG(t *testing.T) {
	assertRevertedJobsDoNotStarveYoungerJob(t, true)
}

func assertRevertedJobsDoNotStarveYoungerJob(t *testing.T, capacityChurn bool) {
	t.Helper()
	conn := startRealPGForCleanup(t)
	for i := 0; i < 21; i++ {
		hash := fmt.Sprintf("r9hol%027d", i)
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id) VALUES($1,$2::uuid,'vod','queued','churn-cluster')`, hash, r9Tenant); err != nil {
			t.Fatal(err)
		}
		job := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 100+i)
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,created_at,updated_at) VALUES($1::uuid,$2::uuid,$3,'transcode','queued',NOW()-($4::int*INTERVAL '1 minute'),NOW()-($4::int*INTERVAL '1 minute'))`, job, r9Tenant, hash, 100-i); err != nil {
			t.Fatal(err)
		}
	}
	young := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 120)
	q := foghorndb.New(conn)
	ctx := context.Background()
	for tick := 0; tick < 3; tick++ {
		rows, err := q.ClaimQueuedProcessingJobs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.JobID == young {
				return
			}
			// Same as ProcessingDispatcher.revertToQueued for a non-local input.
			if err := q.RevertProcessingJobToQueued(ctx, row.JobID); err != nil {
				t.Fatal(err)
			}
		}
		if capacityChurn {
			if err := q.WakeProcessingClusterJobs(ctx, "churn-cluster"); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("younger job was never claimed: 20 reverted older jobs hold the claim head")
}

func TestCapacityWakeKeepsDispatchBackoffAndWakesCapacityWaits_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	for i, reason := range []string{"memory", "slots", "fitting_node_unavailable", "cluster_unavailable", "dispatch_retry"} {
		for _, cluster := range []string{"churn-cluster", "other-cluster"} {
			hash := fmt.Sprintf("wake-%s-%d", cluster, i)
			job := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 300+i)
			if cluster == "other-cluster" {
				job = fmt.Sprintf("7a900000-0000-4000-8000-%012d", 400+i)
			}
			if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id) VALUES($1,$2::uuid,'vod','queued',$3)`, hash, r9Tenant, cluster); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,wait_reason,next_attempt_at) VALUES($1::uuid,$2::uuid,$3,'transcode','queued',$4,NOW()+INTERVAL '3 minutes')`, job, r9Tenant, hash, reason); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := foghorndb.New(conn).WakeProcessingClusterJobs(context.Background(), "churn-cluster"); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(`SELECT a.origin_cluster_id,job.wait_reason,job.next_attempt_at IS NULL FROM foghorn.processing_jobs job JOIN foghorn.artifacts a ON a.artifact_hash=job.artifact_hash AND a.tenant_id=job.tenant_id WHERE job.tenant_id=$1::uuid`, r9Tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cluster, reason string
		var woken bool
		if err := rows.Scan(&cluster, &reason, &woken); err != nil {
			t.Fatal(err)
		}
		if want := cluster == "churn-cluster" && reason != "dispatch_retry"; woken != want {
			t.Errorf("capacity wake for %s/%s: woken=%v, want %v", cluster, reason, woken, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// A node-pinned live/DVR clip whose source node is gone can never run. Before
// the round-8 series recoverStale failed it ("stuck queued: node-pinned source
// unavailable"); it must still reach a terminal state.
func TestR9NodePinnedClipWithGoneSourceTerminates_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const hash, job = "r9pinnedclip0000000000000000001", "7a900000-0000-4000-8000-000000000201"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,created_at) VALUES($1,$2::uuid,'clip','queued',NOW())`, hash, r9Tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,preferred_node_id,source_params,created_at,updated_at) VALUES($1::uuid,$2::uuid,$3,'clip','queued','gone-node','{"source_kind":"live"}'::jsonb,NOW(),NOW())`, job, r9Tenant, hash); err != nil {
		t.Fatal(err)
	}
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
	d.recoverStale()
	var status string
	if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id=$1::uuid`, job).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("node-pinned clip with a gone source node stays %q forever", status)
	}
}
