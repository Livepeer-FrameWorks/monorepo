package handlers

import (
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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

func TestCleanupProcessingStagePathRemovesDerivedSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "processing", "artifact.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + ".dtsh", path + ".gop"} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(path+".blocks", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".blocks", "00000000.block"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanupProcessingStagePath(logrus.NewEntry(logrus.New()), path)

	for _, file := range []string{path, path + ".dtsh", path + ".gop", path + ".blocks"} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or stat failed with %v", file, err)
		}
	}
}

func TestChapterFinalizeCleansProcessingStageOnBuildFailure(t *testing.T) {
	dir := t.TempDir()
	h := NewProcessingJobHandler(logrus.New(), "", dir)
	req := &ipcpb.ProcessingJobRequest{
		JobId:        "job-1",
		ArtifactHash: "chapter-fail",
	}
	var result *ipcpb.ProcessingJobResult

	h.handleChapterFinalize(req, func(msg *ipcpb.ControlMessage) {
		if payload := msg.GetProcessingJobResult(); payload != nil {
			result = payload
		}
	})

	if result == nil || result.GetStatus() != "failed" {
		t.Fatalf("result=%v want failed", result)
	}
	recoveryDir := filepath.Join(dir, "processing", "chapter-"+req.GetArtifactHash())
	if _, err := os.Stat(recoveryDir); !os.IsNotExist(err) {
		t.Fatalf("recovery dir still exists or stat failed with %v", err)
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
	req := expectedProcessingTracks(`[{"process":"AV","codec":"opus","track_select":"audio=all&video=none&subtitle=none"},{"process":"Thumbs","track_select":"video=lowres"}]`)
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
	req := expectedProcessingTracks(`[{"process":"AV","codec":"opus","track_select":"audio=all&video=none&subtitle=none","required":true}]`)
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

func TestExpectedLocalAVVideoProcessesCountsVideoOnly(t *testing.T) {
	processes := `[
		{"process":"AV","codec":"h264","track_select":"video=maxbps&audio=none"},
		{"process":"AV","codec":"opus","track_select":"audio=all&video=none"},
		{"process":"AV","codec":"h265"},
		{"process":"Thumbs","track_select":"video=lowres"}
	]`
	if got := expectedLocalAVVideoProcesses(processes); got != 2 {
		t.Fatalf("expected %d local AV video processes, got %d", 2, got)
	}
}

func TestProcessAVFinalVideoReadyRequiresFinalVideoOutput(t *testing.T) {
	ready := ProcessAVSegmentCompleteEvent{
		TrackType:    "video",
		OutputCodec:  "libx264",
		OutputWidth:  640,
		OutputHeight: 360,
		OutputFrames: 100,
		IsFinal:      true,
	}
	if !processAVFinalVideoReady(ready) {
		t.Fatal("expected final video output to be ready")
	}
	if !processAVVideoProgress(ready) {
		t.Fatal("expected video output frames to count as progress")
	}

	for name, evt := range map[string]ProcessAVSegmentCompleteEvent{
		"non_final": {TrackType: "video", OutputCodec: "libx264", OutputWidth: 640, OutputHeight: 360, OutputFrames: 100},
		"audio":     {TrackType: "audio", OutputCodec: "libx264", OutputWidth: 640, OutputHeight: 360, OutputFrames: 100, IsFinal: true},
		"no_frames": {TrackType: "video", OutputCodec: "libx264", OutputWidth: 640, OutputHeight: 360, IsFinal: true},
		"no_dims":   {TrackType: "video", OutputCodec: "libx264", OutputFrames: 100, IsFinal: true},
	} {
		if processAVFinalVideoReady(evt) {
			t.Fatalf("%s should not count as ready", name)
		}
	}
	if processAVVideoProgress(ProcessAVSegmentCompleteEvent{TrackType: "audio", OutputFrames: 100}) {
		t.Fatal("audio output must not count as video progress")
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
	if len(presence.videoTracks) != 1 || presence.videoTracks[0].height != 360 {
		t.Fatalf("videoTracks = %+v, want one 360p source track", presence.videoTracks)
	}
}

func TestProcessingLivepeerRenditionsReadyBlocksUntilRequestedTracksExist(t *testing.T) {
	processesJSON := `[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360}]}]`
	sourceOutputs := map[string]string{
		"width":       "1920",
		"height":      "1080",
		"duration_ms": "9000",
	}

	ready, missing, err := processingLivepeerRenditionsReady(processingTrackPresence{
		outputs:     sourceOutputs,
		sourceMedia: true,
		videoTracks: []processingMetaVideoTrack{{codec: "H264", width: 1920, height: 1080}},
	}, processesJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ready {
		t.Fatal("expected Livepeer readiness to block while requested rendition is absent")
	}
	if len(missing) != 1 || missing[0] != 360 {
		t.Fatalf("missing = %v, want [360]", missing)
	}

	ready, missing, err = processingLivepeerRenditionsReady(processingTrackPresence{
		outputs:     sourceOutputs,
		sourceMedia: true,
		videoTracks: []processingMetaVideoTrack{
			{codec: "H264", width: 1920, height: 1080},
			{codec: "H264", width: 640, height: 360},
		},
	}, processesJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready || len(missing) != 0 {
		t.Fatalf("ready=%v missing=%v, want ready with no missing heights", ready, missing)
	}
}

func TestExtractActiveStreamMetadataUsesLargestVideoAsSource(t *testing.T) {
	got := extractActiveStreamMetadata(map[string]interface{}{
		"lastms": float64(9000),
		"health": map[string]interface{}{
			"video_H264_640x360_0": map[string]interface{}{
				"codec":  "H264",
				"width":  float64(640),
				"height": float64(360),
			},
			"video_H264_2720x1750_1": map[string]interface{}{
				"codec":  "H264",
				"width":  float64(2720),
				"height": float64(1750),
			},
		},
	})

	if got["resolution"] != "2720x1750" {
		t.Fatalf("resolution = %q, want source-sized 2720x1750", got["resolution"])
	}
}

func TestParseProcessingMetaVideoTracksExcludesThumbnailsAndCarriesSpan(t *testing.T) {
	meta := map[string]interface{}{
		"meta": map[string]interface{}{
			"tracks": map[string]interface{}{
				"video_H264_1280x720_0": map[string]interface{}{
					"codec": "H264", "type": "video",
					"width": float64(1280), "height": float64(720),
					"firstms": float64(0), "lastms": float64(9000),
					"id": float64(7), "idx": float64(1),
				},
				"video_H264_640x360_1": map[string]interface{}{
					"codec": "H264", "type": "video",
					"width": float64(640), "height": float64(360),
					"firstms": float64(0), "lastms": float64(1700), // truncated
					"source": "video_1",
				},
				"video_JPEG_160x90_2":   map[string]interface{}{"codec": "JPEG", "type": "video"}, // thumbnail, excluded
				"audio_AAC_2ch_48000_3": map[string]interface{}{"codec": "AAC", "type": "audio"},
				"meta_thumbvtt_4":       map[string]interface{}{"codec": "thumbvtt", "type": "meta"},
			},
		},
	}
	tracks := parseProcessingMetaVideoTracks(meta)
	if len(tracks) != 2 {
		t.Fatalf("parseProcessingMetaVideoTracks = %d tracks, want 2 (JPEG/audio/meta excluded)", len(tracks))
	}
	var maxSpan, minSpan float64 = 0, 1 << 30
	for _, tr := range tracks {
		if s := tr.spanMs(); s > maxSpan {
			maxSpan = s
		}
		if s := tr.spanMs(); s < minSpan {
			minSpan = s
		}
	}
	if maxSpan != 9000 || minSpan != 1700 {
		t.Fatalf("spans = max %v min %v, want max 9000 (source) min 1700 (truncated rendition)", maxSpan, minSpan)
	}
	for _, tr := range tracks {
		if tr.height == 720 && (!tr.hasTrackID || tr.trackID != 7 || !tr.hasTrackIndex || tr.trackIndex != 1) {
			t.Fatalf("720p parsed track did not carry identity: %+v", tr)
		}
	}
	if got := parseProcessingMetaVideoTracks(map[string]interface{}{}); len(got) != 0 {
		t.Fatalf("parseProcessingMetaVideoTracks(empty) = %d, want 0", len(got))
	}
}

func TestProcessingTracksFromProtoCarriesRecordingEndSpan(t *testing.T) {
	width := int32(1280)
	height := int32(720)
	firstMs := int64(0)
	lastMs := int64(30000)
	trackID := int64(42)
	trackIndex := int32(3)
	tracks := processingTracksFromProto([]*ipcpb.StreamTrack{{
		TrackType:  "video",
		Codec:      "H264",
		Width:      &width,
		Height:     &height,
		FirstMs:    &firstMs,
		LastMs:     &lastMs,
		TrackId:    &trackID,
		TrackIndex: &trackIndex,
	}})
	if len(tracks) != 1 {
		t.Fatalf("tracks=%d, want 1", len(tracks))
	}
	if tracks[0].height != 720 || tracks[0].spanMs() != 30000 {
		t.Fatalf("unexpected track: %+v span=%v", tracks[0], tracks[0].spanMs())
	}
	if tracks[0].selector() != "i42" {
		t.Fatalf("selector = %q, want i42", tracks[0].selector())
	}
}

func TestAuthoritativeSourceSpanFromRecordingEndTracks(t *testing.T) {
	tracks := []processingMetaVideoTrack{
		{codec: "H264", width: 1280, height: 720, firstms: 0, lastms: 30000},
		{codec: "H264", width: 640, height: 360, firstms: 0, lastms: 30000},
	}
	got, ok := authoritativeSourceSpanFromTracks(logrus.NewEntry(logrus.New()), tracks, 1000, 720)
	if !ok {
		t.Fatal("expected source span from final RECORDING_END tracks")
	}
	if got != 30000 {
		t.Fatalf("span=%d, want 30000", got)
	}
}

func TestAuthoritativeSourceSpanFromRecordingEndTracksFailsWithoutSourceHeight(t *testing.T) {
	tracks := []processingMetaVideoTrack{
		{codec: "H264", width: 640, height: 360, firstms: 0, lastms: 30000},
	}
	if _, ok := authoritativeSourceSpanFromTracks(logrus.NewEntry(logrus.New()), tracks, 1000, 720); ok {
		t.Fatal("expected missing source-height track to fail closed")
	}
}

func TestRenditionsCompleteFromTracks(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	entry := logrus.NewEntry(log)
	source := mist.SourceMediaInfo{Width: 1280, Height: 720}
	const srcSpan = 9000.0
	// Ladder intent is raw requested heights (see RequestedRenditionHeights).
	expected := []int{720, 360}
	track := func(w, h int, span float64) processingMetaVideoTrack {
		return processingMetaVideoTrack{codec: "H264", width: w, height: h, firstms: 0, lastms: span}
	}

	// Complete: source (720p) + full-length 720p and 360p renditions.
	full := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(1280, 720, srcSpan), track(640, 360, srcSpan)}
	if !renditionsCompleteFromTracks(entry, expected, full, source, srcSpan) {
		t.Fatal("expected complete rendition set to pass")
	}

	// Missing 720p rendition: only the source 720p track exists, so the 720p
	// profile must NOT be satisfied by the excluded source track.
	missing720 := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(640, 360, srcSpan)}
	if renditionsCompleteFromTracks(entry, expected, missing720, source, srcSpan) {
		t.Fatal("expected missing 720p rendition (only source at 720p) to fail")
	}

	// Truncated rendition: 360p ends far short of the source span.
	truncated := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(1280, 720, srcSpan), track(640, 360, 1700)}
	if renditionsCompleteFromTracks(entry, expected, truncated, source, srcSpan) {
		t.Fatal("expected truncated 360p rendition to fail")
	}

	// No tracks at all: incomplete (fail toward fallback), not complete.
	if renditionsCompleteFromTracks(entry, expected, nil, source, srcSpan) {
		t.Fatal("expected empty track set to be treated as incomplete")
	}

	// Within absolute tolerance: a rendition a few hundred ms short still passes.
	nearFull := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(1280, 720, srcSpan-500), track(640, 360, srcSpan-1500)}
	if !renditionsCompleteFromTracks(entry, expected, nearFull, source, srcSpan) {
		t.Fatal("expected renditions within the absolute span tolerance to pass")
	}
	roundedHeight := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(1280, 720, srcSpan), track(680, 392, srcSpan)}
	if !renditionsCompleteFromTracks(entry, expected, roundedHeight, source, srcSpan) {
		t.Fatal("expected renditions within the resolution rounding tolerance to pass")
	}

	// No independent source span: verify the requested rendition tracks by
	// height. This is the normalized-output case where the source passthrough is
	// intentionally absent from the final artifact.
	if !renditionsCompleteFromTracks(entry, expected, full, source, 0) {
		t.Fatal("expected complete rendition set to pass without an independent source span")
	}

	noSourceOutput := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(640, 360, srcSpan)}
	if !renditionsCompleteFromTracks(entry, expected, noSourceOutput, mist.SourceMediaInfo{}, 0) {
		t.Fatal("expected no-source output to validate by requested rendition heights")
	}

	// A height that cannot be determined for a requested profile fails closed.
	undetermined := []int{0}
	if renditionsCompleteFromTracks(entry, undetermined, full, source, srcSpan) {
		t.Fatal("expected an undeterminable requested rendition height to fail closed")
	}

	// Partial readiness span must not bless a truncated rendition: readiness fired
	// early (1700ms snapshot), but the source passthrough track ran to its full 9s,
	// so the baseline is raised to the source track span and a short 360p still fails.
	partialReadiness := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(1280, 720, srcSpan), track(640, 360, 1700)}
	if renditionsCompleteFromTracks(entry, expected, partialReadiness, source, 1700) {
		t.Fatal("expected a truncated rendition to fail even when readiness captured only a partial span")
	}

	// Same partial readiness span, but full-length renditions: the source track span
	// proves the true length, so the complete set passes.
	partialReadinessFull := []processingMetaVideoTrack{track(1280, 720, srcSpan), track(1280, 720, srcSpan), track(640, 360, srcSpan)}
	if !renditionsCompleteFromTracks(entry, expected, partialReadinessFull, source, 1700) {
		t.Fatal("expected full renditions to pass when the source track proves the full span despite a partial readiness span")
	}

	// Nondeterministic map order must not let a short same-height rendition be
	// excluded as the source. The truncated 720p rendition is listed BEFORE the full
	// 720p source passthrough; the longest-track rule still excludes the source, so
	// the short 720p stays in the pool and fails coverage. (First-match exclusion
	// would wrongly drop the short rendition and let the full source satisfy 720p.)
	shortSameHeightFirst := []processingMetaVideoTrack{track(1280, 720, 1700), track(1280, 720, srcSpan), track(640, 360, srcSpan)}
	if renditionsCompleteFromTracks(entry, expected, shortSameHeightFirst, source, srcSpan) {
		t.Fatal("expected a truncated same-height rendition listed before the source passthrough to fail")
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

func TestGenerateDTSHForPathRequiresSidecarFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"tracks":{}}}`))
	}))
	t.Cleanup(server.Close)

	dtshPath := filepath.Join(t.TempDir(), "artifact.mkv.dtsh")
	err := GenerateDTSHForPath(server.URL, "vod+artifact123", dtshPath, logrus.NewEntry(logrus.New()))
	if err == nil {
		t.Fatal("expected missing sidecar to fail")
	}
	if !strings.Contains(err.Error(), "dtsh file not ready") {
		t.Fatalf("error = %v, want dtsh readiness error", err)
	}
}

func TestGenerateDTSHForPathWaitsForSidecarFile(t *testing.T) {
	dir := t.TempDir()
	dtshPath := filepath.Join(dir, "artifact.mkv.dtsh")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = os.WriteFile(dtshPath, validDTSHBytes(), 0o644)
		}()
		_, _ = w.Write([]byte(`{"meta":{"tracks":{}}}`))
	}))
	t.Cleanup(server.Close)

	if err := GenerateDTSHForPath(server.URL, "vod+artifact123", dtshPath, logrus.NewEntry(logrus.New())); err != nil {
		t.Fatalf("GenerateDTSHForPath failed: %v", err)
	}
}

func TestGenerateDTSHForPathRejectsEmptyTrackSidecar(t *testing.T) {
	dir := t.TempDir()
	dtshPath := filepath.Join(dir, "artifact.mkv.dtsh")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = os.WriteFile(dtshPath, emptyTrackDTSHBytes(), 0o644)
		}()
		_, _ = w.Write([]byte(`{"meta":{"tracks":{}}}`))
	}))
	t.Cleanup(server.Close)

	err := GenerateDTSHForPath(server.URL, "vod+artifact123", dtshPath, logrus.NewEntry(logrus.New()))
	if err == nil {
		t.Fatal("expected invalid sidecar to fail")
	}
	if !strings.Contains(err.Error(), "dtsh file invalid") {
		t.Fatalf("error = %v, want invalid dtsh error", err)
	}
}

func TestProcessingMuxTargetURISelectsAllTracks(t *testing.T) {
	got := processingMuxTargetURI("/var/lib/mistserver/recordings/vod/hash.mkv")
	want := "/var/lib/mistserver/recordings/vod/hash.mkv#audio=all&video=all&meta=all&subtitle=all"
	if got != want {
		t.Fatalf("target URI = %q, want %q", got, want)
	}
}

func TestProcessingMuxTargetURIWithVideoSelector(t *testing.T) {
	got := processingMuxTargetURIWithVideo("/var/lib/mistserver/recordings/vod/hash.mkv", "i7,i8")
	want := "/var/lib/mistserver/recordings/vod/hash.mkv#audio=all&video=i7,i8&meta=all&subtitle=all"
	if got != want {
		t.Fatalf("target URI = %q, want %q", got, want)
	}
}

func TestProcessingMuxTargetURIWithThumbnailSelectors(t *testing.T) {
	got := processingMuxTargetURIWithSelectors("/var/lib/mistserver/recordings/clips/hash.mkv", "i7,JPEG", "all,thumbvtt")
	want := "/var/lib/mistserver/recordings/clips/hash.mkv#audio=all&video=i7,JPEG&meta=all,thumbvtt&subtitle=all"
	if got != want {
		t.Fatalf("target URI = %q, want %q", got, want)
	}
}

func TestChooseProcessingVideoSelectorSelectsCompleteRenditions(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	entry := logrus.NewEntry(log)
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720},{"name":"360p","height":360}]}]`
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}
	tracks := []processingMetaVideoTrack{
		chapterTrack(1, 1920, 1080, 30000),
		chapterTrack(2, 1280, 720, 30000),
		chapterTrack(3, 640, 360, 30000),
	}
	got := chooseProcessingVideoSelector(entry, processes, tracks, source, 30000)
	if got != "i2,i3" {
		t.Fatalf("selector = %q, want rendition tracks", got)
	}
}

func TestAppendAuxiliaryVideoSelectorsRequestsCurrentJPEGTracks(t *testing.T) {
	if got := appendAuxiliaryVideoSelectors("i1", false); got != "i1" {
		t.Fatalf("selector = %q, want playable source only", got)
	}
	if got := appendAuxiliaryVideoSelectors("i1", true); got != "i1,JPEG" {
		t.Fatalf("selector = %q, want playable source plus current JPEG tracks", got)
	}
	if got := appendAuxiliaryVideoSelectors("all", true); got != "all" {
		t.Fatalf("all selector = %q, want all", got)
	}
}

func TestChooseProcessingVideoSelectorFallsBackToSourceWhenRenditionIncomplete(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	entry := logrus.NewEntry(log)
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720},{"name":"360p","height":360}]}]`
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}
	tracks := []processingMetaVideoTrack{
		chapterTrack(1, 1920, 1080, 30000),
		chapterTrack(2, 1280, 720, 30000),
		chapterTrack(3, 640, 360, 1000),
	}
	got := chooseProcessingVideoSelector(entry, processes, tracks, source, 30000)
	if got != "i1" {
		t.Fatalf("selector = %q, want source track", got)
	}
}

func TestChooseProcessingVideoSelectorSameHeightSourceAndRendition(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	entry := logrus.NewEntry(log)
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720},{"name":"360p","height":360}]}]`
	source := mist.SourceMediaInfo{Width: 1280, Height: 720}
	tracks := []processingMetaVideoTrack{
		chapterTrack(9, 1280, 720, 30000),
		chapterTrack(1, 1280, 720, 30000),
		chapterTrack(3, 640, 360, 30000),
	}
	tracks[0].source = "video_1"
	tracks[2].source = "video_1"
	got := chooseProcessingVideoSelector(entry, processes, tracks, source, 30000)
	if got != "i9,i3" {
		t.Fatalf("selector = %q, want same-height rendition plus 360p", got)
	}
}

func chapterTrack(id int64, width, height int, span float64) processingMetaVideoTrack {
	return processingMetaVideoTrack{
		codec:      "H264",
		width:      width,
		height:     height,
		firstms:    0,
		lastms:     span,
		trackID:    id,
		hasTrackID: true,
	}
}

// Source-only material (a clip cut from an untranscoded stream): renditions are
// requested but only the source track exists, so the selector publishes source.
func TestChooseProcessingVideoSelectorSourceOnlyMaterial(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	entry := logrus.NewEntry(log)
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720}]}]`
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}
	tracks := []processingMetaVideoTrack{chapterTrack(1, 1920, 1080, 30000)}
	got := chooseProcessingVideoSelector(entry, processes, tracks, source, 30000)
	if got != "i1" {
		t.Fatalf("selector = %q, want source track", got)
	}
}

func TestClipRequestedSpanMs(t *testing.T) {
	req := &ipcpb.ProcessingJobRequest{Params: map[string]string{
		"source_start_unix": "1000",
		"source_stop_unix":  "1030",
	}}
	if got := clipRequestedSpanMs(req); got != 30000 {
		t.Fatalf("span = %v, want 30000", got)
	}
	if got := clipRequestedSpanMs(&ipcpb.ProcessingJobRequest{}); got != 0 {
		t.Fatalf("empty span = %v, want 0", got)
	}
	inverted := &ipcpb.ProcessingJobRequest{Params: map[string]string{
		"source_start_unix": "1030",
		"source_stop_unix":  "1000",
	}}
	if got := clipRequestedSpanMs(inverted); got != 0 {
		t.Fatalf("inverted span = %v, want 0", got)
	}
}

func TestHasPendingJob(t *testing.T) {
	stream := "processing+test_pending_" + t.Name()

	pendingJobsMu.Lock()
	pendingJobs[stream] = make(chan ProcessingPushEndEvent, 1)
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

	ch := make(chan ProcessingPushEndEvent, 1)
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
	case evt := <-ch:
		if evt.PushStatus != "0" {
			t.Fatalf("PushStatus = %q, want clean status", evt.PushStatus)
		}
	case <-time.After(time.Second):
		t.Fatal("expected signal on registered channel")
	}

	// Signaling an unregistered stream must not panic
	SignalProcessingComplete("processing+nonexistent_stream")
}

func TestProcessingPushFailureMessageIncludesMistDetails(t *testing.T) {
	evt := ProcessingPushEndEvent{
		StreamName:  "processing+artifact",
		PushStatus:  "7",
		LogMessages: "Sink thread failed",
	}
	got := processingPushFailureMessage(evt)
	if !strings.Contains(got, "status=7") || !strings.Contains(got, "Sink thread failed") {
		t.Fatalf("failure message missing details: %q", got)
	}
	if processingPushSucceeded(evt) {
		t.Fatal("expected non-zero push status to fail")
	}
}

func TestProcessingPushStatusJSONIsInformational(t *testing.T) {
	evt := ProcessingPushEndEvent{
		StreamName: "processing+artifact",
		PushStatus: `{"active_ms":4928,"bytes":20810580,"current_target":"/var/lib/mistserver/recordings/vod/artifact.mkv","tracks":[1]}`,
	}
	if !processingPushSucceeded(evt) {
		t.Fatal("expected Mist PUSH_END status JSON to be treated as successful termination")
	}
}

func TestSignalProcessingRecordingEnd(t *testing.T) {
	stream := "processing+test_recording_" + t.Name()
	ch := registerProcessingRecordingEndListener(stream)
	defer unregisterProcessingRecordingEndListener(stream)

	SignalProcessingRecordingEnd(ProcessingRecordingEndEvent{
		StreamName:      stream,
		FilePath:        "/tmp/output.mkv",
		BytesWritten:    12,
		MediaDurationMs: 30,
	})

	select {
	case evt := <-ch:
		if evt.FilePath != "/tmp/output.mkv" || evt.BytesWritten != 12 {
			t.Fatalf("recording event = %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("expected recording end signal on registered channel")
	}

	SignalProcessingRecordingEnd(ProcessingRecordingEndEvent{StreamName: "processing+missing"})
}

func TestValidateProcessingRecordingEnd(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "artifact.mkv")
	if err := validateProcessingRecordingEnd(ProcessingRecordingEndEvent{
		FilePath:        outputPath,
		BytesWritten:    1024,
		MediaDurationMs: 1000,
		ExitReason:      "CLEAN_EOF",
	}, outputPath); err != nil {
		t.Fatalf("expected valid recording end: %v", err)
	}
	if err := validateProcessingRecordingEnd(ProcessingRecordingEndEvent{
		FilePath:        outputPath,
		BytesWritten:    1024,
		MediaDurationMs: 1000,
		ExitReason:      "WRITE_FAILURE",
		HumanExitReason: "disk full",
	}, outputPath); err == nil {
		t.Fatal("expected non-clean exit reason to fail despite positive bytes/duration")
	}
	if err := validateProcessingRecordingEnd(ProcessingRecordingEndEvent{
		FilePath:        outputPath,
		BytesWritten:    1024,
		MediaDurationMs: 1000,
	}, outputPath); err == nil {
		t.Fatal("expected missing exit reason to fail")
	}
	if err := validateProcessingRecordingEnd(ProcessingRecordingEndEvent{
		FilePath:        outputPath,
		BytesWritten:    0,
		MediaDurationMs: 1000,
		ExitReason:      "CLEAN_EOF",
	}, outputPath); err == nil {
		t.Fatal("expected zero-byte recording to fail")
	}
	if err := validateProcessingRecordingEnd(ProcessingRecordingEndEvent{
		FilePath:        outputPath,
		BytesWritten:    1024,
		MediaDurationMs: 0,
		ExitReason:      "CLEAN_EOF",
	}, outputPath); err == nil {
		t.Fatal("expected zero-duration recording to fail")
	}
}

func TestBuildLocalProcessingSourceURL_DefaultsToMKV(t *testing.T) {
	h := &ProcessingJobHandler{mistServerURL: "http://mistserver:4242/api2"}
	req := &ipcpb.ProcessingJobRequest{Params: map[string]string{
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
	for _, want := range []string{"audio=all", "video=all%2C%21JPEG", "meta=all%2C%21thumbvtt", "subtitle=all"} {
		if !strings.Contains(got, want) {
			t.Fatalf("source URL = %q, want %s query", got, want)
		}
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
	req := &ipcpb.ProcessingJobRequest{
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
	req := &ipcpb.ProcessingJobRequest{
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
	req := &ipcpb.ProcessingJobRequest{ArtifactHash: "cliphash", Params: map[string]string{}}

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

func TestWaitForProcessingStreamReadyReturnsLivepeerFallbackOnBootExit(t *testing.T) {
	h := &ProcessingJobHandler{}
	req := &ipcpb.ProcessingJobRequest{
		ProcessesJson: `[{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps"}]`,
	}
	processExitCh := make(chan ProcessExitEvent, 1)
	processExitCh <- ProcessExitEvent{
		StreamName:  "processing+artifact",
		ProcessType: "Livepeer",
		ExitCode:    2,
		BootCount:   1,
		Status:      "unrecoverable",
		Reason:      "too many upload failures",
	}

	_, _, err := h.waitForProcessingStreamReady(logrus.NewEntry(logrus.New()), nil, req, "processing+artifact", req.GetProcessesJson(), processExitCh, nil, nil, map[string]int{})
	var fallbackErr *livepeerReadinessFallbackError
	if !errors.As(err, &fallbackErr) {
		t.Fatalf("expected livepeer readiness fallback error, got %v", err)
	}
	if fallbackErr.evt.Reason != "too many upload failures" {
		t.Fatalf("fallback reason = %q", fallbackErr.evt.Reason)
	}
}

func TestNextProcessExitEventSkipsRetiredGenerations(t *testing.T) {
	ignored := map[string]int{}
	ignoreProcessExitThrough(ignored, "Livepeer", 2)
	processExitCh := make(chan ProcessExitEvent, 2)
	processExitCh <- ProcessExitEvent{ProcessType: "Livepeer", BootCount: 2, Status: "unrecoverable"}
	processExitCh <- ProcessExitEvent{ProcessType: "Livepeer", BootCount: 3, Status: "unrecoverable"}

	evt, ok := nextProcessExitEvent(processExitCh, ignored)
	if !ok {
		t.Fatal("expected eligible process exit event")
	}
	if evt.BootCount != 3 {
		t.Fatalf("boot count = %d, want 3", evt.BootCount)
	}
}

func TestProcessSegmentCompleteListenersRouteByStream(t *testing.T) {
	avCh := RegisterProcessAVSegmentCompleteListener("processing+target")
	defer UnregisterProcessAVSegmentCompleteListener("processing+target")
	lpCh := RegisterLivepeerSegmentCompleteListener("processing+target")
	defer UnregisterLivepeerSegmentCompleteListener("processing+target")

	RouteProcessAVSegmentComplete(ProcessAVSegmentCompleteEvent{StreamName: "processing+other", IsFinal: true})
	RouteLivepeerSegmentComplete(LivepeerSegmentCompleteEvent{StreamName: "processing+other", RenditionCount: 4})
	if _, ok := nextProcessAVSegmentCompleteEvent(avCh); ok {
		t.Fatal("unexpected AV event for different stream")
	}
	if _, ok := nextLivepeerSegmentCompleteEvent(lpCh); ok {
		t.Fatal("unexpected Livepeer event for different stream")
	}

	RouteProcessAVSegmentComplete(ProcessAVSegmentCompleteEvent{StreamName: "processing+target", IsFinal: true})
	RouteLivepeerSegmentComplete(LivepeerSegmentCompleteEvent{StreamName: "processing+target", RenditionCount: 4})
	if _, ok := nextProcessAVSegmentCompleteEvent(avCh); !ok {
		t.Fatal("expected AV event for target stream")
	}
	if _, ok := nextLivepeerSegmentCompleteEvent(lpCh); !ok {
		t.Fatal("expected Livepeer event for target stream")
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

// recordingEndPredatesPush must reject only events from a push that started
// before the current attempt. The current push's recording starts at or after
// the captured push-start on the same host clock, so the boundary is strict:
// TimeStarted == pushStartedAt is the live event, not a stale one.
func TestRecordingEndPredatesPush(t *testing.T) {
	const pushStartedAt int64 = 1000
	cases := []struct {
		name        string
		timeStarted int64
		pushStarted int64
		wantStale   bool
	}{
		{"one second earlier is stale", pushStartedAt - 1, pushStartedAt, true},
		{"same second is live", pushStartedAt, pushStartedAt, false},
		{"later is live", pushStartedAt + 1, pushStartedAt, false},
		{"zero timestamp is never stale", 0, pushStartedAt, false},
		{"no push baseline is never stale", 500, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordingEndPredatesPush(tc.timeStarted, tc.pushStarted); got != tc.wantStale {
				t.Fatalf("recordingEndPredatesPush(%d, %d) = %v, want %v",
					tc.timeStarted, tc.pushStarted, got, tc.wantStale)
			}
		})
	}
}

// A stale RECORDING_END from a retired push must not starve the restarted push's
// authoritative event. With a single-slot listener the stale event fills the
// channel and the non-blocking send drops the real one; the buffered listener
// plus the predicate filter must surface the live event instead of timing out.
func TestProcessingRecordingEnd_StaleThenCurrentDelivery(t *testing.T) {
	const streamName = "processing+stale-then-current"
	ch := registerProcessingRecordingEndListener(streamName)
	defer unregisterProcessingRecordingEndListener(streamName)

	const pushStartedAt int64 = 2000
	// Retired push (started before the current attempt) lands first.
	SignalProcessingRecordingEnd(ProcessingRecordingEndEvent{
		StreamName:   streamName,
		TimeStarted:  pushStartedAt - 1,
		BytesWritten: 1,
	})
	// The restarted push's authoritative completion event.
	SignalProcessingRecordingEnd(ProcessingRecordingEndEvent{
		StreamName:   streamName,
		TimeStarted:  pushStartedAt,
		BytesWritten: 999,
	})

	var got *ProcessingRecordingEndEvent
	for got == nil {
		select {
		case evt := <-ch:
			if recordingEndPredatesPush(evt.TimeStarted, pushStartedAt) {
				continue
			}
			e := evt
			got = &e
		case <-time.After(time.Second):
			t.Fatal("live RECORDING_END was dropped (channel starvation)")
		}
	}
	if got.BytesWritten != 999 {
		t.Fatalf("expected live event (bytes=999), got bytes=%d", got.BytesWritten)
	}
}

func TestDrainProcessingGenerationWaitsUntilStreamDisappears(t *testing.T) {
	const streamName = "processing+drain"
	calls := 0
	err := drainProcessingGenerationFromActiveStreams(logrus.NewEntry(logrus.New()), streamName, func() (map[string]interface{}, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New("mist api temporarily unavailable")
		case 2:
			return map[string]interface{}{
				"active_streams": map[string]interface{}{
					streamName: map[string]interface{}{"outputs": float64(1)},
				},
			}, nil
		default:
			return map[string]interface{}{
				"active_streams": map[string]interface{}{},
			}, nil
		}
	}, 100*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("drain returned error: %v", err)
	}
	if calls < 3 {
		t.Fatalf("drain stopped after %d calls, want at least 3", calls)
	}
}

type fakeProcessingRuntimeClient struct {
	calls  []string
	pushes []mist.PushInfo
}

func (f *fakeProcessingRuntimeClient) PushList() ([]mist.PushInfo, error) {
	f.calls = append(f.calls, "push_list")
	return f.pushes, nil
}

func (f *fakeProcessingRuntimeClient) PushKill(pushID int) error {
	f.calls = append(f.calls, "push_kill:"+strconv.Itoa(pushID))
	return nil
}

func (f *fakeProcessingRuntimeClient) NukeStream(name string) error {
	f.calls = append(f.calls, "nuke_stream:"+name)
	return nil
}

func (f *fakeProcessingRuntimeClient) StopSessions(streamName string) error {
	f.calls = append(f.calls, "stop_sessions:"+streamName)
	return nil
}

func (f *fakeProcessingRuntimeClient) GetActiveStreams() (map[string]interface{}, error) {
	f.calls = append(f.calls, "active_streams")
	return map[string]interface{}{"active_streams": map[string]interface{}{}}, nil
}

func TestRestartProcessingStreamForLocalFallbackStopsSessionsBeforeDrain(t *testing.T) {
	const streamName = "processing+fallback"
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "out.mkv")
	if err := os.WriteFile(outputPath, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := &fakeProcessingRuntimeClient{
		pushes: []mist.PushInfo{{ID: 7, StreamName: streamName}},
	}
	h := NewProcessingJobHandler(logrus.New(), "", dir)

	if err := h.restartProcessingStreamForLocalFallback(logrus.NewEntry(logrus.New()), client, streamName, outputPath, 99); err != nil {
		t.Fatalf("restartProcessingStreamForLocalFallback returned error: %v", err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("retired output still exists or stat failed with %v", err)
	}

	want := strings.Join([]string{
		"push_kill:99",
		"stop_sessions:" + streamName,
		"nuke_stream:" + streamName,
		"stop_sessions:" + streamName,
		"active_streams",
	}, ",")
	if got := strings.Join(client.calls, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestRestartProcessingStreamForLocalFallbackFallsBackToStreamLookupWhenPushIDMissing(t *testing.T) {
	const streamName = "processing+fallback"
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "out.mkv")
	if err := os.WriteFile(outputPath, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := &fakeProcessingRuntimeClient{
		pushes: []mist.PushInfo{
			{ID: 6, StreamName: streamName, TargetURI: "/tmp/other.mkv"},
			{ID: 7, StreamName: streamName, TargetURI: processingMuxTargetURI(outputPath)},
		},
	}
	h := NewProcessingJobHandler(logrus.New(), "", dir)

	if err := h.restartProcessingStreamForLocalFallback(logrus.NewEntry(logrus.New()), client, streamName, outputPath, 0); err != nil {
		t.Fatalf("restartProcessingStreamForLocalFallback returned error: %v", err)
	}

	wantPrefix := strings.Join([]string{
		"push_list",
		"push_kill:7",
	}, ",")
	if got := strings.Join(client.calls[:2], ","); got != wantPrefix {
		t.Fatalf("first calls = %s, want %s", got, wantPrefix)
	}
}

func TestFindProcessingPushIDMatchesStreamAndTarget(t *testing.T) {
	const streamName = "processing+find"
	const targetURI = "/tmp/out.mkv#audio=all&video=all&meta=all&subtitle=all"
	client := &fakeProcessingRuntimeClient{
		pushes: []mist.PushInfo{
			{ID: 3, StreamName: streamName, TargetURI: "/tmp/other.mkv"},
			{ID: 4, StreamName: "processing+other", TargetURI: targetURI},
			{ID: 5, StreamName: streamName, TargetURI: targetURI},
		},
	}

	got := findProcessingPushID(logrus.NewEntry(logrus.New()), client, streamName, targetURI)
	if got != 5 {
		t.Fatalf("push id = %d, want 5", got)
	}
}

func TestDrainProcessingGenerationFailsWhenStreamStaysActive(t *testing.T) {
	const streamName = "processing+stuck"
	err := drainProcessingGenerationFromActiveStreams(logrus.NewEntry(logrus.New()), streamName, func() (map[string]interface{}, error) {
		return map[string]interface{}{
			"active_streams": map[string]interface{}{
				streamName: map[string]interface{}{"outputs": float64(1)},
			},
		}, nil
	}, 5*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected stuck stream to fail drain")
	}
	if !strings.Contains(err.Error(), "still active after drain deadline") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func validDTSHBytes() []byte {
	return dtscHeaderPacket(packedObject(
		packedMember("version", packedInt(1)),
		packedMember("tracks", packedObject(
			packedMember("1", packedObject(
				packedMember("type", packedString("video")),
			)),
		)),
	))
}

func emptyTrackDTSHBytes() []byte {
	return dtscHeaderPacket(packedObject(
		packedMember("version", packedInt(1)),
		packedMember("tracks", packedObject()),
	))
}

func dtscHeaderPacket(payload []byte) []byte {
	out := make([]byte, 8, len(payload)+8)
	copy(out, "DTSC")
	binary.BigEndian.PutUint32(out[4:8], uint32(len(payload)))
	return append(out, payload...)
}

func packedObject(members ...[]byte) []byte {
	out := []byte{0xe0}
	for _, member := range members {
		out = append(out, member...)
	}
	return append(out, 0x00, 0x00, 0xee)
}

func packedMember(name string, value []byte) []byte {
	out := make([]byte, 2, len(name)+len(value)+2)
	binary.BigEndian.PutUint16(out, uint16(len(name)))
	out = append(out, []byte(name)...)
	return append(out, value...)
}

func packedInt(v uint64) []byte {
	out := make([]byte, 9)
	out[0] = 0x01
	binary.BigEndian.PutUint64(out[1:], v)
	return out
}

func packedString(v string) []byte {
	out := make([]byte, 5, len(v)+5)
	out[0] = 0x02
	binary.BigEndian.PutUint32(out[1:], uint32(len(v)))
	return append(out, []byte(v)...)
}
