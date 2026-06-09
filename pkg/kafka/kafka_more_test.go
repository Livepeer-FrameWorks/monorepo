package kafka

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

func TestEnsureHeaderBranches(t *testing.T) {
	t.Run("empty value is noop", func(t *testing.T) {
		in := map[string]string{"a": "1"}
		out := ensureHeader(in, "b", "")
		if !reflect.DeepEqual(out, in) {
			t.Fatalf("expected unchanged map, got %v", out)
		}
	})

	t.Run("nil map creates single-entry map", func(t *testing.T) {
		out := ensureHeader(nil, "k", "v")
		if len(out) != 1 || out["k"] != "v" {
			t.Fatalf("expected {k:v}, got %v", out)
		}
	})

	t.Run("existing non-empty value preserved", func(t *testing.T) {
		in := map[string]string{"k": "orig"}
		out := ensureHeader(in, "k", "new")
		if out["k"] != "orig" {
			t.Fatalf("expected orig preserved, got %q", out["k"])
		}
	})

	t.Run("existing empty value overwritten via clone", func(t *testing.T) {
		in := map[string]string{"k": "", "other": "x"}
		out := ensureHeader(in, "k", "v")
		if out["k"] != "v" {
			t.Fatalf("expected k=v, got %q", out["k"])
		}
		if out["other"] != "x" {
			t.Fatalf("expected other preserved, got %q", out["other"])
		}
		if in["k"] != "" {
			t.Fatalf("expected source map untouched, got %q", in["k"])
		}
	})
}

func TestExtractMessageMetadataFromJSON(t *testing.T) {
	cases := []struct {
		name     string
		value    []byte
		wantOK   bool
		wantMeta messageMetadata
	}{
		{"empty", nil, false, messageMetadata{}},
		{"invalid json", []byte("not json"), false, messageMetadata{}},
		{"no relevant fields", []byte(`{"foo":"bar"}`), false, messageMetadata{}},
		{"only tenant", []byte(`{"tenant_id":"t1"}`), true, messageMetadata{TenantID: "t1"}},
		{"only event_id", []byte(`{"event_id":"e1"}`), true, messageMetadata{EventID: "e1"}},
		{"only event_type", []byte(`{"event_type":"y1"}`), true, messageMetadata{EventType: "y1"}},
		{"non-string tenant ignored", []byte(`{"tenant_id":123}`), false, messageMetadata{}},
		{"all fields", []byte(`{"tenant_id":"t","event_id":"e","event_type":"y"}`), true, messageMetadata{TenantID: "t", EventID: "e", EventType: "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, ok := extractMessageMetadataFromJSON(tc.value)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if meta != tc.wantMeta {
				t.Fatalf("meta = %+v, want %+v", meta, tc.wantMeta)
			}
		})
	}
}

func TestEncodeDLQMessageJSONFallbackOnlyForMissingHeaders(t *testing.T) {
	// tenant_id present in header, but event_id/event_type only in JSON value.
	msg := Message{
		Topic: "t",
		Value: []byte(`{"tenant_id":"json-tenant","event_id":"json-evt","event_type":"json-type"}`),
		Headers: map[string]string{
			"tenant_id": "hdr-tenant",
		},
	}
	b, err := EncodeDLQMessage(msg, nil, "c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var p DLQPayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.TenantID != "hdr-tenant" {
		t.Fatalf("expected header tenant to win, got %q", p.TenantID)
	}
	if p.EventID != "json-evt" {
		t.Fatalf("expected json event_id fallback, got %q", p.EventID)
	}
	if p.EventType != "json-type" {
		t.Fatalf("expected json event_type fallback, got %q", p.EventType)
	}
}

func TestEncodeDLQMessageNoFallbackWhenAllHeadersPresent(t *testing.T) {
	// All three header fields present → JSON value must NOT be consulted even
	// though it carries conflicting values.
	msg := Message{
		Topic: "t",
		Value: []byte(`{"tenant_id":"json-tenant","event_id":"json-evt","event_type":"json-type"}`),
		Headers: map[string]string{
			"tenant_id":  "hdr-tenant",
			"event_id":   "hdr-evt",
			"event_type": "hdr-type",
		},
	}
	b, err := EncodeDLQMessage(msg, nil, "c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var p DLQPayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.TenantID != "hdr-tenant" || p.EventID != "hdr-evt" || p.EventType != "hdr-type" {
		t.Fatalf("expected all header values, got %+v", p)
	}
}

func TestEncodeDLQMessageSingleMissingFieldTriggersJSONFallback(t *testing.T) {
	jsonValue := []byte(`{"tenant_id":"json-tenant","event_id":"json-evt","event_type":"json-type"}`)
	cases := []struct {
		name       string
		headers    map[string]string
		wantTenant string
		wantEvent  string
		wantType   string
	}{
		{
			name:       "only tenant missing",
			headers:    map[string]string{"event_id": "hdr-evt", "event_type": "hdr-type"},
			wantTenant: "json-tenant", wantEvent: "hdr-evt", wantType: "hdr-type",
		},
		{
			name:       "only event_id missing",
			headers:    map[string]string{"tenant_id": "hdr-tenant", "event_type": "hdr-type"},
			wantTenant: "hdr-tenant", wantEvent: "json-evt", wantType: "hdr-type",
		},
		{
			name:       "only event_type missing",
			headers:    map[string]string{"tenant_id": "hdr-tenant", "event_id": "hdr-evt"},
			wantTenant: "hdr-tenant", wantEvent: "hdr-evt", wantType: "json-type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := Message{Topic: "t", Value: jsonValue, Headers: tc.headers}
			b, err := EncodeDLQMessage(msg, nil, "c")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var p DLQPayload
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.TenantID != tc.wantTenant || p.EventID != tc.wantEvent || p.EventType != tc.wantType {
				t.Fatalf("got tenant=%q event=%q type=%q, want %q/%q/%q",
					p.TenantID, p.EventID, p.EventType, tc.wantTenant, tc.wantEvent, tc.wantType)
			}
		})
	}
}

func TestEncodeDLQMessageOmitsKeyWhenEmpty(t *testing.T) {
	msg := Message{Topic: "t", Value: []byte("v"), Key: nil}
	b, err := EncodeDLQMessage(msg, nil, "c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var p DLQPayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.KeyBase64 != "" {
		t.Fatalf("expected empty key base64, got %q", p.KeyBase64)
	}
}

func TestPublishLagNegativeLagClampedToZero(t *testing.T) {
	// committed offset ahead of reported end offset → raw lag negative → clamp 0.
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_lag_neg"}, []string{"topic", "partition"})
	c := &Consumer{
		logger:     logrus.New(),
		groupID:    "g",
		handlers:   map[string]Handler{"events": nil},
		lagTracker: &LagTrackerConfig{Gauge: gauge},
	}
	fetcher := &fakeLagFetcher{
		ends:    buildEnds(map[string]map[int32]int64{"events": {0: 10}}),
		commits: buildCommits(map[string]map[int32]int64{"events": {0: 25}}),
	}
	if err := c.publishLag(context.Background(), fetcher, []string{"events"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := gaugeValue(t, gauge, "events", "0"); got != 0 {
		t.Fatalf("negative lag should clamp to 0, got %v", got)
	}
}
