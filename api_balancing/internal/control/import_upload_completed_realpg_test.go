//go:build schema_verify

package control

import (
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A URL import's upload.completed fires once, when its processing node
// reports the staged source, and carries the source's size. Reports from
// another node, without a size, repeated, or for an uploaded (not imported)
// VOD emit nothing.
func TestImportUploadCompletedWhenSourceStaged_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	const (
		tenant     = "7a1e0000-0000-4000-8000-0000000000a1"
		importHash = "importstagedhash0000000000000001"
		importJob  = "7a1e0000-0000-4000-8000-0000000000a2"
		uploadHash = "uploadstagedhash0000000000000001"
		uploadJob  = "7a1e0000-0000-4000-8000-0000000000a3"
		sourceSize = int64(734003200)
	)
	for _, seed := range []struct{ hash, job, source string }{
		{importHash, importJob, "https://cdn.example/talk.mp4"},
		{uploadHash, uploadJob, ""},
	} {
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status)
			VALUES ($1, 'vod', $2::uuid, 'processing')`, seed.hash, tenant); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.vod_metadata (artifact_hash, filename, source_url)
			VALUES ($1, 'talk.mp4', NULLIF($2, ''))`, seed.hash, seed.source); err != nil {
			t.Fatalf("seed vod metadata: %v", err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, processing_node_id)
			VALUES ($1::uuid, $2::uuid, $3, 'process', 'processing', 'edge-1')`, seed.job, tenant, seed.hash); err != nil {
			t.Fatalf("seed job: %v", err)
		}
	}
	completedFor := func(hash string) []domainRow {
		var out []domainRow
		for _, row := range domainRows(t, conn, "upload.completed") {
			if row.aggregateID == hash {
				out = append(out, row)
			}
		}
		return out
	}
	logger := logging.NewLogger()

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: importJob, SourceSizeBytes: sourceSize}, "edge-2", logger)
	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: importJob, ProgressPct: 5}, "edge-1", logger)
	if got := completedFor(importHash); len(got) != 0 {
		t.Fatalf("upload.completed emitted %d times before the owning node reported a staged source", len(got))
	}

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: importJob, SourceSizeBytes: sourceSize}, "edge-1", logger)
	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: importJob, ProgressPct: 40, SourceSizeBytes: sourceSize}, "edge-1", logger)
	rows := completedFor(importHash)
	if len(rows) != 1 {
		t.Fatalf("upload.completed rows = %d, want exactly 1", len(rows))
	}
	_, msg, err := events.Decode("upload.completed", rows[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	completed, ok := msg.(*publicv1.UploadCompleted)
	if !ok || completed.GetSizeBytes() != sourceSize || completed.GetArtifact().GetArtifactId() != importHash {
		t.Fatalf("upload.completed = %+v, want size %d for %s", msg, sourceSize, importHash)
	}
	var size sql.NullInt64
	if err := conn.QueryRow(`SELECT size_bytes FROM foghorn.artifacts WHERE artifact_hash = $1`, importHash).Scan(&size); err != nil {
		t.Fatal(err)
	}
	if size.Int64 != sourceSize {
		t.Fatalf("import size_bytes = %v, want %d", size, sourceSize)
	}

	processProcessingJobProgress(&ipcpb.ProcessingJobProgress{JobId: uploadJob, SourceSizeBytes: sourceSize}, "edge-1", logger)
	if got := completedFor(uploadHash); len(got) != 0 {
		t.Fatalf("uploaded VOD got %d upload.completed from staging; its upload completed at CompleteVodUpload", len(got))
	}
}
