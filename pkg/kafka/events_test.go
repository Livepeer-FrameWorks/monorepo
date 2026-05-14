package kafka

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestAnalyticsEventHandlerMapsHeaders(t *testing.T) {
	logger := logrus.New()
	var got AnalyticsEvent
	handler := NewAnalyticsEventHandler(func(event AnalyticsEvent) error {
		got = event
		return nil
	}, logger)

	event := AnalyticsEvent{
		EventID:   "event-1",
		EventType: "viewer_connect",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"payload": "value",
		},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	msg := Message{
		Value: payload,
		Headers: map[string]string{
			"tenant_id": "tenant-123",
			"source":    "decklog",
		},
	}

	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TenantID != "tenant-123" {
		t.Fatalf("expected tenant_id tenant-123, got %q", got.TenantID)
	}
	if got.Source != "decklog" {
		t.Fatalf("expected source decklog, got %q", got.Source)
	}
}

// TestAnalyticsEventHandlerBackfillsEnvelopeFromHeaders covers the
// MirrorMaker case: a regional producer stamped envelope headers but the
// body left those fields empty (e.g. older code paths). The aggregator
// consumer must recover identity from the headers.
func TestAnalyticsEventHandlerBackfillsEnvelopeFromHeaders(t *testing.T) {
	logger := logrus.New()
	var got AnalyticsEvent
	handler := NewAnalyticsEventHandler(func(event AnalyticsEvent) error {
		got = event
		return nil
	}, logger)

	body := AnalyticsEvent{
		EventType: "viewer_connect",
		Timestamp: time.Now(),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := Message{
		Value: payload,
		Headers: map[string]string{
			"source":                   "decklog",
			"tenant_id":                "tenant-1",
			"event_id":                 "evt-99",
			"source_region":            "us-east",
			"source_cluster_id":        "us-kafka",
			"stream_origin_region":     "eu-west",
			"stream_origin_cluster_id": "eu-kafka",
		},
	}

	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if got.EventID != "evt-99" {
		t.Errorf("EventID = %q, want evt-99", got.EventID)
	}
	if got.SourceRegion != "us-east" {
		t.Errorf("SourceRegion = %q, want us-east", got.SourceRegion)
	}
	if got.SourceClusterID != "us-kafka" {
		t.Errorf("SourceClusterID = %q, want us-kafka", got.SourceClusterID)
	}
	if got.StreamOriginRegion != "eu-west" {
		t.Errorf("StreamOriginRegion = %q, want eu-west", got.StreamOriginRegion)
	}
	if got.StreamOriginClusterID != "eu-kafka" {
		t.Errorf("StreamOriginClusterID = %q, want eu-kafka", got.StreamOriginClusterID)
	}
}

// TestAnalyticsEventHandlerBodyWinsOverHeaders proves producer intent
// (envelope set on body) is preserved even when MirrorMaker re-stamps
// headers from its own perspective during mirroring.
func TestAnalyticsEventHandlerBodyWinsOverHeaders(t *testing.T) {
	logger := logrus.New()
	var got AnalyticsEvent
	handler := NewAnalyticsEventHandler(func(event AnalyticsEvent) error {
		got = event
		return nil
	}, logger)

	body := AnalyticsEvent{
		EventID:               "body-event",
		EventType:             "stream_lifecycle",
		Source:                "body-source",
		TenantID:              "body-tenant",
		SourceRegion:          "body-region",
		SourceClusterID:       "body-cluster",
		StreamOriginRegion:    "body-origin-region",
		StreamOriginClusterID: "body-origin-cluster",
		Timestamp:             time.Now(),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := Message{
		Value: payload,
		Headers: map[string]string{
			"event_id":                 "header-event",
			"source":                   "header-source",
			"tenant_id":                "header-tenant",
			"source_region":            "header-region",
			"source_cluster_id":        "header-cluster",
			"stream_origin_region":     "header-origin-region",
			"stream_origin_cluster_id": "header-origin-cluster",
		},
	}

	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if got.EventID != "body-event" {
		t.Errorf("EventID overridden by header: %q", got.EventID)
	}
	if got.Source != "body-source" {
		t.Errorf("Source overridden: %q", got.Source)
	}
	if got.TenantID != "body-tenant" {
		t.Errorf("TenantID overridden: %q", got.TenantID)
	}
	if got.SourceRegion != "body-region" {
		t.Errorf("SourceRegion overridden: %q", got.SourceRegion)
	}
	if got.SourceClusterID != "body-cluster" {
		t.Errorf("SourceClusterID overridden: %q", got.SourceClusterID)
	}
	if got.StreamOriginRegion != "body-origin-region" {
		t.Errorf("StreamOriginRegion overridden: %q", got.StreamOriginRegion)
	}
	if got.StreamOriginClusterID != "body-origin-cluster" {
		t.Errorf("StreamOriginClusterID overridden: %q", got.StreamOriginClusterID)
	}
}
