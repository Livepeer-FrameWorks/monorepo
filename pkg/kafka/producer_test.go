package kafka

import (
	"testing"
)

// TestAnalyticsEventHeadersEmitsEnvelopeWhenSet proves the producer-side
// header map includes every envelope field when it carries a value.
func TestAnalyticsEventHeadersEmitsEnvelopeWhenSet(t *testing.T) {
	event := &AnalyticsEvent{
		EventID:               "evt-42",
		EventType:             "viewer_connect",
		Source:                "decklog",
		TenantID:              "tenant-1",
		SourceRegion:          "us-east",
		SourceClusterID:       "us-kafka",
		StreamOriginRegion:    "eu-west",
		StreamOriginClusterID: "eu-kafka",
	}

	got := analyticsEventHeaders(event)
	want := map[string]string{
		"source":                   "decklog",
		"event_type":               "viewer_connect",
		"event_id":                 "evt-42",
		"tenant_id":                "tenant-1",
		"source_region":            "us-east",
		"source_cluster_id":        "us-kafka",
		"stream_origin_region":     "eu-west",
		"stream_origin_cluster_id": "eu-kafka",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %q = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected header set: got %v", got)
	}
}

// TestAnalyticsEventHeadersOmitsEmptyConditionals proves empty optional fields
// do not produce Kafka headers.
func TestAnalyticsEventHeadersOmitsEmptyConditionals(t *testing.T) {
	event := &AnalyticsEvent{
		EventID:   "evt-1",
		EventType: "stream_lifecycle",
		Source:    "decklog",
		// Everything else left empty.
	}
	got := analyticsEventHeaders(event)

	// Required (always present).
	if got["source"] != "decklog" || got["event_type"] != "stream_lifecycle" {
		t.Fatalf("required headers missing: %v", got)
	}
	if got["event_id"] != "evt-1" {
		t.Fatalf("event_id should be emitted when non-empty: %v", got)
	}

	// Conditionals must NOT appear when the underlying field is empty.
	for _, k := range []string{
		"tenant_id",
		"source_region",
		"source_cluster_id",
		"stream_origin_region",
		"stream_origin_cluster_id",
	} {
		if _, ok := got[k]; ok {
			t.Errorf("empty field %q should not produce a header (got %q)", k, got[k])
		}
	}
}
