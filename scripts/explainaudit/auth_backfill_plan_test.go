package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordedAuthBackfillPlansUsePendingIndexes(t *testing.T) {
	reportPath := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if reportPath == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects existing real-engine auth backfill plans")
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err = json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range results {
		if authBackfillFixtureFor(r.ID) == nil {
			continue
		}
		seen[r.Layout]++
		if r.Status != "analyzed" || r.Metrics == nil || r.Metrics.StorageRowsRead == 0 {
			t.Errorf("%s/%s did not execute against live pending heads: %+v", r.Layout, r.Name, r)
		}
		for _, flag := range []string{"SEQ_SCAN_LARGE", "READ_AMPLIFICATION", "SORT_LARGE", "CORRELATED_SUBPLAN"} {
			if hasFlag(r, flag) {
				t.Errorf("%s/%s has unbounded backfill plan flag %s", r.Layout, r.Name, flag)
			}
		}
		data, err = os.ReadFile(filepath.Join(filepath.Dir(reportPath), r.Layout, r.Database, safeFile(r.Name)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var recorded struct {
			Plan []planRoot `json:"plan"`
		}
		if err = json.Unmarshal(data, &recorded); err != nil {
			t.Fatal(err)
		}
		indexed := false
		var inspect func(planNode)
		inspect = func(node planNode) {
			if strings.HasPrefix(node.Index, "idx_refresh_tokens_pending_session_") {
				indexed = true
			}
			for _, child := range node.Plans {
				inspect(child)
			}
		}
		for _, root := range recorded.Plan {
			inspect(root.Plan)
		}
		if !indexed {
			t.Errorf("%s/%s did not use the pending-head range index", r.Layout, r.Name)
		}
	}
	for _, layout := range []string{"declared", "distributed"} {
		if seen[layout] != 4 {
			t.Errorf("%s has %d backfill plans, want dry-run, discovery, batch and verification", layout, seen[layout])
		}
	}
}
