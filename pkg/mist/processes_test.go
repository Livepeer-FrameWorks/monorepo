package mist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
