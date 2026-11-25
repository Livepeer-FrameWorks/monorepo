package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestProcessingTerminalCleanupFailure(t *testing.T) {
	isolatePendingJobs(t)
	isolateProcessingOverrideState(t)
	const artifact = "terminal-cleanup-failure"
	stream := "processing+" + artifact
	processingOverrideRecords[stream] = processingOverrideRecord{JobID: "old-attempt", SourceProbe: "source-probe-before-cleanup"}
	h := &ProcessingJobHandler{logger: logging.NewLogger()}
	reported := false
	h.Handle(&ipcpb.ProcessingJobRequest{JobId: "old-attempt", ArtifactHash: artifact, TenantId: "fixture-tenant"}, func(message *ipcpb.ControlMessage) {
		if message.GetProcessingJobResult() == nil {
			return
		}
		reported = true
		outputs := message.GetProcessingJobResult().GetOutputs()
		if outputs[mist.ProcessingResultTenantID] != "fixture-tenant" || outputs[mist.ProcessingSourceProbeParam] != "source-probe-before-cleanup" {
			t.Errorf("delayed terminal result lost its tenant or source probe: %v", outputs)
		}
		if _, owner, claimed := claimPendingJob(stream, "new-attempt"); !claimed {
			t.Errorf("terminal result allowed redispatch while attempt %q still owned the stream", owner)
		} else {
			releasePendingJob(stream, "new-attempt")
		}
	})
	if !reported {
		t.Fatal("handler did not report its missing-policy failure")
	}
}

func TestProcessingTerminalCleanupKeepsNonterminalReportsImmediate(t *testing.T) {
	var statuses []string
	send, flush := deferProcessingTerminalResult(func(message *ipcpb.ControlMessage) {
		if message.GetProcessingJobProgress() != nil {
			statuses = append(statuses, "progress")
		} else {
			statuses = append(statuses, message.GetProcessingJobResult().GetStatus())
		}
	})
	h := &ProcessingJobHandler{}
	h.sendProgress(send, "job", 1, 1, 100)
	h.sendResult(send, "job", "cache_update", "", nil, "", 0)
	if len(statuses) != 2 || statuses[0] != "progress" || statuses[1] != "cache_update" {
		t.Fatalf("nonterminal reports were held until cleanup: %v", statuses)
	}
	h.sendResult(send, "job", "completed", "", nil, "", 0)
	h.sendProgress(send, "job", 2, 2, 100)
	flush()
	flush()
	if len(statuses) != 3 || statuses[2] != "completed" {
		t.Fatalf("terminal result must flush exactly once, with no later progress: %v", statuses)
	}
}

func TestProcessingTerminalCleanupRetry(t *testing.T) {
	isolatePendingJobs(t)
	const stream = "processing+terminal-cleanup-retry"
	if _, _, claimed := claimPendingJob(stream, "old-attempt"); !claimed {
		t.Fatal("reserve the first attempt")
	}
	t.Cleanup(func() { releasePendingJob(stream, "old-attempt") })
	cleaned, reported := false, false
	send, flushTerminal := deferProcessingTerminalResult(func(message *ipcpb.ControlMessage) {
		if message.GetProcessingJobResult() == nil {
			return
		}
		reported = true
		if !cleaned {
			t.Error("retry published before native resource cleanup finished")
		}
		if _, owner, claimed := claimPendingJob(stream, "new-attempt"); !claimed {
			t.Errorf("redispatch ignored because attempt %q still owns the stream", owner)
		} else {
			releasePendingJob(stream, "new-attempt")
		}
	})
	reporter := newProcessingReporter(send, "old-attempt")
	func() {
		defer flushTerminal()
		stop := reporter.StartLease(time.Hour)
		defer stop()
		defer releasePendingJob(stream, "old-attempt")
		defer func() { cleaned = true }()
		(&ProcessingJobHandler{}).sendResult(reporter.Send, "old-attempt", "retryable", "kernel OOM", nil, "", 0)
		if reported {
			t.Error("retry escaped while the old attempt was still retiring")
		}
	}()
	if !reported {
		t.Fatal("retry result was lost after resource cleanup")
	}
}
