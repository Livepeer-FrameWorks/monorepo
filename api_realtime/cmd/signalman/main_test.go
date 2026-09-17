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

func TestStreamChangeServiceEventMapsToStreamsChannel(t *testing.T) {
	if got := mapEventTypeToChannel("stream_updated"); got != signalmanpb.Channel_CHANNEL_STREAMS {
		t.Fatalf("stream_updated channel=%s, want streams", got)
	}
	if got := mapEventTypeToProto("stream_updated"); got != signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE {
		t.Fatalf("stream_updated event type=%s, want stream lifecycle update", got)
	}
	data := serviceEventToProtoData(kafka.ServiceEvent{
		EventType: "stream_updated", TenantID: "tenant-1",
		Data: map[string]interface{}{
			"stream_id": "stream-1", "changed_fields": []interface{}{"push_target_status", "title"},
		},
	}, logging.NewLogger())
	if data == nil || data.GetStreamChange() == nil {
		t.Fatalf("stream change was not mapped: %+v", data)
	}
	if got := data.GetStreamChange(); got.GetStreamId() != "stream-1" || len(got.GetChangedFields()) != 2 || got.GetChangedFields()[0] != "push_target_status" {
		t.Fatalf("unexpected stream change payload: %+v", got)
	}
}

func TestIncidentUpdatedMapsToSystemChannel(t *testing.T) {
	if got := mapEventTypeToChannel("incident_updated"); got != signalmanpb.Channel_CHANNEL_SYSTEM {
		t.Fatalf("incident_updated channel=%s, want system", got)
	}
	if got := mapEventTypeToProto("incident_updated"); got != signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED {
		t.Fatalf("incident_updated event type=%s, want incident updated", got)
	}
}

func TestIncidentUpdatedServiceEventToProtoData(t *testing.T) {
	// Decklog publishes the payload through protojson, which renders int64 as
	// a JSON string; JSON decoding of a numeric producer yields float64.
	for name, updatedAt := range map[string]interface{}{
		"protojson string": "1757930000123",
		"json number":      float64(1757930000123),
	} {
		t.Run(name, func(t *testing.T) {
			data := serviceEventToProtoData(kafka.ServiceEvent{
				EventType:       "incident_updated",
				TenantID:        "tenant-1",
				SourceRegion:    "eu-west",
				SourceClusterID: "cluster-eu",
				Data: map[string]interface{}{
					"incident_id":   "incident-1",
					"tenant_id":     "tenant-1",
					"cluster_id":    "cluster-eu",
					"status":        "resolved",
					"severity":      "critical",
					"title":         "EdgeDown",
					"change":        "resolved",
					"updated_at_ms": updatedAt,
				},
			}, logging.NewLogger())
			incident := data.GetIncidentUpdated()
			if incident == nil {
				t.Fatalf("incident update was not mapped: %+v", data)
			}
			if incident.GetIncidentId() != "incident-1" || incident.GetTenantId() != "tenant-1" ||
				incident.GetClusterId() != "cluster-eu" || incident.GetStatus() != "resolved" ||
				incident.GetSeverity() != "critical" || incident.GetTitle() != "EdgeDown" ||
				incident.GetChange() != "resolved" || incident.GetUpdatedAtMs() != 1757930000123 {
				t.Fatalf("unexpected incident payload: %+v", incident)
			}
			if data.GetSourceRegion() != "eu-west" || data.GetSourceClusterId() != "cluster-eu" {
				t.Fatalf("incident topology envelope was lost: %+v", data)
			}
		})
	}
}

func TestIncidentUpdatedWithoutIncidentIDIsRejected(t *testing.T) {
	data := serviceEventToProtoData(kafka.ServiceEvent{
		EventType: "incident_updated",
		TenantID:  "tenant-1",
		Data:      map[string]interface{}{"status": "firing"},
	}, logging.NewLogger())
	if data != nil {
		t.Fatalf("incident update without incident_id mapped to %+v, want nil", data)
	}
}

type recordedBroadcast struct {
	audience  string
	channel   signalmanpb.Channel
	eventType signalmanpb.EventType
}

type recordingHub struct{ sent []recordedBroadcast }

func (h *recordingHub) BroadcastToTenant(tenantID string, eventType signalmanpb.EventType, channel signalmanpb.Channel, _ *signalmanpb.EventData) {
	h.sent = append(h.sent, recordedBroadcast{audience: "tenant:" + tenantID, channel: channel, eventType: eventType})
}

func (h *recordingHub) BroadcastInfrastructure(eventType signalmanpb.EventType, _ *signalmanpb.EventData) {
	h.sent = append(h.sent, recordedBroadcast{audience: "infrastructure", channel: signalmanpb.Channel_CHANNEL_SYSTEM, eventType: eventType})
}

func (h *recordingHub) BroadcastPlatform(eventType signalmanpb.EventType, _ *signalmanpb.EventData) {
	h.sent = append(h.sent, recordedBroadcast{audience: "platform", channel: signalmanpb.Channel_CHANNEL_PLATFORM, eventType: eventType})
}

// Incident changes reach tenants only as tenant broadcasts and operators only
// through the platform audience; a tenantless incident change never becomes a
// system broadcast that every tenant subscriber would receive.
func TestIncidentEventsRouteToTheirAudience(t *testing.T) {
	cases := []struct {
		name     string
		rawType  string
		tenantID string
		want     []recordedBroadcast
	}{
		{"tenant incident change", "incident_updated", "tenant-1", []recordedBroadcast{{"tenant:tenant-1", signalmanpb.Channel_CHANNEL_SYSTEM, signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED}}},
		{"tenantless tenant incident change", "incident_updated", "", nil},
		{"operator incident change", "platform_incident_updated", "", []recordedBroadcast{{"platform", signalmanpb.Channel_CHANNEL_PLATFORM, signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED}}},
		{"operator incident change naming a tenant", "platform_incident_updated", "tenant-1", []recordedBroadcast{{"platform", signalmanpb.Channel_CHANNEL_PLATFORM, signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED}}},
		{"infrastructure node change", "node_lifecycle_update", "", []recordedBroadcast{{"infrastructure", signalmanpb.Channel_CHANNEL_SYSTEM, signalmanpb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE}}},
		{"tenantless stream change", "stream_updated", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &recordingHub{}
			routeEvent(hub, tc.rawType, tc.tenantID, mapEventTypeToChannel(tc.rawType), mapEventTypeToProto(tc.rawType), &signalmanpb.EventData{}, logging.NewLogger())
			if len(hub.sent) != len(tc.want) {
				t.Fatalf("broadcasts = %+v, want %+v", hub.sent, tc.want)
			}
			for i := range tc.want {
				if hub.sent[i] != tc.want[i] {
					t.Fatalf("broadcasts = %+v, want %+v", hub.sent, tc.want)
				}
			}
		})
	}
}

func TestPlatformIncidentUpdatedServiceEventToProtoData(t *testing.T) {
	data := serviceEventToProtoData(kafka.ServiceEvent{
		EventType: "platform_incident_updated",
		Data: map[string]interface{}{
			"incident_id": "incident-1",
			"status":      "firing",
			"change":      "opened",
		},
	}, logging.NewLogger())
	incident := data.GetIncidentUpdated()
	if incident == nil || incident.GetIncidentId() != "incident-1" || incident.GetTenantId() != "" || incident.GetChange() != "opened" {
		t.Fatalf("platform incident update mapped to %+v", data)
	}
}

func stringPtr(value string) *string {
	return &value
}
