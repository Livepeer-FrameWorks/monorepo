package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordedIngestCapacityPlansAvoidPerRowAndWholeCensusWork(t *testing.T) {
	reportPath := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if reportPath == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects recorded real-engine plans")
	}
	checks := map[string][]string{
		"ListArtifactsForCatalogProjection": {"CORRELATED_SUBPLAN", "MANY_READ_RPCS"},
		"ListColdStorageUsage":              {"SEQ_SCAN_LARGE", "SORT_LARGE"},
		"ListOpenProjectedIngestSessions":   {"SORT_LARGE"},
		"PurgeEndedPullIngestSessions":      {"MANY_READ_RPCS"},
	}
	for _, layout := range []string{"declared", "distributed"} {
		for name, forbidden := range checks {
			t.Run(layout+"/"+name, func(t *testing.T) {
				path := filepath.Join(filepath.Dir(reportPath), layout, "foghorn", name+".json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var recorded result
				if err := json.Unmarshal(data, &recorded); err != nil {
					t.Fatal(err)
				}
				if recorded.Status != "analyzed" || recorded.Metrics == nil || recorded.Metrics.StorageRowsRead == 0 {
					t.Fatalf("query did not execute against populated live inventory: %+v", recorded)
				}
				for _, flag := range forbidden {
					if hasFlag(recorded, flag) {
						t.Errorf("query retains unbounded work: %s (%+v)", flag, recorded.Metrics)
					}
				}
			})
		}
	}
}

func TestRecordedPlacementMembershipReadsTheNodeSetOnce(t *testing.T) {
	reportPath := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if reportPath == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects recorded real-engine plans")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(reportPath), "distributed", "quartermaster", "GetPlacementMembership.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		result
		Plan []planRoot `json:"plan"`
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Status != "analyzed" || recorded.Metrics == nil {
		t.Fatal("membership did not execute on the real engine")
	}
	seen := false
	var inspect func(planNode)
	inspect = func(node planNode) {
		if node.Relation == "infrastructure_nodes" {
			seen = true
			if node.NodeType == "Seq Scan" && node.ActualLoops > 1 {
				t.Errorf("membership rescans the entire node set %.0f times", node.ActualLoops)
			}
		}
		for _, child := range node.Plans {
			inspect(child)
		}
	}
	for _, root := range recorded.Plan {
		inspect(root.Plan)
	}
	if !seen || hasFlag(recorded.result, "READ_AMPLIFICATION") {
		t.Errorf("membership must read a bounded node set: %+v", recorded.Metrics)
	}
}
