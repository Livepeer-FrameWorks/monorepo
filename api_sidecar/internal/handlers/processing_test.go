package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/sirupsen/logrus"
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

func TestExtractTrackMetadata_IgnoresThumbnailJPEGForPrimaryVideo(t *testing.T) {
	meta := map[string]interface{}{
		"meta": map[string]interface{}{
			"tracks": map[string]interface{}{
				"video_h264": map[string]interface{}{
					"codec":  "H264",
					"width":  float64(640),
					"height": float64(360),
				},
				"video_jpeg": map[string]interface{}{
					"codec":  "JPEG",
					"width":  float64(1600),
					"height": float64(900),
				},
			},
		},
	}

	got := extractTrackMetadata(meta)

	if got["video_codec"] != "H264" {
		t.Fatalf("video_codec = %q, want H264", got["video_codec"])
	}
	if got["resolution"] != "640x360" {
		t.Fatalf("resolution = %q, want 640x360", got["resolution"])
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

func TestProcessingTracksCompleteAllowsMissingOptionalProcTracks(t *testing.T) {
	req := expectedProcessingTracks(`[{"process":"AV","codec":"opus","track_select":"video=none"},{"process":"Thumbs"}]`)
	presence := processingTrackPresence{
		audioCodecs: map[string]bool{"AAC": true},
		videoCodecs: map[string]bool{"H264": true, "JPEG": true},
		metaCodecs:  map[string]bool{"thumbvtt": true},
		sourceMedia: true,
	}
	if !processingRequiredTracksReady(presence, req) {
		t.Fatal("expected source media to satisfy required readiness")
	}
	if processingTracksComplete(presence, req) {
		t.Fatal("expected missing optional opus track to leave enrichment incomplete")
	}

	presence.audioCodecs["opus"] = true
	if !processingTracksComplete(presence, req) {
		t.Fatal("expected source, opus, and thumbnail tracks to satisfy complete readiness")
	}
}

func TestProcessingTracksReadyRequiresExplicitRequiredTracks(t *testing.T) {
	req := expectedProcessingTracks(`[{"process":"AV","codec":"opus","required":true}]`)
	presence := processingTrackPresence{
		audioCodecs: map[string]bool{"AAC": true},
		videoCodecs: map[string]bool{"H264": true},
		metaCodecs:  map[string]bool{},
		sourceMedia: true,
	}
	if processingRequiredTracksReady(presence, req) {
		t.Fatal("expected missing required opus track to block readiness")
	}

	presence.audioCodecs["opus"] = true
	if !processingRequiredTracksReady(presence, req) {
		t.Fatal("expected required opus track to satisfy readiness")
	}
}

func TestInspectProcessingActiveStreamUsesHealthTracks(t *testing.T) {
	presence := inspectProcessingActiveStream(map[string]interface{}{
		"lastms": float64(33000),
		"health": map[string]interface{}{
			"audio_AAC_1ch_44100hz_1": map[string]interface{}{
				"codec":    "AAC",
				"channels": float64(1),
				"rate":     float64(44100),
			},
			"audio_opus_1ch_48000hz_2": map[string]interface{}{
				"codec": "opus",
			},
			"video_H264_640x360_0fps_0": map[string]interface{}{
				"codec":  "H264",
				"width":  float64(640),
				"height": float64(360),
				"kbits":  float64(2200),
			},
			"video_JPEG_160x90_0fps_3": map[string]interface{}{
				"codec": "JPEG",
			},
			"meta_thumbvtt_4": map[string]interface{}{
				"codec": "thumbvtt",
			},
		},
	})

	if !presence.sourceMedia {
		t.Fatal("expected source media to be detected")
	}
	for _, codec := range []string{"AAC", "opus"} {
		if !presence.audioCodecs[codec] {
			t.Fatalf("expected audio codec %s", codec)
		}
	}
	for _, codec := range []string{"H264", "JPEG"} {
		if !presence.videoCodecs[codec] {
			t.Fatalf("expected video codec %s", codec)
		}
	}
	if !presence.metaCodecs["thumbvtt"] {
		t.Fatal("expected thumbvtt metadata track")
	}
	if got := presence.outputs["duration_ms"]; got != "33000" {
		t.Fatalf("duration_ms = %q, want 33000", got)
	}
	if got := presence.outputs["resolution"]; got != "640x360" {
		t.Fatalf("resolution = %q, want 640x360", got)
	}
}

func TestMistJSONURLUsesHTTPPortForAPIURL(t *testing.T) {
	got := mistJSONURL("http://mistserver:4242", "processing+abc", "metaeverywhere=1")
	want := "http://mistserver:8080/json_processing+abc.js?metaeverywhere=1"
	if got != want {
		t.Fatalf("mistJSONURL = %q, want %q", got, want)
	}
}

func TestBootMistStreamRequestsJSONEndpoint(t *testing.T) {
	var gotPath string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"meta":{"tracks":{}}}`))
	}))
	t.Cleanup(server.Close)

	h := &ProcessingJobHandler{mistServerURL: server.URL}
	if err := h.bootMistStream("processing+artifact123"); err != nil {
		t.Fatalf("bootMistStream failed: %v", err)
	}
	if gotPath != "/json_processing+artifact123.js" {
		t.Fatalf("path = %q, want json endpoint", gotPath)
	}
	if gotQuery != "metaeverywhere=1&inclzero=1" {
		t.Fatalf("query = %q, want meta query", gotQuery)
	}
}

func TestGenerateDTSHFetchesJSONEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"meta":{"tracks":{}}}`))
	}))
	t.Cleanup(server.Close)

	if err := GenerateDTSH(server.URL, "vod+artifact123", logrus.NewEntry(logrus.New())); err != nil {
		t.Fatalf("GenerateDTSH failed: %v", err)
	}
	if gotPath != "/json_vod+artifact123.js" {
		t.Fatalf("path = %q, want DTSH json endpoint", gotPath)
	}
}

func TestProcessingMuxTargetURISelectsAllTracks(t *testing.T) {
	got := processingMuxTargetURI("/var/lib/mistserver/recordings/vod/hash.mkv")
	want := "/var/lib/mistserver/recordings/vod/hash.mkv#audio=all&video=all&meta=all&subtitle=all"
	if got != want {
		t.Fatalf("target URI = %q, want %q", got, want)
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

func TestBuildLocalProcessingSourceURL_DefaultsToMKV(t *testing.T) {
	h := &ProcessingJobHandler{mistServerURL: "http://mistserver:4242/api2"}
	req := &pb.ProcessingJobRequest{Params: map[string]string{
		"source_kind":        "live",
		"source_stream_name": "live+stream-1",
		"source_start_unix":  "100",
		"source_stop_unix":   "130",
	}}

	got := h.buildLocalProcessingSourceURL(req)

	if !strings.HasPrefix(got, "http://mistserver:8080/live+stream-1.mkv?") {
		t.Fatalf("source URL = %q, want local Mist MKV clipping URL", got)
	}
	if !strings.Contains(got, "duration=30") {
		t.Fatalf("source URL = %q, want duration query", got)
	}
}

func TestStageProcessingSourceDownloadsSourceClip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "10")
			return
		}
		_, _ = w.Write([]byte("clip-bytes"))
	}))
	t.Cleanup(server.Close)

	h := &ProcessingJobHandler{storagePath: t.TempDir()}
	req := &pb.ProcessingJobRequest{
		ArtifactHash: "cliphash",
		Params: map[string]string{
			"source_format": "mkv",
		},
	}

	path, err := h.stageProcessingSource(logrus.NewEntry(logrus.New()), req, server.URL)
	if err != nil {
		t.Fatalf("stageProcessingSource failed: %v", err)
	}

	if filepath.Base(path) != "cliphash.mkv" {
		t.Fatalf("staged path = %q, want cliphash.mkv", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if string(got) != "clip-bytes" {
		t.Fatalf("staged bytes = %q", got)
	}
}

func TestProcessingOutputPath_ClipUsesStreamScopedClipDir(t *testing.T) {
	root := t.TempDir()
	h := &ProcessingJobHandler{storagePath: root}
	req := &pb.ProcessingJobRequest{
		ArtifactHash: "cliphash",
		Params: map[string]string{
			"output_stream_name": "demo_live_stream_001",
		},
	}

	dir, path, err := h.processingOutputPath(req, true)
	if err != nil {
		t.Fatalf("processingOutputPath returned error: %v", err)
	}

	wantDir := filepath.Join(root, "clips", "demo_live_stream_001")
	if dir != wantDir {
		t.Fatalf("dir = %q, want %q", dir, wantDir)
	}
	wantPath := filepath.Join(wantDir, "cliphash.mkv")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
}

func TestProcessingOutputPath_ClipRequiresOutputStream(t *testing.T) {
	h := &ProcessingJobHandler{storagePath: t.TempDir()}
	req := &pb.ProcessingJobRequest{ArtifactHash: "cliphash", Params: map[string]string{}}

	if _, _, err := h.processingOutputPath(req, true); err == nil {
		t.Fatal("expected missing output_stream_name error")
	}
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
