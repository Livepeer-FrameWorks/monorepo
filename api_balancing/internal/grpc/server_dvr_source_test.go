package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
)

func TestWaitForStreamSourceWithHintUsesHealthyTriggerNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-node-1", true)

	nodeID, baseURL, ok := waitForStreamSourceWithHint(context.Background(), "stream-not-yet-in-lifecycle", "edge-node-1", time.Millisecond)
	if !ok {
		t.Fatal("expected source-node hint to be accepted")
	}
	if nodeID != "edge-node-1" {
		t.Fatalf("expected edge-node-1, got %q", nodeID)
	}
	if baseURL != "http://edge.example/view" {
		t.Fatalf("expected hinted base URL, got %q", baseURL)
	}
}

func TestWaitForStreamSourceWithHintFallsBackToObservedSource(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeInfo("edge-node-2", "http://edge2.example/view", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-node-2", true)
	sm.UpdateNodeStats("demo_stream", "edge-node-2", 1, 1, 0, 0, false)

	nodeID, baseURL, ok := waitForStreamSourceWithHint(context.Background(), "demo_stream", "missing-node", time.Millisecond)
	if !ok {
		t.Fatal("expected observed stream source fallback")
	}
	if nodeID != "edge-node-2" {
		t.Fatalf("expected edge-node-2, got %q", nodeID)
	}
	if baseURL != "http://edge2.example/view" {
		t.Fatalf("expected observed base URL, got %q", baseURL)
	}
}

func TestDVRSourceStreamNameKeepsLivePrefixWhenObservedStateIsBare(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.UpdateStreamFromBuffer("demo_stream", "demo_stream", "edge-node-1", "tenant-1", "FULL", "")

	got := dvrSourceStreamName("demo_stream")
	if got != "live+demo_stream" {
		t.Fatalf("dvrSourceStreamName() = %q, want live+demo_stream", got)
	}
}

func TestDVRSourceStreamNamePreservesConcretePrefixedObservation(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.UpdateStreamFromBuffer("pull+demo_stream", "demo_stream", "edge-node-1", "tenant-1", "FULL", "")

	got := dvrSourceStreamName("demo_stream")
	if got != "pull+demo_stream" {
		t.Fatalf("dvrSourceStreamName() = %q, want pull+demo_stream", got)
	}

	if fallback := control.MistSourceNameForIngestMode("demo_stream", "push"); fallback != "live+demo_stream" {
		t.Fatalf("fallback source = %q", fallback)
	}
}

func TestClipLiveSourceStreamNamePreservesConcreteBareObservation(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.UpdateStreamFromBuffer("demo_stream", "demo_stream", "edge-node-1", "tenant-1", "FULL", "")

	got := clipLiveSourceStreamName("demo_stream")
	if got != "demo_stream" {
		t.Fatalf("clipLiveSourceStreamName() = %q, want demo_stream", got)
	}
}

func TestClipLiveSourceStreamNameFallsBackToLiveWildcard(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	got := clipLiveSourceStreamName("demo_stream")
	if got != "live+demo_stream" {
		t.Fatalf("clipLiveSourceStreamName() = %q, want live+demo_stream", got)
	}
}
