package mist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDisableProcessRestartsMarksEveryEntry(t *testing.T) {
	input := `[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360}]},{"process":"AV","codec":"opus"},{"process":"Thumbs","restart_type":"backoff"}]`
	var got []map[string]any
	if err := json.Unmarshal([]byte(DisableProcessRestarts(input)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 processes, got %d", len(got))
	}
	for i, proc := range got {
		if proc["restart_type"] != "disabled" {
			t.Errorf("process %d restart_type = %#v, want \"disabled\"", i, proc["restart_type"])
		}
	}
}

func TestReplaceLivepeerWithLocalCarriesRestartType(t *testing.T) {
	input := `[{"process":"Livepeer","restart_type":"disabled","target_profiles":[{"name":"360p","profile":"H264Main","height":360,"bitrate":900000}]}]`
	var got []map[string]any
	if err := json.Unmarshal([]byte(ReplaceLivepeerWithLocal(input)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 process, got %d", len(got))
	}
	if got[0]["restart_type"] != "disabled" {
		t.Errorf("restart_type = %#v, want \"disabled\"", got[0]["restart_type"])
	}
}

func TestSetLivepeerBroadcastersFillsEveryLivepeerEntry(t *testing.T) {
	input := `[
		{"process":"AV","codec":"AAC","track_select":"audio=all&video=none&subtitle=none"},
		{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[{"name":"360p","bitrate":900000,"height":360,"profile":"H264ConstrainedHigh"}]}
	]`

	out := SetLivepeerBroadcasters(input, []string{"https://gw1.example.com", "https://gw2.example.com"})

	var got []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var livepeer map[string]json.RawMessage
	for _, p := range got {
		var name string
		_ = json.Unmarshal(p["process"], &name)
		if name == "Livepeer" {
			livepeer = p
		}
	}
	if livepeer == nil {
		t.Fatal("expected a Livepeer entry to remain")
	}
	var encoded string
	if err := json.Unmarshal(livepeer["hardcoded_broadcasters"], &encoded); err != nil {
		t.Fatalf("hardcoded_broadcasters not a string: %v", err)
	}
	var entries []struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal([]byte(encoded), &entries); err != nil {
		t.Fatalf("parse broadcasters %q: %v", encoded, err)
	}
	if len(entries) != 2 || entries[0].Address != "https://gw1.example.com" || entries[1].Address != "https://gw2.example.com" {
		t.Fatalf("broadcasters = %+v, want gw1 then gw2 in order", entries)
	}
}

func TestSetLivepeerWorkloadVODStampsContract(t *testing.T) {
	input := `[
		{"process":"AV","codec":"AAC","track_select":"audio=all&video=none&subtitle=none"},
		{"process":"Livepeer","source_track":"maxbps","target_profiles":[{"name":"360p","height":360}]}
	]`

	out := SetLivepeerWorkload(input, WorkloadVOD, LivepeerVODSegmentDeadlineMs, LivepeerVODMinSpeed)

	var got []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, p := range got {
		var name string
		_ = json.Unmarshal(p["process"], &name)
		if name != "Livepeer" {
			// The AV entry must not be stamped.
			if _, ok := p["workload"]; ok {
				t.Fatal("workload leaked onto non-Livepeer process")
			}
			continue
		}
		var workload string
		if err := json.Unmarshal(p["workload"], &workload); err != nil || workload != WorkloadVOD {
			t.Fatalf("workload = %q err=%v, want vod", workload, err)
		}
		var deadline int
		if err := json.Unmarshal(p["deadline_ms"], &deadline); err != nil || deadline != LivepeerVODSegmentDeadlineMs {
			t.Fatalf("deadline_ms = %d err=%v, want %d", deadline, err, LivepeerVODSegmentDeadlineMs)
		}
		var minSpeed float64
		if err := json.Unmarshal(p["min_speed"], &minSpeed); err != nil || minSpeed != LivepeerVODMinSpeed {
			t.Fatalf("min_speed = %v err=%v, want %v", minSpeed, err, LivepeerVODMinSpeed)
		}
	}
}

func TestSetLivepeerWorkloadLiveOmitsDeadlineAndSpeed(t *testing.T) {
	input := `[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360}]}]`

	out := SetLivepeerWorkload(input, WorkloadLive, 0, 0)

	var got []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := got[0]
	var workload string
	if err := json.Unmarshal(p["workload"], &workload); err != nil || workload != WorkloadLive {
		t.Fatalf("workload = %q err=%v, want live", workload, err)
	}
	if _, ok := p["deadline_ms"]; ok {
		t.Fatal("live must not stamp deadline_ms")
	}
	if _, ok := p["min_speed"]; ok {
		t.Fatal("live must not stamp min_speed")
	}
}

func TestSetLivepeerWorkloadNoLivepeerIsNoop(t *testing.T) {
	input := `[{"process":"AV","codec":"AAC"}]`
	out := SetLivepeerWorkload(input, WorkloadVOD, LivepeerVODSegmentDeadlineMs, LivepeerVODMinSpeed)
	if out != input {
		t.Fatalf("expected passthrough, got %s", out)
	}
}

func TestSetLivepeerBroadcastersEmptyFallsBackToLocalAV(t *testing.T) {
	input := `[{"process":"Livepeer","source_track":"maxbps","target_profiles":[{"name":"360p","bitrate":900000,"height":360,"profile":"H264High"}]}]`

	out := SetLivepeerBroadcasters(input, nil)

	if HasLivepeerProcesses(out) {
		t.Fatalf("expected Livepeer to be replaced with local AV, got %q", out)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0]["process"] != "AV" {
		t.Fatalf("expected single local AV process, got %#v", got)
	}
}

func TestRequestedRenditionHeights_FailClosedVsLegitimatelyEmpty(t *testing.T) {
	src := SourceMediaInfo{Width: 1280, Height: 720, FPS: 30}

	// Valid: two renditions, raw heights returned verbatim, no error.
	heights, err := RequestedRenditionHeights(`[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360},{"name":"720p","height":720}]}]`, src)
	if err != nil || len(heights) != 2 || heights[0] != 360 || heights[1] != 720 {
		t.Fatalf("valid config: got %v, err %v; want [360 720], nil", heights, err)
	}

	// Raw height is the contract even when source aspect math would round the
	// concrete width: a 360 request off a 2720x1750 source stays 360.
	odd := SourceMediaInfo{Width: 2720, Height: 1750, FPS: 24}
	if heights, err := RequestedRenditionHeights(`[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360}]}]`, odd); err != nil || len(heights) != 1 || heights[0] != 360 {
		t.Fatalf("raw-height intent: got %v, err %v; want [360], nil", heights, err)
	}

	// Width-only profile derives its height from the source aspect (Mist owns width;
	// validation still needs a height target). 640 wide off 1280x720 => 360 high.
	if heights, err := RequestedRenditionHeights(`[{"process":"Livepeer","target_profiles":[{"name":"w640","width":640}]}]`, src); err != nil || len(heights) != 1 || heights[0] != 360 {
		t.Fatalf("width-only derive: got %v, err %v; want [360], nil", heights, err)
	}

	// No Livepeer process: empty + nil (legitimately nothing to prove).
	if heights, err := RequestedRenditionHeights(`[{"process":"AV","codec":"AAC"}]`, src); err != nil || len(heights) != 0 {
		t.Fatalf("no-Livepeer: got %v, err %v; want [], nil", heights, err)
	}

	// Livepeer present but every profile inhibited by a small source: empty + nil.
	small := SourceMediaInfo{Width: 320, Height: 240}
	if heights, err := RequestedRenditionHeights(`[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360,"track_inhibit":"video=<640x360"}]}]`, small); err != nil || len(heights) != 0 {
		t.Fatalf("all-inhibited: got %v, err %v; want [], nil", heights, err)
	}
	if heights, err := RequestedRenditionHeights(`[{"process":"Livepeer","track_inhibit":"video=<640x360","target_profiles":[{"name":"360p","height":360}]}]`, small); err != nil || len(heights) != 0 {
		t.Fatalf("process-inhibited: got %v, err %v; want [], nil", heights, err)
	}

	// Fail-closed cases: a Livepeer process we cannot turn into a known rendition
	// set must error, not look like "nothing to prove".
	for name, cfg := range map[string]string{
		"missing target_profiles":   `[{"process":"Livepeer","source_track":"maxbps"}]`,
		"empty target_profiles":     `[{"process":"Livepeer","target_profiles":[]}]`,
		"malformed target_profiles": `[{"process":"Livepeer","target_profiles":"notarray"}]`,
		"malformed config json":     `{not json`,
	} {
		if _, err := RequestedRenditionHeights(cfg, src); err == nil {
			t.Fatalf("%s: expected error (fail closed), got nil", name)
		}
	}
}

func TestNormalizeLivepeerProfilesPreservesRequestedHeight(t *testing.T) {
	got := NormalizeLivepeerProfiles([]LivepeerJSONProfile{
		{"name": "360p", "height": 360, "fps": 0, "profile": "H264ConstrainedHigh"},
	}, SourceMediaInfo{Width: 2720, Height: 1750, FPS: 24})
	if len(got) != 1 {
		t.Fatalf("expected one profile, got %#v", got)
	}
	if got[0]["height"] != 360 {
		t.Fatalf("height = %#v, want 360", got[0]["height"])
	}
	if got[0]["width"] != 560 {
		t.Fatalf("width = %#v, want 560", got[0]["width"])
	}
}

func TestReplaceLivepeerWithLocalUsesMistProcAVOptions(t *testing.T) {
	input := `[
		{"process":"AV","codec":"AAC","track_select":"audio=all&video=none&subtitle=none"},
		{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[
			{"name":"360p","bitrate":900000,"fps":30,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"},
			{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264Main","track_inhibit":"video=<850x480"}
		]},
		{"process":"Thumbs","track_select":"video=lowres","exit_unmask":true}
	]`

	var got []map[string]any
	if err := json.Unmarshal([]byte(ReplaceLivepeerWithLocal(input)), &got); err != nil {
		t.Fatalf("unmarshal local processes: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 processes, got %d: %#v", len(got), got)
	}

	firstLocal := got[1]
	if firstLocal["process"] != "AV" {
		t.Fatalf("first generated process = %v, want AV", firstLocal["process"])
	}
	if firstLocal["codec"] != "H264" {
		t.Errorf("codec = %v, want H264", firstLocal["codec"])
	}
	if firstLocal["profile"] != "high" {
		t.Errorf("profile = %v, want high", firstLocal["profile"])
	}
	if firstLocal["resolution"] != "x360" {
		t.Errorf("resolution = %v, want x360", firstLocal["resolution"])
	}
	if firstLocal["framerate"] != float64(30) {
		t.Errorf("framerate = %v, want 30", firstLocal["framerate"])
	}
	if firstLocal["track_select"] != "video=maxbps&audio=none&subtitle=none" {
		t.Errorf("track_select = %v", firstLocal["track_select"])
	}
	if _, ok := firstLocal["height"]; ok {
		t.Error("generated MistProcAV config must not contain ignored height field")
	}
	if _, ok := firstLocal["fps"]; ok {
		t.Error("generated MistProcAV config must not contain ignored fps field")
	}

	secondLocal := got[2]
	if secondLocal["profile"] != "main" {
		t.Errorf("second profile = %v, want main", secondLocal["profile"])
	}
	if secondLocal["resolution"] != "x480" {
		t.Errorf("second resolution = %v, want x480", secondLocal["resolution"])
	}
	if _, ok := secondLocal["framerate"]; ok {
		t.Error("fps=0 should omit framerate")
	}
}

func TestReplaceLivepeerWithLocalInheritsProcessSourceMask(t *testing.T) {
	input := `[{"process":"Livepeer","source_mask":4,"target_mask":2,"track_select":"video=maxbps","track_inhibit":"video=<640x360","target_profiles":[{"name":"360p","profile":"H264Main","height":360,"bitrate":900000}]}]`

	var got []map[string]any
	if err := json.Unmarshal([]byte(ReplaceLivepeerWithLocal(input)), &got); err != nil {
		t.Fatalf("unmarshal local processes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 process, got %d", len(got))
	}
	proc := got[0]
	checks := map[string]any{
		"process":      "AV",
		"codec":        "H264",
		"resolution":   "x360",
		"source_mask":  float64(4),
		"target_mask":  float64(2),
		"track_select": "video=maxbps&audio=none&subtitle=none",
	}
	for key, want := range checks {
		if got := proc[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	// Profile/parent track_inhibit must not survive the split into per-profile
	// AV processes: one rendition would inhibit the next rung's process.
	if inhibit, ok := proc["track_inhibit"]; ok {
		t.Errorf("track_inhibit = %#v, want absent", inhibit)
	}
}

func TestReplaceLivepeerWithLocalPreservesExplicitMistProcOptions(t *testing.T) {
	input := `[{"process":"Livepeer","target_profiles":[{"name":"source","profile":"H264Baseline","bitrate":500000,"width":1024,"height":576,"resolution":"960x540","track_select":"video=0","inconsequential":true,"exit_unmask":true,"source_mask":"v","target_mask":"t","source_track":"1","gopsize":60}]}]`

	var got []map[string]any
	if err := json.Unmarshal([]byte(ReplaceLivepeerWithLocal(input)), &got); err != nil {
		t.Fatalf("unmarshal local processes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 process, got %d", len(got))
	}
	proc := got[0]
	checks := map[string]any{
		"profile":         "baseline",
		"resolution":      "960x540",
		"track_select":    "video=0",
		"inconsequential": true,
		"exit_unmask":     true,
		"source_mask":     "v",
		"target_mask":     "t",
		"source_track":    "1",
		"gopsize":         float64(60),
	}
	for key, want := range checks {
		if got := proc[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestMaskLivepeerSourceForVOD(t *testing.T) {
	input := `[{"process":"AV","codec":"H264"},{"process":"Livepeer","target_profiles":[{"height":360}]}]`
	out := MaskLivepeerSourceForVOD(input)

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal masked config: %v", err)
	}
	if _, ok := got[0]["source_mask"]; ok {
		t.Fatal("AV-only entry should not be masked by the Livepeer VOD helper")
	}
	if got[1]["source_mask"] != float64(4) {
		t.Fatalf("Livepeer source_mask = %#v, want 4", got[1]["source_mask"])
	}
}

func TestLivepeerProfileResolutionPreservesSingleDimensionIntent(t *testing.T) {
	cases := []struct {
		name string
		prof map[string]interface{}
		want string
	}{
		{"height only", map[string]interface{}{"height": float64(360)}, "x360"},
		{"width only", map[string]interface{}{"width": float64(640)}, "640x"},
		{"both", map[string]interface{}{"width": float64(640), "height": float64(360)}, "640x360"},
		{"explicit", map[string]interface{}{"resolution": "960x540", "height": float64(360)}, "960x540"},
	}
	for _, tc := range cases {
		if got := livepeerProfileResolution(tc.prof); got != tc.want {
			t.Fatalf("%s: resolution = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeProcessConfigSelectorsMakesSchedulerInputsExplicit(t *testing.T) {
	input := `[
		{"process":"AV","codec":"AAC"},
		{"process":"Thumbs","exit_unmask":true},
		{"process":"Livepeer","target_profiles":[{"name":"360p","bitrate":900000}]}
	]`

	var got []map[string]any
	if err := json.Unmarshal([]byte(NormalizeProcessConfigSelectors(input)), &got); err != nil {
		t.Fatalf("unmarshal normalized processes: %v", err)
	}
	if got[0]["track_select"] != "audio=all&video=none&subtitle=none" {
		t.Fatalf("AV track_select = %v", got[0]["track_select"])
	}
	if got[1]["track_select"] != "video=lowres" {
		t.Fatalf("Thumbs track_select = %v", got[1]["track_select"])
	}
	if got[2]["source_track"] != "maxbps" {
		t.Fatalf("Livepeer source_track = %v", got[2]["source_track"])
	}
	if got[2]["track_select"] != "video=maxbps" {
		t.Fatalf("Livepeer track_select = %v", got[2]["track_select"])
	}
}

func TestNormalizeProcessConfigSelectorsPreservesExplicitSelectors(t *testing.T) {
	input := `[{"process":"Thumbs","track_select":"video=maxbps"},{"process":"Livepeer","source_track":"1","track_select":"video=1"}]`
	if got := NormalizeProcessConfigSelectors(input); got != input {
		t.Fatalf("explicit selectors changed: %s", got)
	}
}

func TestValidateProcessConfigShapeRejectsLivepeerFieldsOnExplicitAV(t *testing.T) {
	badConfig := `[{"process":"AV","codec":"H264","height":360,"fps":30,"profile":"H264ConstrainedHigh"}]`
	if err := ValidateProcessConfigShape(badConfig); err == nil {
		t.Fatal("expected explicit AV config with Livepeer-style fields to fail")
	}

	livepeerConfig := `[{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[{"name":"360p","bitrate":900000,"height":360,"fps":30,"profile":"H264ConstrainedHigh"}]}]`
	if err := ValidateProcessConfigShape(livepeerConfig); err != nil {
		t.Fatalf("Livepeer target profile should remain valid: %v", err)
	}

	localAVConfig := `[{"process":"AV","codec":"H264","bitrate":900000,"resolution":"640x360","framerate":30,"profile":"high","track_select":"video=maxbps"}]`
	if err := ValidateProcessConfigShape(localAVConfig); err != nil {
		t.Fatalf("local AV config should be valid: %v", err)
	}

	// A Livepeer process must request at least one rendition (the invariant
	// enforced for both the billing catalog and Commodore override policies).
	for _, bad := range []string{
		`[{"process":"Livepeer","source_track":"maxbps"}]`,      // missing
		`[{"process":"Livepeer","target_profiles":[]}]`,         // empty
		`[{"process":"Livepeer","target_profiles":"notarray"}]`, // wrong type
	} {
		if err := ValidateProcessConfigShape(bad); err == nil {
			t.Fatalf("expected ValidateProcessConfigShape to reject no-rendition Livepeer config: %s", bad)
		}
	}
}

func TestInfrastructureMistServerConfProcessShapes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "infrastructure", "mistserver.conf"))
	if err != nil {
		t.Fatalf("read mistserver.conf: %v", err)
	}
	var cfg struct {
		Streams map[string]struct {
			Processes []map[string]any `json:"processes"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse mistserver.conf: %v", err)
	}
	for name, stream := range cfg.Streams {
		if len(stream.Processes) == 0 {
			continue
		}
		processes, err := json.Marshal(stream.Processes)
		if err != nil {
			t.Fatalf("%s: marshal processes: %v", name, err)
		}
		if err := ValidateProcessConfigShape(string(processes)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestInfrastructureMistServerConfProtocolOptions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "infrastructure", "mistserver.conf"))
	if err != nil {
		t.Fatalf("read mistserver.conf: %v", err)
	}
	var cfg struct {
		Config struct {
			Protocols []map[string]any `json:"protocols"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse mistserver.conf: %v", err)
	}

	findProtocol := func(connector string) map[string]any {
		t.Helper()
		for _, protocol := range cfg.Config.Protocols {
			if protocol["connector"] == connector {
				return protocol
			}
		}
		t.Fatalf("missing protocol %q", connector)
		return nil
	}
	requireBool := func(protocol map[string]any, key string) {
		t.Helper()
		if got := protocol[key]; got != true {
			t.Fatalf("%s %s = %v, want true", protocol["connector"], key, got)
		}
	}
	requireAbsent := func(protocol map[string]any, key string) {
		t.Helper()
		if got, ok := protocol[key]; ok {
			t.Fatalf("%s %s = %v, want unset", protocol["connector"], key, got)
		}
	}

	cmaf := findProtocol("CMAF")
	requireBool(cmaf, "dashlowlatency")
	requireBool(cmaf, "mergesessions")
	requireAbsent(cmaf, "dashllchunked")
	requireAbsent(cmaf, "nonchunked")
	requireAbsent(cmaf, "chunkedsegments")

	hls := findProtocol("HLS")
	requireAbsent(hls, "nonchunked")
	requireAbsent(hls, "chunkedsegments")
}

// TestLivepeerProfileFloatCoercesEveryJSONNumericEncoding pins the numeric-read
// contract every profile dimension/fps lookup is built on: heterogeneous JSON
// encodings (native float, Go ints, json.Number) all decode to float64, and any
// non-numeric or unparseable value reports absence via ok=false rather than a
// silent zero. The ok flag is load-bearing — callers distinguish "field is 0"
// from "field is missing/garbage" to decide whether to apply a default.
func TestLivepeerProfileFloatCoercesEveryJSONNumericEncoding(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		want   float64
		wantOK bool
	}{
		{"float64", float64(360), 360, true},
		{"int", int(360), 360, true},
		{"int64", int64(360), 360, true},
		{"json.Number integer", json.Number("720"), 720, true},
		{"json.Number float", json.Number("29.97"), 29.97, true},
		{"json.Number malformed", json.Number("not-a-number"), 0, false},
		{"explicit zero stays present", float64(0), 0, true},
		{"string is not numeric", "360", 0, false},
		{"bool is not numeric", true, 0, false},
		{"nil/absent", nil, 0, false},
		{"nested map is not numeric", map[string]any{}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := livepeerProfileFloat(LivepeerJSONProfile{"height": tc.value}, "height")
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("livepeerProfileFloat(%v) = (%v, %v), want (%v, %v)", tc.value, got, ok, tc.want, tc.wantOK)
			}
		})
	}
	// A key that is entirely absent must also report not-present (default path).
	if got, ok := livepeerProfileFloat(LivepeerJSONProfile{}, "height"); ok || got != 0 {
		t.Fatalf("absent key = (%v, %v), want (0, false)", got, ok)
	}
}

// TestRequestedRenditionHeightFallbackChain covers the two ladder-intent
// fallback branches not already exercised through RequestedRenditionHeights:
// a profile with no dimensions passes the source height through, while a
// width-only request against an unknown source fails closed at 0 so the
// validator never treats an underivable rendition as satisfied.
func TestRequestedRenditionHeightFallbackChain(t *testing.T) {
	src := SourceMediaInfo{Width: 1280, Height: 720}

	if got := requestedRenditionHeight(LivepeerJSONProfile{"name": "src", "bitrate": 900000}, src); got != 720 {
		t.Fatalf("no-dimensions profile = %d, want source passthrough 720", got)
	}
	// height:0 is "absent", not a real request — must fall through to source.
	if got := requestedRenditionHeight(LivepeerJSONProfile{"height": 0}, src); got != 720 {
		t.Fatalf("height:0 = %d, want source passthrough 720", got)
	}
	// Width-only with unknown source aspect cannot be resolved -> 0 (fail closed).
	if got := requestedRenditionHeight(LivepeerJSONProfile{"width": 640}, SourceMediaInfo{}); got != 0 {
		t.Fatalf("width-only unknown source = %d, want 0 (fail closed)", got)
	}
}

// TestNormalizeLivepeerProfilesDefaultsAndCompletion pins the remaining
// MistProcLivepeer-mirroring branches: missing fps/gop/profile are filled,
// a bare profile inherits both source dimensions, a width-only profile derives
// its height from the source aspect, and an empty input is nil (not []).
func TestNormalizeLivepeerProfilesDefaultsAndCompletion(t *testing.T) {
	if got := NormalizeLivepeerProfiles(nil, SourceMediaInfo{Width: 1280, Height: 720}); got != nil {
		t.Fatalf("empty input = %#v, want nil", got)
	}

	t.Run("bare profile gets defaults and both source dims", func(t *testing.T) {
		got := NormalizeLivepeerProfiles([]LivepeerJSONProfile{{"name": "src", "bitrate": 900000}}, SourceMediaInfo{Width: 1280, Height: 720, FPS: 30})
		if len(got) != 1 {
			t.Fatalf("expected one profile, got %#v", got)
		}
		p := got[0]
		if p["gop"] != "0.0" {
			t.Errorf("gop default = %#v, want \"0.0\"", p["gop"])
		}
		if p["profile"] != "H264High" {
			t.Errorf("profile default = %#v, want H264High", p["profile"])
		}
		if p["fps"] != 30000 || p["fpsDen"] != 1000 {
			t.Errorf("fps default = %#v/%#v, want 30000/1000 (source fps in millihertz)", p["fps"], p["fpsDen"])
		}
		if p["width"] != 1280 || p["height"] != 720 {
			t.Errorf("dims = %#v x %#v, want full source 1280x720", p["width"], p["height"])
		}
	})

	t.Run("zero source fps falls back to 25fps", func(t *testing.T) {
		got := NormalizeLivepeerProfiles([]LivepeerJSONProfile{{"height": 360}}, SourceMediaInfo{Width: 1280, Height: 720})
		if got[0]["fps"] != 25000 {
			t.Fatalf("fps with zero source = %#v, want 25000 default", got[0]["fps"])
		}
	})

	t.Run("width-only derives height from landscape aspect", func(t *testing.T) {
		// 640 wide off a 1280x720 source -> 360 high (even).
		got := NormalizeLivepeerProfiles([]LivepeerJSONProfile{{"width": 640}}, SourceMediaInfo{Width: 1280, Height: 720, FPS: 30})
		if got[0]["width"] != 640 || got[0]["height"] != 360 {
			t.Fatalf("width-only completion = %#v x %#v, want 640x360", got[0]["width"], got[0]["height"])
		}
	})

	t.Run("all profiles inhibited collapses to nil", func(t *testing.T) {
		// Source smaller than the inhibit threshold -> profile dropped -> nil,
		// the signal callers read as "no Livepeer renditions for this source".
		got := NormalizeLivepeerProfiles([]LivepeerJSONProfile{{"height": 360, "track_inhibit": "video=<640x360"}}, SourceMediaInfo{Width: 320, Height: 240})
		if got != nil {
			t.Fatalf("all-inhibited = %#v, want nil", got)
		}
	})

	t.Run("surviving profile has track_inhibit stripped", func(t *testing.T) {
		got := NormalizeLivepeerProfiles([]LivepeerJSONProfile{{"height": 360, "track_inhibit": "video=<640x360"}}, SourceMediaInfo{Width: 1280, Height: 720, FPS: 30})
		if len(got) != 1 {
			t.Fatalf("expected surviving profile, got %#v", got)
		}
		if _, ok := got[0]["track_inhibit"]; ok {
			t.Fatal("track_inhibit must be stripped from the normalized output")
		}
	})

	// Characterization of the portrait-source branch (source.Width < source.Height):
	// a height-only request is reinterpreted as the target width, and the height is
	// recomputed to preserve the source aspect. Pins current behavior; flagged for
	// review if the width/height swap is ever questioned.
	t.Run("portrait source height-only swap preserves aspect", func(t *testing.T) {
		got := NormalizeLivepeerProfiles([]LivepeerJSONProfile{{"height": 480}}, SourceMediaInfo{Width: 720, Height: 1280, FPS: 30})
		if got[0]["width"] != 480 || got[0]["height"] != 854 {
			t.Fatalf("portrait height-only = %#v x %#v, want 480x854", got[0]["width"], got[0]["height"])
		}
	})
}

// TestLivepeerProfileResolutionNeitherDimensionIsEmpty closes the one remaining
// branch: with no usable width or height the resolution string is empty so the
// generated MistProcAV config omits the option entirely (passthrough) rather
// than emitting a malformed "x".
func TestLivepeerProfileResolutionNeitherDimensionIsEmpty(t *testing.T) {
	for _, prof := range []map[string]any{
		{},
		{"width": float64(0), "height": float64(0)},
		{"width": "garbage"},
	} {
		if got := livepeerProfileResolution(prof); got != "" {
			t.Fatalf("resolution(%#v) = %q, want empty", prof, got)
		}
	}
}

// TestLivepeerProfileToAVProfileMapping pins the Livepeer->MistProcAV profile-name
// map, including the default: an unrecognized name maps to "" (omitted) so a
// local AV process never inherits a profile string MistProcAV cannot parse. The
// match is substring-based and case-sensitive.
func TestLivepeerProfileToAVProfileMapping(t *testing.T) {
	cases := map[string]string{
		"H264ConstrainedHigh":     "high",
		"H264High":                "high",
		"H264Main":                "main",
		"H264ConstrainedBaseline": "baseline",
		"H264Baseline":            "baseline",
		"H264Extended":            "", // unmapped -> omitted
		"":                        "",
		"h264high":                "", // case-sensitive: lowercase does not match
	}
	for in, want := range cases {
		if got := livepeerProfileToAVProfile(in); got != want {
			t.Fatalf("livepeerProfileToAVProfile(%q) = %q, want %q", in, got, want)
		}
	}
}

// processNames extracts the "process" field from a process-config JSON array so
// tests can assert which entries survived a transform without depending on
// re-marshaled key ordering or whitespace.
func processNames(t *testing.T, processesJSON string) []string {
	t.Helper()
	var procs []map[string]any
	if err := json.Unmarshal([]byte(processesJSON), &procs); err != nil {
		t.Fatalf("unmarshal %q: %v", processesJSON, err)
	}
	names := make([]string, 0, len(procs))
	for _, p := range procs {
		name, _ := p["process"].(string)
		names = append(names, name)
	}
	return names
}

// TestStripLivepeerProcesses pins the self-hosted-without-transcoding path:
// Livepeer entries are dropped while audio/thumbnail processes are kept, and
// the function fails open (returns the input unchanged) on malformed JSON
// rather than discarding a config it could not parse.
func TestStripLivepeerProcesses(t *testing.T) {
	t.Run("drops only livepeer entries", func(t *testing.T) {
		input := `[
			{"process":"AV","codec":"AAC"},
			{"process":"Livepeer","target_profiles":[{"name":"360p","height":360}]},
			{"process":"FFMPEG","track_select":"video=all"}
		]`
		got := processNames(t, StripLivepeerProcesses(input))
		want := []string{"AV", "FFMPEG"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("surviving processes = %v, want %v", got, want)
		}
	})

	t.Run("all livepeer marshals to null", func(t *testing.T) {
		// filtered stays a nil slice, which json.Marshal renders as "null".
		// Callers treat that as "no processes" — pin it so a change to "[]"
		// (which some consumers parse differently) is a deliberate decision.
		input := `[{"process":"Livepeer","target_profiles":[{"height":720}]}]`
		if got := StripLivepeerProcesses(input); got != "null" {
			t.Fatalf("all-Livepeer strip = %q, want \"null\"", got)
		}
	})

	t.Run("no livepeer keeps every entry", func(t *testing.T) {
		input := `[{"process":"AV","codec":"AAC"},{"process":"FFMPEG"}]`
		got := processNames(t, StripLivepeerProcesses(input))
		if len(got) != 2 || got[0] != "AV" || got[1] != "FFMPEG" {
			t.Fatalf("surviving processes = %v, want [AV FFMPEG]", got)
		}
	})

	t.Run("malformed json fails open", func(t *testing.T) {
		input := `{not valid json`
		if got := StripLivepeerProcesses(input); got != input {
			t.Fatalf("malformed input should be returned verbatim, got %q", got)
		}
	})
}
