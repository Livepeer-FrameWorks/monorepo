package triggers

import (
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestLivePlaybackRetainsOnlyCompatibleOriginals(t *testing.T) {
	selector := mist.RetainSelector(mist.RetainRung{ShortSide: 360, Bitrate: 900000})
	encoded, err := json.Marshal([]map[string]any{{"process": "Livepeer", "target_profiles": []map[string]any{{"name": "360p", "height": 360, "bitrate": 900000, "profile": "H264ConstrainedHigh", "track_retain": selector}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, codec, workload string
		bframes, retained     bool
	}{
		{"live H264 without reordering", "H264", mist.WorkloadLive, false, true},
		{"live H264 with reordering", "H264", mist.WorkloadLive, true, false},
		{"live HEVC needs a compatible H264 rendition", "HEVC", mist.WorkloadLive, false, false},
		{"VOD preserves H264 reordering", "H264", mist.WorkloadVOD, true, true},
		{"VOD preserves HEVC", "HEVC", mist.WorkloadVOD, false, true},
	} {
		for _, local := range []bool{false, true} {
			name := tc.name + "/gateway"
			if local {
				name = tc.name + "/local"
			}
			t.Run(name, func(t *testing.T) {
				raw, marshalErr := json.Marshal([]map[string]any{{"Index": 0, "Codec": tc.codec, "Width": 640, "Height": 360, "Bps": 112500, "Original": true, "has_bframes": tc.bframes}})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				var tracks []mist.TrackInfo
				if decodeErr := json.Unmarshal(raw, &tracks); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				policy := string(encoded)
				if local {
					policy = mist.ReplaceLivepeerWithLocal(policy)
				}
				p := &Processor{}
				bound := p.ApplyLivepeerWorkload(policy, tc.workload)
				t.Logf("bound policy: %s", bound)
				repeated := p.ApplyLivepeerWorkload(bound, tc.workload)
				if bound != repeated {
					t.Fatal("binding live playback requirements must be idempotent")
				}
				renditions, renditionErr := mist.ExpectedVideoRenditions(bound, mist.SourceMediaInfo{VideoCodec: tc.codec, Width: 640, Height: 360, FPS: 15}, tracks...)
				if renditionErr != nil {
					t.Fatalf("live rendition contract is invalid: %v", renditionErr)
				}
				if len(renditions) != 1 || renditions[0].Retained != tc.retained {
					t.Fatalf("retained rendition=%+v, want retained=%v; live playback must have a decodable H264 track without B-frames; policy=%s", renditions, tc.retained, bound)
				}
			})
		}
	}
}
