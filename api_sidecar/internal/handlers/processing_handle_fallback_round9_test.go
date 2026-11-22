package handlers

import (
	"testing"
	"time"
)

func TestHandleFallbackTearsDownOnce(t *testing.T) {
	for _, phase := range []string{"readiness", "push"} {
		t.Run(phase, func(t *testing.T) {
			hx := startRecordingGraceJob(t, "vod", "handle-fallback-"+phase, recordingGeometry{
				width: 640, height: 360, sourceEnd: 10000, fallbackPhase: phase,
				processes: `[{"process":"Livepeer","target_profiles":[{"name":"180p","profile":"H264ConstrainedHigh","height":180,"bitrate":300000}]}]`,
			})
			if got := hx.nukeCount.Load(); got != 1 {
				t.Errorf("%s fallback nuked its generation %d times, want exactly once", phase, got)
			}
			// End the fake local generation after observing the caller, so terminal cleanup is separate.
			RouteProcessExit(ProcessExitEvent{StreamName: hx.streamName, ProcessType: "AV", Config: `{"codec":"H264"}`, ExitCode: 2, BootCount: 1, Status: "unrecoverable", Reason: "fixture finished"})
			if result := hx.awaitResult(t, 10*time.Second); result.GetStatus() != "failed" {
				t.Fatalf("fake local generation did not terminate: %+v", result)
			}
		})
	}
}
