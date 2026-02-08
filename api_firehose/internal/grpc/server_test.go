package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type produceCall struct {
	topic   string
	key     []byte
	value   []byte
	headers map[string]string
}

type fakeProducer struct {
	produceCalls []produceCall
	publishCalls []*kafka.AnalyticsEvent
	produceErr   error
	publishErr   error
}

func (f *fakeProducer) ProduceMessage(topic string, key []byte, value []byte, headers map[string]string) error {
	f.produceCalls = append(f.produceCalls, produceCall{
		topic:   topic,
		key:     key,
		value:   value,
		headers: headers,
	})
	return f.produceErr
}

func (f *fakeProducer) PublishTypedBatch(events []kafka.AnalyticsEvent) error {
	for idx := range events {
		event := events[idx]
		f.publishCalls = append(f.publishCalls, &event)
	}
	return f.publishErr
}

func (f *fakeProducer) PublishTypedEvent(event *kafka.AnalyticsEvent) error {
	f.publishCalls = append(f.publishCalls, event)
	return f.publishErr
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

func newTestServer(producer kafka.ProducerInterface) *DecklogServer {
	logger := logging.NewLogger()
	return NewDecklogServer(producer, logger, nil, "service_events_test")
}

func TestSendEventRejectsNilTrigger(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	_, err := server.SendEvent(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil trigger")
	}
	if len(producer.publishCalls) != 0 {
		t.Fatalf("expected no publishes, got %d", len(producer.publishCalls))
	}
}

func TestSendEventRejectsMissingTenant(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{},
		},
	}

	_, err := server.SendEvent(context.Background(), trigger)
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
	if len(producer.publishCalls) != 0 {
		t.Fatalf("expected no publishes, got %d", len(producer.publishCalls))
	}
}

func TestSendEventPublishesAnalyticsEvent(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	tenantID := "2f64c7d0-8c66-4b3b-88c4-421f8a3027f2"
	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		TenantId:    proto.String(tenantID),
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{},
		},
	}

	_, err := server.SendEvent(context.Background(), trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(producer.publishCalls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(producer.publishCalls))
	}
	event := producer.publishCalls[0]
	if event.EventType != "push_end" {
		t.Fatalf("expected event type push_end, got %s", event.EventType)
	}
	if event.Source != "foghorn" {
		t.Fatalf("expected source foghorn, got %s", event.Source)
	}
	if event.TenantID != tenantID {
		t.Fatalf("expected tenant %s, got %s", tenantID, event.TenantID)
	}
	if _, ok := event.Data["trigger_type"]; !ok {
		t.Fatalf("expected trigger_type in data, got %v", event.Data)
	}
}

func TestSendEventReturnsPublishError(t *testing.T) {
	publishErr := errors.New("publish failed")
	producer := &fakeProducer{publishErr: publishErr}
	server := newTestServer(producer)

	tenantID := "1d2ed4fd-1f2c-4b02-9531-412bde6c45ab"
	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		TenantId:    proto.String(tenantID),
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{},
		},
	}

	_, err := server.SendEvent(context.Background(), trigger)
	if err == nil {
		t.Fatal("expected publish error")
	}
	if len(producer.publishCalls) != 1 {
		t.Fatalf("expected 1 publish attempt, got %d", len(producer.publishCalls))
	}
}

func TestSendServiceEventPublishesToKafka(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	tenantID := "eaa0a2d3-7b64-4df2-9c36-5c5812f6d908"
	serviceEvent := &pb.ServiceEvent{
		EventId:      "event-123",
		EventType:    "auth",
		Source:       "api_gateway",
		TenantId:     tenantID,
		UserId:       "user-456",
		ResourceType: "session",
		ResourceId:   "resource-789",
		Payload: &pb.ServiceEvent_AuthEvent{
			AuthEvent: &pb.AuthEvent{
				UserId:   "user-456",
				TenantId: tenantID,
				AuthType: "token",
			},
		},
	}

	_, err := server.SendServiceEvent(context.Background(), serviceEvent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(producer.produceCalls) != 1 {
		t.Fatalf("expected 1 kafka message, got %d", len(producer.produceCalls))
	}
	call := producer.produceCalls[0]
	if call.topic != "service_events_test" {
		t.Fatalf("expected topic service_events_test, got %s", call.topic)
	}
	if got := call.headers["tenant_id"]; got != tenantID {
		t.Fatalf("expected tenant header %s, got %s", tenantID, got)
	}
	if got := call.headers["event_type"]; got != "auth" {
		t.Fatalf("expected event_type auth, got %s", got)
	}
	if got := call.headers["source"]; got != "api_gateway" {
		t.Fatalf("expected source api_gateway, got %s", got)
	}

	var payload kafka.ServiceEvent
	if err := json.Unmarshal(call.value, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.EventID != "event-123" {
		t.Fatalf("expected event_id event-123, got %s", payload.EventID)
	}
	if payload.TenantID != tenantID {
		t.Fatalf("expected tenant %s, got %s", tenantID, payload.TenantID)
	}
	if payload.Data["auth_type"] != "token" {
		t.Fatalf("expected auth_type token, got %v", payload.Data["auth_type"])
	}
}

func TestSendServiceEventPartitionKeyAndTimestamp(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	tenantID := "d4c7e5a0-1234-4abc-9f01-abcdef123456"
	eventID := "event-001"
	timestamp := time.Date(2024, 5, 6, 12, 30, 0, 0, time.UTC)

	serviceEvent := &pb.ServiceEvent{
		EventId:   eventID,
		EventType: "tenant_update",
		Timestamp: timestamppb.New(timestamp),
		Source:    "api_control",
		TenantId:  tenantID,
	}

	if _, err := server.SendServiceEvent(context.Background(), serviceEvent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(producer.produceCalls) != 1 {
		t.Fatalf("expected 1 produced message, got %d", len(producer.produceCalls))
	}

	call := producer.produceCalls[0]
	if string(call.key) != eventID {
		t.Fatalf("expected partition key %q, got %q", eventID, string(call.key))
	}

	var payload kafka.ServiceEvent
	if err := json.Unmarshal(call.value, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if !payload.Timestamp.Equal(timestamp) {
		t.Fatalf("expected payload timestamp %s, got %s", timestamp, payload.Timestamp)
	}
}

func TestSendServiceEventOutOfOrderAndDuplicates(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	tenantID := "b8f3a2c1-5678-4def-8901-fedcba654321"
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
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(producer.produceCalls) != 2 {
		t.Fatalf("expected 2 produced messages, got %d", len(producer.produceCalls))
	}

	var firstPayload kafka.ServiceEvent
	if err := json.Unmarshal(producer.produceCalls[0].value, &firstPayload); err != nil {
		t.Fatalf("failed to unmarshal first payload: %v", err)
	}
	var secondPayload kafka.ServiceEvent
	if err := json.Unmarshal(producer.produceCalls[1].value, &secondPayload); err != nil {
		t.Fatalf("failed to unmarshal second payload: %v", err)
	}

	if !firstPayload.Timestamp.Equal(newer) {
		t.Fatalf("expected first timestamp %s, got %s", newer, firstPayload.Timestamp)
	}
	if !secondPayload.Timestamp.Equal(older) {
		t.Fatalf("expected second timestamp %s, got %s", older, secondPayload.Timestamp)
	}
}

func TestSendServiceEventRejectsMissingTenant(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	serviceEvent := &pb.ServiceEvent{
		EventId:   "event-123",
		EventType: "auth",
		Source:    "api_gateway",
		Payload: &pb.ServiceEvent_AuthEvent{
			AuthEvent: &pb.AuthEvent{
				UserId:   "user-456",
				TenantId: "",
				AuthType: "token",
			},
		},
	}

	_, err := server.SendServiceEvent(context.Background(), serviceEvent)
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
	if len(producer.produceCalls) != 0 {
		t.Fatalf("expected no kafka messages, got %d", len(producer.produceCalls))
	}
}

func TestConvertProtobufToKafkaEventSerializationFailure(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	tenantID := "70af8a55-99f4-4797-8d11-6dfe0a81f7c8"
	_, err := server.convertProtobufToKafkaEvent(structpb.NewStringValue("bad"), "test_event", "source", tenantID)
	if err == nil {
		t.Fatal("expected serialization error")
	}
}
