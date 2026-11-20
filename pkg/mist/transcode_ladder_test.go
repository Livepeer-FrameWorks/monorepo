package mist

import (
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/transcode/contract"
)

func TestNamedLaddersUseTheSourceShortSide(t *testing.T) {
	rungs := []contract.RenditionSpec{
		{Name: "1080p", Codec: "H264", Resolution: "x1080", Bitrate: 6500000, Profile: "high", TrackInhibit: "video=<1920x1080"},
		{Name: "1440p", Codec: "H264", Resolution: "x1440", Bitrate: 10800000, Profile: "high", TrackInhibit: "video=<2560x1440"},
	}
	for _, size := range [][2]int{{3840, 1080}, {1440, 1080}, {1080, 1080}, {1080, 1920}} {
		profiles := NormalizeLivepeerProfiles(LadderProfiles(rungs), SourceMediaInfo{Width: size[0], Height: size[1]})
		if len(profiles) != 1 || profiles[0]["name"] != "1080p" {
			t.Errorf("%dx%d must keep its 1080 rung without upscaling 1440: %v", size[0], size[1], profiles)
		}
	}
}

func TestNamedLadderPreservesSquareSourcesAtTheMinimumRung(t *testing.T) {
	rungs := []contract.RenditionSpec{
		{Name: "360p", Codec: "H264", Resolution: "x360", Bitrate: 900000, Profile: "high", TrackInhibit: "video=<640x360"},
		{Name: "480p", Codec: "H264", Resolution: "x480", Bitrate: 1600000, Profile: "high", TrackInhibit: "video=<850x480"},
	}
	policy, err := SetTranscodeLadder(`[{"process":"Livepeer","track_inhibit":"video=<640x360"}]`, rungs)
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{policy, ReplaceLivepeerWithLocal(policy)} {
		for _, short := range []int{360, 480} {
			got, err := ExpectedVideoRenditions(config, SourceMediaInfo{Width: short, Height: short})
			want := 1
			if short == 480 {
				want = 2
			}
			if err != nil || len(got) != want {
				t.Fatalf("square %d: expected %d rungs, got %v: %v", short, want, got, err)
			}
		}
	}
}

func TestSetTranscodeLadderReplacesOnlyTheLivepeerProfiles(t *testing.T) {
	input := `[{"process":"AV","codec":"AAC"},{"process":"Livepeer","source_mask":4,"job_token":"token","target_profiles":[{"height":360}]}]`
	rungs := []contract.RenditionSpec{{Name: "1440p", Codec: "H264", Resolution: "x1440", Bitrate: 10800000, Profile: "high", TrackInhibit: "video=<2560x1440"}, {Name: "2160p", Codec: "H264", Resolution: "x2160", Bitrate: 15000000, Profile: "high", TrackInhibit: "video=<3840x2160"}}
	got, err := SetTranscodeLadder(input, rungs)
	if err != nil {
		t.Fatal(err)
	}
	var processes []map[string]any
	if err := json.Unmarshal([]byte(got), &processes); err != nil {
		t.Fatal(err)
	}
	if processes[0]["codec"] != "AAC" || processes[1]["source_mask"] != float64(4) || processes[1]["job_token"] != "token" {
		t.Fatalf("other policy changed: %s", got)
	}
	for _, policy := range []string{got, ReplaceLivepeerWithLocal(got)} {
		expected, err := ExpectedVideoRenditions(policy, SourceMediaInfo{Width: 3840, Height: 2160})
		if err != nil || len(expected) != 2 || expected[0].ShortSide != 1440 || expected[1].ShortSide != 2160 {
			t.Fatalf("UHD geometry = %v, error %v", expected, err)
		}
	}
}
