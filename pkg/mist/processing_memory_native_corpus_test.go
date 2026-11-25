package mist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProcessingMemoryPlansMatchNativeCorpus(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join("testdata", "processing_memory_20261125.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		SourceCommit           string `json:"source_commit"`
		BinaryDistSHA256       string `json:"binary_dist_sha256"`
		AllRecordingsValidated bool   `json:"all_recordings_validated"`
		Cases                  []struct {
			Name                   string              `json:"case"`
			Source                 SourceMediaInfo     `json:"source"`
			Tracks                 []TrackInfo         `json:"tracks"`
			InputPolicy            json.RawMessage     `json:"input_policy"`
			Processes              json.RawMessage     `json:"processes"`
			ExpectedRenditions     []ExpectedRendition `json:"expected_renditions"`
			PlannerMode            string              `json:"planner_mode"`
			CPUQuota               int                 `json:"cpu_quota"`
			BudgetBytes            uint64              `json:"budget_bytes"`
			ObservedBytes          uint64              `json:"observed_process_bytes"`
			CatalogSHA256          string              `json:"catalog_sha256"`
			RecordingValidated     bool                `json:"recording_validated"`
			RuntimeSourceValidated bool                `json:"runtime_source_validated"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(encoded, &corpus); err != nil {
		t.Fatal(err)
	}
	if !corpus.AllRecordingsValidated || len(corpus.Cases) != 36 {
		t.Fatal("native calibration needs all 36 completed recordings")
	}
	for value, length := range map[string]int{corpus.SourceCommit: 40, corpus.BinaryDistSHA256: 64} {
		if _, err = hex.DecodeString(value); err != nil || len(value) != length {
			t.Fatal("native calibration lacks its exact source and package identity")
		}
	}
	catalog, err := os.ReadFile(filepath.Join("..", "..", "api_billing", "internal", "bootstrap", "catalog", "billing_tiers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	catalogHash := sha256.Sum256(catalog)
	seen := make(map[string]bool)
	for _, sample := range corpus.Cases {
		if seen[sample.Name] {
			t.Fatalf("duplicate measured case %s", sample.Name)
		}
		seen[sample.Name] = true
		t.Run(sample.Name, func(t *testing.T) {
			if !sample.RecordingValidated || !sample.RuntimeSourceValidated || sample.ObservedBytes == 0 {
				t.Fatal("measurement lacks complete output and native source validation")
			}
			if sample.CatalogSHA256 != hex.EncodeToString(catalogHash[:]) {
				t.Fatal("catalog changed since native policy measurement")
			}
			var plan ProcessingMemoryPlan
			var planErr error
			switch sample.PlannerMode {
			case "node":
				plan, planErr = PlanProcessingMemory(string(sample.InputPolicy), sample.Source, sample.BudgetBytes, sample.CPUQuota, sample.Tracks...)
			case "quality_preserving":
				plan, planErr = PlanProcessingMemoryThreads(string(sample.InputPolicy), sample.Source, sample.BudgetBytes, sample.CPUQuota, sample.Tracks...)
			default:
				t.Fatalf("unknown measured planner mode %q", sample.PlannerMode)
			}
			if planErr != nil || !plan.Fits {
				t.Fatalf("measured policy no longer fits: %+v, %v", plan, planErr)
			}
			var measured, admitted any
			if err := json.Unmarshal(sample.Processes, &measured); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(plan.ProcessesJSON), &admitted); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(admitted, measured) {
				t.Fatalf("production planner changed the measured process policy: got %s; measured %s", plan.ProcessesJSON, sample.Processes)
			}
			renditions, err := ExpectedVideoRenditions(plan.ProcessesJSON, sample.Source, sample.Tracks...)
			if err != nil || !reflect.DeepEqual(renditions, sample.ExpectedRenditions) {
				t.Fatalf("production renditions differ from complete measured output: got %+v, want %+v, error %v", renditions, sample.ExpectedRenditions, err)
			}
			t.Logf("admission estimate / native measured peak = %.3f", float64(plan.Bytes)/float64(sample.ObservedBytes))
		})
	}
}
