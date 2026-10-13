package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// A clip recording that Mist ends with PROCESS_TRACKS_CHANGED (a thumbnail track
// that appeared after the recording header) is reported retryable, so Foghorn
// requeues the clip within its retry budget instead of failing it, and the
// report waits for the nuked processing stream to stop.
func TestClipRecordingWithChangedProcessTracksIsRetryable(t *testing.T) {
	hx := startRecordingGraceJob(t, "clip", "clipretryhash")
	end := hx.cleanRecordingEnd()
	end.BytesWritten = 993988
	end.MediaDurationMs = 9867
	end.ExitReason = mist.ExitReasonProcessTracksChanged
	end.HumanExitReason = "EBML track 6 (JPEG) was not declared in the recording header; declared: 0 1 3 4"
	SignalProcessingRecordingEnd(end)

	result := hx.awaitResult(t, 60*time.Second)
	if result.GetStatus() != processingResultRetryable {
		t.Fatalf("clip result status = %q (%s), want %q", result.GetStatus(), result.GetError(), processingResultRetryable)
	}
	if !hx.nuked.Load() {
		t.Fatal("the retryable result was reported before the processing stream was stopped")
	}
}
