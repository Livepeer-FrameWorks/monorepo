package analyticsevents

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TriggerWithPayload returns a MistTrigger whose oneof is set to an empty
// message of the given field.
func triggerWithPayload(fd protoreflect.FieldDescriptor) *ipcpb.MistTrigger {
	trigger := &ipcpb.MistTrigger{}
	m := trigger.ProtoReflect()
	m.Set(fd, protoreflect.ValueOfMessage(m.NewField(fd).Message()))
	return trigger
}

// Every MistTrigger payload is either routed to an analytics_events type or
// declared unrouted with a reason, so a new payload cannot reach Decklog
// without a decision about its consumer.
func TestEveryTriggerPayloadIsClassified(t *testing.T) {
	oneof := (&ipcpb.MistTrigger{}).ProtoReflect().Descriptor().Oneofs().ByName("trigger_payload")
	if oneof == nil {
		t.Fatal("MistTrigger has no trigger_payload oneof")
	}
	fields := oneof.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		name := string(fd.Name())
		eventType, routed := TriggerEventType(triggerWithPayload(fd))
		reason, unrouted := UnroutedTriggerPayloads[name]
		switch {
		case routed && unrouted:
			t.Errorf("%s is routed as %q and also declared unrouted (%s)", name, eventType, reason)
		case !routed && !unrouted:
			t.Errorf("%s is neither routed to an analytics_events type nor declared in UnroutedTriggerPayloads", name)
		case routed && eventType == "":
			t.Errorf("%s is routed with an empty event type", name)
		case unrouted && reason == "":
			t.Errorf("%s is declared unrouted without a reason", name)
		}
	}
	for name := range UnroutedTriggerPayloads {
		if fields.ByName(protoreflect.Name(name)) == nil {
			t.Errorf("UnroutedTriggerPayloads names %q, which is not a MistTrigger payload", name)
		}
	}
}

func TestEveryGatewayTelemetryPayloadIsRouted(t *testing.T) {
	oneof := (&ipcpb.GatewayTelemetryEvent{}).ProtoReflect().Descriptor().Oneofs().ByName("payload")
	fields := oneof.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		event := &ipcpb.GatewayTelemetryEvent{}
		m := event.ProtoReflect()
		m.Set(fd, protoreflect.ValueOfMessage(m.NewField(fd).Message()))
		if eventType, ok := GatewayTelemetryEventType(event); !ok || eventType == "" {
			t.Errorf("gateway telemetry payload %s has no analytics_events type", fd.Name())
		}
	}
}
