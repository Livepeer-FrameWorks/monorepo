//go:build schema_verify

package jobs

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestProcessingStartReleasesWaitersWithoutAdmission_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	q := foghorndb.New(conn)
	for i, tc := range []struct {
		kind           string
		sourceKind     string
		awaitAdmission bool
	}{
		{kind: "process", sourceKind: "live"},
		{kind: "process", sourceKind: "dvr_rolling"},
		{kind: "process", sourceKind: "chapter"},
		{kind: "process", sourceKind: " live "},
		{kind: "thumbnail"},
		{kind: "dvr_chapter_finalize"},
		{kind: "transcode", awaitAdmission: true},
		{kind: "process", awaitAdmission: true},
		{kind: "process", sourceKind: "vod", awaitAdmission: true},
	} {
		t.Run(tc.kind+"/"+tc.sourceKind, func(t *testing.T) {
			cluster := "started-memory-" + tc.kind + tc.sourceKind
			node := "edge-" + tc.kind + tc.sourceKind
			older := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 500+i*2)
			younger := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 501+i*2)
			for age, id := range []string{older, younger} {
				hash := fmt.Sprintf("started-memory-%d-%d", i, age)
				artifactType := "vod"
				if tc.sourceKind != "" && !tc.awaitAdmission {
					artifactType = "clip"
				}
				if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id)
					VALUES($1,$2::uuid,$4,'processing',$3)`, hash, r9Tenant, cluster, artifactType); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs
					(job_id,tenant_id,artifact_hash,job_type,status,required_memory_bytes,processing_node_id,memory_admitted_at,created_at,source_params)
					VALUES($1::uuid,$2::uuid,$3,$4,'processing',1024,$7,NOW() AT TIME ZONE 'UTC',
					(NOW() AT TIME ZONE 'UTC')-INTERVAL '10 minutes'+($5::int*INTERVAL '1 minute'),
					CASE WHEN $6::text='' THEN NULL ELSE jsonb_build_object('source_kind',$6::text,'source_stream_name','live+source') END)`,
					id, r9Tenant, hash, tc.kind, age, tc.sourceKind, node); err != nil {
					t.Fatal(err)
				}
				changed, err := q.RequeueCapacityDeferredProcessingJob(t.Context(), foghorndb.RequeueCapacityDeferredProcessingJobParams{
					JobID: id, TenantID: r9Tenant, NodeID: node, RequiredMemoryBytes: 1024, MemoryFloorBytes: 1024,
				})
				if err != nil || changed != 1 {
					t.Fatalf("requeue after OOM: changed=%d error=%v", changed, err)
				}
				if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET next_attempt_at=NULL,
					memory_wait_started_at=(NOW() AT TIME ZONE 'UTC')-INTERVAL '90 seconds',
					wait_started_at=(NOW() AT TIME ZONE 'UTC')-INTERVAL '90 seconds'
					WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, id, r9Tenant); err != nil {
					t.Fatal(err)
				}
			}
			claimed, err := q.ClaimQueuedProcessingJobs(t.Context())
			if err != nil || len(claimed) != 2 {
				t.Fatalf("claim OOM retries: jobs=%d error=%v", len(claimed), err)
			}
			reserve := func(id string) int64 {
				t.Helper()
				n, err := q.ReserveProcessingNode(t.Context(), foghorndb.ReserveProcessingNodeParams{
					NodeID: node, RequiredBytes: 1024, JobID: id, TenantID: r9Tenant,
					WaitThreshold: time.Now().UTC().Add(-time.Minute), NodeBudget: 4096,
				})
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			if n := reserve(older); n != 1 {
				t.Fatalf("oldest memory waiter did not reserve node: %d rows", n)
			}
			n, err := q.CommitDispatchedProcessingJob(t.Context(), foghorndb.CommitDispatchedProcessingJobParams{
				JobID: older, TenantID: r9Tenant, ProcessingNodeID: sql.NullString{String: node, Valid: true},
			})
			if err != nil || n != 1 {
				t.Fatalf("start job: %d rows, %v", n, err)
			}
			waiters, err := q.ListProcessingMemoryWaiters(t.Context(), foghorndb.ListProcessingMemoryWaitersParams{
				WaitThreshold: time.Now().UTC().Add(-time.Minute), UploadDeadline: time.Now().UTC().Add(-24 * time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			listed := false
			for _, waiter := range waiters {
				listed = listed || waiter.JobID == older
			}
			if listed != tc.awaitAdmission {
				t.Errorf("running %s memory-waiter eligibility = %v, want %v", tc.kind, listed, tc.awaitAdmission)
			}
			if err := q.ClearFinishedProcessingReservations(t.Context()); err != nil {
				t.Fatal(err)
			}
			reservation, err := q.GetProcessingMemoryReservation(t.Context(), node)
			if err != nil {
				t.Fatal(err)
			}
			if held := reservation.JobID.Valid; held != tc.awaitAdmission {
				t.Errorf("running %s retained node reservation = %v, want %v", tc.kind, held, tc.awaitAdmission)
			}
			want := int64(1)
			if tc.awaitAdmission {
				want = 0
			}
			if n := reserve(younger); n != want {
				t.Errorf("younger waiter reserved %d rows after %s started, want %d", n, tc.kind, want)
			}
		})
	}
}

func TestProcessingMemoryRetryClearsPreviousAdmission_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	q := foghorndb.New(conn)
	const job = "7a900000-0000-4000-8000-000000000599"
	const hash = "readmitted-memory-retry"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id)
		VALUES($1,$2::uuid,'vod','processing','origin')`, hash, r9Tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs
		(job_id,tenant_id,artifact_hash,job_type,status,processing_node_id,memory_admitted_at,memory_reserved_bytes)
		VALUES($1::uuid,$2::uuid,$3,'transcode','processing','edge',NOW(),1024)`, job, r9Tenant, hash); err != nil {
		t.Fatal(err)
	}
	changed, err := q.RequeueCapacityDeferredProcessingJob(t.Context(), foghorndb.RequeueCapacityDeferredProcessingJobParams{
		JobID: job, TenantID: r9Tenant, NodeID: "edge", RequiredMemoryBytes: 2048, MemoryFloorBytes: 2048,
	})
	if err != nil || changed != 1 {
		t.Fatalf("requeue OOM: changed=%d error=%v", changed, err)
	}
	var admitted sql.NullTime
	var reserved int64
	if err := conn.QueryRow(`SELECT memory_admitted_at,memory_reserved_bytes FROM foghorn.processing_jobs
		WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant).Scan(&admitted, &reserved); err != nil {
		t.Fatal(err)
	}
	if admitted.Valid || reserved != 0 {
		t.Errorf("memory retry retained previous admission: admitted=%v reserved=%d", admitted.Valid, reserved)
	}
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET memory_wait_started_at=NOW()-INTERVAL '3 minutes'
		WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant); err != nil {
		t.Fatal(err)
	}
	waiters, err := q.ListProcessingMemoryWaiters(t.Context(), foghorndb.ListProcessingMemoryWaitersParams{
		WaitThreshold: time.Now().UTC().Add(-time.Minute), UploadDeadline: time.Now().UTC().Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(waiters) != 1 || waiters[0].JobID != job {
		t.Fatalf("OOM retry lost reservation eligibility: %+v", waiters)
	}
}
