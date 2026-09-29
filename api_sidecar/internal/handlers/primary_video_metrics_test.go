package handlers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestPrimaryVideoMetricsExcludeThumbnails(t *testing.T) {
	for _, codec := range []string{"JPEG", "jpeg", "MJPEG"} {
		t.Run(codec, func(t *testing.T) {
			tracks := []*ipcpb.StreamTrack{
				{TrackType: "video", Codec: codec, Width: int32Ptr(1280), Height: int32Ptr(720)},
				{TrackType: "video", Codec: "H264", Width: int32Ptr(320), Height: int32Ptr(180), Fps: float64Ptr(15)},
			}
			if got := determineQualityTier(tracks); got != "SD15 H264" {
				t.Fatalf("thumbnail selected as quality tier: %q", got)
			}
			list := &ipcpb.StreamTrackListTrigger{Tracks: tracks}
			enrichLiveTrackListTrigger(list)
			if list.GetPrimaryVideoCodec() != "H264" || list.GetPrimaryWidth() != 320 || list.GetPrimaryHeight() != 180 {
				t.Fatalf("thumbnail selected as primary track: %v", list)
			}
			if list.GetVideoTrackCount() != 2 {
				t.Fatal("thumbnail must remain in the full track inventory")
			}
			buffer := &ipcpb.StreamBufferTrigger{Tracks: tracks}
			enrichStreamBufferTrigger(buffer)
			if buffer.GetQualityTier() != "SD15 H264" {
				t.Fatalf("buffer trigger selected thumbnail: %q", buffer.GetQualityTier())
			}
			details := []map[string]any{
				{"type": "video", "codec": codec, "width": 1280, "height": 720},
				{"type": "video", "codec": "H264", "width": 320, "height": 180, "fps": float64(15)},
			}
			poll := convertStreamAPIToMistTrigger("node", "live+stream", "stream", nil, nil, details, 2, logging.NewLogger()).GetStreamLifecycleUpdate()
			if poll.GetPrimaryCodec() != "H264" || poll.GetPrimaryWidth() != 320 || poll.GetQualityTier() != "SD15 H264" {
				t.Fatalf("poll selected thumbnail: %v", poll)
			}
			if got := determineQualityTier(tracks[:1]); got != "" {
				t.Fatalf("thumbnail-only tracks are not a video quality tier: %q", got)
			}
		})
	}
}

// Mist reports tracks as a JSON object, so a rendition or preview can precede
// the source. The source's resolution is the stream's tier on every path:
// STREAM_BUFFER and LIVE_TRACK_LIST triggers and the stream API poll.
func TestPrimaryVideoIsSourceNotRendition(t *testing.T) {
	tracks := []*ipcpb.StreamTrack{
		{TrackType: "video", Codec: "JPEG", TrackIndex: int32Ptr(3), Width: int32Ptr(160), Height: int32Ptr(90)},
		{TrackType: "video", Codec: "H264", TrackIndex: int32Ptr(4), SourceTrack: stringPtr("0"), Width: int32Ptr(640), Height: int32Ptr(360), Fps: float64Ptr(30)},
		{TrackType: "audio", Codec: "AAC", TrackIndex: int32Ptr(1)},
		{TrackType: "video", Codec: "H264", TrackIndex: int32Ptr(0), Width: int32Ptr(1920), Height: int32Ptr(1080), Fps: float64Ptr(30)},
	}
	if got := determineQualityTier(tracks); got != "1080p30 H264" {
		t.Fatalf("quality tier = %q, want 1080p30 H264", got)
	}
	list := &ipcpb.StreamTrackListTrigger{Tracks: tracks}
	enrichLiveTrackListTrigger(list)
	if list.GetPrimaryWidth() != 1920 || list.GetPrimaryHeight() != 1080 || list.GetQualityTier() != "1080p30 H264" {
		t.Fatalf("track list primary = %dx%d tier %q, want the 1920x1080 source", list.GetPrimaryWidth(), list.GetPrimaryHeight(), list.GetQualityTier())
	}
	buffer := &ipcpb.StreamBufferTrigger{Tracks: tracks}
	enrichStreamBufferTrigger(buffer)
	if buffer.GetQualityTier() != "1080p30 H264" {
		t.Fatalf("buffer quality tier = %q, want 1080p30 H264", buffer.GetQualityTier())
	}
	details := []map[string]any{
		{"type": "video", "codec": "H264", "track_index": 4, "source_track": "0", "width": 640, "height": 360, "fps": float64(30)},
		{"type": "video", "codec": "H264", "track_index": 5, "width": 1920, "height": 1080, "fps": float64(30)},
		{"type": "video", "codec": "JPEG", "track_index": 3, "width": 160, "height": 90},
	}
	poll := convertStreamAPIToMistTrigger("node", "live+stream", "stream", nil, nil, details, 3, logging.NewLogger()).GetStreamLifecycleUpdate()
	if poll.GetPrimaryHeight() != 1080 || poll.GetQualityTier() != "1080p30 H264" {
		t.Fatalf("poll primary height %d tier %q, want the 1080p source", poll.GetPrimaryHeight(), poll.GetQualityTier())
	}
}

// A source whose dimensions Mist has not reported yet (0 at boot) has an
// unknown resolution: no tier and no primary dimensions, never SD, and never
// a rendition's resolution in its place.
func TestPrimaryVideoUnknownHeightStaysUnknown(t *testing.T) {
	tracks := []*ipcpb.StreamTrack{
		{TrackType: "video", Codec: "H264", TrackIndex: int32Ptr(0), Width: int32Ptr(0), Height: int32Ptr(0)},
		{TrackType: "video", Codec: "H264", TrackIndex: int32Ptr(2), SourceTrack: stringPtr("0"), Width: int32Ptr(640), Height: int32Ptr(360)},
	}
	if got := determineQualityTier(tracks); got != "" {
		t.Fatalf("quality tier = %q, want unknown", got)
	}
	list := &ipcpb.StreamTrackListTrigger{Tracks: tracks}
	enrichLiveTrackListTrigger(list)
	if list.PrimaryHeight != nil || list.PrimaryWidth != nil || list.QualityTier != nil {
		t.Fatalf("unknown source dimensions reported as primary %v x %v tier %v", list.PrimaryWidth, list.PrimaryHeight, list.QualityTier)
	}
	details := []map[string]any{
		{"type": "video", "codec": "H264", "track_index": 0, "width": 0, "height": 0},
		{"type": "video", "codec": "H264", "track_index": 2, "source_track": "0", "width": 640, "height": 360},
	}
	poll := convertStreamAPIToMistTrigger("node", "live+stream", "stream", nil, nil, details, 2, logging.NewLogger()).GetStreamLifecycleUpdate()
	if poll.PrimaryHeight != nil || poll.QualityTier != nil {
		t.Fatalf("poll reported unknown source as height %v tier %v", poll.PrimaryHeight, poll.QualityTier)
	}
}
