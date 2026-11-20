package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func applyViewerSnapshot(t *testing.T, sm *StreamStateManager, fence, seq int64, viewers int) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"node_id": "node-1", "fence": fence, "seq": seq, "streams": map[string]int{"stream-1": viewers}})
	if err != nil {
		t.Fatal(err)
	}
	sm.applyRedisChange(StateChange{Entity: StateEntity("node_stream_counts"), Operation: StateOpUpsert, NodeID: "node-1", Payload: payload})
}

func TestNodeCountsReplaceLeakedViewersAndFenceOldOwners(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.TouchNode("node-1", true)
	for range 50 {
		sm.EnsureViewerStreamIdentity("stream-1", "node-1", "tenant-1")
	}
	sm.mu.Lock()
	sm.streamInstances["stream-1"]["node-1"].Viewers = 50
	sm.streamInstances["stream-1"]["node-1"].TotalConnections = 3
	sm.mu.Unlock()
	applyViewerSnapshot(t, sm, 5, 10, 2)
	got := sm.GetStreamState("stream-1")
	if got.Viewers != 2 || got.TotalConnections != 3 {
		t.Fatalf("snapshot after 50 starts = viewers %d total %d, want 2/3", got.Viewers, got.TotalConnections)
	}
	sm.applyRedisChange(StateChange{Entity: StateEntityStreamInstance, Operation: StateOpUpsert, StreamName: "stream-1", NodeID: "node-1", Payload: json.RawMessage(`{"node_id":"node-1","viewers":50,"total_connections":3}`)})
	applyViewerSnapshot(t, sm, 4, 99, 50)
	if got := sm.GetStreamState("stream-1"); got.Viewers != 2 {
		t.Errorf("stale owner or instance restored %d viewers", got.Viewers)
	}
	applyViewerSnapshot(t, sm, 5, 9, 51)
	if got := sm.GetNodeActiveViewers("node-1"); got != 2 {
		t.Errorf("out-of-order snapshot restored %d viewers", got)
	}
	applyViewerSnapshot(t, sm, 6, 1, 1)
	if got := sm.GetNodeActiveViewers("node-1"); got != 1 {
		t.Errorf("new control owner reports %d viewers", got)
	}
}

func TestNodeCountsRehydrateDiscardsLegacyInstanceTotals(t *testing.T) {
	store, _, client := newRedisStateStore(t)
	if err := store.SetNode("node-1", &NodeState{NodeID: "node-1", IsHealthy: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStream("stream-1", &StreamState{InternalName: "stream-1"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(context.Background(), store.keyStreamInstance("stream-1", "node-1"), `{"node_id":"node-1","viewers":50,"total_connections":3}`, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.setJSON(context.Background(), store.keyNodeStreamCounts("node-1"), NodeStreamCountsSnapshot{NodeID: "node-1", Fence: 5, Seq: 10, Streams: map[string]uint32{"stream-1": 2}}); err != nil {
		t.Fatal(err)
	}
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	if err := sm.rehydrateFromRedis(store); err != nil {
		t.Fatal(err)
	}
	if got := sm.GetStreamState("stream-1"); got.Viewers != 2 || got.TotalConnections != 3 {
		t.Fatalf("rehydrated audience/total %d/%d, want 2/3", got.Viewers, got.TotalConnections)
	}
	applyViewerSnapshot(t, sm, 4, 99, 50)
	if got := sm.GetNodeActiveViewers("node-1"); got != 2 {
		t.Fatalf("rehydration lost fence: %d viewers", got)
	}
}

func TestNodeCountsExpireAndDoNotRideInstanceSnapshots(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.TouchNode("node-1", true)
	sm.EnsureViewerStreamIdentity("stream-1", "node-1", "tenant-1")
	applyViewerSnapshot(t, sm, 5, 10, 2)
	raw, err := json.Marshal(sm.streamInstances["stream-1"]["node-1"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["viewers"]; ok {
		t.Error("unfenced instance snapshot still carries audience")
	}
	census := sm.nodeStreamCounts["node-1"]
	census.appliedAt = time.Now().Add(-31 * time.Second)
	sm.nodeStreamCounts["node-1"] = census
	if got := sm.GetStreamState("stream-1"); got.Viewers != 0 {
		t.Errorf("stale stream census retained %d viewers", got.Viewers)
	}
	if got := sm.GetNodeActiveViewers("node-1"); got != 0 {
		t.Errorf("stale node census retained %d viewers", got)
	}
}

func TestNodeCountsAbsentStreamClearsAudience(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.TouchNode("node-1", true)
	sm.UpdateNodeStats("stream-1", "node-1", 3, 1, 0, 0, false)
	applyViewerSnapshot(t, sm, 5, 10, 2)
	sm.ReconcileNodeStreamPresence("node-1", map[string]struct{}{})
	if got := sm.GetNodeActiveViewers("node-1"); got != 0 {
		t.Errorf("missing stream retained %d viewers", got)
	}
	if got := sm.GetStreamState("stream-1"); got.Viewers != 0 {
		t.Errorf("offline stream retained %d viewers", got.Viewers)
	}
}

func TestPeerCensusCannotInterleaveWithOlderPresenceWrites(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.TouchNode("node-1", true)
	sm.UpdateNodeStats("stream-1", "node-1", 3, 1, 0, 0, false)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- sm.ApplyNodeStreamReport(context.Background(), NodeStreamCountsSnapshot{NodeID: "node-1", Fence: 5, Seq: 10, Streams: map[string]uint32{"stream-1": 50}}, func() {
			close(entered)
			<-release
			sm.ReconcileNodeStreamPresence("node-1", map[string]struct{}{})
		})
	}()
	<-entered
	peerDone := make(chan struct{})
	peerPayload := json.RawMessage(`{"node_id":"node-1","fence":6,"seq":1,"streams":{"stream-1":2}}`)
	go func() {
		sm.applyRedisChange(StateChange{Entity: StateEntityNodeStreamCounts, Operation: StateOpUpsert, NodeID: "node-1", Payload: peerPayload})
		close(peerDone)
	}()
	select {
	case <-peerDone:
		t.Error("new peer census interleaved with an older report's presence writes")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-peerDone
	if got := sm.GetNodeActiveViewers("node-1"); got != 2 {
		t.Fatalf("old presence cleared newer peer census: %d", got)
	}
}
