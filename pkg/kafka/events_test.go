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
