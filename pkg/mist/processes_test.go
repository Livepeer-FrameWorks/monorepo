package mist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceLivepeerWithLocalUsesMistProcAVOptions(t *testing.T) {
	input := `[
		{"process":"AV","codec":"AAC","track_select":"video=none"},
		{"process":"Livepeer","target_profiles":[
			{"name":"360p","bitrate":900000,"fps":30,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"},
			{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264Main","track_inhibit":"video=<850x480"}
		]},
		{"process":"Thumbs","exit_unmask":true}
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
	if firstLocal["resolution"] != "640x360" {
		t.Errorf("resolution = %v, want 640x360", firstLocal["resolution"])
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
	if secondLocal["resolution"] != "854x480" {
		t.Errorf("second resolution = %v, want 854x480", secondLocal["resolution"])
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

func TestValidateProcessConfigShapeRejectsLivepeerFieldsOnExplicitAV(t *testing.T) {
	badConfig := `[{"process":"AV","codec":"H264","height":360,"fps":30,"profile":"H264ConstrainedHigh"}]`
	if err := ValidateProcessConfigShape(badConfig); err == nil {
		t.Fatal("expected explicit AV config with Livepeer-style fields to fail")
	}

	livepeerConfig := `[{"process":"Livepeer","target_profiles":[{"name":"360p","bitrate":900000,"height":360,"fps":30,"profile":"H264ConstrainedHigh"}]}]`
	if err := ValidateProcessConfigShape(livepeerConfig); err != nil {
		t.Fatalf("Livepeer target profile should remain valid: %v", err)
	}

	localAVConfig := `[{"process":"AV","codec":"H264","bitrate":900000,"resolution":"640x360","framerate":30,"profile":"high"}]`
	if err := ValidateProcessConfigShape(localAVConfig); err != nil {
		t.Fatalf("local AV config should be valid: %v", err)
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
