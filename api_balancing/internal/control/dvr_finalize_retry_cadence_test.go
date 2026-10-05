package control

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// While a segment stays pending, the finalize loop keeps polling the ledger but
// must not re-send RetryDVRSegmentUpload before the sidecar's previous attempt
// (presign + PUT, bounded by dvr.SegmentRetryUploadTimeout) can have finished.
// Re-sending on every poll stacked a new 30 s PUT of the same object every 2 s.
func TestWaitForOutstandingUploads_DoesNotResendWithinUploadTimeout(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	ensureRegistry(t)
	stream := &captureStream{}
	registry.mu.Lock()
	registry.conns["edge-eu"] = &conn{stream: stream}
	registry.mu.Unlock()

	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 6; i++ {
		mock.ExpectQuery(`FROM foghorn.dvr_segments\s+WHERE artifact_hash = \$1\s+AND status IN \('pending', 'failed_upload'\)`).
			WithArgs("art-1", sqlmock.AnyArg(), 500).
			WillReturnRows(sampleSegmentRow())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	_ = waitForOutstandingUploads(ctx, "art-1", "edge-eu", logging.NewLogger())

	stream.mu.Lock()
	defer stream.mu.Unlock()
	retries := 0
	for _, msg := range stream.sent {
		if _, ok := msg.GetPayload().(*ipcpb.ControlMessage_RetryDvrSegmentUpload); ok {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("RetryDVRSegmentUpload sent %d times within 4.5 s, want 1 (resend only after the upload timeout)", retries)
	}
}
