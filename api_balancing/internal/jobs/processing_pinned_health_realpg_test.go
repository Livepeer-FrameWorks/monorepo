//go:build schema_verify

package jobs

import (
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestPinnedSourceHealthFailureKeepsQueuedJob_RealPG(t *testing.T) {
	for i, kind := range []string{"live", "dvr_rolling"} {
		t.Run(kind, func(t *testing.T) {
			conn := startRealPGForCleanup(t)
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			touchInCluster(sm, "source", routeTestCluster)
			sm.TouchNode("source", false)
			hash := fmt.Sprintf("pinunhealthy%020d", i)
			job := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 501+i)
			if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status) VALUES($1,$2::uuid,'clip','queued')`, hash, r9Tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,preferred_node_id,source_params) VALUES($1::uuid,$2::uuid,$3,'clip','queued','source',jsonb_build_object('source_kind',$4::text))`, job, r9Tenant, hash, kind); err != nil {
				t.Fatal(err)
			}
			d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
			d.recoverStale()
			var status string
			if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "queued" {
				t.Fatalf("connected source with failed health poll made pinned job %q", status)
			}
		})
	}
}
