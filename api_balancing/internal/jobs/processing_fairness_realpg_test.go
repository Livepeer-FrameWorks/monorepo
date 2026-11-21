//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestOldestMemoryWaiterHoldsNodeUntilActualAdmission_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	useRuntimeProcessingInventory(t)
	oldAccess := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = oldAccess })
	touchInCluster(sm, "fair-edge", routeTestCluster)
	setNodeMemory(sm, "fair-edge", 4<<30, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	const tenant = "7a6f0000-0000-4000-8000-000000000002"
	ids := []string{"7a6f0000-0000-4000-8000-000000000010", "7a6f0000-0000-4000-8000-000000000011"}
	for i, id := range ids {
		hash := fmt.Sprintf("fairupload%022d", i)
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id) VALUES($1,$2::uuid,'vod','queued',$3)`, hash, tenant, routeTestCluster); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,required_memory_bytes,wait_reason,wait_started_at,created_at) VALUES($1::uuid,$2::uuid,$3,'transcode','queued',2147483648,'memory',(NOW() AT TIME ZONE 'UTC')-INTERVAL '40 minutes',(NOW() AT TIME ZONE 'UTC')-INTERVAL '50 minutes'+($4::int*INTERVAL '1 minute'))`, id, tenant, hash, i); err != nil {
			t.Fatal(err)
		}
	}
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
	refresh := func() {
		t.Helper()
		if err := d.refreshProcessingReservations(t.Context(), newProcessingTickInventory(true)); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	q := foghorndb.New(conn)
	row, err := q.GetProcessingMemoryReservation(t.Context(), "fair-edge")
	if err != nil {
		t.Fatal(err)
	}
	if row.JobID.String != ids[0] || row.RequiredMemoryBytes != 2<<30 {
		t.Fatalf("oldest waiter not protected: %+v", row)
	}
	// Dispatch alone must not free the hold for a younger job.
	if _, err = conn.Exec(`UPDATE foghorn.processing_jobs SET status='dispatched',processing_node_id='fair-edge' WHERE job_id=$1 AND tenant_id=$2::uuid`, ids[0], tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = q.CacheProcessingJobDispatchPolicy(t.Context(), foghorndb.CacheProcessingJobDispatchPolicyParams{ProcessesJson: sql.NullString{String: "[]", Valid: true}, JobID: ids[0], TenantID: tenant, NodeID: sql.NullString{String: "fair-edge", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	refresh()
	row, err = q.GetProcessingMemoryReservation(t.Context(), "fair-edge")
	if err != nil || row.JobID.String != ids[0] {
		t.Fatalf("dispatch released protection before memory acquisition: %+v %v", row, err)
	}
	firstVersion := row.Revision
	// Admission on any node cancels the original protection. The next oldest
	// waiter inherits the node; no waiting job spent a retry during 40 minutes.
	if _, err = conn.Exec(`UPDATE foghorn.processing_jobs SET memory_admitted_at=NOW(),memory_reserved_bytes=2147483648,wait_reason=NULL WHERE job_id=$1 AND tenant_id=$2::uuid`, ids[0], tenant); err != nil {
		t.Fatal(err)
	}
	refresh()
	row, err = q.GetProcessingMemoryReservation(t.Context(), "fair-edge")
	if err != nil || row.JobID.String != ids[1] || row.Revision <= firstVersion {
		t.Fatalf("admission did not advance fair owner: %+v %v", row, err)
	}
	var retries int
	if err = conn.QueryRow(`SELECT COALESCE(sum(retry_count),0) FROM foghorn.processing_jobs WHERE tenant_id=$1::uuid`, tenant).Scan(&retries); err != nil {
		t.Fatal(err)
	}
	if retries != 0 {
		t.Fatalf("waiting spent tenant retries: %d", retries)
	}
	// Terminal completion produces a versioned clear rather than deleting the
	// revision fence, so delayed deliveries cannot reinstate the old hold.
	if _, err = conn.Exec(`UPDATE foghorn.processing_jobs SET status='completed' WHERE job_id=$1 AND tenant_id=$2::uuid`, ids[1], tenant); err != nil {
		t.Fatal(err)
	}
	refresh()
	clear, err := q.GetProcessingMemoryReservation(t.Context(), "fair-edge")
	if err != nil || clear.JobID.Valid || clear.RequiredMemoryBytes != 0 || clear.Revision <= row.Revision {
		t.Fatalf("completion lost the clear fence: %+v %v", clear, err)
	}
}

func TestProcessingClaimKeepsFIFOAcrossArtifactTypes_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	q := foghorndb.New(conn)
	const tenant = "7a6f0000-0000-4000-8000-000000000099"
	for i, kind := range []string{"vod", "clip"} {
		hash := fmt.Sprintf("fifoupload%022d", i)
		id := fmt.Sprintf("7a6f0000-0000-4000-8000-%012d", 100+i)
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status) VALUES($1,$2::uuid,$3,'queued')`, hash, tenant, kind); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,created_at) VALUES($1::uuid,$2::uuid,$3,'transcode','queued',NOW()-INTERVAL '1 hour'+($4::int*INTERVAL '1 minute'))`, id, tenant, hash, i); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := q.ClaimQueuedProcessingJobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ArtifactType != "vod" || jobs[1].ArtifactType != "clip" {
		t.Fatalf("younger clip displaced older upload: %+v", jobs)
	}
}
