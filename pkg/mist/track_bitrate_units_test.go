package mist

import (
	"strconv"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Mist reports a track's bps in bytes per second (DTSC::Meta::getBps) and
// health kbits as getBps*8/1024. StreamTrack.bitrate_bps is bits per second
// and bitrate_kbps decimal kilobits per second, whatever the payload carried.
// The values below are a 4 Mbit/s video and a 128 kbit/s audio track.
func TestTrackBitrateUnitsAcrossMistPayloads(t *testing.T) {
	const videoBytesPerSec = 500000 // 4,000,000 bit/s
	const audioBytesPerSec = 16000  // 128,000 bit/s
	logger := logging.NewLogger()

	type want struct {
		bps  int64
		kbps int32
	}
	check := func(t *testing.T, name string, got map[string][2]int64, expect map[string]want) {
		t.Helper()
		for track, w := range expect {
			g, ok := got[track]
			if !ok {
				t.Fatalf("%s: track %s missing", name, track)
			}
			if g[0] != w.bps || g[1] != int64(w.kbps) {
				t.Fatalf("%s: %s bitrate_bps=%d bitrate_kbps=%d, want %d bit/s and %d kbit/s", name, track, g[0], g[1], w.bps, w.kbps)
			}
		}
	}

	t.Run("LIVE_TRACK_LIST bps is bytes per second", func(t *testing.T) {
		list := `{"video_H264_1920x1080_30fps_0":{"codec":"H264","type":"video","idx":0,"width":1920,"height":1080,"bps":500000},` +
			`"audio_AAC_2ch_48000hz_1":{"codec":"AAC","type":"audio","idx":1,"channels":2,"rate":48000,"bps":16000}}`
		trig, err := ParseTriggerToProtobuf(TriggerLiveTrackList, joinPayload("live+x", list), "node-1", logger)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][2]int64{}
		for _, tr := range trig.GetTrackList().GetTracks() {
			got[tr.GetTrackType()] = [2]int64{tr.GetBitrateBps(), int64(tr.GetBitrateKbps())}
		}
		check(t, "LIVE_TRACK_LIST", got, map[string]want{
			"video": {videoBytesPerSec * 8, 4000},
			"audio": {audioBytesPerSec * 8, 128},
		})
	})

	t.Run("STREAM_BUFFER kbits is bytes*8/1024", func(t *testing.T) {
		// Mist: kbits = getBps*8/1024, integer.
		videoKbits := videoBytesPerSec * 8 / 1024 // 3906
		audioKbits := audioBytesPerSec * 8 / 1024 // 125
		health := `{"health":{"buffer":5000,"video_H264_1920x1080_30fps_0":{"codec":"H264","type":"video","idx":0,"width":1920,"height":1080,"kbits":` +
			strconv.Itoa(videoKbits) + `},"audio_AAC_2ch_48000hz_1":{"codec":"AAC","type":"audio","idx":1,"kbits":` + strconv.Itoa(audioKbits) + `}}}`
		trig, err := ParseTriggerToProtobuf(TriggerStreamBuffer, joinPayload("live+x", "FULL", health), "node-1", logger)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][2]int64{}
		for _, tr := range trig.GetStreamBuffer().GetTracks() {
			got[tr.GetTrackType()] = [2]int64{tr.GetBitrateBps(), int64(tr.GetBitrateKbps())}
		}
		check(t, "STREAM_BUFFER", got, map[string]want{
			"video": {int64(videoKbits) * 1024, 3999},
			"audio": {int64(audioKbits) * 1024, 128},
		})
	})

	t.Run("RECORDING_END summary bps is bytes per second", func(t *testing.T) {
		summary := `{"tracks":[{"idx":0,"id":1,"type":"video","codec":"H264","width":1920,"height":1080,"firstms":0,"lastms":30000,"bps":500000}]}`
		payload := []byte("processing+job1\n/tmp/out.mkv\nMistOutEBML\n10261530\n19\n1700000000\n1700000019\n30033\n0\n30033\nCLEAN_EOF\nend of stream\n" + summary + "\n")
		trig, err := ParseTriggerToProtobuf(TriggerRecordingEnd, payload, "node-1", logger)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][2]int64{}
		for _, tr := range trig.GetRecordingComplete().GetTracks() {
			got[tr.GetTrackType()] = [2]int64{tr.GetBitrateBps(), int64(tr.GetBitrateKbps())}
		}
		check(t, "RECORDING_END", got, map[string]want{"video": {videoBytesPerSec * 8, 4000}})
	})
}
