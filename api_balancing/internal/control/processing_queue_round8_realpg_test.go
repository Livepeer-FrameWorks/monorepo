//go:build schema_verify

package control

import (
	"database/sql"
	"frameworks/api_balancing/internal/database/foghorndb"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestMemoryDeferralDoesNotExpireAfterFortyMinutes_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	seedActiveImportJob(t, conn)
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET wait_started_at=NOW()-INTERVAL '40 minutes' WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant); err != nil {
		t.Fatal(err)
	}
	err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "retryable", Outputs: map[string]string{mist.ProcessingResultTenantID: processingFailureTenant, mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryPressure, mist.ProcessingRequiredMemoryBytes: "2147483648"}}, "edge-1", logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var retries int
	if err = conn.QueryRow(`SELECT status,retry_count FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&status, &retries); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || retries != 0 {
		t.Fatalf("forty-minute memory wait became terminal or spent retries: status=%s retries=%d", status, retries)
	}
}

func TestQueueDeferralCannotCrossTenant_RealPG(t *testing.T) {
	conn := startRealPG(t)
	seedActiveImportJob(t, conn)
	if err := foghorndb.New(conn).DeferUnroutableProcessingJob(t.Context(), foghorndb.DeferUnroutableProcessingJobParams{JobID: processingFailureJob, TenantID: "7a1e0000-0000-4000-8000-000000000099", WaitReason: "slots", DeferSeconds: 15}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "processing" {
		t.Fatalf("another tenant deferred the assigned job: %s", status)
	}
}

func TestProcessingAdmissionInvalidatesQueuedCatalogReason_RealPG(t *testing.T) {
	conn := startRealPG(t)
	seedActiveImportJob(t, conn)
	if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET wait_reason='memory',wait_started_at=NOW() AT TIME ZONE 'UTC' WHERE job_id=$1 AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := conn.QueryRow(`SELECT catalog_revision FROM foghorn.artifacts WHERE artifact_hash=$1 AND tenant_id=$2::uuid`, processingFailureHash, processingFailureTenant).Scan(&before); err != nil {
		t.Fatal(err)
	}
	changed, err := foghorndb.New(conn).UpdateProcessingJobCache(t.Context(), foghorndb.UpdateProcessingJobCacheParams{JobID: processingFailureJob, ArtifactHash: sql.NullString{String: processingFailureHash, Valid: true}, ProcessesJson: sql.NullString{String: "[]", Valid: true}, ProcessingNodeID: sql.NullString{String: "edge-1", Valid: true}, TenantID: processingFailureTenant, MemoryReservedBytes: 128 << 20, RequiredMemoryBytes: 128 << 20})
	if err != nil || changed != 1 {
		t.Fatalf("admit job: changed=%d error=%v", changed, err)
	}
	var after int64
	if err = conn.QueryRow(`SELECT catalog_revision FROM foghorn.artifacts WHERE artifact_hash=$1 AND tenant_id=$2::uuid`, processingFailureHash, processingFailureTenant).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("admitted job left a stale queued reason in the catalog: revision %d -> %d", before, after)
	}
}

func TestAdmissionInfrastructureOutagePreservesRetries_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	seedActiveImportJob(t, conn)
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := conn.Exec(`UPDATE foghorn.processing_jobs SET status='processing',processing_node_id='edge-1' WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant); err != nil {
			t.Fatal(err)
		}
		err := processProcessingJobResult(&ipcpb.ProcessingJobResult{JobId: processingFailureJob, Status: "retryable", Error: "processing memory ledger temporarily unavailable", Outputs: map[string]string{mist.ProcessingResultTenantID: processingFailureTenant, mist.ProcessingResultRetryCause: "processing_node_unavailable"}}, "edge-1", logging.NewLogger())
		if err != nil {
			t.Fatal(err)
		}
		var status, reason string
		var retries int
		var required int64
		if err = conn.QueryRow(`SELECT status,COALESCE(wait_reason,''),retry_count,required_memory_bytes FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, processingFailureJob, processingFailureTenant).Scan(&status, &reason, &retries, &required); err != nil {
			t.Fatal(err)
		}
		if status != "queued" || reason != "fitting_node_unavailable" || retries != 0 || required != 0 {
			t.Fatalf("outage spent tenant retries or rejected node: status=%s reason=%s retries=%d required=%d", status, reason, retries, required)
		}
	}
}
