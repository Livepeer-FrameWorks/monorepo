package mist

import (
	"encoding/json"
	"testing"
)

func TestMemoryPlanDoesNotPublishAboveSourceEncoders(t *testing.T) {
	policy := `[{"process":"AV","codec":"H264","resolution":"0x2160","track_inhibit":"video=<0x2160"},{"process":"AV","codec":"H264","resolution":"0x1440","track_inhibit":"video=<0x1440"},{"process":"AV","codec":"H264","resolution":"0x1080","track_inhibit":"video=<0x1080"},{"process":"AV","codec":"AAC"},{"process":"Thumbs"}]`
	for _, source := range []SourceMediaInfo{{Width: 1920, Height: 1080}, {Width: 1080, Height: 1920, Rotation: 90}} {
		plan, err := PlanProcessingMemory(policy, source, 8<<30, 16)
		if err != nil {
			t.Fatal(err)
		}
		var processes []map[string]any
		if err = json.Unmarshal([]byte(plan.ProcessesJSON), &processes); err != nil {
			t.Fatal(err)
		}
		if len(processes) != 3 {
			t.Fatalf("uncounted above-source encoders can start transiently: %s", plan.ProcessesJSON)
		}
		if processes[0]["resolution"] != "0x1080" || processes[1]["codec"] != "AAC" || processes[2]["process"] != "Thumbs" {
			t.Fatalf("source-fit video/audio/thumbnail policy was lost: %s", plan.ProcessesJSON)
		}
		for _, process := range processes {
			if processOptionInt(process, "decode_threads", 0) < 1 || processOptionInt(process, "encode_threads", 0) < 1 {
				t.Fatalf("published process has unbounded concurrency: %s", plan.ProcessesJSON)
			}
		}
	}
}
