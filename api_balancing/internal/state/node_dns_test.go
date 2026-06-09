package state

import (
	"testing"
)

// extractIPFromBaseURL pulls an IP literal out of a node's base URL for DNS
// publishing — and ONLY an IP. A DNS hostname, an empty/garbage URL, or a
// missing host all yield "" (Foghorn can't derive an authoritative IP, so it
// publishes nothing rather than a guess).
func TestExtractIPFromBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"https://1.2.3.4:8080":      "1.2.3.4",
		"http://1.2.3.4":            "1.2.3.4",
		"https://edge.example.com":  "", // hostname, not an IP
		"https://[2001:db8::1]:443": "2001:db8::1",
		"not a url with spaces":     "",
		"https://":                  "",
	}
	for in, want := range cases {
		if got := extractIPFromBaseURL(in); got != want {
			t.Errorf("extractIPFromBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// NodeDNSSnapshot.equals must compare every DNS-relevant field — a difference in
// any one (health, cluster, IP, or a capability) is a real change the coalescer
// must publish.
func TestNodeDNSSnapshotEquals(t *testing.T) {
	base := NodeDNSSnapshot{NodeID: "n1", IsHealthy: true, ClusterID: "c1", ExternalIP: "1.2.3.4", CapEdge: true}
	if !base.equals(base) {
		t.Fatal("snapshot must equal itself")
	}
	diffs := []NodeDNSSnapshot{
		{NodeID: "n2", IsHealthy: true, ClusterID: "c1", ExternalIP: "1.2.3.4", CapEdge: true},
		{NodeID: "n1", IsHealthy: false, ClusterID: "c1", ExternalIP: "1.2.3.4", CapEdge: true},
		{NodeID: "n1", IsHealthy: true, ClusterID: "c2", ExternalIP: "1.2.3.4", CapEdge: true},
		{NodeID: "n1", IsHealthy: true, ClusterID: "c1", ExternalIP: "9.9.9.9", CapEdge: true},
		{NodeID: "n1", IsHealthy: true, ClusterID: "c1", ExternalIP: "1.2.3.4", CapEdge: false},
		{NodeID: "n1", IsHealthy: true, ClusterID: "c1", ExternalIP: "1.2.3.4", CapEdge: true, CapStorage: true},
	}
	for i, d := range diffs {
		if base.equals(d) {
			t.Errorf("case %d: snapshots differing in one field must not be equal", i)
		}
	}
}

// SnapshotNodeDNS builds the DNS view from a seeded node; a node not in the
// manager reports not-found.
func TestSnapshotNodeDNS(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("n1", "https://1.2.3.4:8080", true, nil, nil, "", "", nil)

	snap, ok := sm.SnapshotNodeDNS("n1")
	if !ok || snap.NodeID != "n1" || snap.ExternalIP != "1.2.3.4" || !snap.IsHealthy {
		t.Fatalf("snapshot = %+v ok=%v", snap, ok)
	}
	if _, ok := sm.SnapshotNodeDNS("ghost"); ok {
		t.Fatal("unknown node should report not-found")
	}
}

// ConsumeDNSRelevantDeltas drains the dirty set and emits only genuine changes:
// a freshly-marked node emits once, a re-mark with no state change is coalesced
// (silent), and an empty dirty set returns nil.
func TestConsumeDNSRelevantDeltas(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Empty dirty set → nil.
	if got := sm.ConsumeDNSRelevantDeltas(); got != nil {
		t.Fatalf("empty dirty set should return nil, got %v", got)
	}

	// SetNodeInfo marks the node dirty.
	sm.SetNodeInfo("n1", "https://1.2.3.4:8080", true, nil, nil, "", "", nil)
	first := sm.ConsumeDNSRelevantDeltas()
	if len(first) != 1 || first[0].NodeID != "n1" {
		t.Fatalf("first consume = %+v, want 1 delta for n1", first)
	}

	// Nothing re-marked → nil.
	if got := sm.ConsumeDNSRelevantDeltas(); got != nil {
		t.Fatalf("no new dirty → nil, got %v", got)
	}

	// Re-mark with identical state → coalesced (matches last published).
	sm.MarkNodeDNSChanged("n1")
	if got := sm.ConsumeDNSRelevantDeltas(); len(got) != 0 {
		t.Fatalf("unchanged re-mark should coalesce to empty, got %+v", got)
	}
}

// AllReportedNodes returns every node's DNS snapshot for the repair loop; with no
// stale filter it includes all seeded nodes.
func TestAllReportedNodes(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("n1", "https://1.2.3.4", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("n2", "https://5.6.7.8", false, nil, nil, "", "", nil)

	all := sm.AllReportedNodes(0)
	if len(all) != 2 {
		t.Fatalf("got %d nodes, want 2", len(all))
	}
	ids := map[string]bool{}
	for _, s := range all {
		ids[s.NodeID] = true
	}
	if !ids["n1"] || !ids["n2"] {
		t.Fatalf("missing nodes: %+v", all)
	}
}
