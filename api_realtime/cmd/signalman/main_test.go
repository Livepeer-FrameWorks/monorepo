package main

import (
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSkipperInvestigationMapping(t *testing.T) {
	channel := mapEventTypeToChannel("skipper_investigation")
	if channel != signalmanpb.Channel_CHANNEL_AI {
		t.Fatalf("expected CHANNEL_AI, got %v", channel)
	}

	eventType := mapEventTypeToProto("skipper_investigation")
	if eventType != signalmanpb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION {
		t.Fatalf("expected EVENT_TYPE_SKIPPER_INVESTIGATION, got %v", eventType)
	}
}

func TestClientLifecycleBatchMapping(t *testing.T) {
	eventType := mapEventTypeToProto("client_lifecycle_batch")
	if eventType != signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE {
		t.Fatalf("expected EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE, got %v", eventType)
	}
}

func TestClientLifecycleBatchToProtoDataExpandsSamples(t *testing.T) {
	streamID := "stream-1"
	originClusterID := "cluster-eu"
	controlCellID := "control-eu"
	trigger := &ipcpb.MistTrigger{
		SourceRegion:       "us-east",
		ClusterId:          stringPtr("cluster-us"),
		StreamOriginRegion: "eu-west",
		OriginClusterId:    &originClusterID,
		ControlCellId:      &controlCellID,
		SchemaVersion:      2,
		TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleBatch{
			ClientLifecycleBatch: &ipcpb.ClientLifecycleBatch{
				StreamId: &streamID,
				Samples: []*ipcpb.ClientLifecycleUpdate{
					{SessionId: stringPtr("sess-1"), StreamId: &streamID},
					{SessionId: stringPtr("sess-2"), StreamId: &streamID},
				},
			},
		},
	}

	raw, err := protojson.Marshal(trigger)
	if err != nil {
		t.Fatalf("marshal trigger: %v", err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal trigger JSON: %v", err)
	}

	events := clientLifecycleBatchToProtoData(data, logging.NewLogger())
	if len(events) != 2 {
		t.Fatalf("expected 2 expanded events, got %d", len(events))
	}
	if got := events[0].GetClientLifecycle().GetSessionId(); got != "sess-1" {
		t.Fatalf("expected first session sess-1, got %q", got)
	}
	if got := events[1].GetClientLifecycle().GetSessionId(); got != "sess-2" {
		t.Fatalf("expected second session sess-2, got %q", got)
	}
	for i, event := range events {
		if event.GetSourceRegion() != "us-east" || event.GetSourceClusterId() != "cluster-us" ||
			event.GetStreamOriginRegion() != "eu-west" || event.GetStreamOriginClusterId() != "cluster-eu" ||
			event.GetControlCellId() != "control-eu" || event.GetSchemaVersion() != 2 {
			t.Fatalf("sample %d lost topology envelope: %+v", i, event)
		}
	}
}

func TestEventToProtoDataPreservesTopologyEnvelope(t *testing.T) {
	controlCellID := "control-eu"
	originClusterID := "cluster-eu"
	trigger := &ipcpb.MistTrigger{
		ClusterId:          stringPtr("cluster-us"),
		OriginClusterId:    &originClusterID,
		ControlCellId:      &controlCellID,
		SourceRegion:       "us-east",
		StreamOriginRegion: "eu-west",
		SchemaVersion:      2,
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{StreamId: stringPtr("stream-1")},
		},
	}
	raw, err := protojson.Marshal(trigger)
	if err != nil {
		t.Fatalf("marshal trigger: %v", err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal trigger JSON: %v", err)
	}

	got := eventToProtoData(data, logging.NewLogger())
	if got.GetSourceRegion() != "us-east" || got.GetSourceClusterId() != "cluster-us" ||
		got.GetStreamOriginRegion() != "eu-west" || got.GetStreamOriginClusterId() != "cluster-eu" ||
		got.GetSchemaVersion() != 2 || got.GetControlCellId() != "control-eu" {
		t.Fatalf("topology envelope = source %q/%q origin %q/%q schema %d control %q",
			got.GetSourceRegion(), got.GetSourceClusterId(), got.GetStreamOriginRegion(),
			got.GetStreamOriginClusterId(), got.GetSchemaVersion(), got.GetControlCellId())
	}
}

func TestServiceEventToProtoDataPreservesTopologyEnvelope(t *testing.T) {
	got := serviceEventToProtoData(kafka.ServiceEvent{
		EventType:             "conversation_created",
		TenantID:              "tenant-1",
		SourceRegion:          "us-east",
		SourceClusterID:       "cluster-us",
		StreamOriginRegion:    "eu-west",
		StreamOriginClusterID: "cluster-eu",
		SchemaVersion:         2,
		Data:                  map[string]interface{}{"conversation_id": "conversation-1"},
	}, logging.NewLogger())
	if got == nil || got.GetMessageLifecycle() == nil {
		t.Fatalf("service event was not mapped: %+v", got)
	}
	if got.GetSourceRegion() != "us-east" || got.GetSourceClusterId() != "cluster-us" ||
		got.GetStreamOriginRegion() != "eu-west" || got.GetStreamOriginClusterId() != "cluster-eu" ||
		got.GetSchemaVersion() != 2 {
		t.Fatalf("service topology envelope was lost: %+v", got)
	}
}

func stringPtr(value string) *string {
	return &value
}
