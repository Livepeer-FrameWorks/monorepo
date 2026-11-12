package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRecordedSecurityEmailPlansStayBounded(t *testing.T) {
	path := os.Getenv("EXPLAIN_EMAIL_AUDIT_REPORT")
	if path == "" {
		path = os.Getenv("EXPLAIN_AUDIT_REPORT")
	}
	if path == "" {
		t.Skip("requires recorded real-engine email plans")
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
		if r.ID != "commodore:ClaimAccountEmailBatch" && r.ID != "commodore:ListTerminalAccountEmailKeys" {
			continue
		}
		seen[r.Layout]++
		if r.Status != "analyzed" || r.Metrics == nil {
			t.Errorf("%s/%s did not execute: %s", r.Layout, r.Name, r.Reason)
			continue
		}
		if r.ID == "commodore:ClaimAccountEmailBatch" && r.Metrics.RowsReturned > 1 {
			t.Errorf("%s SMTP claim returned %.0f rows, one send must fit within its lease", r.Layout, r.Metrics.RowsReturned)
		}
		if r.Metrics.StorageRowsRead > 400 || r.Metrics.ReadRequests > 5 {
			t.Errorf("%s/%s exceeded bounded email work: %+v", r.Layout, r.Name, r.Metrics)
		}
		for _, flag := range []string{"SEQ_SCAN_LARGE", "SORT_LARGE", "SORT_FEEDS_LIMIT", "INDEX_SCAN_AMPLIFICATION", "READ_AMPLIFICATION", "MANY_READ_RPCS"} {
			if hasFlag(r, flag) {
				t.Errorf("%s/%s has unbounded email flag %s", r.Layout, r.Name, flag)
			}
		}
	}
	for _, layout := range []string{"declared", "distributed"} {
		if seen[layout] != 2 {
			t.Errorf("%s has %d email plans, want claim and retention", layout, seen[layout])
		}
	}
}
