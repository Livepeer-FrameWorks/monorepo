//go:build schema_verify

package jobs

import (
	"database/sql"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

func TestCatalogProjectionQueueReasonsPreservePageAndTenant_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const tenant = "7a6f0000-0000-4000-8000-000000000080"
	const otherTenant = "7a6f0000-0000-4000-8000-000000000081"
	const cluster = "catalog-queue-test"
	const revisionBase int64 = 4503599627370496
	for i := range 3 {
		hash := fmt.Sprintf("catalogqueue%020d", i)
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts
			(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id,catalog_revision)
			VALUES($1,$2::uuid,'vod','queued',$3,$4)`, hash, tenant, cluster, revisionBase+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	for i, row := range []struct{ tenant, status, reason string }{
		{otherTenant, "queued", "cluster_unavailable"},
		{tenant, "completed", "slots"},
		{tenant, "queued", "memory"},
		{tenant, "queued", "fitting_node_unavailable"},
	} {
		id := fmt.Sprintf("7a6f0000-0000-4000-8000-%012d", 800+i)
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs
			(job_id,tenant_id,artifact_hash,job_type,status,wait_reason,created_at)
			VALUES($1::uuid,$2::uuid,$3,'transcode',$4,$5,
			(NOW() AT TIME ZONE 'UTC')-INTERVAL '1 hour'+($6::int*INTERVAL '1 minute'))`,
			id, row.tenant, "catalogqueue00000000000000000000", row.status, row.reason, i); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := foghorndb.New(conn).ListArtifactsForCatalogProjection(t.Context(), foghorndb.ListArtifactsForCatalogProjectionParams{
		Limit: 2, OriginClusterID: sql.NullString{String: cluster, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].CatalogRevision != revisionBase || rows[1].CatalogRevision != revisionBase+1 {
		t.Fatalf("projection must return two distinct artifacts in revision order: %+v", rows)
	}
	if rows[0].ProcessingWaitReason != "memory" || rows[1].ProcessingWaitReason != "" {
		t.Fatalf("expected oldest queued reason from the owning tenant, then no reason: %+v", rows)
	}
}

func TestProcessingReservationHistoryPurgePreservesClearFence_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const tenant = "7a6f0000-0000-4000-8000-000000000082"
	const job = "7a6f0000-0000-4000-8000-000000000083"
	const hash = "reservationpurge0000000000000001"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status)
		VALUES($1,$2::uuid,'vod','ready')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,updated_at)
		VALUES($1::uuid,$2::uuid,$3,'transcode','completed',(NOW() AT TIME ZONE 'UTC')-INTERVAL '2 hours')`, job, tenant, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_node_reservations(node_id,job_id,tenant_id,required_memory_bytes)
		VALUES('purge-node',$1::uuid,$2::uuid,1024),('clear-node',NULL,NULL,0)`, job, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_node_reservations(node_id,job_id,tenant_id,required_memory_bytes)
		VALUES('duplicate-node',$1::uuid,$2::uuid,1024)`, job, tenant); err == nil {
		t.Fatal("the same job must not reserve two nodes")
	}
	q := foghorndb.New(conn)
	before, err := q.GetProcessingMemoryReservation(t.Context(), "purge-node")
	if err != nil {
		t.Fatal(err)
	}
	n, err := q.PurgeTerminalProcessingJobs(t.Context(), foghorndb.PurgeTerminalProcessingJobsParams{RetentionSeconds: 3600, BatchLimit: 100})
	if err != nil || n != 1 {
		t.Fatalf("purge terminal job: deleted=%d error=%v", n, err)
	}
	after, err := q.GetProcessingMemoryReservation(t.Context(), "purge-node")
	if err != nil || after.JobID.Valid || after.RequiredMemoryBytes != 0 || after.Revision <= before.Revision {
		t.Fatalf("purge must retain a newer clear fence: before=%+v after=%+v error=%v", before, after, err)
	}
	rows, err := q.ListProcessingMemoryReservations(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("both cleared node fences must remain publishable: %+v error=%v", rows, err)
	}
}
