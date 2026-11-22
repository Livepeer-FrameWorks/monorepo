//go:build schema_verify

package jobs

import (
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func seedR9FairnessWait(t *testing.T, reason string) (*sql.DB, *foghorndb.Queries, string) {
	t.Helper()
	conn := startRealPGForCleanup(t)
	const hash, job = "r9standing0000000000000000000001", "7a900000-0000-4000-8000-000000000301"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id) VALUES($1,$2::uuid,'vod','processing','origin')`, hash, r9Tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,processing_node_id,required_memory_bytes,wait_reason,wait_started_at) VALUES($1::uuid,$2::uuid,$3,'process','processing','edge',1024,$4,(NOW() AT TIME ZONE 'UTC')-INTERVAL '2 hours')`, job, r9Tenant, hash, reason); err != nil {
		t.Fatal(err)
	}
	return conn, foghorndb.New(conn), job
}

func TestR9MemoryWaitKeepsStandingAcrossRoutingReasons_RealPG(t *testing.T) {
	_, q, job := seedR9FairnessWait(t, "memory")
	err := q.DeferUnroutableProcessingJob(t.Context(), foghorndb.DeferUnroutableProcessingJobParams{DeferSeconds: 15, JobID: job, TenantID: r9Tenant, WaitReason: "fitting_node_unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	waiters, err := q.ListProcessingMemoryWaiters(t.Context(), foghorndb.ListProcessingMemoryWaitersParams{WaitThreshold: time.Now().UTC().Add(-time.Minute), UploadDeadline: time.Now().UTC().Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(waiters) != 1 || waiters[0].JobID != job {
		t.Fatalf("routing deferral lost memory standing: %v", waiters)
	}
}

func TestR9ClusterWaitDoesNotAgeMemoryFairness_RealPG(t *testing.T) {
	_, q, job := seedR9FairnessWait(t, "cluster_unavailable")
	_, err := q.RequeueCapacityDeferredProcessingJob(t.Context(), foghorndb.RequeueCapacityDeferredProcessingJobParams{JobID: job, NodeID: "edge", TenantID: r9Tenant, RequiredMemoryBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	waiters, err := q.ListProcessingMemoryWaiters(t.Context(), foghorndb.ListProcessingMemoryWaitersParams{WaitThreshold: time.Now().UTC().Add(-time.Minute), UploadDeadline: time.Now().UTC().Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(waiters) != 0 {
		t.Fatalf("fresh memory wait inherited two hours of cluster outage: %v", waiters)
	}
}
