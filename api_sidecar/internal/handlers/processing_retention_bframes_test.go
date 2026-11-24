package handlers

import (
	"encoding/json"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestRetentionReadinessPreservesBFrames(t *testing.T) {
	outputs := extractActiveStreamMetadata(map[string]interface{}{"lastms": float64(30000), "health": map[string]interface{}{
		"video_H264_640x360_0": map[string]interface{}{"codec": "H264", "width": float64(640), "height": float64(360), "kbits": float64(879), "idx": float64(0), "id": float64(1), "bframes": true},
	}})
	var tracks []map[string]any
	if err := json.Unmarshal([]byte(outputs[processingSourceVideoTracksOutput]), &tracks); err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0]["has_bframes"] != true {
		t.Fatalf("readiness lost source frame reordering: %s", outputs[processingSourceVideoTracksOutput])
	}
}

func TestRetentionFrameReorderingSurvivesEachTrackInput(t *testing.T) {
	raw := map[string]interface{}{"video_1": map[string]interface{}{
		"type": "video", "codec": "H264", "width": float64(640), "height": float64(360), "bframes": true,
	}}
	bframes := true
	inputs := map[string][]processingMetaVideoTrack{
		"health":   inspectProcessingActiveStream(map[string]interface{}{"health": raw}).videoTracks,
		"metadata": parseProcessingMetaVideoTracks(map[string]interface{}{"tracks": raw}),
		"protocol": processingTracksFromProto([]*ipcpb.StreamTrack{{TrackType: "video", TrackName: "video_1", Codec: "H264", HasBframes: &bframes}}),
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			infos, _ := retentionTracks(input, nil)
			encoded, err := json.Marshal(infos)
			if err != nil {
				t.Fatal(err)
			}
			var tracks []map[string]any
			if decodeErr := json.Unmarshal(encoded, &tracks); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if len(tracks) != 1 || tracks[0]["has_bframes"] != true {
				t.Fatalf("frame reordering was lost: %s", encoded)
			}
		})
	}
}

func TestRetentionReadinessWithoutBFrameKnowledgeIsRefreshed(t *testing.T) {
	legacy := `[{"Index":0,"Codec":"H264","Width":640,"Height":360,"Bps":112500,"Original":true,"rotation":0,"mirror":false}]`
	if tracks := readinessVideoTracks(map[string]string{processingSourceVideoTracksOutput: legacy}); len(tracks) != 0 {
		t.Fatalf("legacy readiness cannot prove the live source can bypass its encoder: %+v", tracks)
	}
}

func TestRetentionNumericBFrameMetadata(t *testing.T) {
	raw := map[string]interface{}{"video_0": map[string]interface{}{"type": "video", "codec": "H264", "width": float64(640), "height": float64(360), "bframes": float64(1)}}
	tracks := parseProcessingMetaVideoTracks(map[string]interface{}{"tracks": raw})
	infos, _ := retentionTracks(tracks, nil)
	encoded, err := json.Marshal(infos)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if decodeErr := json.Unmarshal(encoded, &out); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(out) != 1 || out[0]["has_bframes"] != true {
		t.Fatalf("numeric Mist frame reordering was lost: %s", encoded)
	}
}
