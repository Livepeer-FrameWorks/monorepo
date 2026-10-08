package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
)

func TestProberEventsDoNotBecomeGatewayObservations(t *testing.T) {
	for _, kind := range []string{"orchestrator_discovery_observed", "orchestrator_state_update", "orchestrator_observer_report"} {
		event := kafka.AnalyticsEvent{EventType: kind, Timestamp: time.Now(), Data: map[string]any{"producer_kind": "prober"}}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		// A prober event must be rejected before touching gateway inventory storage.
		if err := (&API{}).Gateway(context.Background(), kafka.Message{Value: raw}); err != nil {
			t.Fatal(err)
		}
	}
}
