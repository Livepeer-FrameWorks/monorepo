package handlers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/analyticsevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Every analytics_events type Decklog can publish has an ingest handler, so
// no emitted event reaches the "Unknown event type" branch. A payload with no
// consumer must instead be declared in analyticsevents.UnroutedTriggerPayloads,
// which Decklog refuses.
func TestEveryDecklogEventTypeHasAnIngestHandler(t *testing.T) {
	oneof := (&ipcpb.MistTrigger{}).ProtoReflect().Descriptor().Oneofs().ByName("trigger_payload")
	fields := oneof.Fields()
	// The trigger type selects between the two restream status event types.
	triggerTypes := []string{"", string(mist.TriggerRestreamStatusFinal)}
payloads:
	for i := range fields.Len() {
		fd := fields.Get(i)
		for _, triggerType := range triggerTypes {
			trigger := &ipcpb.MistTrigger{TriggerType: triggerType}
			m := trigger.ProtoReflect()
			m.Set(fd, protoreflect.ValueOfMessage(m.NewField(fd).Message()))
			eventType, routed := analyticsevents.TriggerEventType(trigger)
			if !routed {
				if _, declared := analyticsevents.UnroutedTriggerPayloads[string(fd.Name())]; !declared {
					t.Errorf("MistTrigger payload %s is neither routed nor declared unrouted", fd.Name())
				}
				continue payloads
			}
			if _, ok := analyticsEventHandlers[eventType]; !ok {
				t.Errorf("Decklog publishes MistTrigger payload %s as %q, which ingest has no handler for", fd.Name(), eventType)
				continue payloads
			}
		}
	}

	gatewayFields := (&ipcpb.GatewayTelemetryEvent{}).ProtoReflect().Descriptor().Oneofs().ByName("payload").Fields()
	for i := range gatewayFields.Len() {
		fd := gatewayFields.Get(i)
		event := &ipcpb.GatewayTelemetryEvent{}
		m := event.ProtoReflect()
		m.Set(fd, protoreflect.ValueOfMessage(m.NewField(fd).Message()))
		eventType, ok := analyticsevents.GatewayTelemetryEventType(event)
		if !ok {
			t.Errorf("gateway telemetry payload %s has no analytics_events type", fd.Name())
			continue
		}
		if _, handled := analyticsEventHandlers[eventType]; !handled {
			t.Errorf("Decklog publishes gateway telemetry payload %s as %q, which ingest has no handler for", fd.Name(), eventType)
		}
	}
}
