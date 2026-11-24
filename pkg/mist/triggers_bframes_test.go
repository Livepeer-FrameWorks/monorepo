package mist

import (
	"encoding/json"
	"testing"
)

func TestParseTracksFromJSONNativeBFrameRepresentations(t *testing.T) {
	for _, tc := range []struct {
		raw          string
		value, valid bool
	}{
		{"true", true, true}, {"false", false, true}, {"1", true, true}, {"0", false, true},
		{"2", false, false}, {"0.5", false, false}, {"null", false, false}, {`"true"`, false, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal([]byte(`{"video_0":{"type":"video","codec":"H264","bframes":`+tc.raw+`}}`), &raw); err != nil {
				t.Fatal(err)
			}
			tracks := parseTracksFromJSON(raw)
			if len(tracks) != 1 {
				t.Fatalf("parsed tracks=%v", tracks)
			}
			if tc.valid && (tracks[0].HasBframes == nil || tracks[0].GetHasBframes() != tc.value) {
				t.Fatalf("native bframes %s lost or changed: %+v", tc.raw, tracks[0].HasBframes)
			}
			if !tc.valid && tracks[0].HasBframes != nil {
				t.Fatalf("invalid frame flag became a known value: %s", tc.raw)
			}
		})
	}
}
