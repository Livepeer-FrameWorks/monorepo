package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// An exact aggregate returns one row; its cost is the indexed failure set,
// rather than the single-row denominator used by the generic amplification flag.
func TestRecordedBillingRecoveryCountReadsFailureIndexes(t *testing.T) {
	reportPath := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if reportPath == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects real-engine plans")
	}
	for _, layout := range []string{"declared", "distributed"} {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(reportPath), layout, "purser", "ProviderWebhookInboxRepeatedFailures.json"))
		if err != nil {
			t.Fatal(err)
		}
		var recorded struct {
			Status  string
			Plan    []planRoot
			Metrics *metrics
		}
		if err := json.Unmarshal(data, &recorded); err != nil {
			t.Fatal(err)
		}
		if recorded.Status != "analyzed" || recorded.Metrics == nil || recorded.Metrics.StorageRowsRead == 0 {
			t.Fatalf("%s failure count was not executed on populated inventory", layout)
		}
		seen := map[string]bool{}
		var inspect func(planNode)
		inspect = func(n planNode) {
			if n.Relation == "provider_webhook_inbox" || n.Relation == "provider_settlements" {
				seen[n.Relation] = true
				if n.Index == "" {
					t.Errorf("%s count scans failure history without an index: %+v", layout, n)
				}
			}
			for _, child := range n.Plans {
				inspect(child)
			}
		}
		for _, root := range recorded.Plan {
			inspect(root.Plan)
		}
		if len(seen) != 2 {
			t.Errorf("%s count omits an obligation family: %v", layout, seen)
		}
	}
}
