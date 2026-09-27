package handlers

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// The staged source size reaches Foghorn on the first progress after Mist
// opens the source, and every later report and lease renewal keeps carrying
// it, so a lost first message does not lose the size.
func TestProcessingReporterCarriesStagedSourceSize(t *testing.T) {
	var messages []*ipcpb.ControlMessage
	reporter := newProcessingReporter(func(msg *ipcpb.ControlMessage) { messages = append(messages, msg) }, "job-import")
	h := &ProcessingJobHandler{}

	h.sendSourceStaged(reporter.Send, "job-import", 60000, 734003200)
	h.sendProgress(reporter.Send, "job-import", 10, 6000, 60000)
	reporter.renewLease(time.Hour, true)

	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(messages))
	}
	for i, msg := range messages {
		if got := msg.GetProcessingJobProgress().GetSourceSizeBytes(); got != 734003200 {
			t.Fatalf("message %d source_size_bytes = %d, want 734003200", i, got)
		}
	}
}

// A locally staged source (unsafe wrapper) is sized from its file when the
// relay staged nothing for the artifact.
func TestStagedProcessingSourceSizeFallsBackToStagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.flv")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stagedProcessingSourceSize("no-relay-input", path); got != 4096 {
		t.Fatalf("staged size = %d, want 4096", got)
	}
	if got := stagedProcessingSourceSize("no-relay-input", ""); got != 0 {
		t.Fatalf("unstaged size = %d, want 0", got)
	}
}
