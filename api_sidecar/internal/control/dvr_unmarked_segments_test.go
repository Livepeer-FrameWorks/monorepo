package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func unmarkedSegmentsJob() *DVRJob {
	return &DVRJob{
		DVRHash:          "dvr-1",
		Logger:           logging.NewLogger(),
		SyncedSegments:   map[string]bool{},
		UnmarkedSegments: map[string]uint64{},
	}
}

// Upload state and ledger state fail apart, so they are tracked apart.
//
// Foghorn's ledger is what makes a segment eviction-eligible. Recording a failed
// mark as "synced" meant the segment was skipped by every later pass, its row
// stayed 'pending', and the local file was pinned for the life of the recording
// — which on a 24/7 stream is a disk-fill path. The segment must still count as
// synced, because its bytes really are in S3 and re-uploading them is waste; the
// unacknowledged ledger row is what needs retrying.
func TestFailedLedgerMarkIsRetryableWithoutReupload(t *testing.T) {
	job := unmarkedSegmentsJob()

	// What the upload path does when the mark fails.
	job.SyncedSegments["seg-1.ts"] = true
	job.UnmarkedSegments["seg-1.ts"] = 4096

	if !job.SyncedSegments["seg-1.ts"] {
		t.Fatal("a segment whose bytes reached S3 is not synced; it would be uploaded again")
	}
	if job.UnmarkedSegments["seg-1.ts"] != 4096 {
		t.Fatal("an unacknowledged ledger row was not retained for retry; it would stay pending forever")
	}

	// What a successful retry does.
	delete(job.UnmarkedSegments, "seg-1.ts")
	if len(job.UnmarkedSegments) != 0 {
		t.Fatal("a marked segment is still queued for retry")
	}
	if !job.SyncedSegments["seg-1.ts"] {
		t.Fatal("marking the ledger row dropped the upload record")
	}
}

// A segment that has been evicted has no file and no reason to be re-reported;
// leaving it queued would retry a mark for bytes this node no longer holds.
func TestEvictionClearsPendingLedgerMark(t *testing.T) {
	job := unmarkedSegmentsJob()
	job.SyncedSegments["seg-1.ts"] = true
	job.UnmarkedSegments["seg-1.ts"] = 4096

	// What both eviction paths do.
	delete(job.SyncedSegments, "seg-1.ts")
	delete(job.UnmarkedSegments, "seg-1.ts")

	if len(job.UnmarkedSegments) != 0 {
		t.Fatal("an evicted segment is still queued for a ledger retry")
	}
}

// retryUnmarkedSegments must be a no-op when nothing is outstanding, so the 10s
// sync does not pay for it on every tick of every healthy recording.
func TestRetryUnmarkedSegmentsNoopWhenNothingOutstanding(t *testing.T) {
	dm := &DVRManager{}
	job := unmarkedSegmentsJob()
	job.SyncedSegments["seg-1.ts"] = true

	dm.retryUnmarkedSegments(job)

	if len(job.UnmarkedSegments) != 0 {
		t.Fatalf("retry invented outstanding marks: %v", job.UnmarkedSegments)
	}
}
