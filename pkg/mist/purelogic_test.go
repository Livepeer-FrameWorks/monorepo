package mist

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- evenScaledDimension ---

// The H.264/H.265 encoders reject odd pixel dimensions, so a scaled dimension
// must always be even, and a degenerate scale (zero/negative denominator or
// requested side) must collapse to 0 rather than divide-by-zero or return junk.
func TestEvenScaledDimension(t *testing.T) {
	tests := []struct {
		name                              string
		numerator, requested, denominator int
		want                              int
	}{
		// 1280 * 360 / 720 = 640 (already even, unchanged)
		{"even result unchanged", 1280, 360, 720, 640},
		// 1750 * 360 / 2720 = 231.6 -> round 232 (even, unchanged)
		{"rounds then already even", 1750, 360, 2720, 232},
		// 641 * 1 / 1 = 641 -> odd -> bumped to 642
		{"odd value bumped even", 641, 1, 1, 642},
		{"zero denominator", 1280, 360, 0, 0},
		{"negative denominator", 1280, 360, -1, 0},
		{"zero requested", 1280, 0, 720, 0},
	}
	for _, tc := range tests {
		if got := evenScaledDimension(tc.numerator, tc.requested, tc.denominator); got != tc.want {
			t.Errorf("%s: evenScaledDimension(%d,%d,%d)=%d, want %d",
				tc.name, tc.numerator, tc.requested, tc.denominator, got, tc.want)
		}
		if got := evenScaledDimension(tc.numerator, tc.requested, tc.denominator); tc.want != 0 && got%2 != 0 {
			t.Errorf("%s: result %d is odd; H.264/H.265 require even dimensions", tc.name, got)
		}
	}
}

// --- numberAsInt ---

// numberAsInt rounds (never truncates) every numeric shape JSON/Go can hand it,
// and reports !ok for anything non-numeric so callers can distinguish "0" from
// "absent/garbage". NaN/Inf are intentionally not asserted: int(NaN) is
// implementation-defined in Go, so pinning a value here would test UB.
func TestNumberAsInt(t *testing.T) {
	tests := []struct {
		name   string
		in     any
		want   int
		wantOK bool
	}{
		{"float64 rounds up", float64(3.7), 4, true},
		{"float64 rounds down", float64(3.2), 3, true},
		{"float64 negative rounds away from zero", float64(-3.7), -4, true},
		{"float32 rounds", float32(3.7), 4, true},
		{"int passthrough", int(42), 42, true},
		{"int64 passthrough", int64(99), 99, true},
		{"json.Number rounds", json.Number("3.7"), 4, true},
		{"json.Number invalid", json.Number("abc"), 0, false},
		{"string is not numeric", "5", 0, false},
		{"bool is not numeric", true, 0, false},
		{"nil is not numeric", nil, 0, false},
	}
	for _, tc := range tests {
		got, ok := numberAsInt(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: numberAsInt(%#v)=(%d,%v), want (%d,%v)", tc.name, tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// --- BuildDVRTarget ---

// retention_days must be converted to seconds (days*86400); the defaults
// (6s segments, 7200s age) apply only when the config key is missing or <=0.
func TestBuildDVRTarget(t *testing.T) {
	t.Run("retention days converted to seconds", func(t *testing.T) {
		got := BuildDVRTarget("/store", "abcd", map[string]any{
			"retention_days":   2,
			"segment_duration": 4,
		})
		if !strings.Contains(got, "targetAge=172800") { // 2*24*3600
			t.Errorf("retention_days=2 should yield targetAge=172800, got %q", got)
		}
		if !strings.Contains(got, "split=4") {
			t.Errorf("segment_duration=4 should yield split=4, got %q", got)
		}
	})

	t.Run("defaults when missing", func(t *testing.T) {
		got := BuildDVRTarget("/store", "abcd", nil)
		for _, want := range []string{"split=6", "targetAge=7200", "append=1", "noendlist=1", "/store/abcd/", "abcd.m3u8"} {
			if !strings.Contains(got, want) {
				t.Errorf("default target missing %q: %q", want, got)
			}
		}
	})

	t.Run("non-positive config falls back to defaults", func(t *testing.T) {
		got := BuildDVRTarget("/store", "abcd", map[string]any{
			"retention_days":   0,
			"segment_duration": -1,
		})
		if !strings.Contains(got, "split=6") || !strings.Contains(got, "targetAge=7200") {
			t.Errorf("zero/negative config must use defaults, got %q", got)
		}
	})
}

// --- avTrackSelectForCodec ---

// The selector decides which tracks a transcode process consumes. An audio
// codec must drop video (and vice-versa); anything unknown or non-string must
// fall back to keeping both, never silently dropping a track.
func TestAVTrackSelectForCodec(t *testing.T) {
	const both = "audio=all&video=all"
	const audioOnly = "audio=all&video=none&subtitle=none"
	const videoOnly = "video=maxbps&audio=none&subtitle=none"
	tests := []struct {
		in   any
		want string
	}{
		{"aac", audioOnly},
		{"opus", audioOnly},
		{"AAC", audioOnly}, // case-insensitive
		{"h264", videoOnly},
		{"HEVC", videoOnly},
		{"av1", videoOnly},
		{"unknown-codec", both},
		{"", both},
		{42, both}, // non-string
		{nil, both},
	}
	for _, tc := range tests {
		if got := avTrackSelectForCodec(tc.in); got != tc.want {
			t.Errorf("avTrackSelectForCodec(%#v)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- shouldInhibitLivepeerSelector ---

// A "video=<WxH" inhibitor skips a rendition only when the source is strictly
// smaller than the ceiling on BOTH axes (upscaling is pointless). Equal or
// larger source, malformed selectors, and unknown source dims must all
// fail-open (don't inhibit), so a parse slip can never silently drop a needed
// rendition.
func TestShouldInhibitLivepeerSelector(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		source SourceMediaInfo
		want   bool
	}{
		{"source strictly smaller -> inhibit", "video=<1920x1080", SourceMediaInfo{Width: 1280, Height: 720}, true},
		{"source equal -> keep", "video=<1920x1080", SourceMediaInfo{Width: 1920, Height: 1080}, false},
		{"source larger -> keep", "video=<1280x720", SourceMediaInfo{Width: 1920, Height: 1080}, false},
		{"only one axis smaller -> keep", "video=<1920x1080", SourceMediaInfo{Width: 1280, Height: 1080}, false},
		{"not a dimension selector", "audio=all", SourceMediaInfo{Width: 1280, Height: 720}, false},
		{"missing x separator", "video=<1920", SourceMediaInfo{Width: 1280, Height: 720}, false},
		{"non-numeric dims", "video=<axb", SourceMediaInfo{Width: 1280, Height: 720}, false},
		{"unknown source dims", "video=<1920x1080", SourceMediaInfo{Width: 0, Height: 0}, false},
	}
	for _, tc := range tests {
		if got := shouldInhibitLivepeerSelector(tc.raw, tc.source); got != tc.want {
			t.Errorf("%s: shouldInhibitLivepeerSelector(%q,%+v)=%v, want %v", tc.name, tc.raw, tc.source, got, tc.want)
		}
	}
}

// --- pairSessionShares ---

// Names and seconds arrive as two parallel CSVs; the pairing must fail closed
// (return nil) on any length mismatch or unparseable seconds rather than
// misattributing watch-time to the wrong stream element.
func TestPairSessionShares(t *testing.T) {
	t.Run("exact pairing with trimming", func(t *testing.T) {
		got := pairSessionShares(" live+a , live+b ", "120, 45")
		if len(got) != 2 {
			t.Fatalf("want 2 shares, got %d", len(got))
		}
		if got[0].Name != "live+a" || got[0].Seconds != 120 {
			t.Errorf("share[0]=%+v, want {live+a 120}", got[0])
		}
		if got[1].Name != "live+b" || got[1].Seconds != 45 {
			t.Errorf("share[1]=%+v, want {live+b 45}", got[1])
		}
	})
	t.Run("zero seconds is valid", func(t *testing.T) {
		if got := pairSessionShares("live+a", "0"); len(got) != 1 || got[0].Seconds != 0 {
			t.Errorf("zero seconds should pair, got %+v", got)
		}
	})
	failCases := []struct {
		name        string
		names, secs string
	}{
		{"length mismatch", "a,b", "10"},
		{"empty inputs", "", ""},
		{"negative seconds", "a", "-5"},
		{"float seconds", "a", "10.5"},
		{"non-numeric seconds", "a", "x"},
	}
	for _, tc := range failCases {
		if got := pairSessionShares(tc.names, tc.secs); got != nil {
			t.Errorf("%s: expected nil (fail-closed), got %+v", tc.name, got)
		}
	}
}

// --- LivepeerProfilesFromProcessesJSON ---

// Extracts the FIRST Livepeer process's target_profiles and normalizes them;
// non-Livepeer entries are skipped, and a malformed config (bad JSON or a
// present-but-invalid target_profiles on the first Livepeer entry) returns nil
// so callers fail closed instead of transcoding an empty ladder.
func TestLivepeerProfilesFromProcessesJSON(t *testing.T) {
	src := SourceMediaInfo{Width: 1280, Height: 720, FPS: 30}

	t.Run("skips non-Livepeer, returns first Livepeer normalized", func(t *testing.T) {
		in := `[
			{"process":"AV","codec":"AAC"},
			{"process":"Livepeer","target_profiles":[{"name":"360p","height":360}]},
			{"process":"Livepeer","target_profiles":[{"name":"480p","height":480}]}
		]`
		got := LivepeerProfilesFromProcessesJSON(in, src)
		if len(got) != 1 {
			t.Fatalf("want 1 profile from first Livepeer entry, got %d (%+v)", len(got), got)
		}
		// NormalizeLivepeerProfiles injects defaults; gop proves normalization ran.
		if _, ok := got[0]["gop"]; !ok {
			t.Errorf("expected normalized profile to carry default gop, got %+v", got[0])
		}
		if got[0]["name"] != "360p" {
			t.Errorf("expected the FIRST Livepeer entry's profile, got name=%v", got[0]["name"])
		}
	})

	t.Run("no Livepeer process -> nil", func(t *testing.T) {
		if got := LivepeerProfilesFromProcessesJSON(`[{"process":"AV"}]`, src); got != nil {
			t.Errorf("no Livepeer process should yield nil, got %+v", got)
		}
	})

	t.Run("unparseable JSON -> nil", func(t *testing.T) {
		if got := LivepeerProfilesFromProcessesJSON(`not json`, src); got != nil {
			t.Errorf("bad JSON should yield nil, got %+v", got)
		}
	})

	t.Run("malformed target_profiles on first Livepeer -> nil", func(t *testing.T) {
		in := `[{"process":"Livepeer","target_profiles":"oops"}]`
		if got := LivepeerProfilesFromProcessesJSON(in, src); got != nil {
			t.Errorf("invalid target_profiles should fail closed (nil), got %+v", got)
		}
	})
}

// --- calculatePasswordHash ---

// MistServer's controller auth is MD5(hex(MD5(password)) + challenge). The
// two-step ordering is a wire contract: reordering it (e.g. hashing
// password+challenge directly) still compiles and runs but silently breaks
// login. These golden vectors were computed once externally and are frozen
// here precisely so a refactor that changes the algorithm fails loudly.
func TestCalculatePasswordHash_GoldenVectors(t *testing.T) {
	c := &Client{}
	tests := []struct {
		password, challenge, want string
	}{
		{"secret", "abc123", "7840a038e45863d5ef110af0145f1b06"},
		{"", "", "74be16979710d4c4e7c6647856088456"},
	}
	for _, tc := range tests {
		if got := c.calculatePasswordHash(tc.password, tc.challenge); got != tc.want {
			t.Errorf("calculatePasswordHash(%q,%q)=%q, want %q", tc.password, tc.challenge, got, tc.want)
		}
	}
}
