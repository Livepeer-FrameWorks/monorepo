package control

import (
	"testing"

	"frameworks/api_balancing/internal/state"
)

func TestGetStreamSourceDoesNotPromoteReplicaThroughBufferFallback(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	previousDB := db
	db = nil
	t.Cleanup(func() { db = previousDB })
	sm.SetNodeInfo("edge", "http://edge:8080", true, nil, nil, "", "", nil)
	if err := sm.UpdateStreamFromBuffer("live+stream", "stream", "edge", "tenant", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	sm.UpdateNodeStats("stream", "edge", 1, 0, 0, 0, true)
	if node, _, ok := GetStreamSource("stream"); ok {
		t.Fatalf("replica selected as recording source: %s", node)
	}
	sm.UpdateNodeStats("stream", "edge", 0, 0, 0, 0, false)
	if node, _, ok := GetStreamSource("stream"); !ok || node != "edge" {
		t.Fatalf("early buffer source rejected: node=%s ok=%v", node, ok)
	}
	sm.UpdateNodeStats("stream", "edge", 1, 0, 0, 0, false)
	if node, _, ok := GetStreamSource("stream"); !ok || node != "edge" {
		t.Fatalf("real source rejected: node=%s ok=%v", node, ok)
	}
}
