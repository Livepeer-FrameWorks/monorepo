package mist

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// catalogLivepeerProcesses is the live ABR ladder from the billing catalog, as
// dispatched (Foghorn stamps the workload onto the Livepeer process).
const catalogLivepeerProcesses = `[{"process":"Livepeer","workload":"live","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"},{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"},{"name":"720p","bitrate":3200000,"fps":0,"height":720,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1280x720"},{"name":"1080p","bitrate":6500000,"fps":0,"height":1080,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1920x1080"}],"track_inhibit":"video=<640x360"}]`

type wireRung struct {
	name          string
	width, height int
	bitrate       int
}

// gatewayWire renders profiles the way go-livepeer's auth webhook sends them:
// Mist's profiles decoded into lpms' ffmpeg.JsonProfile and re-encoded, so every
// JsonProfile field is present (zero-valued when Mist did not set it) and any
// other key is gone. It then decodes them the way Foghorn's handler does.
func gatewayWire(t *testing.T, rungs []wireRung, fpsNum, fpsDen int) []LivepeerJSONProfile {
	t.Helper()
	parts := make([]string, 0, len(rungs))
	for _, r := range rungs {
		parts = append(parts, fmt.Sprintf(
			`{"name":%q,"width":%d,"height":%d,"bitrate":%d,"fps":%d,"fpsDen":%d,"profile":"H264ConstrainedHigh","gop":"0.0","encoder":"","colorDepth":0,"chromaFormat":0,"quality":0}`,
			r.name, r.width, r.height, r.bitrate, fpsNum, fpsDen))
	}
	var out []LivepeerJSONProfile
	if err := json.Unmarshal([]byte("["+strings.Join(parts, ",")+"]"), &out); err != nil {
		t.Fatalf("decode gateway wire profiles: %v", err)
	}
	return out
}

func expectedCatalogProfiles(t *testing.T, source SourceMediaInfo) []LivepeerJSONProfile {
	t.Helper()
	spec, err := LivepeerJobSpecFromProcessesJSON(catalogLivepeerProcesses)
	if err != nil {
		t.Fatalf("parse catalog spec: %v", err)
	}
	return NormalizeLivepeerProfiles(spec.Profiles, source)
}

// Widths below come from Mist's process_livepeer evenScaledDimension
// ((n*r + d/2)/d, rounded up to even), computed by hand, not by Go's normaliser.
func TestLivepeerProfileMismatchAcceptsGatewayWireShape(t *testing.T) {
	cases := []struct {
		name           string
		source         SourceMediaInfo
		fpsNum, fpsDen int
		rungs          []wireRung
	}{
		{
			name: "720p30 landscape (staging rc5)", source: SourceMediaInfo{Width: 1280, Height: 720, FPS: 30},
			fpsNum: 30000, fpsDen: 1000,
			rungs: []wireRung{{"360p", 640, 360, 900000}, {"480p", 854, 480, 1600000}, {"720p", 1280, 720, 3200000}},
		},
		{
			name: "1080p30 landscape", source: SourceMediaInfo{Width: 1920, Height: 1080, FPS: 30},
			fpsNum: 30000, fpsDen: 1000,
			rungs: []wireRung{{"360p", 640, 360, 900000}, {"480p", 854, 480, 1600000}, {"720p", 1280, 720, 3200000}, {"1080p", 1920, 1080, 6500000}},
		},
		{
			name: "480p15 landscape", source: SourceMediaInfo{Width: 854, Height: 480, FPS: 15},
			fpsNum: 15000, fpsDen: 1000,
			rungs: []wireRung{{"360p", 642, 360, 900000}, {"480p", 854, 480, 1600000}},
		},
		{
			name: "720x1280 portrait", source: SourceMediaInfo{Width: 720, Height: 1280, FPS: 30},
			fpsNum: 30000, fpsDen: 1000,
			rungs: []wireRung{{"360p", 360, 640, 900000}, {"480p", 480, 854, 1600000}, {"720p", 720, 1280, 3200000}},
		},
		{
			name: "measured source rate drifts from the probe", source: SourceMediaInfo{Width: 1280, Height: 720, FPS: 30},
			fpsNum: 29983, fpsDen: 1000,
			rungs: []wireRung{{"360p", 640, 360, 900000}, {"480p", 854, 480, 1600000}, {"720p", 1280, 720, 3200000}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := expectedCatalogProfiles(t, tc.source)
			observed := NormalizeLivepeerProfiles(gatewayWire(t, tc.rungs, tc.fpsNum, tc.fpsDen), tc.source)
			// The wire carries lpms fields the spec never sets, so a structural
			// comparison of the two can never succeed (staging rc5 spec_mismatch).
			if _, ok := expected[0]["encoder"]; ok {
				t.Fatal("setup: the job spec must not carry lpms' encoder field")
			}
			if _, ok := observed[0]["encoder"]; !ok {
				t.Fatal("setup: gateway wire profiles must carry lpms' encoder field")
			}
			if diff := LivepeerProfileMismatch(observed, expected, tc.source); diff != "" {
				t.Fatalf("gateway profiles rejected: %s", diff)
			}
		})
	}
}

func TestLivepeerProfileMismatchRejectsChangedWork(t *testing.T) {
	source := SourceMediaInfo{Width: 1280, Height: 720, FPS: 30}
	ladder := []wireRung{{"360p", 640, 360, 900000}, {"480p", 854, 480, 1600000}, {"720p", 1280, 720, 3200000}}
	cases := []struct {
		name  string
		wire  func(t *testing.T) []LivepeerJSONProfile
		field string
	}{
		{"doubled bitrate", func(t *testing.T) []LivepeerJSONProfile {
			rungs := append([]wireRung(nil), ladder...)
			rungs[2].bitrate *= 2
			return gatewayWire(t, rungs, 30000, 1000)
		}, "bitrate"},
		{"upscaled extra rung", func(t *testing.T) []LivepeerJSONProfile {
			return gatewayWire(t, append(append([]wireRung(nil), ladder...), wireRung{"1080p", 1920, 1080, 6500000}), 30000, 1000)
		}, "profile count"},
		{"frame rate far from the source", func(t *testing.T) []LivepeerJSONProfile {
			return gatewayWire(t, ladder, 60000, 1000)
		}, "fps"},
		{"non-default encoder", func(t *testing.T) []LivepeerJSONProfile {
			wire := gatewayWire(t, ladder, 30000, 1000)
			wire[0]["encoder"] = "nvenc"
			return wire
		}, "encoder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := expectedCatalogProfiles(t, source)
			observed := NormalizeLivepeerProfiles(tc.wire(t), source)
			diff := LivepeerProfileMismatch(observed, expected, source)
			if !strings.Contains(diff, tc.field) {
				t.Fatalf("want a %q difference, got %q", tc.field, diff)
			}
		})
	}
}

func TestLivepeerProfileMismatchExplicitRateIsExact(t *testing.T) {
	source := SourceMediaInfo{Width: 1280, Height: 720, FPS: 30}
	expected := []LivepeerJSONProfile{{"name": "720p", "width": 1280, "height": 720, "bitrate": 3200000, "fps": 25, "fpsDen": 1, "profile": "H264ConstrainedHigh", "gop": "0.0"}}
	observed := gatewayWire(t, []wireRung{{"720p", 1280, 720, 3200000}}, 30000, 1000)
	if diff := LivepeerProfileMismatch(observed, expected, source); !strings.Contains(diff, "fps") {
		t.Fatalf("a configured 25 fps rung must not accept the source's 30 fps, got %q", diff)
	}
	matching := gatewayWire(t, []wireRung{{"720p", 1280, 720, 3200000}}, 25000, 1000)
	if diff := LivepeerProfileMismatch(matching, expected, source); diff != "" {
		t.Fatalf("25/1 and 25000/1000 are the same rate, got %q", diff)
	}
}

func TestLivepeerProfileMismatchUnknownSourceUsesDefaultRate(t *testing.T) {
	source := SourceMediaInfo{Width: 1280, Height: 720}
	expected := expectedCatalogProfiles(t, source)
	rungs := []wireRung{{"360p", 640, 360, 900000}, {"480p", 854, 480, 1600000}, {"720p", 1280, 720, 3200000}}
	observed := gatewayWire(t, rungs, 60000, 1000)
	if diff := LivepeerProfileMismatch(observed, expected, source); !strings.Contains(diff, "fps") {
		t.Fatalf("unknown source must not authorize an arbitrary observed rate, got %q", diff)
	}
	observed = gatewayWire(t, rungs, 25000, 1000)
	if diff := LivepeerProfileMismatch(observed, expected, source); diff != "" {
		t.Fatalf("default 25 fps rate should match, got %q", diff)
	}
}

func TestLivepeerProfileMismatchDoesNotRoundNumericFields(t *testing.T) {
	expected := []LivepeerJSONProfile{{"name": "360p", "width": 640, "height": 360, "bitrate": 900000, "fps": 25, "fpsDen": 1}}
	observed := []LivepeerJSONProfile{{"name": "360p", "width": 640, "height": 360, "bitrate": 900000.4, "fps": 25, "fpsDen": 1}}
	if diff := LivepeerProfileMismatch(observed, expected, SourceMediaInfo{}); !strings.Contains(diff, "bitrate") {
		t.Fatalf("a changed numeric field must not be rounded away, got %q", diff)
	}
}
