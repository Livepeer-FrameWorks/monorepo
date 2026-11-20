package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/dvrpolicy"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

func TestChapterLedgerGapIsNotReportedAsLostSource(t *testing.T) {
	isolateProcessingOverrideState(t)
	storage := t.TempDir()
	appconfigtest.Setenv(t, "HELMSMAN_STORAGE_LOCAL_PATH", storage)
	segmentDir := filepath.Join(storage, "dvr", "stream", "dvr-hash", "segments")
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.ts", "second.ts"} {
		if err := os.WriteFile(filepath.Join(segmentDir, name), []byte("retained source"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := NewProcessingJobHandler(logrus.New(), "", storage, t.TempDir())
	request := &ipcpb.ProcessingJobRequest{
		JobId: "ledger-gap-job", ArtifactHash: "ledger-gap-artifact",
		SourceDvrHash: "dvr-hash", SourceChapterId: "chapter-gap",
		SourceSegments: []*ipcpb.DVRChapterSegmentRef{
			{SegmentName: "first.ts", MediaStartMs: 1000, MediaEndMs: 9013, DurationMs: 8013},
			{SegmentName: "second.ts", MediaStartMs: 9014, MediaEndMs: 15014, DurationMs: 6000},
		},
	}
	var result *ipcpb.ProcessingJobResult
	h.handleChapterFinalize(request, func(message *ipcpb.ControlMessage) {
		if payload := message.GetProcessingJobResult(); payload != nil {
			result = payload
		}
	})
	if result == nil || result.GetStatus() != "failed" || result.GetOutputs()["chapter_failure"] != dvrpolicy.ChapterFailureLedgerInconsistent ||
		!strings.Contains(result.GetError(), "LEDGER_INCONSISTENT") {
		t.Fatalf("ledger gap must retain its distinct failure class, got %v", result)
	}
	for _, name := range []string{"first.ts", "second.ts"} {
		data, err := os.ReadFile(filepath.Join(segmentDir, name))
		if err != nil || string(data) != "retained source" {
			t.Fatalf("source %s changed during failed finalize: data=%q err=%v", name, data, err)
		}
	}
}
