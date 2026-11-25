//go:build schema_verify

package jobs

import (
	"database/sql"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestPinnedSourceDisconnectDoesNotFailQueuedClips_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	useRuntimeProcessingInventory(t)
	for i, kind := range []string{"live", "dvr_rolling"} {
		for j, path := range []string{"dispatch", "recovery"} {
			t.Run(kind+"/"+path, func(t *testing.T) {
				sm := state.ResetDefaultManagerForTests()
				t.Cleanup(sm.Shutdown)
				touchInCluster(sm, "source", routeTestCluster)
				hash := fmt.Sprintf("pin-reconnect-%d-%d", i, j)
				job := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 700+i*2+j)
				source := fmt.Sprintf(`{"source_kind":%q,"source_stream_name":"live+source"}`, kind)
				if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id)
					VALUES($1,$2::uuid,'clip','queued',$3)`, hash, r9Tenant, routeTestCluster); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,preferred_node_id,source_params)
					VALUES($1::uuid,$2::uuid,$3,'process','queued','source',$4::jsonb)`, job, r9Tenant, hash, source); err != nil {
					t.Fatal(err)
				}
				sm.MarkNodeDisconnected("source")
				d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
				if path == "dispatch" {
					if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET status='dispatched' WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant); err != nil {
						t.Fatal(err)
					}
					d.dispatchJob(t.Context(), &processingJob{
						JobID: job, TenantID: r9Tenant, JobType: "process", OriginCluster: routeTestCluster,
						PreferredNode: sql.NullString{String: "source", Valid: true}, SourceParams: sql.NullString{String: source, Valid: true},
					})
				} else {
					d.recoverStale()
				}
				var status string
				var retries int
				if err := conn.QueryRow(`SELECT status,retry_count FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant).Scan(&status, &retries); err != nil {
					t.Fatal(err)
				}
				if status != "queued" || retries != 0 {
					t.Fatalf("transient %s source disconnect at %s: status=%s retries=%d, want queued without retries", kind, path, status, retries)
				}
				// A reconnect must leave the same job eligible for the next claim.
				sm.TouchNode("source", true)
				if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET next_attempt_at=NULL WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant); err != nil {
					t.Fatal(err)
				}
				claimed, err := foghorndb.New(conn).ClaimQueuedProcessingJobs(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, row := range claimed {
					found = found || row.JobID == job
				}
				if !found {
					t.Fatal("reconnected source's clip was not claimable")
				}
			})
		}
	}
}
