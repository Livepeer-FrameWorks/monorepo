package main

import (
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSkipperInvestigationMapping(t *testing.T) {
	channel := mapEventTypeToChannel("skipper_investigation")
	if channel != pb.Channel_CHANNEL_AI {
		t.Fatalf("expected CHANNEL_AI, got %v", channel)
	}

	eventType := mapEventTypeToProto("skipper_investigation")
	if eventType != pb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION {
		t.Fatalf("expected EVENT_TYPE_SKIPPER_INVESTIGATION, got %v", eventType)
	}
}

func TestClientLifecycleBatchMapping(t *testing.T) {
	eventType := mapEventTypeToProto("client_lifecycle_batch")
	if eventType != pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE {
		t.Fatalf("expected EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE, got %v", eventType)
	}
}

func TestClientLifecycleBatchToProtoDataExpandsSamples(t *testing.T) {
	streamID := "stream-1"
	trigger := &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_ClientLifecycleBatch{
			ClientLifecycleBatch: &pb.ClientLifecycleBatch{
				StreamId: &streamID,
				Samples: []*pb.ClientLifecycleUpdate{
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
}

func stringPtr(value string) *string {
	return &value
}
