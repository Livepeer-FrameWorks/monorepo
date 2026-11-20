package triggers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
)

func TestOfflinePublisherDrainKeepsRetiredBindingAfterReconnect(t *testing.T) {
	reg := installRegistryForTest(t)
	const internal = "publisher-drain-reconnect"
	projectSourceForTest(t, reg, internal, "new-node", 10, "new-trigger", "new-generation", 20)
	p := minimalProcessorForStreamEnd(t)
	unavailable := errors.New("old node disconnected")
	calls := 0
	p.drainStream = func(_ context.Context, node, runtime, reason, generation, prior, tenantID string) error {
		calls++
		if node != "old-node" || runtime != "live+"+internal || generation != "old-generation" || prior != "old-generation" {
			t.Errorf("retired binding changed: node=%q runtime=%q generation=%q prior=%q", node, runtime, generation, prior)
		}
		return unavailable
	}
	effect := control.OfflineEffect{InternalName: internal, NodeID: "old-node", SourceGeneration: "old-generation", SourceRevision: 19, DrainPublisher: true, DrainRuntimeName: "live+" + internal, StateSuperseded: true}
	for i := 0; i < 2; i++ {
		_, err := p.ApplyOfflineEffect(context.Background(), effect)
		if !errors.Is(err, unavailable) {
			t.Fatalf("lost retryable publisher drain: calls=%d err=%v", calls, err)
		}
	}
	if calls != 2 {
		t.Fatalf("publisher stop dispatches=%d want 2", calls)
	}
	generation, active, found := reg.SourceGenerationSnapshot(internal, "new-node")
	if !found || !active || generation != "new-generation" {
		t.Fatalf("retired drain changed replacement: generation=%q active=%v found=%v", generation, active, found)
	}
}
