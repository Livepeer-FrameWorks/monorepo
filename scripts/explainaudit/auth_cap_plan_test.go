package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordedSessionCapPlansStayBounded(t *testing.T) {
	path := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if path == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects recorded real-engine cap plans")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err = json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range results {
		fixture := authCapFixtureFor(r.ID)
		if fixture == nil {
			continue
		}
		seen[r.Layout]++
		if r.Status != "analyzed" || r.Metrics == nil || r.Metrics.StorageRowsRead == 0 {
			t.Errorf("%s/%s did not execute a populated cap query: %+v", r.Layout, r.Name, r)
			continue
		}
		bound := float64(120)
		requiredIndex := "idx_auth_sessions_owner_oldest"
		switch fixture.name {
		case "CountAuthSessionsForIssuance":
			bound = 400
			requiredIndex = "idx_auth_sessions_owner_active"
		case "ListAuthSessionCapOwners":
			bound = 400
			requiredIndex = ""
		}
		if r.Metrics.StorageRowsRead > bound {
			t.Errorf("%s/%s scanned %.0f rows, cap bound %.0f", r.Layout, r.Name, r.Metrics.StorageRowsRead, bound)
		}
		for _, flag := range []string{"SEQ_SCAN_LARGE", "SORT_LARGE", "SORT_FEEDS_LIMIT", "INDEX_SCAN_AMPLIFICATION"} {
			if hasFlag(r, flag) {
				t.Errorf("%s/%s has unbounded cap flag %s", r.Layout, r.Name, flag)
			}
		}
		if requiredIndex == "" {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), r.Layout, r.Database, safeFile(r.Name)+".json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		var recorded struct {
			Plan []planRoot `json:"plan"`
		}
		if err = json.Unmarshal(raw, &recorded); err != nil {
			t.Fatal(err)
		}
		indexed := false
		var inspect func(planNode)
		inspect = func(node planNode) {
			if node.Index == requiredIndex {
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
			t.Errorf("%s/%s did not use %s", r.Layout, r.Name, requiredIndex)
		}
	}
	for _, layout := range []string{"declared", "distributed"} {
		if seen[layout] != 4 {
			t.Errorf("%s has %d cap plans, want 4", layout, seen[layout])
		}
	}
}
