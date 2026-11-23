package mist

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// catalogRungs are the standard ladder's rungs in catalog order.
var catalogRungs = []RetainRung{{360, 900000}, {480, 1600000}, {720, 4500000}, {1080, 7800000}}

// mbit converts megabits per second to Mist's track bps (bytes per second).
func mbit(v float64) uint64 { return uint64(v * 1e6 / 8) }

func h264(index, w, h int, bps uint64) TrackInfo {
	return TrackInfo{Index: index, Codec: "H264", Width: w, Height: h, Bps: bps, Original: true}
}

func TestRetainSelectorComposition(t *testing.T) {
	for _, tc := range []struct {
		rung RetainRung
		want string
	}{
		{RetainRung{360, 900000}, "video=h264,hevc,|source,|>0x327,|<0x393,|>439kbps,|<1758kbps"},
		{RetainRung{480, 1600000}, "video=h264,hevc,|source,|>0x447,|<0x513,|>781kbps,|<3126kbps"},
		{RetainRung{720, 4500000}, "video=h264,hevc,|source,|>0x683,|<0x757,|>2197kbps,|<8790kbps"},
		{RetainRung{1080, 7800000}, "video=h264,hevc,|source,|>0x1025,|<0x1135,|>3808kbps,|<15235kbps"},
		{RetainRung{0, 900000}, ""},
		{RetainRung{360, 0}, ""},
	} {
		if got := RetainSelector(tc.rung); got != tc.want {
			t.Errorf("RetainSelector(%v) = %q, want %q", tc.rung, got, tc.want)
		}
	}
}

// Mist compares a track's bytes per second strictly against kbps*1024/8, so
// the band boundary is the composed kbps value, not the exact 0.5x/2x rate.
func TestRetainSelectorBandBoundaryMatchesMist(t *testing.T) {
	rung := RetainRung{480, 1600000}
	for _, tc := range []struct {
		bps  uint64
		want bool
	}{
		{781 * 128, false},
		{781*128 + 1, true},
		{100000, true},
		{400000, true},
		{3126*128 - 1, true},
		{3126 * 128, false},
	} {
		plan := PlanRetention([]TrackInfo{h264(0, 1920, 1080, mbit(30)), h264(1, 854, 480, tc.bps)}, []RetainRung{rung})
		if len(plan.Rungs) != 1 {
			t.Fatalf("rungs = %v, want one entry", plan.Rungs)
		}
		if got := plan.Rungs[0].Track == 1; got != tc.want {
			t.Errorf("480p track at %d B/s retained = %v, want %v", tc.bps, got, tc.want)
		}
	}
}

func TestPlanRetention(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tracks   []TrackInfo
		source   int
		retained []int
		masked   []int
	}{
		{
			name:     "sane 1080/720/360",
			tracks:   []TrackInfo{h264(0, 1920, 1080, mbit(7.5)), h264(1, 1280, 720, mbit(4)), h264(2, 640, 360, mbit(1))},
			source:   0,
			retained: []int{2, -1, 1, 0},
		},
		{
			name:     "whack 720 at 15 Mbps is the source and is replaced",
			tracks:   []TrackInfo{h264(0, 1920, 1080, mbit(7.5)), h264(1, 1280, 720, mbit(15))},
			source:   1,
			retained: []int{-1, -1, -1, 0},
			masked:   []int{1},
		},
		{
			name: "HEVC 720 in band",
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(7.5)),
				{Index: 1, Codec: "HEVC", Width: 1280, Height: 720, Bps: mbit(4), Original: true}},
			source:   0,
			retained: []int{-1, -1, 1, 0},
		},
		{
			name: "AV1 720 is out",
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(7.5)),
				{Index: 1, Codec: "AV1", Width: 1280, Height: 720, Bps: mbit(4), Original: true}},
			source:   0,
			retained: []int{-1, -1, -1, 0},
			masked:   []int{1},
		},
		{
			name:     "portrait",
			tracks:   []TrackInfo{h264(0, 1080, 1920, mbit(7.5)), h264(1, 720, 1280, mbit(4))},
			source:   0,
			retained: []int{-1, -1, 1, 0},
		},
		{
			name:     "two candidates for one rung",
			tracks:   []TrackInfo{h264(0, 1920, 1080, mbit(7.5)), h264(1, 1280, 720, mbit(3)), h264(2, 1280, 720, mbit(5))},
			source:   0,
			retained: []int{-1, -1, 2, 0},
			masked:   []int{1},
		},
		{
			name:     "single in-band source fills its rung",
			tracks:   []TrackInfo{h264(0, 1920, 1080, mbit(6))},
			source:   0,
			retained: []int{-1, -1, -1, 0},
		},
		{
			name:     "single out-of-band source stays masked and transcoded",
			tracks:   []TrackInfo{h264(0, 1920, 1080, mbit(20))},
			source:   0,
			retained: []int{-1, -1, -1, -1},
			masked:   []int{0},
		},
		{
			name:     "source below every rung keeps playing",
			tracks:   []TrackInfo{h264(0, 426, 240, mbit(5))},
			source:   0,
			retained: []int{-1, -1, -1, -1},
		},
		{
			name: "renditions and images never retain or become the source",
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(7.5)),
				{Index: 1, Codec: "H264", Width: 1280, Height: 720, Bps: mbit(40)},
				{Index: 2, Codec: "JPEG", Width: 640, Height: 360, Bps: mbit(50), Original: true, Image: true}},
			source:   0,
			retained: []int{-1, -1, -1, 0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanRetention(tc.tracks, catalogRungs)
			if plan.Source != tc.source {
				t.Errorf("source = %d, want %d", plan.Source, tc.source)
			}
			if len(plan.Rungs) != len(catalogRungs) {
				t.Fatalf("rungs = %v, want %d entries", plan.Rungs, len(catalogRungs))
			}
			var source TrackInfo
			for _, track := range tc.tracks {
				if track.Index == tc.source {
					source = track
				}
			}
			for i, rung := range plan.Rungs {
				if rung.Track != tc.retained[i] {
					t.Errorf("%dp retained by %d, want %d", catalogRungs[i].ShortSide, rung.Track, tc.retained[i])
				}
				wantTranscode := tc.retained[i] < 0 && min(source.Width, source.Height) >= catalogRungs[i].ShortSide
				if rung.Transcode != wantTranscode {
					t.Errorf("%dp transcode = %v, want %v", catalogRungs[i].ShortSide, rung.Transcode, wantTranscode)
				}
			}
			if fmt.Sprint(plan.Masked) != fmt.Sprint(tc.masked) {
				t.Errorf("masked = %v, want %v", plan.Masked, tc.masked)
			}
		})
	}
}

func TestTranscodeSourceIndexIsMaxbpsOriginal(t *testing.T) {
	tracks := []TrackInfo{h264(3, 1920, 1080, mbit(5)), h264(1, 1280, 720, mbit(15)), {Index: 0, Codec: "H264", Width: 3840, Height: 2160, Bps: mbit(40)}}
	if got := TranscodeSourceIndex(tracks); got != 1 {
		t.Errorf("source = %d, want the 15 Mbps original 1", got)
	}
	unrated := []TrackInfo{h264(5, 1920, 1080, 0), h264(2, 1280, 720, 0)}
	if got := TranscodeSourceIndex(unrated); got != 2 {
		t.Errorf("unrated source = %d, want the lowest index 2", got)
	}
	if got := TranscodeSourceIndex(nil); got != -1 {
		t.Errorf("empty source = %d, want -1", got)
	}
}

func retainLadderConfig(t *testing.T) string {
	t.Helper()
	profiles := []map[string]any{}
	for _, rung := range catalogRungs {
		profiles = append(profiles, map[string]any{
			"name": fmt.Sprintf("%dp", rung.ShortSide), "height": rung.ShortSide, "bitrate": rung.Bitrate,
			"track_inhibit": fmt.Sprintf("video=<0x%d", rung.ShortSide), "track_retain": RetainSelector(rung),
		})
	}
	encoded, err := json.Marshal([]map[string]any{{"process": "Livepeer", "track_inhibit": "video=<0x360", "target_profiles": profiles}})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type renditionOutcome struct {
	Name     string
	Retained bool
	Track    int
	Short    int
}

func renditionOutcomes(got []ExpectedRendition) []renditionOutcome {
	out := make([]renditionOutcome, 0, len(got))
	for _, r := range got {
		out = append(out, renditionOutcome{r.Name, r.Retained, r.TrackIndex, r.ShortSide})
	}
	return out
}

func TestExpectedVideoRenditionsReportsRetainedRungs(t *testing.T) {
	config := retainLadderConfig(t)
	for _, tc := range []struct {
		name   string
		source SourceMediaInfo
		tracks []TrackInfo
		want   []renditionOutcome
	}{
		{
			name:   "sane multi-rendition upload",
			source: SourceMediaInfo{Width: 1920, Height: 1080},
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(7.5)), h264(1, 1280, 720, mbit(4)), h264(2, 640, 360, mbit(1))},
			want:   []renditionOutcome{{"360p", true, 2, 360}, {"480p", false, 0, 480}, {"720p", true, 1, 720}, {"1080p", true, 0, 1080}},
		},
		{
			name:   "single in-band source fills its own rung",
			source: SourceMediaInfo{Width: 1920, Height: 1080},
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(6))},
			want:   []renditionOutcome{{"360p", false, 0, 360}, {"480p", false, 0, 480}, {"720p", false, 0, 720}, {"1080p", true, 0, 1080}},
		},
		{
			name:   "single out-of-band source transcodes every rung",
			source: SourceMediaInfo{Width: 1920, Height: 1080},
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(20))},
			want:   []renditionOutcome{{"360p", false, 0, 360}, {"480p", false, 0, 480}, {"720p", false, 0, 720}, {"1080p", false, 0, 1080}},
		},
		{
			name:   "without a track list nothing is retained",
			source: SourceMediaInfo{Width: 1920, Height: 1080},
			want:   []renditionOutcome{{"360p", false, 0, 360}, {"480p", false, 0, 480}, {"720p", false, 0, 720}, {"1080p", false, 0, 1080}},
		},
		{
			name:   "too small is judged against the source only",
			source: SourceMediaInfo{Width: 1280, Height: 720},
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(7.5)), h264(1, 1280, 720, mbit(15))},
			want:   []renditionOutcome{{"360p", false, 0, 360}, {"480p", false, 0, 480}, {"720p", false, 0, 720}, {"1080p", true, 0, 1080}},
		},
		{
			name:   "an out-of-band 1080 above a 720 source is not rendered",
			source: SourceMediaInfo{Width: 1280, Height: 720},
			tracks: []TrackInfo{h264(0, 1920, 1080, mbit(1)), h264(1, 1280, 720, mbit(4))},
			want:   []renditionOutcome{{"360p", false, 0, 360}, {"480p", false, 0, 480}, {"720p", true, 1, 720}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpectedVideoRenditions(config, tc.source, tc.tracks...)
			if err != nil {
				t.Fatal(err)
			}
			if outcomes := renditionOutcomes(got); !reflect.DeepEqual(outcomes, tc.want) {
				t.Errorf("renditions = %+v, want %+v", outcomes, tc.want)
			}
		})
	}
}

func TestExpectedVideoRenditionsRetainsLocalAVRung(t *testing.T) {
	config := fmt.Sprintf(`[{"process":"AV","codec":"H264","resolution":"x720","bitrate":4500000,"track_inhibit":"video=<1280x720","track_retain":%q}]`,
		RetainSelector(RetainRung{720, 4500000}))
	tracks := []TrackInfo{h264(0, 1920, 1080, mbit(7.5)), h264(1, 1280, 720, mbit(4))}
	got, err := ExpectedVideoRenditions(config, SourceMediaInfo{Width: 1920, Height: 1080}, tracks...)
	if err != nil {
		t.Fatal(err)
	}
	if want := []renditionOutcome{{"", true, 1, 720}}; !reflect.DeepEqual(renditionOutcomes(got), want) {
		t.Errorf("renditions = %+v, want %+v", renditionOutcomes(got), want)
	}
	if got[0].Width != 1280 || got[0].Height != 720 {
		t.Errorf("retained geometry = %dx%d, want the track's 1280x720", got[0].Width, got[0].Height)
	}
}

func TestExpectedVideoRenditionsRejectsUnknownRetainSelector(t *testing.T) {
	config := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720,"bitrate":4500000,"track_retain":"video=~1280x720"}]}]`
	if _, err := ExpectedVideoRenditions(config, SourceMediaInfo{Width: 1920, Height: 1080}, h264(0, 1920, 1080, mbit(7.5))); err == nil {
		t.Error("unknown track_retain selector accepted")
	}
}
