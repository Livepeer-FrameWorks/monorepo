package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordedBillingDetailsAnomaliesUsePointLookups(t *testing.T) {
	reportPath := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if reportPath == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects existing real-engine billing-details plans")
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err = json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	layouts := map[string]bool{}
	for _, analyzed := range results {
		if analyzed.Database != "purser" || analyzed.Name != "GetTenantBillingDetails" {
			continue
		}
		layouts[analyzed.Layout] = true
		data, err = os.ReadFile(filepath.Join(filepath.Dir(reportPath), analyzed.Layout, "purser", "GetTenantBillingDetails.json"))
		if err != nil {
			t.Fatal(err)
		}
		var recorded struct {
			Plan []planRoot `json:"plan"`
		}
		if err = json.Unmarshal(data, &recorded); err != nil {
			t.Fatal(err)
		}
		lookups := 0
		var inspect func(planNode)
		inspect = func(node planNode) {
			if node.Relation == "crypto_accounting_anomalies" {
				lookups++
				if (node.NodeType != "Index Scan" && node.NodeType != "Index Only Scan") || !strings.Contains(node.IndexCond, "reference_type") || !strings.Contains(node.IndexCond, "reference_id") {
					t.Errorf("%s anomaly lookup is not bounded by its provider reference: node=%s index=%s IndexCond=%s", analyzed.Layout, node.NodeType, node.Index, node.IndexCond)
				}
			}
			for _, child := range node.Plans {
				inspect(child)
			}
		}
		for _, root := range recorded.Plan {
			inspect(root.Plan)
		}
		if lookups == 0 {
			t.Errorf("%s billing-details plan contains no anomaly lookup", analyzed.Layout)
		}
	}
	for _, layout := range []string{"declared", "distributed"} {
		if !layouts[layout] {
			t.Errorf("missing %s real-engine billing-details plan", layout)
		}
	}
}
