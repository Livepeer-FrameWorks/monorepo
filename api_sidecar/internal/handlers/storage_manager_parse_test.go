package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"frameworks/pkg/logging"
)

func TestParseHLSManifest_Standard(t *testing.T) {
	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.000,
seg0.ts
#EXTINF:5.500,
seg1.ts
#EXTINF:4.200,
seg2.ts
#EXT-X-ENDLIST`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(m.Segments))
	}
	if m.Segments[0].Name != "seg0.ts" {
		t.Fatalf("expected seg0.ts, got %s", m.Segments[0].Name)
	}
	if m.Segments[1].Duration != 5.5 {
		t.Fatalf("expected duration 5.5, got %f", m.Segments[1].Duration)
	}
	if m.Segments[2].Name != "seg2.ts" {
		t.Fatalf("expected seg2.ts, got %s", m.Segments[2].Name)
	}
}

func TestParseHLSManifest_Empty(t *testing.T) {
	m, err := parseHLSManifest("")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 0 {
		t.Fatalf("expected 0 segments, got %d", len(m.Segments))
	}
	if m.TargetDuration != 6 {
		t.Fatalf("expected default target duration 6, got %d", m.TargetDuration)
	}
}

func TestParseHLSManifest_TargetDuration(t *testing.T) {
	content := `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXTINF:9.000,
chunk.ts`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetDuration != 10 {
		t.Fatalf("expected 10, got %d", m.TargetDuration)
	}
}

func TestParseHLSManifest_QueryParams(t *testing.T) {
	content := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXTINF:6.000,
seg0.ts?token=abc123&expires=999
#EXTINF:6.000,
seg1.ts?v=2`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(m.Segments))
	}
	if m.Segments[0].Name != "seg0.ts" {
		t.Fatalf("expected query params stripped, got %s", m.Segments[0].Name)
	}
	if m.Segments[1].Name != "seg1.ts" {
		t.Fatalf("expected query params stripped, got %s", m.Segments[1].Name)
	}
}

func TestParseHLSManifest_SubdirPaths(t *testing.T) {
	content := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXTINF:6.000,
segments/chunk000.ts
#EXTINF:6.000,
segments/chunk001.ts`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("expected 2, got %d", len(m.Segments))
	}
	if m.Segments[0].Name != "chunk000.ts" {
		t.Fatalf("expected base name extracted, got %s", m.Segments[0].Name)
	}
}

func TestCreateLiveManifest(t *testing.T) {
	sm := &StorageManager{logger: logging.NewLogger()}
	manifest := sm.createLiveManifest("test-hash", 8)

	if !strings.Contains(manifest, "#EXTM3U") {
		t.Fatal("missing EXTM3U tag")
	}
	if !strings.Contains(manifest, "#EXT-X-VERSION:3") {
		t.Fatal("missing VERSION tag")
	}
	if !strings.Contains(manifest, "#EXT-X-TARGETDURATION:8") {
		t.Fatal("missing or wrong TARGETDURATION")
	}
	if !strings.Contains(manifest, "#EXT-X-PLAYLIST-TYPE:EVENT") {
		t.Fatal("missing PLAYLIST-TYPE")
	}
}

func TestAppendSegmentToManifest(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "test.m3u8")

	if err := os.WriteFile(manifestPath, []byte("#EXTM3U\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sm := &StorageManager{logger: logging.NewLogger()}
	sm.appendSegmentToManifest(manifestPath, "chunk000.ts", 6.0)

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "#EXTINF:6.000,") {
		t.Fatalf("expected EXTINF tag, got:\n%s", content)
	}
	if !strings.Contains(content, "segments/chunk000.ts") {
		t.Fatalf("expected segment path, got:\n%s", content)
	}
}

func TestFinalizeManifest(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "test.m3u8")

	if err := os.WriteFile(manifestPath, []byte("#EXTM3U\n#EXTINF:6.000,\nseg0.ts\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sm := &StorageManager{logger: logging.NewLogger()}
	sm.finalizeManifest(manifestPath)

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "#EXT-X-ENDLIST") {
		t.Fatalf("expected ENDLIST tag, got:\n%s", string(data))
	}
}

func TestDefrostJobDeduplication(t *testing.T) {
	sm := &StorageManager{logger: logging.NewLogger()}
	sm.defrostTracker.inFlight = make(map[string]*DefrostJob)

	// First call should create
	job1, shouldInitiate := sm.getOrCreateDefrostJob("hash-1", AssetTypeClip, "req-1")
	if !shouldInitiate {
		t.Fatal("first call should initiate")
	}
	if job1.AssetHash != "hash-1" {
		t.Fatalf("expected hash-1, got %s", job1.AssetHash)
	}
	if job1.Waiters != 1 {
		t.Fatalf("expected 1 waiter, got %d", job1.Waiters)
	}

	// Second call should return same job
	job2, shouldInitiate2 := sm.getOrCreateDefrostJob("hash-1", AssetTypeClip, "req-2")
	if shouldInitiate2 {
		t.Fatal("second call should NOT initiate")
	}
	if job2 != job1 {
		t.Fatal("expected same job pointer")
	}
	if atomic.LoadInt32(&job2.Waiters) != 2 {
		t.Fatalf("expected 2 waiters, got %d", atomic.LoadInt32(&job2.Waiters))
	}

	// Different hash should create new job
	job3, shouldInitiate3 := sm.getOrCreateDefrostJob("hash-2", AssetTypeDVR, "req-3")
	if !shouldInitiate3 {
		t.Fatal("different hash should initiate")
	}
	if job3 == job1 {
		t.Fatal("different hash should create different job")
	}
}

func TestMarkDefrostJobDone(t *testing.T) {
	sm := &StorageManager{logger: logging.NewLogger()}
	sm.defrostTracker.inFlight = make(map[string]*DefrostJob)

	job, _ := sm.getOrCreateDefrostJob("hash-1", AssetTypeClip, "req-1")

	testErr := fmt.Errorf("test error")
	sm.markDefrostJobDone("hash-1", testErr, "/data/restored.mp4", 4096)

	// Check Done channel is closed
	select {
	case <-job.Done:
		// good
	default:
		t.Fatal("Done channel should be closed")
	}

	if job.Err != testErr {
		t.Fatalf("expected test error, got %v", job.Err)
	}
	if job.LocalPath != "/data/restored.mp4" {
		t.Fatalf("expected path, got %s", job.LocalPath)
	}
	if job.SizeBytes != 4096 {
		t.Fatalf("expected 4096, got %d", job.SizeBytes)
	}

	// Double-call should not panic (closeOnce protection)
	sm.markDefrostJobDone("hash-1", nil, "", 0)
}
