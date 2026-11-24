package mist

import "testing"

func TestLiveRetentionPreservesExplicitNonH264AndNonRetainingPolicies(t *testing.T) {
	for _, policy := range []string{
		`[{"process":"Thumbs","track_retain":"video=all"}]`,
		`[{"process":"AV","codec":"HEVC","track_retain":"video=hevc"}]`,
		`[{"process":"Livepeer","target_profiles":[{"encoder":"VP9","track_retain":"video=vp9"}]}]`,
		`[{"process":"Livepeer","target_profiles":[{"profile":"H265Main","track_retain":"video=hevc"}]}]`,
		`[{"process":"Livepeer","target_profiles":[{"profile":"H264Main"}]}]`,
		`[{"process":"AV","codec":"H264","track_retain":""}]`,
		`not-json`,
	} {
		if got := ConstrainLiveRenditionRetention(policy); got != policy {
			t.Fatalf("unrelated or non-retaining policy changed: %s", got)
		}
	}
}

func TestRetainSelectorRemovesOnlyReorderedTracks(t *testing.T) {
	tracks := []TrackInfo{
		{Index: 0, Codec: "H264", Original: true},
		{Index: 1, Codec: "H264", Original: true, HasBframes: true},
		{Index: 2, Codec: "HEVC", Original: true},
	}
	for _, selector := range []string{"video=h264,hevc,|source,|h264,!bframes", "video=!bframes,|h264"} {
		selected, err := selectRetainTracks(selector, tracks)
		if err != nil || len(selected) != 1 || selected[0].Index != 0 {
			t.Fatalf("selector %q = %+v, %v; want only the H264 track without reordering", selector, selected, err)
		}
	}
}
