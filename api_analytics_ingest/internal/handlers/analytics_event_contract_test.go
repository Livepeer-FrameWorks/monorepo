package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/analyticsevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestV3GatewayTelemetryUsesReplaySafeProjection(t *testing.T) {
	for _, eventType := range []string{
		"orchestrator_discovery_observed", "orchestrator_state_update",
		"orchestrator_transcode_outcome", "orchestrator_candidate_decision",
		"orchestrator_segment_result", "orchestrator_observer_report",
	} {
		t.Run(eventType, func(t *testing.T) {
			handle, ok := analyticsEventHandlers[eventType]
			if !ok {
				t.Fatal("missing network observation handler")
			}
			// Replay-safe facts reject a missing producer ID before any database write.
			err := handle(&AnalyticsHandler{}, context.Background(), kafka.AnalyticsEvent{
				EventType: eventType, SchemaVersion: 3,
			})
			if err == nil || !strings.Contains(err.Error(), "requires stable event ID") {
				t.Fatalf("V3 payload bypassed replay-safe projection: %v", err)
			}
		})
	}
}

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
