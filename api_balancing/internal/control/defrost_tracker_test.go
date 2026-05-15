package control

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
)

func resetDefrostTracker(t *testing.T) {
	t.Helper()
	globalDefrostTracker.mu.Lock()
	globalDefrostTracker.counts = make(map[string]int)
	globalDefrostTracker.lastBootstrap = time.Time{}
	globalDefrostTracker.mu.Unlock()

	retryGuardMu.Lock()
	retryGuard = make(map[string]retryGuardEntry)
	retryGuardMu.Unlock()
}

func TestDefrostTracker_IncrementAndDecrement(t *testing.T) {
	resetDefrostTracker(t)
	IncrementDefrost("n1")
	IncrementDefrost("n1")
	IncrementDefrost("n2")
	counts := ActiveDefrostCount()
	if counts["n1"] != 2 {
		t.Fatalf("expected n1=2, got %d", counts["n1"])
	}
	if counts["n2"] != 1 {
		t.Fatalf("expected n2=1, got %d", counts["n2"])
	}
	DecrementDefrost("n1")
	DecrementDefrost("n1")
	DecrementDefrost("n1") // bounded at 0; should not go negative
	counts = ActiveDefrostCount()
	if _, ok := counts["n1"]; ok {
		t.Fatalf("expected n1 absent after decrements, got %+v", counts)
	}
}

func TestRetryGuard_FirstCallConsumesWindow(t *testing.T) {
	resetDefrostTracker(t)
	if !TryConsumeRetryGuard("artifactA", time.Second) {
		t.Fatal("first call must consume the guard")
	}
	if TryConsumeRetryGuard("artifactA", time.Second) {
		t.Fatal("second call within window must be blocked")
	}
	if !TryConsumeRetryGuard("artifactB", time.Second) {
		t.Fatal("different artifact must not be blocked")
	}
}

func TestRetryGuard_ExpiryAllowsRetry(t *testing.T) {
	resetDefrostTracker(t)
	if !TryConsumeRetryGuard("artifactC", time.Millisecond) {
		t.Fatal("first call must consume the guard")
	}
	time.Sleep(5 * time.Millisecond)
	if !TryConsumeRetryGuard("artifactC", time.Millisecond) {
		t.Fatal("post-expiry retry must be allowed")
	}
}

func TestPickDefrostNode_SpreadsByActiveCount(t *testing.T) {
	resetDefrostTracker(t)
	original := loadBalancerInstance
	defer func() { loadBalancerInstance = original }()

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-busy": {NodeID: "node-busy", CapStorage: true, IsHealthy: true},
			"node-idle": {NodeID: "node-idle", CapStorage: true, IsHealthy: true},
		},
	}
	IncrementDefrost("node-busy")
	IncrementDefrost("node-busy")
	IncrementDefrost("node-idle")

	got, err := PickDefrostNode(0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "node-idle" {
		t.Fatalf("expected node-idle (fewer in-flight), got %q", got)
	}
}

func TestPickDefrostNode_ExcludesFailedNode(t *testing.T) {
	resetDefrostTracker(t)
	original := loadBalancerInstance
	defer func() { loadBalancerInstance = original }()

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-a": {NodeID: "node-a", CapStorage: true, IsHealthy: true},
			"node-b": {NodeID: "node-b", CapStorage: true, IsHealthy: true},
		},
	}
	got, err := PickDefrostNode(0, 0, map[string]struct{}{"node-a": {}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "node-b" {
		t.Fatalf("expected node-b (a excluded), got %q", got)
	}
}

func TestPickDefrostNode_DiskUsageTieBreaks(t *testing.T) {
	resetDefrostTracker(t)
	original := loadBalancerInstance
	defer func() { loadBalancerInstance = original }()

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-emptier": {
				NodeID: "node-emptier", CapStorage: true, IsHealthy: true,
				DiskUsedBytes: 10, DiskTotalBytes: 100, // 10%
			},
			"node-fuller": {
				NodeID: "node-fuller", CapStorage: true, IsHealthy: true,
				DiskUsedBytes: 80, DiskTotalBytes: 100, // 80%
			},
		},
	}
	// Both have zero active defrosts and no geo: disk usage is the tie-breaker.
	got, err := PickDefrostNode(0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "node-emptier" {
		t.Fatalf("expected node-emptier (lower usage), got %q", got)
	}
}

func TestPickDefrostNode_SkipsUnhealthyAndStale(t *testing.T) {
	resetDefrostTracker(t)
	original := loadBalancerInstance
	defer func() { loadBalancerInstance = original }()

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-unhealthy": {NodeID: "node-unhealthy", CapStorage: true, IsHealthy: false},
			"node-stale":     {NodeID: "node-stale", CapStorage: true, IsHealthy: true, IsStale: true},
			"node-nostorage": {NodeID: "node-nostorage", CapStorage: false, IsHealthy: true},
			"node-good":      {NodeID: "node-good", CapStorage: true, IsHealthy: true},
		},
	}
	got, err := PickDefrostNode(0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "node-good" {
		t.Fatalf("expected node-good, got %q", got)
	}
}

func TestPickDefrostNode_NoEligibleReturnsError(t *testing.T) {
	resetDefrostTracker(t)
	original := loadBalancerInstance
	defer func() { loadBalancerInstance = original }()

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-bad": {NodeID: "node-bad", CapStorage: false, IsHealthy: true},
		},
	}
	if _, err := PickDefrostNode(0, 0, nil); err == nil {
		t.Fatal("expected error when no eligible node available")
	}
}
