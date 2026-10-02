package handlers

import (
	"testing"
	"time"
)

// A recording that Mist ends with SHM_LOST lost its stream buffer (the buffer
// timed out or was shut down under it); nothing about the media failed. Every
// processing job kind reports the attempt retryable, after the processing
// stream has been stopped.
func TestRecordingEndedWithShmLostIsRetryable(t *testing.T) {
	for _, kind := range recordingGraceKinds {
		t.Run(kind, func(t *testing.T) {
			setRecordingEndGraceForTest(t, 10*time.Second)
			hx := startRecordingGraceJob(t, kind, "shmlost"+kind)
			end := hx.cleanRecordingEnd()
			end.ExitReason = "SHM_LOST"
			end.HumanExitReason = "lost internal connection to stream data"
			SignalProcessingRecordingEnd(end)

			result := hx.awaitResult(t, 45*time.Second)
			if result.GetStatus() != processingResultRetryable {
				t.Fatalf("status = %q (%s), want %q", result.GetStatus(), result.GetError(), processingResultRetryable)
			}
			if !hx.nuked.Load() {
				t.Fatal("the retryable result was reported before the processing stream was stopped")
			}
		})
	}
}

// A recording that Mist ends with a format error failed on the media itself;
// a fresh attempt reads the same input, so the job fails.
func TestRecordingEndedWithFormatErrorStaysFailed(t *testing.T) {
	for _, kind := range recordingGraceKinds {
		t.Run(kind, func(t *testing.T) {
			setRecordingEndGraceForTest(t, 10*time.Second)
			hx := startRecordingGraceJob(t, kind, "formaterr"+kind)
			end := hx.cleanRecordingEnd()
			end.ExitReason = "FORMAT_SPECIFIC"
			end.HumanExitReason = "Inconsistent MP4 input"
			SignalProcessingRecordingEnd(end)

			result := hx.awaitResult(t, 45*time.Second)
			if result.GetStatus() != "failed" {
				t.Fatalf("status = %q (%s), want failed", result.GetStatus(), result.GetError())
			}
		})
	}
}
