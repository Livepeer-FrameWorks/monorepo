//go:build schema_verify

package control

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
)

func TestR9SoftEstimateMayShrinkWithoutLosingOOMFloor_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	seedActiveImportJob(t, conn)
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET required_memory_bytes=4294967296 WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant); err != nil {
		t.Fatal(err)
	}
	report := func(required, floor string, want int64) {
		t.Helper()
		if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET status='processing',processing_node_id='edge-1' WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant); err != nil {
			t.Fatal(err)
		}
		outputs := map[string]string{mist.ProcessingResultTenantID: processingFailureTenant, mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryPressure, mist.ProcessingRequiredMemoryBytes: required}
		if floor != "" {
			outputs["oom_required_memory_bytes"] = floor
		}
		if err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "retryable", Error: "memory pressure", Outputs: outputs}, "edge-1", logging.NewLogger()); err != nil {
			t.Fatal(err)
		}
		var actual int64
		if err := conn.QueryRow(`SELECT required_memory_bytes FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != want {
			t.Fatalf("soft estimate or hard OOM floor lost: required=%s floor=%s actual=%d want=%d", required, floor, actual, want)
		}
	}
	report("1073741824", "", 1073741824)
	report("2147483648", "2147483648", 2147483648)
	report("1073741824", "", 2147483648)
}
