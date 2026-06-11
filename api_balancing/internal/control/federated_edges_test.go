package control

import (
	"testing"
	"time"
)

// FederatedEdgeCandidates is the play path's pre-warmed alternative to the
// cold QueryStream fan-out: it surfaces StreamAdvertisement-fed edges from
// fresh, live, non-local Locations only.

func federatedAdLocation(cands ...EdgeCandidate) Location {
	return Location{IsLiveNow: true, AdTimestamp: time.Now().Unix(), EdgeCandidates: cands}
}

func TestFederatedEdgeCandidates_ReturnsFreshPeerEdges(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	r.UpsertFederatedSource("cluster-B",
		StreamEntry{InternalName: "s1", TenantID: "tenant-1"},
		federatedAdLocation(EdgeCandidate{
			NodeID: "edge-b1", BaseURL: "https://b1", DTSCURL: "dtsc://b1/live+s1",
			BWAvailable: 1000, CPUPercent: 10, RAMUsed: 512, RAMMax: 1024,
		}),
	)

	got := r.FederatedEdgeCandidates("s1", 20*time.Second)
	if len(got) != 1 || len(got["cluster-B"]) != 1 {
		t.Fatalf("candidates = %+v, want one cluster-B edge", got)
	}
	c := got["cluster-B"][0]
	if c.NodeID != "edge-b1" || c.RAMMax != 1024 || c.RAMUsed != 512 {
		t.Fatalf("edge fields wrong (RAM must round-trip): %+v", c)
	}

	// Deep-copy: mutating the returned slice must not corrupt the registry.
	got["cluster-B"][0].NodeID = "tampered"
	again := r.FederatedEdgeCandidates("s1", 20*time.Second)
	if again["cluster-B"][0].NodeID != "edge-b1" {
		t.Fatal("returned slice aliases registry state")
	}
}

// FederatedRemoteEdges is the shared conversion both viewer-resolution
// surfaces (HTTP /play, gRPC ViewerControlService) call: ad-fed registry
// edges → balancer candidates, dropping RAM-less edges (old peers) so the
// caller falls through to the fan-out instead of presenting a warm-but-
// unscorable set.
func TestFederatedRemoteEdges_ConvertsAndDropsRAMless(t *testing.T) {
	prev := StreamRegistryInstance
	StreamRegistryInstance = NewStreamRegistry(nil, "cluster-local", time.Minute)
	t.Cleanup(func() { StreamRegistryInstance = prev })

	StreamRegistryInstance.UpsertFederatedSource("cluster-remote",
		StreamEntry{InternalName: "s1", TenantID: "tenant-1"},
		federatedAdLocation(
			EdgeCandidate{NodeID: "edge-modern", BaseURL: "https://m", BWAvailable: 1000, CPUPercent: 12, RAMUsed: 256, RAMMax: 1024},
			EdgeCandidate{NodeID: "edge-old-peer", BaseURL: "https://o", BWAvailable: 1000}, // no RAM fields
		),
	)

	got := FederatedRemoteEdges("s1")
	if len(got) != 1 {
		t.Fatalf("candidates = %+v, want exactly the RAM-bearing edge", got)
	}
	c := got[0]
	if c.NodeID != "edge-modern" || c.ClusterID != "cluster-remote" || c.RAMMax != 1024 || c.RAMUsed != 256 || c.BWAvailable != 1000 {
		t.Fatalf("converted candidate wrong: %+v", c)
	}

	// All edges RAM-less → nothing usable → caller falls through to fan-out.
	StreamRegistryInstance.UpsertFederatedSource("cluster-remote",
		StreamEntry{InternalName: "s2"},
		federatedAdLocation(EdgeCandidate{NodeID: "edge-old-peer", BaseURL: "https://o", BWAvailable: 1000}),
	)
	if got := FederatedRemoteEdges("s2"); len(got) != 0 {
		t.Fatalf("RAM-less edges must be dropped, got %+v", got)
	}

	// Nil registry (boot, tests) is a safe no-op.
	StreamRegistryInstance = nil
	if got := FederatedRemoteEdges("s1"); got != nil {
		t.Fatalf("nil registry must yield nil, got %+v", got)
	}
}

func TestFederatedEdgeCandidates_ExcludesOwnClusterStaleAndWithdrawn(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	r.UpsertFederatedSource("cluster-B",
		StreamEntry{InternalName: "s1"},
		federatedAdLocation(EdgeCandidate{NodeID: "edge-b1", RAMMax: 1024}),
	)

	// Stale: a Location older than maxAge is dead data.
	r.mu.Lock()
	ce := r.byInt["s1"]
	loc := ce.entry.Locations["cluster-B"]
	loc.UpdatedAt = time.Now().Add(-time.Minute)
	ce.entry.Locations["cluster-B"] = loc
	r.mu.Unlock()
	if got := r.FederatedEdgeCandidates("s1", 20*time.Second); got != nil {
		t.Fatalf("stale Location served: %+v", got)
	}

	// Refresh, then confirm the local cluster's own Location is excluded.
	r.UpsertFederatedSource("cluster-B",
		StreamEntry{InternalName: "s1"},
		federatedAdLocation(EdgeCandidate{NodeID: "edge-b1", RAMMax: 1024}),
	)
	r.mu.Lock()
	ce = r.byInt["s1"]
	ce.entry.Locations["cluster-A"] = Location{ClusterID: "cluster-A", IsLiveNow: true, UpdatedAt: time.Now(), EdgeCandidates: []EdgeCandidate{{NodeID: "local-node", RAMMax: 1}}}
	r.mu.Unlock()
	got := r.FederatedEdgeCandidates("s1", 20*time.Second)
	if len(got) != 1 || got["cluster-A"] != nil {
		t.Fatalf("own cluster leaked into federated candidates: %+v", got)
	}

	// Withdrawal ad (IsLiveNow=false) drops the peer Location entirely.
	r.UpsertFederatedSource("cluster-B", StreamEntry{InternalName: "s1"}, Location{IsLiveNow: false})
	if got := r.FederatedEdgeCandidates("s1", 20*time.Second); got != nil {
		t.Fatalf("withdrawn Location served: %+v", got)
	}
}
