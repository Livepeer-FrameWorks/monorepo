package handlers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsHLSSource_M3U8Extension(t *testing.T) {
	got := isHLSSource("https://bucket.s3.amazonaws.com/recordings/abc123.m3u8", nil)
	if !got {
		t.Fatal("expected true for .m3u8 URL")
	}
}

func TestIsHLSSource_SegmentURLsParam(t *testing.T) {
	params := map[string]string{
		"segment_urls": "seg0.ts=https://presigned/seg0\nseg1.ts=https://presigned/seg1",
	}
	got := isHLSSource("https://bucket.s3.amazonaws.com/recordings/abc123.mp4", params)
	if !got {
		t.Fatal("expected true when segment_urls param is present")
	}
}

func TestIsHLSSource_RegularFile(t *testing.T) {
	got := isHLSSource("https://bucket.s3.amazonaws.com/recordings/abc123.mp4", nil)
	if got {
		t.Fatal("expected false for .mp4 URL with no params")
	}
}

func TestExtractTrackMetadata_VideoAudio(t *testing.T) {
	meta := map[string]interface{}{
		"meta": map[string]interface{}{
			"tracks": map[string]interface{}{
				"video1": map[string]interface{}{
					"codec":  "H264",
					"width":  float64(1920),
					"height": float64(1080),
					"fpks":   float64(30000),
					"bps":    float64(5000000),
				},
				"audio1": map[string]interface{}{
					"codec":    "AAC",
					"channels": float64(2),
					"rate":     float64(48000),
				},
			},
			"lastms": float64(120000),
		},
	}

	got := extractTrackMetadata(meta)

	expect := map[string]string{
		"video_codec":       "H264",
		"width":             "1920",
		"height":            "1080",
		"resolution":        "1920x1080",
		"fps":               "30.00",
		"bitrate_kbps":      "5000",
		"audio_codec":       "AAC",
		"audio_channels":    "2",
		"audio_sample_rate": "48000",
		"duration_ms":       "120000",
	}

	for k, v := range expect {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(expect) {
		t.Errorf("result has %d keys, want %d", len(got), len(expect))
	}
}

func TestExtractTrackMetadata_VideoOnly(t *testing.T) {
	meta := map[string]interface{}{
		"meta": map[string]interface{}{
			"tracks": map[string]interface{}{
				"video1": map[string]interface{}{
					"codec":  "VP9",
					"width":  float64(1280),
					"height": float64(720),
					"fpks":   float64(25000),
					"bps":    float64(3000000),
				},
			},
			"lastms": float64(60000),
		},
	}

	got := extractTrackMetadata(meta)

	videoKeys := []string{"video_codec", "width", "height", "resolution", "fps", "bitrate_kbps", "duration_ms"}
	for _, k := range videoKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("expected key %q to be present", k)
		}
	}

	audioKeys := []string{"audio_codec", "audio_channels", "audio_sample_rate"}
	for _, k := range audioKeys {
		if _, ok := got[k]; ok {
			t.Errorf("unexpected audio key %q in video-only result", k)
		}
	}
}

func TestExtractTrackMetadata_EmptyMeta(t *testing.T) {
	for _, meta := range []map[string]interface{}{nil, {}, {"unrelated": "data"}} {
		got := extractTrackMetadata(meta)
		if len(got) != 0 {
			t.Errorf("expected empty map for meta %v, got %v", meta, got)
		}
	}
}

func TestHasPendingJob(t *testing.T) {
	stream := "processing+test_pending_" + t.Name()

	pendingJobsMu.Lock()
	pendingJobs[stream] = make(chan struct{}, 1)
	pendingJobsMu.Unlock()

	if !HasPendingJob(stream) {
		t.Fatal("expected true after registering channel")
	}

	pendingJobsMu.Lock()
	delete(pendingJobs, stream)
	pendingJobsMu.Unlock()

	if HasPendingJob(stream) {
		t.Fatal("expected false after cleanup")
	}
}

func TestSignalProcessingComplete(t *testing.T) {
	stream := "processing+test_signal_" + t.Name()

	ch := make(chan struct{}, 1)
	pendingJobsMu.Lock()
	pendingJobs[stream] = ch
	pendingJobsMu.Unlock()

	defer func() {
		pendingJobsMu.Lock()
		delete(pendingJobs, stream)
		pendingJobsMu.Unlock()
	}()

	SignalProcessingComplete(stream)

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected signal on registered channel")
	}

	// Signaling an unregistered stream must not panic
	SignalProcessingComplete("processing+nonexistent_stream")
}

func TestShouldIgnoreProcessExitByBootCount(t *testing.T) {
	ignored := map[string]int{}

	if shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "Livepeer", BootCount: 1}, ignored) {
		t.Fatal("unexpected ignore before any generation is retired")
	}

	ignoreProcessExitThrough(ignored, "Livepeer", 3)

	if !shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "Livepeer", BootCount: 2}, ignored) {
		t.Fatal("expected older Livepeer boot count to be ignored")
	}
	if !shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "Livepeer", BootCount: 3}, ignored) {
		t.Fatal("expected retired Livepeer boot count to be ignored")
	}
	if shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "Livepeer", BootCount: 4}, ignored) {
		t.Fatal("expected newer Livepeer boot count to remain eligible")
	}
	if shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "AV", BootCount: 1}, ignored) {
		t.Fatal("expected other process types to remain eligible")
	}
}

func TestShouldIgnoreProcessExitWhenTypeRetiredWithoutBootCount(t *testing.T) {
	ignored := map[string]int{}
	ignoreProcessExitThrough(ignored, "Livepeer", 0)

	if !shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "Livepeer", BootCount: 99}, ignored) {
		t.Fatal("expected all retired Livepeer exits to be ignored")
	}
	if !shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "Livepeer"}, ignored) {
		t.Fatal("expected missing boot count to be ignored for retired Livepeer exits")
	}
	if shouldIgnoreProcessExit(ProcessExitEvent{ProcessType: "AV", BootCount: 1}, ignored) {
		t.Fatal("expected non-retired process types to remain eligible")
	}
}

func TestWaitForProcessingOutput_SucceedsWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "out.mkv")
	if err := os.WriteFile(outputPath, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}

	size, err := waitForProcessingOutput(outputPath, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 2 {
		t.Fatalf("expected size 2, got %d", size)
	}
}

func TestWaitForProcessingOutput_WaitsForDelayedFile(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "delayed.mkv")

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(outputPath, []byte("ready"), 0644)
	}()

	size, err := waitForProcessingOutput(outputPath, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 5 {
		t.Fatalf("expected size 5, got %d", size)
	}
}

func TestWaitForProcessingOutput_FailsForEmptyFile(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "empty.mkv")
	if err := os.WriteFile(outputPath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := waitForProcessingOutput(outputPath, 150*time.Millisecond); err == nil {
		t.Fatal("expected validation error for empty file")
	}
}
