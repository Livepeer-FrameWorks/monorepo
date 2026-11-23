package mist

import (
	"encoding/json"
	"testing"
)

// retainLadder is a local AV ladder whose rungs carry the track_retain
// selector the catalog composes for them.
func retainLadder(t *testing.T) string {
	t.Helper()
	var processes []map[string]any
	for _, rung := range []RetainRung{{1080, 7800000}, {720, 4500000}, {480, 1600000}, {360, 900000}} {
		processes = append(processes, map[string]any{
			"process": "AV", "codec": "H264", "resolution": "0x" + itoa(rung.ShortSide),
			"bitrate": rung.Bitrate, "track_retain": RetainSelector(rung),
		})
	}
	encoded, err := json.Marshal(processes)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func itoa(v int) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}

// An upload carrying 1080p, 720p and 360p H264 originals at in-band bitrates
// (Bps is bytes per second).
func multiRenditionOriginals() []TrackInfo {
	return []TrackInfo{
		{Index: 0, Codec: "H264", Width: 1920, Height: 1080, Bps: 975000, Original: true},
		{Index: 1, Codec: "H264", Width: 1280, Height: 720, Bps: 562500, Original: true},
		{Index: 2, Codec: "H264", Width: 640, Height: 360, Bps: 112500, Original: true},
	}
}

func TestProcessingMemoryReservesNothingForRetainedRungs(t *testing.T) {
	source := SourceMediaInfo{Width: 1920, Height: 1080}
	tracks := multiRenditionOriginals()
	var processes []map[string]any
	if err := json.Unmarshal([]byte(retainLadder(t)), &processes); err != nil {
		t.Fatal(err)
	}
	only480, err := json.Marshal(processes[2:3])
	if err != nil {
		t.Fatal(err)
	}
	want, err := EstimateProcessingMemory(string(only480), source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := EstimateProcessingMemory(retainLadder(t), source, tracks...)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("retained rungs reserved memory: estimate %d bytes, want %d (only the 480p rung is transcoded)", got, want)
	}

	plan, err := PlanProcessingMemory(retainLadder(t), source, 64*1024*processingMiB, 8, tracks...)
	if err != nil || !plan.Fits {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	var planned []map[string]any
	if err = json.Unmarshal([]byte(plan.ProcessesJSON), &planned); err != nil {
		t.Fatal(err)
	}
	alone, err := json.Marshal(planned[2:3])
	if err != nil {
		t.Fatal(err)
	}
	transcoded, err := EstimateProcessingMemory(string(alone), source)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Bytes != transcoded {
		t.Fatalf("memory plan reserved %d bytes, want %d for the transcoded 480p rung alone", plan.Bytes, transcoded)
	}
	if threads := processOptionInt(planned[2], "encode_threads", 0); threads != 8 {
		t.Fatalf("480p rung got %d encode threads, want all 8: retained rungs must not share the thread budget", threads)
	}

	// Without the track list nothing is retained and every rung is reserved.
	all, err := EstimateProcessingMemory(retainLadder(t), source)
	if err != nil || all <= want {
		t.Fatalf("estimate without tracks = %d (%v), want more than the 480p rung alone (%d)", all, err, want)
	}
}

// A 720p original at 15 Mbps is the transcode source (highest bitrate), so the
// 1080p rung's size inhibitor fires; the in-band 1080p original still retains
// that rung, so the plan keeps it in the published policy and reserves nothing.
func TestProcessingMemoryPlanKeepsRetainedRungTheSourceIsTooSmallFor(t *testing.T) {
	tracks := []TrackInfo{
		{Index: 0, Codec: "H264", Width: 1920, Height: 1080, Bps: 975000, Original: true},
		{Index: 1, Codec: "H264", Width: 1280, Height: 720, Bps: 1875000, Original: true},
	}
	source := SourceMediaInfo{Width: 1280, Height: 720}
	var processes []map[string]any
	if err := json.Unmarshal([]byte(retainLadder(t)), &processes); err != nil {
		t.Fatal(err)
	}
	processes[0]["track_inhibit"] = "video=<0x1080"
	encoded, err := json.Marshal(processes)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanProcessingMemory(string(encoded), source, 64*1024*processingMiB, 8, tracks...)
	if err != nil {
		t.Fatal(err)
	}
	var planned []map[string]any
	if err = json.Unmarshal([]byte(plan.ProcessesJSON), &planned); err != nil {
		t.Fatal(err)
	}
	if len(planned) != 4 || planned[0]["resolution"] != "0x1080" {
		t.Fatalf("retained 1080p rung dropped from the policy: %s", plan.ProcessesJSON)
	}
	expected, err := ExpectedVideoRenditions(plan.ProcessesJSON, source, tracks...)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) == 0 || !expected[0].Retained || expected[0].TrackIndex != 0 {
		t.Fatalf("1080p rung = %+v, want retained by original track 0", expected)
	}
}
