package control

import (
	"context"
	"testing"
	"time"
)

// TestStorePreservesLocationsOnHydrationRefresh locks the P0a fix.
// Production TTL is 60s. Any source resolver call after expiry triggers
// hydrate→store, which previously REPLACED the cached entry with a
// Locations-free StreamEntry — wiping SourceActive, OwnerNodeID,
// ReplicatingFrom, etc. Every 60s of stable ingest would silently
// reopen duplicate-publisher admission. store() must merge identity
// into the existing entry instead.
func TestStorePreservesLocationsOnHydrationRefresh(t *testing.T) {
	r := newPopulatedRegistry(t)
	const internal = "60546679b497415db2338cd5cae54992"

	// Project a publisher; this populates Locations with SourceActive +
	// OwnerNodeID and refreshes ce.cached.
	projectSourceForTest(t, r, internal, "node-A", 0, "", "gen-seed", 1)

	// Force TTL expiry without going through the normal cache-cooling
	// path, then trigger a resolve. ResolveSourceByInternalName must
	// preserve SourceActive across the hydrate cycle.
	r.mu.Lock()
	if ce := r.byInt[internal]; ce != nil {
		ce.cached = time.Now().Add(-2 * time.Minute)
	}
	r.mu.Unlock()

	if _, err := r.ResolveSourceByInternalName(context.Background(), internal); err != nil {
		t.Fatalf("resolve after TTL expiry: %v", err)
	}

	r.mu.RLock()
	loc := r.byInt[internal].entry.Locations["cluster-A"]
	r.mu.RUnlock()
	if !loc.SourceActive {
		t.Error("SourceActive=false after hydrate refresh; store() wiped Locations")
	}
	if loc.OwnerNodeID != "node-A" {
		t.Errorf("OwnerNodeID=%q after hydrate refresh; want node-A", loc.OwnerNodeID)
	}

	// SourceActive must have SURVIVED the hydrate refresh: the stream-scoped snapshot still reports the
	// source active on node-A. Had store() wiped SourceActive, this would report inactive — exactly the
	// regression this guards.
	if gen, active, ok := r.SourceGenerationSnapshot(internal, ""); !ok || !active || gen != "gen-seed" {
		t.Errorf("source did not survive the hydrate refresh (gen=%q active=%v ok=%v)", gen, active, ok)
	}
}

// TestSweeperPreservesActiveSourceLocation locks the P0b fix. A stable
// long-running publisher's local Location only has UpdatedAt set at
// admission time. The 5-min sweeper would otherwise delete it and
// reopen admission. The fix: any Location with SourceActive=true OR
// OwnerNodeID set OR ReplicatingFrom set OR OutboundPullers non-empty
// is preserved regardless of UpdatedAt age.
func TestSweeperPreservesActiveSourceLocation(t *testing.T) {
	r := newPopulatedRegistry(t)
	const internal = "60546679b497415db2338cd5cae54992"

	projectSourceForTest(t, r, internal, "node-A", 0, "", "gen-seed", 1)
	// Backdate UpdatedAt so the sweeper's cutoff is past.
	r.mu.Lock()
	loc := r.byInt[internal].entry.Locations["cluster-A"]
	loc.UpdatedAt = time.Now().Add(-10 * time.Minute)
	r.byInt[internal].entry.Locations["cluster-A"] = loc
	r.mu.Unlock()

	removed, evicted := r.SweepStaleLocations(1 * time.Minute)
	if removed != 0 || evicted != 0 {
		t.Errorf("sweeper removed active Location: removed=%d evicted=%d", removed, evicted)
	}

	r.mu.RLock()
	loc = r.byInt[internal].entry.Locations["cluster-A"]
	r.mu.RUnlock()
	if !loc.SourceActive || loc.OwnerNodeID != "node-A" {
		t.Errorf("active state lost: SourceActive=%v OwnerNodeID=%q", loc.SourceActive, loc.OwnerNodeID)
	}
}

// TestSweeperEvictsInactiveStaleLocation: the sweeper still does its
// job — Locations with no active state get evicted when UpdatedAt is
// past cutoff. Without this complement the P0b fix could let stale
// federation peer entries leak forever.
func TestSweeperEvictsInactiveStaleLocation(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	// Federated entry from a peer that's gone idle. Mimic a peer
	// Location with stale UpdatedAt and no active state.
	r.UpsertFederatedSource("peer-X", StreamEntry{
		InternalName:    "stream-stale",
		OriginClusterID: "peer-X",
	}, Location{IsLiveNow: true})
	r.mu.Lock()
	loc := r.byInt["stream-stale"].entry.Locations["peer-X"]
	loc.UpdatedAt = time.Now().Add(-10 * time.Minute)
	loc.SourceActive = false
	loc.OwnerNodeID = ""
	loc.ReplicatingFrom = ""
	loc.OutboundPullers = nil
	r.byInt["stream-stale"].entry.Locations["peer-X"] = loc
	r.mu.Unlock()

	removed, _ := r.SweepStaleLocations(1 * time.Minute)
	if removed == 0 {
		t.Error("sweeper did not evict an inactive stale Location")
	}
}

// TestRecordOutboundPullCreatesMinimalEntry locks the P1 fix. The
// federation NotifyOriginPull handler returns Accepted: true after
// calling RecordOutboundPull. Previously, if no byInt entry existed
// (registry evicted, identity not yet hydrated), RecordOutboundPull
// would silently no-op and the source cluster would ack a pull it
// couldn't track. Mirror MarkReplicating: create a minimal entry.
func TestRecordOutboundPullCreatesMinimalEntry(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	const internal = "fresh-stream"

	r.RecordOutboundPull(internal, OutboundPull{
		DestClusterID: "peer-B",
		DestNodeID:    "edge-b-1",
		SourceNodeID:  "edge-a-1",
		DTSCURL:       "dtsc://edge-a-1:4200/live+fresh-stream",
	})

	r.mu.RLock()
	ce, ok := r.byInt[internal]
	r.mu.RUnlock()
	if !ok {
		t.Fatal("RecordOutboundPull did not create a minimal entry")
	}
	loc := ce.entry.Locations["cluster-A"]
	if len(loc.OutboundPullers) != 1 {
		t.Fatalf("OutboundPullers len = %d, want 1", len(loc.OutboundPullers))
	}
	if loc.OutboundPullers[0].DestClusterID != "peer-B" {
		t.Errorf("OutboundPull[0].DestClusterID = %q", loc.OutboundPullers[0].DestClusterID)
	}
}
