package kafka

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEncodeDLQMessageExtractsTenantIDFromPayload(t *testing.T) {
	timestamp := time.Date(2024, 10, 5, 12, 30, 0, 0, time.UTC)
	msg := Message{
		Topic:     "analytics_events",
		Partition: 2,
		Offset:    42,
		Timestamp: timestamp,
		Key:       []byte("event-key"),
		Value:     []byte(`{"tenant_id":"tenant-123","event_id":"evt-1"}`),
		Headers: map[string]string{
			"event_type": "viewer_connect",
		},
	}

	payloadBytes, err := EncodeDLQMessage(msg, errors.New("clickhouse insert failed"), "periscope-ingest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload DLQPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.TenantID != "tenant-123" {
		t.Fatalf("expected tenant_id tenant-123, got %q", payload.TenantID)
	}
	if payload.Headers["tenant_id"] != "tenant-123" {
		t.Fatalf("expected tenant_id header tenant-123, got %q", payload.Headers["tenant_id"])
	}
	if payload.Headers["event_type"] != "viewer_connect" {
		t.Fatalf("expected event_type header viewer_connect, got %q", payload.Headers["event_type"])
	}
	if payload.Topic != msg.Topic || payload.Partition != msg.Partition || payload.Offset != msg.Offset {
		t.Fatalf("payload topic/partition/offset mismatch")
	}
	if !payload.Timestamp.Equal(timestamp) {
		t.Fatalf("expected timestamp %v, got %v", timestamp, payload.Timestamp)
	}
	if payload.Error == "" {
		t.Fatal("expected error string to be set")
	}
	if payload.Consumer != "periscope-ingest" {
		t.Fatalf("expected consumer periscope-ingest, got %q", payload.Consumer)
	}

	key, err := base64.StdEncoding.DecodeString(payload.KeyBase64)
	if err != nil {
		t.Fatalf("failed to decode key: %v", err)
	}
	if string(key) != string(msg.Key) {
		t.Fatalf("expected key %q, got %q", string(msg.Key), string(key))
	}

	value, err := base64.StdEncoding.DecodeString(payload.ValueBase64)
	if err != nil {
		t.Fatalf("failed to decode value: %v", err)
	}
	if string(value) != string(msg.Value) {
		t.Fatalf("expected value %q, got %q", string(msg.Value), string(value))
	}
}

func TestEncodeDLQMessageUsesHeaderTenantID(t *testing.T) {
	msg := Message{
		Topic:     "service_events",
		Partition: 1,
		Offset:    7,
		Timestamp: time.Now(),
		Value:     []byte("not-json"),
		Headers: map[string]string{
			"tenant_id": "tenant-999",
		},
	}

	payloadBytes, err := EncodeDLQMessage(msg, errors.New("kafka publish failed"), "signalman")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload DLQPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.TenantID != "tenant-999" {
		t.Fatalf("expected tenant_id tenant-999, got %q", payload.TenantID)
	}
	if payload.Headers["tenant_id"] != "tenant-999" {
		t.Fatalf("expected tenant_id header tenant-999, got %q", payload.Headers["tenant_id"])
	}
}
