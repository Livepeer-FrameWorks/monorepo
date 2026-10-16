//go:build schema_verify

package control

import (
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const (
	thumbnailJobTenant = "7a1e0000-0000-4000-8000-0000000000a1"
	thumbnailJobID     = "7a1e0000-0000-4000-8000-0000000000a2"
	thumbnailJobHash   = "thumbjobvod000000000000000000001"
)

// A thumbnail job runs on a published VOD. Its progress and its completion, which carries no media output, leave
// the VOD ready and synced and emit no lifecycle event; the job itself completes.
func TestThumbnailJobLeavesPublishedAssetUntouched_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location, format)
		VALUES ($1, 'vod', $2::uuid, 'ready', 'synced', 's3', 'mkv')`, thumbnailJobHash, thumbnailJobTenant); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, processing_node_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'processing', 'edge-1')`, thumbnailJobID, thumbnailJobTenant, thumbnailJobHash, mist.ProcessingJobTypeThumbnail); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	logger := logging.NewLogger()

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: thumbnailJobID, ProgressPct: 40, LastMs: 4000}, "edge-1", logger)
	var progress int
	if err := conn.QueryRow(`SELECT COALESCE(progress, 0) FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, thumbnailJobID).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	if progress != 40 {
		t.Fatalf("job progress = %d, want 40", progress)
	}

	if err := processProcessingJobResult(&ipcpb.ProcessingJobResult{
		JobId: thumbnailJobID, Status: "completed", Outputs: map[string]string{"thumbnails": "uploaded"},
	}, "edge-1", logger); err != nil {
		t.Fatalf("completion: %v", err)
	}

	var jobStatus, jobErr, artifactStatus, syncStatus, location string
	if err := conn.QueryRow(`SELECT status, COALESCE(error_message, '') FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, thumbnailJobID).Scan(&jobStatus, &jobErr); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT status, sync_status, storage_location FROM foghorn.artifacts WHERE artifact_hash = $1`, thumbnailJobHash).Scan(&artifactStatus, &syncStatus, &location); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "completed" {
		t.Fatalf("thumbnail job = %q (%q), want completed", jobStatus, jobErr)
	}
	if artifactStatus != "ready" || syncStatus != "synced" || location != "s3" {
		t.Fatalf("artifact = %s/%s/%s, want ready/synced/s3", artifactStatus, syncStatus, location)
	}
	assertNoArtifactEvents(t, conn, thumbnailJobHash)
	var nodeCopies int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM foghorn.artifact_nodes WHERE artifact_hash = $1`, thumbnailJobHash).Scan(&nodeCopies); err != nil {
		t.Fatal(err)
	}
	if nodeCopies != 0 {
		t.Fatalf("thumbnail job registered %d node copies of the asset", nodeCopies)
	}
}

func assertNoArtifactEvents(t *testing.T, conn *sql.DB, hash string) {
	t.Helper()
	var events int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM foghorn.artifact_event_outbox WHERE artifact_id = $1`, hash).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("thumbnail job emitted %d artifact lifecycle events", events)
	}
}
