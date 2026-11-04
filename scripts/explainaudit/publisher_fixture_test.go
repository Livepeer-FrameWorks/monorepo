package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPublisherTransitionBindingsContainExecutableRequests(t *testing.T) {
	for _, name := range []string{"PreparePublisherAdmission", "ConfirmPublisherAdmission", "SettlePublisherAdmission", "MarkPublisherLeasesReleased"} {
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
			if name == "ConfirmPublisherAdmission" && (request["source_revision"] == nil || request["broadcast_live"] != true) {
				t.Fatal("confirm fixture must execute active transition and effect obligation")
			}
			if name == "SettlePublisherAdmission" && (request["reason"] != "admission_lapsed" || request["idle_event"] == nil) {
				t.Fatal("settle fixture must exercise actual lapsed retirement")
			}
		})
	}
}
