package state

import (
	"context"
	"testing"
)

// normalizeNodeOperationalMode is the admission validator for node modes: empty
// normalizes to "normal", the three real modes pass through, and anything else is
// rejected — a corrupt mode must never be accepted and silently alter routing.
func TestNormalizeNodeOperationalMode(t *testing.T) {
	if m, err := normalizeNodeOperationalMode(""); err != nil || m != NodeModeNormal {
		t.Fatalf("empty -> (%q,%v), want (normal,nil)", m, err)
	}
	for _, mode := range []NodeOperationalMode{NodeModeNormal, NodeModeDraining, NodeModeMaintenance} {
		if m, err := normalizeNodeOperationalMode(mode); err != nil || m != mode {
			t.Fatalf("%q -> (%q,%v), want passthrough", mode, m, err)
		}
	}
	if _, err := normalizeNodeOperationalMode("turbo"); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}

// SetNodeOperationalMode persists a validated mode in memory; GetNodeOperationalMode
// reads it back, defaulting to normal for an unset/unknown node. An invalid mode
// is rejected without mutating state.
func TestSetGetNodeOperationalMode(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	ctx := context.Background()
	sm.SetNodeInfo("n1", "https://1.2.3.4", true, nil, nil, "", "", nil)

	// Unknown / unset node defaults to normal.
	if got := sm.GetNodeOperationalMode("ghost"); got != NodeModeNormal {
		t.Fatalf("unknown node mode = %q, want normal", got)
	}

	if err := sm.SetNodeOperationalMode(ctx, "n1", NodeModeMaintenance, "test"); err != nil {
		t.Fatalf("set maintenance: %v", err)
	}
	if got := sm.GetNodeOperationalMode("n1"); got != NodeModeMaintenance {
		t.Fatalf("mode = %q, want maintenance", got)
	}

	if err := sm.SetNodeOperationalMode(ctx, "n1", "turbo", "test"); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
	// State unchanged after the rejected set.
	if got := sm.GetNodeOperationalMode("n1"); got != NodeModeMaintenance {
		t.Fatalf("mode after invalid set = %q, want maintenance (unchanged)", got)
	}
}

// GetBalancerSnapshotAtomicWithOptions controls whether stale (not-recently-seen)
// nodes appear: a freshly-touched healthy node is always present; the includeStale
// flag governs whether a stale node is surfaced (debug views) or hidden (routing).
func TestGetBalancerSnapshotAtomicWithOptions(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("fresh", "https://1.2.3.4", true, nil, nil, "", "", nil)
	sm.TouchNode("fresh", true)

	excl := sm.GetBalancerSnapshotAtomicWithOptions(false)
	if excl == nil {
		t.Fatal("snapshot should be non-nil")
	}
	found := false
	for _, n := range excl.Nodes {
		if n.NodeID == "fresh" {
			found = true
		}
	}
	if !found {
		t.Fatal("a fresh healthy node must appear in the routing snapshot")
	}

	// includeStale=true is a superset of includeStale=false.
	all := sm.GetBalancerSnapshotAtomicWithOptions(true)
	if len(all.Nodes) < len(excl.Nodes) {
		t.Fatalf("includeStale=true (%d) must be >= includeStale=false (%d)", len(all.Nodes), len(excl.Nodes))
	}
}
