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

func TestUnwrapMistTriggerAllTypes(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	outerTenant := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	innerTenant := "11111111-2222-3333-4444-555555555555"

	tests := []struct {
		name          string
		trigger       *pb.MistTrigger
		wantEventType string
		wantTenantID  string
	}{
		{
			name: "PushRewrite uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_PushRewrite{
					PushRewrite: &pb.PushRewriteTrigger{StreamName: "test"},
				},
			},
			wantEventType: "push_rewrite",
			wantTenantID:  outerTenant,
		},
		{
			name: "PlayRewrite uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_PlayRewrite{
					PlayRewrite: &pb.ViewerResolveTrigger{RequestedStream: "test"},
				},
			},
			wantEventType: "play_rewrite",
			wantTenantID:  outerTenant,
		},
		{
			name: "StreamSource uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StreamSource{
					StreamSource: &pb.StreamSourceTrigger{StreamName: "test"},
				},
			},
			wantEventType: "stream_source",
			wantTenantID:  outerTenant,
		},
		{
			name: "PushOutStart uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_PushOutStart{
					PushOutStart: &pb.PushOutStartTrigger{StreamName: "test"},
				},
			},
			wantEventType: "push_out_start",
			wantTenantID:  outerTenant,
		},
		{
			name: "PushEnd uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_PushEnd{
					PushEnd: &pb.PushEndTrigger{},
				},
			},
			wantEventType: "push_end",
			wantTenantID:  outerTenant,
		},
		{
			name: "ViewerConnect uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ViewerConnect{
					ViewerConnect: &pb.ViewerConnectTrigger{StreamName: "test"},
				},
			},
			wantEventType: "viewer_connect",
			wantTenantID:  outerTenant,
		},
		{
			name: "ViewerDisconnect uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
					ViewerDisconnect: &pb.ViewerDisconnectTrigger{StreamName: "test"},
				},
			},
			wantEventType: "viewer_disconnect",
			wantTenantID:  outerTenant,
		},
		{
			name: "StreamBuffer uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StreamBuffer{
					StreamBuffer: &pb.StreamBufferTrigger{StreamName: "test"},
				},
			},
			wantEventType: "stream_buffer",
			wantTenantID:  outerTenant,
		},
		{
			name: "StreamEnd uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StreamEnd{
					StreamEnd: &pb.StreamEndTrigger{StreamName: "test"},
				},
			},
			wantEventType: "stream_end",
			wantTenantID:  outerTenant,
		},
		{
			name: "TrackList uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_TrackList{
					TrackList: &pb.StreamTrackListTrigger{StreamName: "test"},
				},
			},
			wantEventType: "stream_track_list",
			wantTenantID:  outerTenant,
		},
		{
			name: "RecordingComplete uses outer tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_RecordingComplete{
					RecordingComplete: &pb.RecordingCompleteTrigger{},
				},
			},
			wantEventType: "recording_complete",
			wantTenantID:  outerTenant,
		},
		{
			name: "StreamLifecycleUpdate overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StreamLifecycleUpdate{
					StreamLifecycleUpdate: &pb.StreamLifecycleUpdate{
						NodeId:   "node-1",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "stream_lifecycle_update",
			wantTenantID:  innerTenant,
		},
		{
			name: "StreamLifecycleUpdate falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StreamLifecycleUpdate{
					StreamLifecycleUpdate: &pb.StreamLifecycleUpdate{
						NodeId: "node-1",
					},
				},
			},
			wantEventType: "stream_lifecycle_update",
			wantTenantID:  outerTenant,
		},
		{
			name: "ClientLifecycleUpdate overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ClientLifecycleUpdate{
					ClientLifecycleUpdate: &pb.ClientLifecycleUpdate{
						NodeId:   "node-1",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "client_lifecycle_update",
			wantTenantID:  innerTenant,
		},
		{
			name: "ClientLifecycleUpdate falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ClientLifecycleUpdate{
					ClientLifecycleUpdate: &pb.ClientLifecycleUpdate{
						NodeId: "node-1",
					},
				},
			},
			wantEventType: "client_lifecycle_update",
			wantTenantID:  outerTenant,
		},
		{
			name: "NodeLifecycleUpdate overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
					NodeLifecycleUpdate: &pb.NodeLifecycleUpdate{
						NodeId:   "node-1",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "node_lifecycle_update",
			wantTenantID:  innerTenant,
		},
		{
			name: "NodeLifecycleUpdate falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
					NodeLifecycleUpdate: &pb.NodeLifecycleUpdate{
						NodeId: "node-1",
					},
				},
			},
			wantEventType: "node_lifecycle_update",
			wantTenantID:  outerTenant,
		},
		{
			name: "LoadBalancingData overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_LoadBalancingData{
					LoadBalancingData: &pb.LoadBalancingData{
						SelectedNode: "node-1",
						TenantId:     proto.String(innerTenant),
					},
				},
			},
			wantEventType: "load_balancing",
			wantTenantID:  innerTenant,
		},
		{
			name: "LoadBalancingData falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_LoadBalancingData{
					LoadBalancingData: &pb.LoadBalancingData{
						SelectedNode: "node-1",
					},
				},
			},
			wantEventType: "load_balancing",
			wantTenantID:  outerTenant,
		},
		{
			name: "ClipLifecycleData overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ClipLifecycleData{
					ClipLifecycleData: &pb.ClipLifecycleData{
						ClipHash: "abc123",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "clip_lifecycle",
			wantTenantID:  innerTenant,
		},
		{
			name: "ClipLifecycleData falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ClipLifecycleData{
					ClipLifecycleData: &pb.ClipLifecycleData{
						ClipHash: "abc123",
					},
				},
			},
			wantEventType: "clip_lifecycle",
			wantTenantID:  outerTenant,
		},
		{
			name: "DvrLifecycleData overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
					DvrLifecycleData: &pb.DVRLifecycleData{
						DvrHash:  "dvr123",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "dvr_lifecycle",
			wantTenantID:  innerTenant,
		},
		{
			name: "DvrLifecycleData falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
					DvrLifecycleData: &pb.DVRLifecycleData{
						DvrHash: "dvr123",
					},
				},
			},
			wantEventType: "dvr_lifecycle",
			wantTenantID:  outerTenant,
		},
		{
			name: "StorageLifecycleData overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
					StorageLifecycleData: &pb.StorageLifecycleData{
						AssetHash: "stor123",
						TenantId:  proto.String(innerTenant),
					},
				},
			},
			wantEventType: "storage_lifecycle",
			wantTenantID:  innerTenant,
		},
		{
			name: "StorageLifecycleData falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StorageLifecycleData{
					StorageLifecycleData: &pb.StorageLifecycleData{
						AssetHash: "stor123",
					},
				},
			},
			wantEventType: "storage_lifecycle",
			wantTenantID:  outerTenant,
		},
		{
			name: "ProcessBilling overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{
						NodeId:   "node-1",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "process_billing",
			wantTenantID:  innerTenant,
		},
		{
			name: "ProcessBilling falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_ProcessBilling{
					ProcessBilling: &pb.ProcessBillingEvent{
						NodeId: "node-1",
					},
				},
			},
			wantEventType: "process_billing",
			wantTenantID:  outerTenant,
		},
		{
			name: "StorageSnapshot overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StorageSnapshot{
					StorageSnapshot: &pb.StorageSnapshot{
						NodeId:   "node-1",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "storage_snapshot",
			wantTenantID:  innerTenant,
		},
		{
			name: "StorageSnapshot falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_StorageSnapshot{
					StorageSnapshot: &pb.StorageSnapshot{
						NodeId: "node-1",
					},
				},
			},
			wantEventType: "storage_snapshot",
			wantTenantID:  outerTenant,
		},
		{
			name: "VodLifecycleData overrides with inner tenant",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_VodLifecycleData{
					VodLifecycleData: &pb.VodLifecycleData{
						VodHash:  "vod123",
						TenantId: proto.String(innerTenant),
					},
				},
			},
			wantEventType: "vod_lifecycle",
			wantTenantID:  innerTenant,
		},
		{
			name: "VodLifecycleData falls back to outer when inner is nil",
			trigger: &pb.MistTrigger{
				TenantId: proto.String(outerTenant),
				TriggerPayload: &pb.MistTrigger_VodLifecycleData{
					VodLifecycleData: &pb.VodLifecycleData{
						VodHash: "vod123",
					},
				},
			},
			wantEventType: "vod_lifecycle",
			wantTenantID:  outerTenant,
		},
		{
			name: "outer tenant nil yields empty string for simple payload",
			trigger: &pb.MistTrigger{
				TriggerPayload: &pb.MistTrigger_PushEnd{
					PushEnd: &pb.PushEndTrigger{},
				},
			},
			wantEventType: "push_end",
			wantTenantID:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, eventType, tenantID := server.unwrapMistTrigger(tt.trigger)
			if eventType != tt.wantEventType {
				t.Errorf("eventType = %q, want %q", eventType, tt.wantEventType)
			}
			if tenantID != tt.wantTenantID {
				t.Errorf("tenantID = %q, want %q", tenantID, tt.wantTenantID)
			}
		})
	}
}

func TestUnwrapMistTriggerDefaultUnknown(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	tests := []struct {
		name    string
		trigger *pb.MistTrigger
	}{
		{
			name:    "nil payload",
			trigger: &pb.MistTrigger{TenantId: proto.String("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")},
		},
		{
			name: "unhandled payload type RecordingSegment",
			trigger: &pb.MistTrigger{
				TenantId: proto.String("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
				TriggerPayload: &pb.MistTrigger_RecordingSegment{
					RecordingSegment: &pb.RecordingSegmentTrigger{},
				},
			},
		},
		{
			name: "unhandled payload type ApiRequestBatch",
			trigger: &pb.MistTrigger{
				TenantId: proto.String("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
				TriggerPayload: &pb.MistTrigger_ApiRequestBatch{
					ApiRequestBatch: &pb.APIRequestBatch{},
				},
			},
		},
		{
			name: "unhandled payload type MessageLifecycleData",
			trigger: &pb.MistTrigger{
				TenantId: proto.String("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
				TriggerPayload: &pb.MistTrigger_MessageLifecycleData{
					MessageLifecycleData: &pb.MessageLifecycleData{},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, eventType, _ := server.unwrapMistTrigger(tt.trigger)
			if eventType != "unknown" {
				t.Errorf("eventType = %q, want %q", eventType, "unknown")
			}
		})
	}
}

func TestServiceEventPayloadToMapAllTypes(t *testing.T) {
	tenantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	tests := []struct {
		name    string
		event   *pb.ServiceEvent
		wantKey string
	}{
		{
			name: "ApiRequestBatch",
			event: &pb.ServiceEvent{
				EventType: "api_request",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_ApiRequestBatch{
					ApiRequestBatch: &pb.APIRequestBatch{
						SourceNode: "gw-1",
						Timestamp:  1000,
					},
				},
			},
			wantKey: "source_node",
		},
		{
			name: "AuthEvent",
			event: &pb.ServiceEvent{
				EventType: "auth",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_AuthEvent{
					AuthEvent: &pb.AuthEvent{
						UserId:   "user-1",
						TenantId: tenantID,
						AuthType: "token",
					},
				},
			},
			wantKey: "auth_type",
		},
		{
			name: "TenantEvent",
			event: &pb.ServiceEvent{
				EventType: "tenant",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_TenantEvent{
					TenantEvent: &pb.TenantEvent{
						TenantId:      tenantID,
						ChangedFields: []string{"name"},
					},
				},
			},
			wantKey: "tenant_id",
		},
		{
			name: "ClusterEvent",
			event: &pb.ServiceEvent{
				EventType: "cluster",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_ClusterEvent{
					ClusterEvent: &pb.ClusterEvent{
						ClusterId: "cluster-1",
						TenantId:  tenantID,
					},
				},
			},
			wantKey: "cluster_id",
		},
		{
			name: "StreamChangeEvent",
			event: &pb.ServiceEvent{
				EventType: "stream_change",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_StreamChangeEvent{
					StreamChangeEvent: &pb.StreamChangeEvent{
						StreamId:      "stream-1",
						ChangedFields: []string{"title"},
					},
				},
			},
			wantKey: "stream_id",
		},
		{
			name: "StreamKeyEvent",
			event: &pb.ServiceEvent{
				EventType: "stream_key",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_StreamKeyEvent{
					StreamKeyEvent: &pb.StreamKeyEvent{
						StreamId: "stream-1",
						KeyId:    "key-1",
					},
				},
			},
			wantKey: "key_id",
		},
		{
			name: "BillingEvent",
			event: &pb.ServiceEvent{
				EventType: "billing",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_BillingEvent{
					BillingEvent: &pb.BillingEvent{
						TenantId:  tenantID,
						PaymentId: "pay-1",
						Amount:    9.99,
						Currency:  "USD",
					},
				},
			},
			wantKey: "payment_id",
		},
		{
			name: "SupportEvent",
			event: &pb.ServiceEvent{
				EventType: "support",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_SupportEvent{
					SupportEvent: &pb.MessageLifecycleData{
						ConversationId: "conv-1",
						Timestamp:      1000,
					},
				},
			},
			wantKey: "conversation_id",
		},
		{
			name: "ArtifactEvent",
			event: &pb.ServiceEvent{
				EventType: "artifact",
				TenantId:  tenantID,
				Payload: &pb.ServiceEvent_ArtifactEvent{
					ArtifactEvent: &pb.ArtifactEvent{
						ArtifactId: "art-1",
						StreamId:   "stream-1",
						Status:     "completed",
					},
				},
			},
			wantKey: "artifact_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := serviceEventPayloadToMap(tt.event)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) == 0 {
				t.Fatal("expected non-empty map")
			}
			if _, ok := result[tt.wantKey]; !ok {
				t.Errorf("expected key %q in map, got keys: %v", tt.wantKey, mapKeys(result))
			}
		})
	}
}

func TestServiceEventPayloadToMapNilEvent(t *testing.T) {
	result, err := serviceEventPayloadToMap(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty map for nil event, got %v", result)
	}
}

func TestServiceEventPayloadToMapNoPayload(t *testing.T) {
	event := &pb.ServiceEvent{
		EventType: "test",
		TenantId:  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}
	result, err := serviceEventPayloadToMap(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty map for event with no payload, got %v", result)
	}
}

func TestSendEventNilMetrics(t *testing.T) {
	producer := &fakeProducer{}
	logger := logging.NewLogger()
	server := NewDecklogServer(producer, logger, nil, "test_topic")

	tenantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		TenantId:    proto.String(tenantID),
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{},
		},
	}

	_, err := server.SendEvent(context.Background(), trigger)
	if err != nil {
		t.Fatalf("unexpected error with nil metrics: %v", err)
	}
	if len(producer.publishCalls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(producer.publishCalls))
	}
}

func TestSendServiceEventNilTimestamp(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	tenantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	before := time.Now()

	serviceEvent := &pb.ServiceEvent{
		EventId:   "event-nil-ts",
		EventType: "auth",
		Source:    "api_gateway",
		TenantId:  tenantID,
		Payload: &pb.ServiceEvent_AuthEvent{
			AuthEvent: &pb.AuthEvent{
				UserId:   "user-1",
				TenantId: tenantID,
				AuthType: "token",
			},
		},
	}

	_, err := server.SendServiceEvent(context.Background(), serviceEvent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := time.Now()

	if len(producer.produceCalls) != 1 {
		t.Fatalf("expected 1 produce call, got %d", len(producer.produceCalls))
	}

	var payload kafka.ServiceEvent
	if err := json.Unmarshal(producer.produceCalls[0].value, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.Timestamp.Before(before) || payload.Timestamp.After(after) {
		t.Fatalf("expected timestamp between %s and %s, got %s", before, after, payload.Timestamp)
	}
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
