package mist

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCatalogProcessingMemoryPlans(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "api_billing", "internal", "bootstrap", "catalog", "billing_tiers.yaml")
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Ladders []struct {
			Name       string           `yaml:"name"`
			Renditions []map[string]any `yaml:"renditions"`
		} `yaml:"transcode_ladders"`
	}
	if err = yaml.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Ladders) == 0 {
		t.Fatal("no catalog ladders")
	}
	configs := make(map[string]any)
	for _, ladder := range catalog.Ladders {
		for _, height := range []int{1080, 2160} {
			for _, cpus := range []int{4, 16} {
				key := fmt.Sprintf("%s_%dp_cpus%d", ladder.Name, height, cpus)
				t.Run(key, func(t *testing.T) {
					var processes []map[string]any
					for _, rendition := range ladder.Renditions {
						process := make(map[string]any)
						for name, value := range rendition {
							process[name] = value
						}
						process["process"] = "AV"
						process["track_select"] = "video=maxbps&audio=none&subtitle=none&meta=none"
						process["x-LSP-name"] = process["name"]
						delete(process, "name")
						processes = append(processes, process)
					}
					processes = append(processes, map[string]any{"process": "AV", "codec": "AAC", "track_select": "video=none&audio=all&subtitle=none&meta=none", "track_inhibit": "audio=aac"}, map[string]any{"process": "AV", "codec": "opus", "track_select": "video=none&audio=all&subtitle=none&meta=none", "track_inhibit": "audio=opus"}, map[string]any{"process": "Thumbs", "track_select": "video=maxbps", "track_inhibit": "meta=thumbvtt", "inconsequential": true})
					encoded, err := json.Marshal(processes)
					if err != nil {
						t.Fatal(err)
					}
					source := SourceMediaInfo{Width: height * 16 / 9, Height: height, VideoCodec: "H264"}
					plan, err := PlanProcessingMemory(string(encoded), source, 24*1024*processingMiB, cpus)
					if err != nil || !plan.Fits {
						t.Fatalf("ample-node plan: %+v %v", plan, err)
					}
					processes = nil
					if err = json.Unmarshal([]byte(plan.ProcessesJSON), &processes); err != nil {
						t.Fatal(err)
					}
					measuredPolicy, marshalErr := json.Marshal(processes)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if string(measuredPolicy) != plan.ProcessesJSON {
						t.Errorf("measurement export changed admitted policy: got %s want %s", measuredPolicy, plan.ProcessesJSON)
					}
					configs[key] = map[string]any{"estimate_bytes": plan.Bytes, "processes": processes, "source": source, "cpus": cpus}
					for _, process := range processes {
						if process["process"] != "AV" {
							continue
						}
						codec, _ := process["codec"].(string)
						if !isAVVideoCodec(codec) {
							continue
						}
						raw, _ := json.Marshal(process)
						var spec map[string]json.RawMessage
						if err = json.Unmarshal(raw, &spec); err != nil {
							t.Fatal(err)
						}
						if processTrackInhibited(spec, source) {
							t.Errorf("uncounted inhibited rendition can transiently start: %s", raw)
						}
					}
				})
			}
		}
	}
	if destination := os.Getenv("FRAMEWORKS_PROCESSING_MEASURE_CONFIG"); destination != "" {
		encoded, err := json.MarshalIndent(configs, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(destination, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("exported %d actual catalog plans to %s", len(configs), destination)
	}
}

func TestProcessingMemoryEstimateCoversMeasuredCatalogWithoutWaste(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join("testdata", "processing_memory_20261120.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		AllRecordingsValidated bool   `json:"all_recordings_validated"`
		BinaryDistSHA256       string `json:"binary_dist_sha256"`
		SourceCommit           string `json:"source_commit"`
		Cases                  []struct {
			Name               string          `json:"case"`
			Width              int             `json:"source_width"`
			Height             int             `json:"source_height"`
			Processes          json.RawMessage `json:"processes"`
			Observed           uint64          `json:"observed_process_bytes"`
			RecordingValidated bool            `json:"recording_validated"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(encoded, &corpus); err != nil {
		t.Fatal(err)
	}
	if !corpus.AllRecordingsValidated {
		t.Fatal("calibration requires twelve completed recordings, not partial output")
	}
	for name, value := range map[string]string{"binary artifact": corpus.BinaryDistSHA256, "Mist source": corpus.SourceCommit} {
		want := 64
		if name == "Mist source" {
			want = 40
		}
		if _, err := hex.DecodeString(value); err != nil || len(value) != want {
			t.Fatalf("calibration must identify its exact %s: %q", name, value)
		}
	}
	if len(corpus.Cases) != 12 {
		t.Fatalf("incomplete catalog/source/CPU matrix: %d cases", len(corpus.Cases))
	}
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			if !sample.RecordingValidated || sample.Observed == 0 {
				t.Fatal("calibration case has no validated complete recording or memory measurement")
			}
			estimate, err := EstimateProcessingMemory(string(sample.Processes), SourceMediaInfo{VideoCodec: "H264", Width: sample.Width, Height: sample.Height})
			if err != nil {
				t.Fatal(err)
			}
			if estimate < sample.Observed*110/100 {
				t.Errorf("estimate %d lacks 10%% margin over observed private/shared peak %d", estimate, sample.Observed)
			}
			if estimate > sample.Observed*150/100 {
				t.Errorf("estimate %d wastes node capacity against observed private/shared peak %d (over 50%%)", estimate, sample.Observed)
			}
			t.Logf("estimate=%.1fMiB observed=%.1fMiB", float64(estimate)/1048576, float64(sample.Observed)/1048576)
		})
	}
}
