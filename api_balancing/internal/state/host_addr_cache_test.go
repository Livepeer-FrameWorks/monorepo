package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Staging: an intermittent Livepeer auth node_mismatch for a known edge. The
// edge's FQDN was resolved on every request inside a shared 500 ms budget.
// Resolved addresses are now cached, and a failed refresh keeps the last ones.
func TestNodeIDByClientIPUsesCachedHostAddresses(t *testing.T) {
	calls := 0
	failing := false
	previous := nodeHostAddrs
	nodeHostAddrs = &hostAddrCache{
		ttl:     time.Minute,
		timeout: time.Second,
		entries: map[string]hostAddrEntry{},
		lookup: func(context.Context, string) ([]string, error) {
			calls++
			if failing {
				return nil, errors.New("dns timeout")
			}
			return []string{"192.0.2.24"}, nil
		},
	}
	t.Cleanup(func() { nodeHostAddrs = previous })

	sm := NewStreamStateManager()
	sm.nodes["edge-us"] = &NodeState{NodeID: "edge-us", BaseURL: "https://edge-us.example.test"}

	if got := sm.NodeIDByClientIP("192.0.2.24"); got != "edge-us" {
		t.Fatalf("first lookup = %q, want edge-us", got)
	}
	failing = true
	if got := sm.NodeIDByClientIP("192.0.2.24"); got != "edge-us" || calls != 1 {
		t.Fatalf("cached lookup = %q after %d DNS calls, want edge-us after 1", got, calls)
	}
	// Past the TTL a failed refresh keeps the last known addresses.
	entry := nodeHostAddrs.entries["edge-us.example.test"]
	entry.resolvedAt = time.Now().Add(-time.Hour)
	nodeHostAddrs.entries["edge-us.example.test"] = entry
	if got := sm.NodeIDByClientIP("192.0.2.24"); got != "edge-us" {
		t.Fatalf("lookup after a failed refresh = %q, want edge-us", got)
	}
	if got := sm.NodeIDByClientIP("198.51.100.9"); got != "" {
		t.Fatalf("unknown IP = %q, want none", got)
	}
}
