package handlers

import "testing"

// CountPendingJobs backs the video_transcode slots_used a node reports to
// Foghorn, so it must reflect the live in-flight processing-job count.
func TestCountPendingJobs(t *testing.T) {
	pendingJobsMu.Lock()
	saved := pendingJobs
	pendingJobs = map[string]chan ProcessingPushEndEvent{}
	pendingJobsMu.Unlock()
	t.Cleanup(func() {
		pendingJobsMu.Lock()
		pendingJobs = saved
		pendingJobsMu.Unlock()
	})

	if n := CountPendingJobs(); n != 0 {
		t.Fatalf("empty: want 0, got %d", n)
	}

	pendingJobsMu.Lock()
	pendingJobs["processing+a"] = make(chan ProcessingPushEndEvent, 1)
	pendingJobs["processing+b"] = make(chan ProcessingPushEndEvent, 1)
	pendingJobsMu.Unlock()

	if n := CountPendingJobs(); n != 2 {
		t.Fatalf("two in-flight: want 2, got %d", n)
	}
}
