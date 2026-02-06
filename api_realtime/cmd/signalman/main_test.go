package main

import (
	"testing"

	pb "frameworks/pkg/proto"
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
