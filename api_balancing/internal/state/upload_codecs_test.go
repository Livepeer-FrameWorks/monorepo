package state

import "testing"

func TestUploadCodecTrackClassification(t *testing.T) {
	for codec, want := range map[string]string{"MPEG2": "video", "MPEG4": "video", "EAC3": "audio", "MP2": "audio", "FLAC": "audio", "PCM": "audio", "opus": "audio"} {
		tracks := extractTracksFromDetails(map[string]any{"track": map[string]any{"codec": codec}})
		if len(tracks) != 1 || tracks[0].Type != want {
			t.Errorf("%s: %v, want %s", codec, tracks, want)
		}
	}
}
