package handlers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A transcoded live buffer lists the source next to same-size and smaller
// renditions. Mist's health map is unordered, so the primary track must come
// from the track index, not from map iteration.
func TestPollerPrimaryTracksAreTheSourceTracks(t *testing.T) {
	previousLogger := monitorLogger
	monitorLogger = logging.NewLogger()
	t.Cleanup(func() { monitorLogger = previousLogger })
	var sent *ipcpb.MistTrigger
	pm := &PrometheusMonitor{sendControlTrigger: func(trigger *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		sent = trigger
		return &control.MistTriggerResult{}, nil
	}}
	video := func(idx, width, height, kbits float64) map[string]any {
		return map[string]any{"idx": idx, "codec": "H264", "width": width, "height": height, "fpks": float64(30000), "kbits": kbits}
	}
	audio := func(idx float64, codec string) map[string]any {
		return map[string]any{"idx": idx, "codec": codec, "channels": float64(2), "rate": float64(48000), "kbits": float64(96)}
	}
	health := map[string]any{
		"buffer":                         float64(9000),
		"video_H264_1920x1080_30fps_0":   video(0, 1920, 1080, 5800),
		"audio_AAC_2ch_48000hz_1":        audio(1, "AAC"),
		"video_H264_1280x720_30fps_2":    video(2, 1280, 720, 2800),
		"video_H264_854x480_30fps_3":     video(3, 854, 480, 1200),
		"video_H264_640x360_30fps_4":     video(4, 640, 360, 700),
		"video_H264_1920x1080_30fps_5":   video(5, 1920, 1080, 4500),
		"audio_opus_2ch_48000hz_6":       audio(6, "opus"),
		"video_JPEG_1600x900_0fps_thu_7": map[string]any{"idx": float64(7), "codec": "JPEG", "width": float64(1600), "height": float64(900)},
		"meta_thumbvtt__8":               map[string]any{"idx": float64(8), "codec": "thumbvtt"},
		"video_JPEG_160x90_0fps_pre_9":   map[string]any{"idx": float64(9), "codec": "JPEG", "width": float64(160), "height": float64(90)},
	}
	now := time.Now()
	// Go randomizes map iteration; repeat so an order-dependent pick fails.
	for i := 0; i < 64; i++ {
		pm.processObservedStreamDataContext(context.Background(), "node", "live+stream",
			map[string]any{"inputs": float64(1), "health": health}, now.Add(-time.Second), now)
		update := sent.GetStreamLifecycleUpdate()
		if update.GetPrimaryWidth() != 1920 || update.GetPrimaryHeight() != 1080 || update.GetPrimaryBitrate() != 5800 {
			t.Fatalf("run %d: primary video is a rendition: %dx%d @ %d kbps", i, update.GetPrimaryWidth(), update.GetPrimaryHeight(), update.GetPrimaryBitrate())
		}
		if update.GetAudioCodec() != "AAC" {
			t.Fatalf("run %d: primary audio is the transcoded %q track", i, update.GetAudioCodec())
		}
	}
}
