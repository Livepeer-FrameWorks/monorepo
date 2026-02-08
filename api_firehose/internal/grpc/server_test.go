package grpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type producedMessage struct {
	topic   string
	key     []byte
	value   []byte
	headers map[string]string
}

type fakeProducer struct {
	produced []producedMessage
	typed    []*kafka.AnalyticsEvent
	err      error
}

func (f *fakeProducer) ProduceMessage(topic string, key []byte, value []byte, headers map[string]string) error {
	if f.err != nil {
		return f.err
	}
	f.produced = append(f.produced, producedMessage{
		topic:   topic,
		key:     key,
		value:   value,
		headers: headers,
	})
	return nil
}

func (f *fakeProducer) PublishTypedBatch(events []kafka.AnalyticsEvent) error {
	if f.err != nil {
		return f.err
	}
	for i := range events {
		event := events[i]
		f.typed = append(f.typed, &event)
	}
	return nil
}

func (f *fakeProducer) PublishTypedEvent(event *kafka.AnalyticsEvent) error {
	if f.err != nil {
		return f.err
	}
	f.typed = append(f.typed, event)
	return nil
}

func (f *fakeProducer) Close() error {
	return nil
}

func (f *fakeProducer) HealthCheck() error {
	return nil
}

func (f *fakeProducer) GetMetrics() (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func TestSendServiceEventRoutingAndPartitionKey(t *testing.T) {
	producer := &fakeProducer{}
	logger := logging.Logger(logrus.New())
	server := NewDecklogServer(producer, logger, nil, "service_events_custom")

	tenantID := uuid.NewString()
	eventID := "event-001"
	timestamp := time.Date(2024, 5, 6, 12, 30, 0, 0, time.UTC)

	event := &pb.ServiceEvent{
		EventId:   eventID,
		EventType: "tenant_update",
		Timestamp: timestamppb.New(timestamp),
		Source:    "api_control",
		TenantId:  tenantID,
	}

	if _, err := server.SendServiceEvent(context.Background(), event); err != nil {
		t.Fatalf("SendServiceEvent returned error: %v", err)
	}

	if len(producer.produced) != 1 {
		t.Fatalf("expected 1 produced message, got %d", len(producer.produced))
	}

	produced := producer.produced[0]
	if produced.topic != "service_events_custom" {
		t.Fatalf("expected topic service_events_custom, got %q", produced.topic)
	}
	if string(produced.key) != eventID {
		t.Fatalf("expected partition key %q, got %q", eventID, string(produced.key))
	}
	if produced.headers["tenant_id"] != tenantID {
		t.Fatalf("expected tenant_id header %q, got %q", tenantID, produced.headers["tenant_id"])
	}
	if produced.headers["event_type"] != "tenant_update" {
		t.Fatalf("expected event_type header tenant_update, got %q", produced.headers["event_type"])
	}
	if produced.headers["source"] != "api_control" {
		t.Fatalf("expected source header api_control, got %q", produced.headers["source"])
	}

	var payload kafka.ServiceEvent
	if err := json.Unmarshal(produced.value, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.EventID != eventID {
		t.Fatalf("expected payload event ID %q, got %q", eventID, payload.EventID)
	}
	if !payload.Timestamp.Equal(timestamp) {
		t.Fatalf("expected payload timestamp %s, got %s", timestamp, payload.Timestamp)
	}
}

func TestSendServiceEventOutOfOrderAndDuplicates(t *testing.T) {
	producer := &fakeProducer{}
	logger := logging.Logger(logrus.New())
	server := NewDecklogServer(producer, logger, nil, "")

	tenantID := uuid.NewString()
	eventID := "event-dup"

	newer := time.Date(2024, 5, 6, 14, 0, 0, 0, time.UTC)
	older := time.Date(2024, 5, 6, 13, 0, 0, 0, time.UTC)

	events := []*pb.ServiceEvent{
		{
			EventId:   eventID,
			EventType: "stream_update",
			Timestamp: timestamppb.New(newer),
			Source:    "api_rooms",
			TenantId:  tenantID,
		},
		{
			EventId:   eventID,
			EventType: "stream_update",
			Timestamp: timestamppb.New(older),
			Source:    "api_rooms",
			TenantId:  tenantID,
		},
	}

	for _, event := range events {
		if _, err := server.SendServiceEvent(context.Background(), event); err != nil {
			t.Fatalf("SendServiceEvent returned error: %v", err)
		}
	}

	if len(producer.produced) != 2 {
		t.Fatalf("expected 2 produced messages, got %d", len(producer.produced))
	}

	var firstPayload kafka.ServiceEvent
	if err := json.Unmarshal(producer.produced[0].value, &firstPayload); err != nil {
		t.Fatalf("failed to unmarshal first payload: %v", err)
	}
	var secondPayload kafka.ServiceEvent
	if err := json.Unmarshal(producer.produced[1].value, &secondPayload); err != nil {
		t.Fatalf("failed to unmarshal second payload: %v", err)
	}

	if !firstPayload.Timestamp.Equal(newer) {
		t.Fatalf("expected first timestamp %s, got %s", newer, firstPayload.Timestamp)
	}
	if !secondPayload.Timestamp.Equal(older) {
		t.Fatalf("expected second timestamp %s, got %s", older, secondPayload.Timestamp)
	}
}

func TestSendEventTenantRoutingInvariant(t *testing.T) {
	producer := &fakeProducer{}
	logger := logging.Logger(logrus.New())
	server := NewDecklogServer(producer, logger, nil, "")

	tenantID := uuid.NewString()
	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &pb.StreamLifecycleUpdate{
				TenantId: &tenantID,
			},
		},
	}

	if _, err := server.SendEvent(context.Background(), trigger); err != nil {
		t.Fatalf("SendEvent returned error: %v", err)
	}

	if len(producer.typed) != 1 {
		t.Fatalf("expected 1 published analytics event, got %d", len(producer.typed))
	}
	if producer.typed[0].TenantID != tenantID {
		t.Fatalf("expected tenant ID %q, got %q", tenantID, producer.typed[0].TenantID)
	}

	missingTenant := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_StreamSource{
			StreamSource: &pb.StreamSource{},
		},
	}

	if _, err := server.SendEvent(context.Background(), missingTenant); err == nil {
		t.Fatalf("expected error for missing tenant ID")
	}
	if len(producer.typed) != 1 {
		t.Fatalf("expected no additional published events on tenant error")
	}
}
