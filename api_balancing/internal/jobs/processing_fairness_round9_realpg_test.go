//go:build schema_verify

package jobs

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// D3g: once the reserved node disconnects (still registered in Quartermaster),
// the oldest memory waiter must move its protection to a live fitting node.
// Otherwise the protection guards a dead node and younger work can starve the
// waiter on every live node.
func TestR9ReservationLeavesDisconnectedNode_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	useRuntimeProcessingInventory(t)
	oldAccess := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = oldAccess })
	classes := map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}}
	touchInCluster(sm, "fit-small", routeTestCluster)
	setNodeMemory(sm, "fit-small", 8<<30, classes)
	touchInCluster(sm, "fit-large", routeTestCluster)
	setNodeMemory(sm, "fit-large", 16<<30, classes)

	const job, hash = "7a910000-0000-4000-8000-000000000001", "r9fairmove00000000000000000001"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id) VALUES($1,$2::uuid,'vod','queued',$3)`, hash, r9Tenant, routeTestCluster); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,required_memory_bytes,wait_reason,wait_started_at,created_at) VALUES($1::uuid,$2::uuid,$3,'transcode','queued',2147483648,'memory',(NOW() AT TIME ZONE 'UTC')-INTERVAL '40 minutes',(NOW() AT TIME ZONE 'UTC')-INTERVAL '41 minutes')`, job, r9Tenant, hash); err != nil {
		t.Fatal(err)
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
	small, err := q.GetProcessingMemoryReservation(t.Context(), "fit-small")
	if err != nil || small.JobID.String != job {
		t.Fatalf("best-fitting node not reserved first: %+v %v", small, err)
	}
	sm.MarkNodeDisconnected("fit-small")
	refresh()
	large, err := q.GetProcessingMemoryReservation(t.Context(), "fit-large")
	if err != nil || large.JobID.String != job {
		small, _ = q.GetProcessingMemoryReservation(t.Context(), "fit-small")
		t.Fatalf("protection stayed on the disconnected node: small=%+v large=%+v err=%v", small, large, err)
	}
}
