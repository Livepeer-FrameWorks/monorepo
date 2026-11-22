//go:build schema_verify

package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// D3a: a job whose memory wait began forty minutes ago is requeued again on
// the next memory_pressure refusal, without failing or spending retries.
func TestR9FortyMinuteMemoryWaitStaysQueued_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	seedActiveImportJob(t, conn)
	column := "wait_started_at"
	var legacy bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='foghorn' AND table_name='processing_jobs' AND column_name='capacity_wait_started_at')`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy {
		column = "capacity_wait_started_at"
	}
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET `+column+`=NOW()-INTERVAL '40 minutes', started_at=NOW()-INTERVAL '40 minutes' WHERE job_id=$1::uuid`, processingFailureJob); err != nil {
		t.Fatal(err)
	}
	if err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "retryable", Outputs: map[string]string{mist.ProcessingResultTenantID: processingFailureTenant, mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryPressure, mist.ProcessingRequiredMemoryBytes: "2147483648"}}, "edge-1", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	var status string
	var retries int
	if err := conn.QueryRow(`SELECT status,retry_count FROM foghorn.processing_jobs WHERE job_id=$1::uuid`, processingFailureJob).Scan(&status, &retries); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || retries != 0 {
		t.Fatalf("forty-minute memory wait: status=%s retries=%d", status, retries)
	}
}
