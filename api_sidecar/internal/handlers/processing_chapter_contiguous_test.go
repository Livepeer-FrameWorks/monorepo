package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// Ledger rows written before Helmsman kept segment timing contiguous can
// overlap: the dev stack's final segment started 2018 ms before the previous
// one ended. The remux re-times each segment to its manifest start, so the
// overlap dropped the start of the last segment. The manifest starts each
// segment where the previous one ended.
func TestBuildChapterHLSWritesContiguousSegmentStarts(t *testing.T) {
	storage := t.TempDir()
	appconfigtest.Setenv(t, "HELMSMAN_STORAGE_LOCAL_PATH", storage)
	segDir := filepath.Join(storage, "dvr", "stream-1", "dvr-hash", "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"56_33.ts", "56_34.ts"} {
		if err := os.WriteFile(filepath.Join(segDir, name), []byte("ts"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 9, 26, 13, 56, 31, 491_000_000, time.UTC).UnixMilli()
	req := &ipcpb.ProcessingJobRequest{
		SourceDvrHash:   "dvr-hash",
		SourceChapterId: "chapter",
		SourceSegments: []*ipcpb.DVRChapterSegmentRef{
			{SegmentName: "56_33.ts", MediaStartMs: base, MediaEndMs: base + 6000, DurationMs: 6000},
			{SegmentName: "56_34.ts", MediaStartMs: base + 3982, MediaEndMs: base + 3982 + 4344, DurationMs: 4344},
		},
	}
	manifestPath := filepath.Join(t.TempDir(), "chapter.m3u8")
	h := &ProcessingJobHandler{}
	count, _, _, endMs, detail, err := h.buildChapterHLS(context.Background(), logrus.NewEntry(logrus.New()), req, manifestPath, t.TempDir())
	if err != nil || count != 2 {
		t.Fatalf("buildChapterHLS: count=%d detail=%q err=%v", count, detail, err)
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	wantSecond := time.UnixMilli(base + 6000).UTC().Format(time.RFC3339Nano)
	if !strings.Contains(string(manifest), "#EXT-X-PROGRAM-DATE-TIME:"+wantSecond) {
		t.Fatalf("second segment does not start where the first ended (%s):\n%s", wantSecond, manifest)
	}
	if endMs != base+6000+4344 {
		t.Fatalf("media end = %d, want %d", endMs, base+6000+4344)
	}
}
