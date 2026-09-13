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
