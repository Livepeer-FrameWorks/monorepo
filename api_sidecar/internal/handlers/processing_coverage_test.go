package handlers

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// coverageTrack builds a recorded track whose written span is first..last.
// The buffer window (first_ms/last_ms) is set to a later, evicted window on
// purpose: coverage must never read it.
func coverageTrack(name, typ, codec, source string, first, last int64) *ipcpb.StreamTrack {
	windowFirst, windowLast := last-1000, last
	t := &ipcpb.StreamTrack{TrackName: name, TrackType: typ, Codec: codec, FirstMs: &windowFirst, LastMs: &windowLast,
		WrittenFirstMs: &first, WrittenLastMs: &last}
	if source != "" {
		t.SourceTrack = &source
	}
	return t
}

func TestRenditionCoverageError(t *testing.T) {
	src := coverageTrack("video_H264_854x480", "video", "H264", "", 0, 180000)
	audio := coverageTrack("audio_AAC", "audio", "AAC", "", 0, 180000)
	srcName := "video_H264_854x480"
	thumbs := coverageTrack("video_JPEG_thu", "video", "JPEG", "video_H264_854x480", 170000, 170000)
	tests := []struct {
		name    string
		tracks  []*ipcpb.StreamTrack
		wantErr string
	}{
		{"full ladder", []*ipcpb.StreamTrack{src, audio, thumbs,
			coverageTrack("360p", "video", "H264", "video_H264_854x480", 0, 179960)}, ""},
		{"start within one key interval", []*ipcpb.StreamTrack{src,
			coverageTrack("360p", "video", "H264", "video_H264_854x480", 2000, 180000)}, ""},
		// The staging defect: a rendition that begins after the source's first
		// minute is missing media even though its tail reaches the end.
		{"rendition starts late", []*ipcpb.StreamTrack{src,
			coverageTrack("360p", "video", "H264", "video_H264_854x480", 62000, 180000)}, "starts 62000ms after the source"},
		{"rendition ends early", []*ipcpb.StreamTrack{src,
			coverageTrack("360p", "video", "H264", "video_H264_854x480", 0, 120000)}, "ends 60000ms before the source"},
		{"thumbnails are not renditions", []*ipcpb.StreamTrack{src, thumbs}, ""},
		{"no source video", []*ipcpb.StreamTrack{audio,
			coverageTrack("360p", "video", "H264", "video_H264_854x480", 90000, 91000)}, ""},
		{"rendition without a written span is not judged", []*ipcpb.StreamTrack{src,
			{TrackName: "360p", TrackType: "video", Codec: "H264", SourceTrack: &srcName}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := renditionCoverageError(tc.tracks)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestRenditionCoverageUsesSourceKeyInterval(t *testing.T) {
	src := coverageTrack("video", "video", "H264", "", 0, 60000)
	keyMs := 4000.0
	src.KeyframeMsMax = &keyMs
	if err := renditionCoverageError([]*ipcpb.StreamTrack{src, coverageTrack("r", "video", "H264", "video", 4000, 60000)}); err != nil {
		t.Fatalf("a rendition starting one source key interval late must pass: %v", err)
	}
	if err := renditionCoverageError([]*ipcpb.StreamTrack{src, coverageTrack("r", "video", "H264", "video", 4001, 60000)}); err == nil {
		t.Fatal("a rendition starting beyond one source key interval must fail")
	}
}

func TestRecordingEndRetryable(t *testing.T) {
	swapped := ProcessingRecordingEndEvent{ExitReason: mist.ExitReasonProcessTracksChanged, HumanExitReason: "EBML track 5 was not declared"}
	if !recordingEndRetryable(swapped) {
		t.Fatal("a producer swapped after the header must be retryable")
	}
	if err := validateProcessingRecordingEnd(swapped, ""); err == nil {
		t.Fatal("the swapped recording itself must still fail validation")
	}
	for _, reason := range []string{"", "UNKNOWN", "WRITE_FAILURE", "CLEAN_EOF"} {
		if recordingEndRetryable(ProcessingRecordingEndEvent{ExitReason: reason}) {
			t.Fatalf("exit reason %q must not be retryable", reason)
		}
	}
}

func TestActiveStreamsContain(t *testing.T) {
	name := "processing+abc"
	if !activeStreamsContain(map[string]interface{}{"active_streams": map[string]interface{}{name: map[string]interface{}{}}}, name) {
		t.Fatal("a listed stream is active")
	}
	if !activeStreamsContain(map[string]interface{}{"active_streams": []interface{}{name}}, name) {
		t.Fatal("a stream in the short form list is active")
	}
	if activeStreamsContain(map[string]interface{}{"active_streams": map[string]interface{}{}}, name) ||
		activeStreamsContain(map[string]interface{}{}, name) {
		t.Fatal("an unlisted stream has stopped")
	}
}

func TestRecordingDurationError(t *testing.T) {
	if err := recordingDurationError(133, 90000); err == nil || !strings.Contains(err.Error(), "133ms of a 90000ms") {
		t.Fatalf("a 133 ms recording of a 90 s source must fail: %v", err)
	}
	if err := recordingDurationError(89960, 90000); err != nil {
		t.Fatalf("a full recording must pass: %v", err)
	}
	if err := recordingDurationError(133, 0); err != nil {
		t.Fatalf("an unknown source duration is not judged: %v", err)
	}
}

// Staging (video-only import): RECORDING_END listed the 360/480/720
// renditions the recorder selected, but only the source was written. The
// unwritten ones must not count as present or reach the catalog.
func TestProcessingRecordingEndDropsSelectedButUnwrittenTracks(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	i64 := func(v int64) *int64 { return &v }
	sourceName := "video_src"
	source := &ipcpb.StreamTrack{TrackName: "video_src", TrackType: "video", Codec: "H264", Width: i32(1280), Height: i32(720),
		FirstMs: i64(0), LastMs: i64(9966), WrittenFirstMs: i64(0), WrittenLastMs: i64(9966)}
	unwritten := func(name string, w, h int32) *ipcpb.StreamTrack {
		return &ipcpb.StreamTrack{TrackName: name, TrackType: "video", Codec: "H264", Width: i32(w), Height: i32(h),
			FirstMs: i64(0), LastMs: i64(9966), SourceTrack: &sourceName}
	}
	rec := &ipcpb.RecordingCompleteTrigger{Tracks: []*ipcpb.StreamTrack{
		source, unwritten("r360", 640, 360), unwritten("r480", 854, 480), unwritten("r720", 1280, 720),
	}}
	evt := processingRecordingEndEvent(rec)
	if len(evt.FullTracks) != 1 || evt.FullTracks[0].GetTrackName() != "video_src" {
		t.Fatalf("full tracks = %v, want only the written source", evt.FullTracks)
	}
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720},{"name":"480p","height":480},{"name":"360p","height":360}]}]`
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	if livepeerRenditionsCompleteFromTracks(logrus.NewEntry(log), processes, evt.Tracks, mist.SourceMediaInfo{Width: 1280, Height: 720}, 9966) {
		t.Fatal("unwritten renditions were counted as present")
	}

	// A Mist that reports no written spans keeps every selected track.
	for _, tr := range rec.Tracks {
		tr.WrittenFirstMs, tr.WrittenLastMs = nil, nil
	}
	if got := len(processingRecordingEndEvent(rec).FullTracks); got != 4 {
		t.Fatalf("tracks without span reporting = %d, want 4", got)
	}
}
