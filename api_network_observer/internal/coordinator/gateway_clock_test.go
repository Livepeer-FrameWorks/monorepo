package coordinator

import (
	"frameworks/api_network_observer/internal/store"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"testing"
	"time"
)

func TestGatewayFutureClockCannotMakeObserverPermanentlyHealthy(t *testing.T) {
	a := &API{}
	s := store.NewState()
	event := kafka.AnalyticsEvent{EventType: "orchestrator_observer_report", Timestamp: time.Now().Add(24 * time.Hour), Data: map[string]any{"producer_kind": "gateway", "gateway_id": "site", "report_kind": "heartbeat", "healthy": true}}
	if err := a.applyGateway(s, event); err == nil {
		t.Fatal("future gateway heartbeat admitted into current health")
	}
	if len(s.Observers) != 0 {
		t.Fatal("invalid clock mutated observer state before rejection")
	}
}
