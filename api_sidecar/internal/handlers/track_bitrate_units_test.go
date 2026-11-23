package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A 4 Mbit/s source: Mist bps (bytes/s) 500000, health kbits (bps*8/1024) 3906.
const (
	unitsVideoMistBps   = 500000
	unitsVideoMistKbits = unitsVideoMistBps * 8 / 1024
)

func TestLiveTrackListPrimaryBitrateIsKilobitsPerSecond(t *testing.T) {
	list := `{"video_H264_1920x1080_30fps_0":{"codec":"H264","type":"video","idx":0,"width":1920,"height":1080,"fpks":30000,"bps":500000},` +
		`"audio_AAC_2ch_48000hz_1":{"codec":"AAC","type":"audio","idx":1,"channels":2,"rate":48000,"bps":16000}}`
	trig, err := mist.ParseTriggerToProtobuf(mist.TriggerLiveTrackList, []byte("live+x\n"+list+"\n"), "node-1", logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	tl := trig.GetTrackList()
	enrichLiveTrackListTrigger(tl)
	if tl.GetPrimaryVideoBitrate() != 4000 || tl.GetPrimaryAudioBitrate() != 128 {
		t.Fatalf("primary bitrates = video %d / audio %d kbit/s, want 4000 / 128", tl.GetPrimaryVideoBitrate(), tl.GetPrimaryAudioBitrate())
	}
	if !strings.Contains(tl.GetQualityTier(), "4.0Mbps") {
		t.Fatalf("quality tier %q does not name the 4.0Mbps source", tl.GetQualityTier())
	}
}

func TestPollerPrimaryBitrateFromHealthKbits(t *testing.T) {
	previousLogger := monitorLogger
	monitorLogger = logging.NewLogger()
	t.Cleanup(func() { monitorLogger = previousLogger })
	var sent *ipcpb.MistTrigger
	pm := &PrometheusMonitor{sendControlTrigger: func(trigger *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		sent = trigger
		return &control.MistTriggerResult{}, nil
	}}
	health := map[string]any{
		"buffer": float64(9000),
		"video_H264_1920x1080_30fps_0": map[string]any{
			"idx": float64(0), "codec": "H264", "width": float64(1920), "height": float64(1080), "fpks": float64(30000), "kbits": float64(unitsVideoMistKbits),
		},
		"audio_AAC_2ch_48000hz_1": map[string]any{"idx": float64(1), "codec": "AAC", "channels": float64(2), "rate": float64(48000), "kbits": float64(125)},
	}
	now := time.Now()
	pm.processObservedStreamDataContext(context.Background(), "node", "live+stream",
		map[string]any{"inputs": float64(1), "health": health}, now.Add(-time.Second), now)
	update := sent.GetStreamLifecycleUpdate()
	if update.GetPrimaryBitrate() != 3999 || update.GetAudioBitrate() != 128 {
		t.Fatalf("primary bitrates = video %d / audio %d kbit/s, want 3999 / 128", update.GetPrimaryBitrate(), update.GetAudioBitrate())
	}
}

// Rendition retention compares TrackInfo.Bps as Mist bytes/s, so the
// RECORDING_END track bitrate must reach it as Mist reported it.
func TestRecordingEndTrackBpsStaysMistBytesPerSecond(t *testing.T) {
	summary := `{"tracks":[{"idx":0,"id":1,"type":"video","codec":"H264","width":1920,"height":1080,"firstms":0,"lastms":30000,"bps":500000}]}`
	payload := []byte("processing+job1\n/tmp/out.mkv\nMistOutEBML\n10261530\n19\n1700000000\n1700000019\n30033\n0\n30033\nCLEAN_EOF\nend of stream\n" + summary + "\n")
	trig, err := mist.ParseTriggerToProtobuf(mist.TriggerRecordingEnd, payload, "node-1", logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	tracks := processingTracksFromProto(trig.GetRecordingComplete().GetTracks())
	if len(tracks) != 1 || tracks[0].bps != unitsVideoMistBps {
		t.Fatalf("processing track bps = %+v, want Mist bytes/s %d", tracks, unitsVideoMistBps)
	}
}

// The artifact metadata bitrate_kbps is decimal kbit/s, not Mist's Kibit/s.
func TestActiveStreamMetadataBitrateIsDecimalKilobits(t *testing.T) {
	outputs := extractActiveStreamMetadata(map[string]interface{}{"health": map[string]interface{}{
		"video_H264_1920x1080_0": map[string]interface{}{"codec": "H264", "type": "video", "idx": 0.0, "width": 1920.0, "height": 1080.0, "kbits": float64(unitsVideoMistKbits)},
	}})
	if outputs["bitrate_kbps"] != "3999" {
		t.Fatalf("bitrate_kbps = %q, want 3999 (3906 Kibit/s)", outputs["bitrate_kbps"])
	}
}
