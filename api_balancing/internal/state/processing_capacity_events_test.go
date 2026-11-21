package state

import (
	"testing"
	"time"
)

func TestProcessingCapacityEventsWakeForNewlyAvailableWork(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	count := 0
	sm.SetProcessingCapacityHandler(func(_ string) { count++ })
	sm.TouchNode("edge", true)
	budget := uint64(1024)
	metrics := NodeMetrics{CapProcessing: true, ProcessingClasses: map[string]ClassCapacity{"video_transcode": {Total: 2, Used: 2}}}
	sm.updateNodeMetrics("edge", metrics, &budget)
	if count != 2 {
		t.Fatalf("registration and first budget did not wake jobs: %d events want 2", count)
	}
	sm.updateNodeMetrics("edge", metrics, &budget)
	if count != 2 {
		t.Fatalf("unchanged metrics woke waiting jobs: %d", count)
	}
	budget = 2048
	sm.updateNodeMetrics("edge", metrics, &budget)
	if count != 3 {
		t.Fatalf("higher reported budget did not wake jobs: %d", count)
	}
	metrics.ProcessingClasses = map[string]ClassCapacity{"video_transcode": {Total: 2, Used: 1}}
	sm.updateNodeMetrics("edge", metrics, &budget)
	if count != 4 {
		t.Fatalf("released processing slot did not wake jobs: %d", count)
	}
	if err := sm.SetNodeOperationalMode(t.Context(), "edge", NodeModeDraining, "operator"); err != nil {
		t.Fatal(err)
	}
	if err := sm.SetNodeOperationalMode(t.Context(), "edge", NodeModeNormal, "operator"); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("undrain did not wake jobs: %d", count)
	}
	sm.TouchNode("edge", false)
	sm.TouchNode("edge", true)
	if count != 6 {
		t.Fatalf("node recovery did not wake jobs: %d", count)
	}
}

func TestReplicatedProcessingBudgetKeepsLatestMetricsObservation(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.TouchNode("edge", true)
	budget := uint64(2048)
	sm.updateNodeMetrics("edge", NodeMetrics{CapProcessing: true}, &budget)
	incoming := sm.GetNodeState("edge")
	incoming.MetricsObservedAt = incoming.MetricsObservedAt.Add(-time.Second)
	oldBudget := uint64(512)
	incoming.ProcessingMemoryBudgetBytes = &oldBudget
	sm.applyRedisChange(StateChange{Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "edge", Payload: marshalNodeState(incoming)})
	if current := sm.GetNodeState("edge").ProcessingMemoryBudgetBytes; current == nil || *current != 2048 {
		t.Fatalf("older peer snapshot overwrote latest processing budget: %v", current)
	}
	count := 0
	sm.SetProcessingCapacityHandler(func(_ string) { count++ })
	incoming = sm.GetNodeState("edge")
	incoming.MetricsObservedAt = incoming.MetricsObservedAt.Add(time.Second)
	larger := uint64(4096)
	incoming.ProcessingMemoryBudgetBytes = &larger
	sm.applyRedisChange(StateChange{Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "edge", Payload: marshalNodeState(incoming)})
	if count != 1 {
		t.Fatalf("replicated larger budget did not wake waiting jobs: events=%d", count)
	}
	incoming = sm.GetNodeState("edge")
	sm.applyRedisChange(StateChange{Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "edge", Payload: marshalNodeState(incoming)})
	if count != 1 {
		t.Fatalf("unchanged replicated heartbeat woke waiting jobs: events=%d", count)
	}
}

func TestLocalProcessingCapacityExpansionWakesWaitingJobs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after map[string]ClassCapacity
	}{
		{"larger class", map[string]ClassCapacity{"video_transcode": {Total: 2, Used: 2}}, map[string]ClassCapacity{"video_transcode": {Total: 3, Used: 2}}},
		{"new class", map[string]ClassCapacity{}, map[string]ClassCapacity{"cpu_heavy": {Total: 1}}},
		{"unlimited class", map[string]ClassCapacity{"video_transcode": {Total: 2, Used: 2}}, map[string]ClassCapacity{"video_transcode": {Total: 0, Used: 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewStreamStateManager()
			defer sm.Shutdown()
			sm.UpdateNodeMetrics("edge", NodeMetrics{CapProcessing: true, ProcessingClasses: tc.before})
			count := 0
			sm.SetProcessingCapacityHandler(func(string) { count++ })
			next := NodeMetrics{CapProcessing: true, ProcessingClasses: tc.after}
			sm.UpdateNodeMetrics("edge", next)
			if count != 1 {
				t.Fatalf("additional processing capacity did not wake jobs: events=%d", count)
			}
			sm.UpdateNodeMetrics("edge", next)
			if count != 1 {
				t.Fatalf("unchanged capacity woke jobs: events=%d", count)
			}
		})
	}
}
