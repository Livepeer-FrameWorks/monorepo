package handlers

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func isolatePendingJobs(t *testing.T) {
	t.Helper()
	pendingJobsMu.Lock()
	savedJobs, savedIDs := pendingJobs, pendingJobIDs
	savedCancels, savedReleased := pendingJobCancels, pendingJobReleased
	pendingJobs = map[string]chan ProcessingPushEndEvent{}
	pendingJobIDs = map[string]string{}
	pendingJobCancels = map[string]context.CancelFunc{}
	pendingJobReleased = map[string]chan struct{}{}
	pendingJobsMu.Unlock()
	t.Cleanup(func() {
		pendingJobsMu.Lock()
		pendingJobs, pendingJobIDs = savedJobs, savedIDs
		pendingJobCancels, pendingJobReleased = savedCancels, savedReleased
		pendingJobsMu.Unlock()
	})
}

// CountPendingJobs backs the video_transcode slots_used a node reports to
// Foghorn, so it must reflect the live in-flight processing-job count.
func TestCountPendingJobs(t *testing.T) {
	isolatePendingJobs(t)

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

func TestClaimPendingJobIsAtomicAndAttemptBound(t *testing.T) {
	isolatePendingJobs(t)

	if _, existing, ok := claimPendingJob("processing+chapter", "chapter-finalize-v2-1-c"); !ok || existing != "" {
		t.Fatalf("first claim = ok:%v existing:%q", ok, existing)
	}
	if _, existing, ok := claimPendingJob("processing+chapter", "chapter-finalize-v2-2-c"); ok || existing != "chapter-finalize-v2-1-c" {
		t.Fatalf("competing claim = ok:%v existing:%q", ok, existing)
	}
	releasePendingJob("processing+chapter", "chapter-finalize-v2-2-c")
	if !HasPendingJob("processing+chapter") {
		t.Fatal("a stale attempt released the active job")
	}
	releasePendingJob("processing+chapter", "chapter-finalize-v2-1-c")
	if HasPendingJob("processing+chapter") {
		t.Fatal("owner release left the stream reserved")
	}
}

func TestClaimPendingJobConcurrentSingleOwner(t *testing.T) {
	isolatePendingJobs(t)

	const contenders = 32
	start := make(chan struct{})
	owners := make(chan string, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(jobID string) {
			defer wg.Done()
			<-start
			if _, _, ok := claimPendingJob("processing+same-artifact", jobID); ok {
				owners <- jobID
			}
		}(fmt.Sprintf("job-%d", i))
	}
	close(start)
	wg.Wait()
	close(owners)
	ownerCount := 0
	ownerID := ""
	for id := range owners {
		ownerCount++
		ownerID = id
	}
	if ownerCount != 1 {
		t.Fatalf("concurrent claims produced %d owners, want 1", ownerCount)
	}
	releasePendingJob("processing+same-artifact", ownerID)
}

func TestReplacePendingJobChannelRetainsOwner(t *testing.T) {
	isolatePendingJobs(t)
	first, _, ok := claimPendingJob("processing+vod", "job-1")
	if !ok {
		t.Fatal("initial claim failed")
	}
	replacement, replaced := replacePendingJobChannel("processing+vod", "job-1")
	if !replaced || replacement == first {
		t.Fatalf("replacement = %p replaced=%v, want a new owned channel", replacement, replaced)
	}
	if _, replaced := replacePendingJobChannel("processing+vod", "job-2"); replaced {
		t.Fatal("foreign job replaced the active channel")
	}
}

func TestSupersedePendingChapterJobCancelsOlderAttempt(t *testing.T) {
	isolatePendingJobs(t)
	oldCtx, oldCancel := context.WithCancel(context.Background())
	if _, _, ok := claimPendingJob("processing+chapter", "chapter-finalize-v2-1-c", oldCancel); !ok {
		t.Fatal("old attempt claim failed")
	}
	go func() {
		<-oldCtx.Done()
		releasePendingJob("processing+chapter", "chapter-finalize-v2-1-c")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	duplicate, err := supersedePendingChapterJob(ctx, "processing+chapter", "chapter-finalize-v2-2-c")
	if err != nil || duplicate {
		t.Fatalf("supersede = duplicate:%v err:%v", duplicate, err)
	}
	if HasPendingJob("processing+chapter") {
		t.Fatal("older attempt remained reserved after cancellation")
	}
}

func TestSupersedePendingChapterJobCancelsLegacyOwner(t *testing.T) {
	isolatePendingJobs(t)
	oldCtx, oldCancel := context.WithCancel(context.Background())
	if _, _, ok := claimPendingJob("processing+chapter", "chapter-finalize-c", oldCancel); !ok {
		t.Fatal("legacy attempt claim failed")
	}
	go func() {
		<-oldCtx.Done()
		releasePendingJob("processing+chapter", "chapter-finalize-c")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	duplicate, err := supersedePendingChapterJob(ctx, "processing+chapter", "chapter-finalize-v2-2-c")
	if err != nil || duplicate {
		t.Fatalf("legacy supersede = duplicate:%v err:%v", duplicate, err)
	}
	if HasPendingJob("processing+chapter") {
		t.Fatal("legacy attempt remained reserved after cancellation")
	}
}

func TestSupersedePendingChapterJobAcceptsLegacyDispatchOnIdleStream(t *testing.T) {
	isolatePendingJobs(t)
	duplicate, err := supersedePendingChapterJob(context.Background(), "processing+chapter", "chapter-finalize-c")
	if err != nil || duplicate {
		t.Fatalf("legacy idle dispatch = duplicate:%v err:%v, want claimable", duplicate, err)
	}
	if _, existing, claimed := claimPendingJob("processing+chapter", "chapter-finalize-c"); !claimed || existing != "" {
		t.Fatalf("legacy idle claim = claimed:%v existing:%q", claimed, existing)
	}
}

func TestSupersedePendingChapterJobRejectsLegacyDispatchOverActiveJob(t *testing.T) {
	isolatePendingJobs(t)
	if _, _, claimed := claimPendingJob("processing+chapter", "chapter-finalize-v2-2-c"); !claimed {
		t.Fatal("active attempt claim failed")
	}
	duplicate, err := supersedePendingChapterJob(context.Background(), "processing+chapter", "chapter-finalize-c")
	if err == nil || duplicate {
		t.Fatalf("legacy supersede = duplicate:%v err:%v, want rejected", duplicate, err)
	}
}

func TestSupersedePendingChapterJobRecognizesDuplicateLegacyDispatch(t *testing.T) {
	isolatePendingJobs(t)
	const jobID = "chapter-finalize-c"
	if _, _, claimed := claimPendingJob("processing+chapter", jobID); !claimed {
		t.Fatal("legacy attempt claim failed")
	}
	duplicate, err := supersedePendingChapterJob(context.Background(), "processing+chapter", jobID)
	if err != nil || !duplicate {
		t.Fatalf("legacy duplicate = duplicate:%v err:%v, want recognized", duplicate, err)
	}
}

func TestProcessingReporterRenewsLatestProgressAndStopsAtResult(t *testing.T) {
	var mu sync.Mutex
	var messages []*ipcpb.ControlMessage
	reporter := newProcessingReporter(func(msg *ipcpb.ControlMessage) {
		mu.Lock()
		messages = append(messages, msg)
		mu.Unlock()
	}, "job-lease")
	stop := reporter.StartLease(time.Hour)
	reporter.Send(processingProgressMessage("job-lease", 42, 4200, 10000))
	reporter.Send(processingProgressMessage("job-lease", 0, 0, 0))
	reporter.renewLease(time.Hour, true)
	reporter.Send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ProcessingJobResult{
		ProcessingJobResult: &ipcpb.ProcessingJobResult{JobId: "job-lease", Status: "completed"},
	}})
	reporter.renewLease(time.Hour, true)
	stop()

	mu.Lock()
	defer mu.Unlock()
	terminalIndex := -1
	heartbeatWithLatest := false
	seenForwardProgress := false
	for i, msg := range messages {
		if progress := msg.GetProcessingJobProgress(); progress != nil {
			if terminalIndex >= 0 {
				t.Fatalf("progress emitted after terminal result at indexes %d and %d", terminalIndex, i)
			}
			if progress.GetProgressPct() == 42 && progress.GetLastMs() == 4200 && progress.GetSourceDurationMs() == 10000 && i > 1 {
				heartbeatWithLatest = true
			}
			if progress.GetProgressPct() == 42 {
				seenForwardProgress = true
			} else if seenForwardProgress {
				t.Fatalf("progress regressed after reaching 42: %+v", progress)
			}
		}
		if msg.GetProcessingJobResult() != nil {
			terminalIndex = i
		}
	}
	if terminalIndex < 0 || !heartbeatWithLatest {
		t.Fatalf("messages did not contain latest-progress lease followed by terminal result: %d messages", len(messages))
	}
}

func TestChapterFinalizeDeadlineRejectsExpiredDispatch(t *testing.T) {
	if d, ok := chapterFinalizeDeadline(&ipcpb.ProcessingJobRequest{DeadlineUnixMs: time.Now().Add(-time.Second).UnixMilli()}); ok || d != 0 {
		t.Fatalf("expired deadline = %s ok:%v, want rejected", d, ok)
	}
	if d, ok := chapterFinalizeDeadline(&ipcpb.ProcessingJobRequest{}); !ok || d != time.Hour {
		t.Fatalf("legacy missing deadline = %s ok:%v, want one-hour compatibility budget", d, ok)
	}
}
