package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPublisherTransitionBindingsContainExecutableRequests(t *testing.T) {
	for _, name := range []string{"PreparePublisherAdmission", "ConfirmPublisherAdmission", "SettlePublisherAdmission", "MarkPublisherLeasesReleased", "InsertIsolatedIngestSession"} {
		t.Run(name, func(t *testing.T) {
			b := &binder{s: &seeder{tables: map[string]*table{}}, queryID: "foghorn:" + name, now: time.Now().UTC()}
			params := b.bind("SELECT $1::jsonb", []string{"request"}, []string{"jsonb"})
			if len(params) != 1 || params[0].Source != "publisher admission fixture" {
				t.Fatalf("publisher transition has no executable binding: %+v", params)
			}
			if name == "MarkPublisherLeasesReleased" {
				var batch []map[string]any
				if err := json.Unmarshal([]byte(params[0].Value), &batch); err != nil || len(batch) != 1 {
					t.Fatalf("release acknowledgement must bind an actual generation array: %s (%v)", params[0].Value, err)
				}
				for _, field := range []string{"tenant_id", "generation"} {
					if value, ok := batch[0][field].(string); !ok || value == "" {
						t.Fatalf("release acknowledgement missing %s: %v", field, batch)
					}
				}
				return
			}
			var request map[string]any
			if err := json.Unmarshal([]byte(params[0].Value), &request); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"tenant_id", "node_id", "trigger_uuid", "generation", "internal_name"} {
				if value, ok := request[field].(string); !ok || value == "" {
					t.Fatalf("%s fixture missing %s: %v", name, field, request)
				}
			}
			if name == "PreparePublisherAdmission" && (request["connected_event"] == nil || request["authority"] == nil || request["stream_id"] == nil) {
				t.Fatal("prepare fixture omits durable publisher/event identity")
			}
			if name == "InsertIsolatedIngestSession" && (request["publisher_lease_epoch"] != float64(19) || request["runtime_name"] != "live+audit-"+request["stream_id"].(string)+"_fw1p_"+strings.ReplaceAll(request["generation"].(string), "-", "")) {
				t.Fatal("isolated insert fixture omits its exact runtime or ownership epoch")
			}
			if name == "ConfirmPublisherAdmission" && (request["source_revision"] == nil || request["broadcast_live"] != true) {
				t.Fatal("confirm fixture must execute active transition and effect obligation")
			}
			if name == "SettlePublisherAdmission" && (request["reason"] != "admission_lapsed" || request["idle_event"] == nil) {
				t.Fatal("settle fixture must exercise actual lapsed retirement")
			}
		})
	}
}

func TestRecordedIsolatedPublisherInsertExecutesBothLayouts(t *testing.T) {
	path := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if path == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects recorded real-engine plans")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range results {
		if r.ID != "foghorn:InsertIsolatedIngestSession" {
			continue
		}
		seen[r.Layout]++
		if r.Status != "analyzed" || r.Metrics == nil || r.Metrics.RowsReturned != 1 || len(r.Params) != 1 || r.Params[0].Source != "publisher admission fixture" {
			t.Errorf("%s did not execute the isolated publisher insert: %+v", r.Layout, r)
		}
	}
	for _, layout := range []string{"declared", "distributed"} {
		if seen[layout] != 1 {
			t.Errorf("%s has %d isolated publisher insert plans, want 1", layout, seen[layout])
		}
	}
}
