package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestIsMediaHealthTrack(t *testing.T) {
	cases := []struct {
		trackType, codec string
		want             bool
	}{
		{"video", "H264", true},
		{"video", "", true},
		{"audio", "opus", true},
		{"video", "JPEG", false},
		{"video", "PNG", false},
		{"meta", "JSON", false},
		{"meta", "thumbvtt", false},
		{"meta", "subtitle", false},
		{"unknown", "H264", false},
		{"", "AAC", false},
	}
	for _, c := range cases {
		if got := isMediaHealthTrack(c.trackType, c.codec); got != c.want {
			t.Errorf("isMediaHealthTrack(%q, %q) = %v, want %v", c.trackType, c.codec, got, c.want)
		}
	}
}

// Thumbnail and metadata tracks are sparse by design; their buffer and jitter
// must not flag the stream as unhealthy on either the trigger or poll path.
func TestStreamHealthIgnoresNonMediaTracks(t *testing.T) {
	tracks := []*ipcpb.StreamTrack{
		{TrackName: "video_H264_1", TrackType: "video", Codec: "H264", Buffer: int32Ptr(2000), Jitter: int32Ptr(20)},
		{TrackName: "audio_AAC_2", TrackType: "audio", Codec: "AAC", Buffer: int32Ptr(2000), Jitter: int32Ptr(20)},
		{TrackName: "video_JPEG_3", TrackType: "video", Codec: "JPEG", Buffer: int32Ptr(0), Jitter: int32Ptr(5000)},
		{TrackName: "meta_JSON_4", TrackType: "meta", Codec: "JSON", Buffer: int32Ptr(0), Jitter: int32Ptr(5000)},
		{TrackName: "meta_thumbvtt_5", TrackType: "meta", Codec: "thumbvtt", Buffer: int32Ptr(0), Jitter: int32Ptr(5000)},
	}
	buffer := &ipcpb.StreamBufferTrigger{Tracks: tracks}
	enrichStreamBufferTrigger(buffer)
	if buffer.GetHasIssues() {
		t.Fatalf("buffer trigger flagged non-media tracks: %q", buffer.GetIssuesDescription())
	}

	details := []map[string]any{
		{"track_name": "video_H264_1", "type": "video", "codec": "H264", "buffer": 2000, "jitter": 20, "width": 320, "height": 180},
		{"track_name": "video_PNG_3", "type": "video", "codec": "PNG", "buffer": 0, "jitter": 5000, "width": 1280, "height": 720},
		{"track_name": "video_JPEG_4", "type": "video", "codec": "JPEG", "buffer": 0, "jitter": 5000},
		{"track_name": "meta_JSON_5", "type": "meta", "codec": "JSON", "buffer": 0, "jitter": 5000},
	}
	poll := convertStreamAPIToMistTrigger("node", "live+stream", "stream", nil, nil, details, len(details), logging.NewLogger()).GetStreamLifecycleUpdate()
	if poll.GetHasIssues() {
		t.Fatalf("poll flagged non-media tracks: %q", poll.GetIssuesDescription())
	}
	if poll.GetPrimaryWidth() != 320 {
		t.Fatalf("poll selected a PNG thumbnail as primary video: width %d", poll.GetPrimaryWidth())
	}
}

func TestStreamHealthStillFlagsMediaTracks(t *testing.T) {
	buffer := &ipcpb.StreamBufferTrigger{Tracks: []*ipcpb.StreamTrack{
		{TrackName: "video_H264_1", TrackType: "video", Codec: "H264", Buffer: int32Ptr(10), Jitter: int32Ptr(500)},
	}}
	enrichStreamBufferTrigger(buffer)
	desc := buffer.GetIssuesDescription()
	if !buffer.GetHasIssues() || !strings.Contains(desc, "High jitter") || !strings.Contains(desc, "Low buffer") {
		t.Fatalf("media track issues not reported: %q", desc)
	}

	details := []map[string]any{
		{"track_name": "audio_AAC_1", "type": "audio", "codec": "AAC", "buffer": 10, "jitter": 500},
	}
	poll := convertStreamAPIToMistTrigger("node", "live+stream", "stream", nil, nil, details, 1, logging.NewLogger()).GetStreamLifecycleUpdate()
	if !poll.GetHasIssues() || !strings.Contains(poll.GetIssuesDescription(), "High jitter") {
		t.Fatalf("poll did not report media track issues: %q", poll.GetIssuesDescription())
	}
}

// A transcoder hands back whole segments, so a rendition's jitter equals its
// segment interval. Mist judges stability on original tracks only; the trigger
// and the poll path (parsed from Mist's health map) must do the same.
func TestStreamHealthIgnoresTranscodedRenditions(t *testing.T) {
	buffer := &ipcpb.StreamBufferTrigger{Tracks: []*ipcpb.StreamTrack{
		{TrackName: "video_H264_1920x1080_30fps_0", TrackType: "video", Codec: "H264", Buffer: int32Ptr(2000), Jitter: int32Ptr(20)},
		{TrackName: "video_H264_1280x720_30fps_2", TrackType: "video", Codec: "H264", Buffer: int32Ptr(2000), Jitter: int32Ptr(2000), SourceTrack: stringPtr("video_H264_1920x1080_30fps_0")},
	}}
	enrichStreamBufferTrigger(buffer)
	if buffer.GetHasIssues() {
		t.Fatalf("buffer trigger flagged a rendition: %q", buffer.GetIssuesDescription())
	}

	previousLogger := monitorLogger
	monitorLogger = logging.NewLogger()
	t.Cleanup(func() { monitorLogger = previousLogger })
	var sent *ipcpb.MistTrigger
	pm := &PrometheusMonitor{sendControlTrigger: func(trigger *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		sent = trigger
		return &control.MistTriggerResult{}, nil
	}}
	health := map[string]any{
		"buffer":                       float64(9000),
		"video_H264_1920x1080_30fps_0": map[string]any{"idx": float64(0), "codec": "H264", "width": float64(1920), "height": float64(1080), "jitter": float64(20), "buffer": float64(2000)},
		"video_H264_1280x720_30fps_2":  map[string]any{"idx": float64(2), "codec": "H264", "width": float64(1280), "height": float64(720), "jitter": float64(2000), "buffer": float64(2000), "source": "video_H264_1920x1080_30fps_0"},
	}
	now := time.Now()
	pm.processObservedStreamDataContext(context.Background(), "node", "live+stream",
		map[string]any{"inputs": float64(1), "health": health}, now.Add(-time.Second), now)
	if update := sent.GetStreamLifecycleUpdate(); update.GetHasIssues() {
		t.Fatalf("poll flagged a rendition: %q", update.GetIssuesDescription())
	}

	health["video_H264_1920x1080_30fps_0"].(map[string]any)["jitter"] = float64(2000)
	pm.processObservedStreamDataContext(context.Background(), "node", "live+stream",
		map[string]any{"inputs": float64(1), "health": health}, now.Add(-time.Second), now)
	if desc := sent.GetStreamLifecycleUpdate().GetIssuesDescription(); !strings.Contains(desc, "High jitter on track video_H264_1920x1080_30fps_0") || strings.Contains(desc, "1280x720") {
		t.Fatalf("poll issues = %q, want only the source track", desc)
	}
}
