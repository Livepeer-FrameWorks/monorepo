package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRecordedProcessingQueuePlansStayBatched(t *testing.T) {
	path := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if path == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects real-engine processing queue plans")
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
		if r.Database != "foghorn" {
			continue
		}
		switch r.Name {
		case "ListArtifactsForCatalogProjection":
			seen[r.Layout]++
			if r.Status != "analyzed" || r.Metrics == nil || r.Metrics.RowsReturned != 100 {
				t.Errorf("%s projection did not execute a full catalog page", r.Layout)
				continue
			}
			// Match the existing two-request baseline's audit allowance. A single
			// lookup for each of the 100 page entries must never fit this bound.
			if r.Metrics.ReadRequests > 14 || hasFlag(r, "CORRELATED_SUBPLAN") || hasFlag(r, "NESTED_LOOP_UNBATCHED") {
				t.Errorf("%s projection must batch its queue lookup: reads=%.0f flags=%v", r.Layout, r.Metrics.ReadRequests, r.Flags)
			}
		case "ClearFinishedProcessingReservations", "ListProcessingMemoryReservations":
			seen[r.Layout]++
			if r.Status != "analyzed" || r.Metrics == nil || hasFlag(r, "SEQ_SCAN_LARGE") {
				t.Errorf("%s/%s must operate on node-sized reservation state: %+v", r.Layout, r.Name, r)
			}
		}
	}
	for _, layout := range []string{"declared", "distributed"} {
		if seen[layout] != 3 {
			t.Errorf("%s has %d processing queue plans, want 3", layout, seen[layout])
		}
	}
}

func TestRecordedProcessingHistoryPurgeIgnoresClearedFences(t *testing.T) {
	path := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if path == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects real-engine processing history plans")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err = json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if r.Database != "foghorn" || r.Name != "PurgeTerminalProcessingJobs" {
			continue
		}
		seen[r.Layout] = true
		if r.Status != "analyzed" || r.Metrics == nil {
			t.Errorf("%s history purge was not executed", r.Layout)
			continue
		}
		// The existing 2448-row baseline permits fewer than 6000 reads. A
		// separate scan of 300 clear fences for each deletion exceeds it.
		if r.Metrics.StorageRowsRead >= 6000 || hasFlag(r, "READ_AMPLIFICATION") {
			t.Errorf("%s history purge revisits cleared node fences: rows=%.0f flags=%v", r.Layout, r.Metrics.StorageRowsRead, r.Flags)
		}
	}
	for _, layout := range []string{"declared", "distributed"} {
		if !seen[layout] {
			t.Errorf("%s history purge plan is missing", layout)
		}
	}
}
