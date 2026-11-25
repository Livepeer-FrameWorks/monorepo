package control

import (
	"testing"
	"time"
)

func TestFederatedPublisherAdvertisementsCannotRollBackOwnership(t *testing.T) {
	for _, test := range []struct {
		name            string
		epoch, revision int64
		generation      string
		live, active    bool
	}{
		{"older epoch with larger local counter", 1, 999, "old", true, true},
		{"older revision within current epoch", 2, 1, "current", true, true},
		{"conflicting owner in same epoch", 2, 3, "other", true, true},
		{"legacy withdrawal", 0, 0, "", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := NewStreamRegistry(nil, "local", time.Minute)
			identity := StreamEntry{TenantID: "tenant", InternalName: "stream", PlaybackID: "play"}
			current := Location{IsLiveNow: true, SourceActive: true, OwnerNodeID: "node", SourceGeneration: "current", SourceLeaseEpoch: 2, SourceRevision: 2}
			r.UpsertFederatedSource("peer", identity, current)
			stale := Location{IsLiveNow: test.live, SourceActive: test.active, OwnerNodeID: "node", SourceGeneration: test.generation, SourceLeaseEpoch: test.epoch, SourceRevision: test.revision}
			r.UpsertFederatedSource("peer", identity, stale)
			entry, ok := r.lookup(r.byInt, "stream")
			got := entry.Locations["peer"]
			if !ok || got.SourceLeaseEpoch != 2 || got.SourceRevision != 2 || got.SourceGeneration != "current" || !got.SourceActive || !got.IsLiveNow {
				t.Fatalf("delayed advertisement replaced current publisher: found=%v location=%+v", ok, got)
			}
		})
	}
}

func TestFederatedPublisherWithdrawalRetainsOwnershipFence(t *testing.T) {
	r := NewStreamRegistry(nil, "local", time.Minute)
	identity := StreamEntry{TenantID: "tenant", InternalName: "stream", PlaybackID: "play"}
	live := Location{IsLiveNow: true, SourceActive: true, OwnerNodeID: "node", SourceGeneration: "current", SourceLeaseEpoch: 2, SourceRevision: 2}
	r.UpsertFederatedSource("peer", identity, live)
	ended := live
	ended.SourceActive = false
	ended.IsLiveNow = false
	ended.SourceRevision = 3
	r.UpsertFederatedSource("peer", identity, ended)
	r.UpsertFederatedSource("peer", identity, live)
	entry, ok := r.lookup(r.byInt, "stream")
	got := entry.Locations["peer"]
	if !ok || got.SourceLeaseEpoch != 2 || got.SourceRevision != 3 || got.SourceActive || got.IsLiveNow {
		t.Fatalf("withdrawn publisher resurrected: found=%v location=%+v", ok, got)
	}
	successor := live
	successor.SourceLeaseEpoch = 3
	successor.SourceRevision = 1
	successor.SourceGeneration = "successor"
	r.UpsertFederatedSource("peer", identity, successor)
	entry, ok = r.lookup(r.byInt, "stream")
	got = entry.Locations["peer"]
	if !ok || got.SourceLeaseEpoch != 3 || got.SourceGeneration != "successor" || !got.SourceActive {
		t.Fatalf("new global owner rejected by old local counter: found=%v location=%+v", ok, got)
	}
}

func TestFencedFederatedOwnershipSurvivesStaleReplicaWrites(t *testing.T) {
	storeA, client, _ := newTestRedis(t)
	storeB := NewRedisRegistryStore(client, "cluster-test")
	makeReplica := func(store *RedisRegistryStore, id string) *StreamRegistry {
		r := NewStreamRegistry(nil, "local", time.Minute)
		r.redisStore = store
		r.instanceID = id
		return r
	}
	a, b := makeReplica(storeA, "a"), makeReplica(storeB, "b")
	identity := StreamEntry{TenantID: "tenant", InternalName: "stream", PlaybackID: "play"}
	a.UpsertFederatedSource("peer", identity, Location{IsLiveNow: true})
	newer := Location{SourceLeaseEpoch: 3, SourceRevision: 1, SourceGeneration: "new", OwnerNodeID: "node-new", SourceActive: true, IsLiveNow: true, AdvertisedSource: true}
	b.UpsertFederatedSource("peer", identity, newer)
	older := Location{SourceLeaseEpoch: 2, SourceRevision: 999, SourceGeneration: "old", OwnerNodeID: "node-old", SourceActive: true, IsLiveNow: true, AdvertisedSource: true}
	a.UpsertFederatedSource("peer", identity, older)
	requireCurrent := func(step string) {
		t.Helper()
		entry, found, err := storeA.GetSource(t.Context(), "stream")
		loc := entry.Locations["peer"]
		if err != nil || !found || loc.SourceLeaseEpoch != 3 || loc.SourceGeneration != "new" || !loc.SourceActive {
			t.Fatalf("%s replaced shared owner: found=%v loc=%+v err=%v", step, found, loc, err)
		}
	}
	requireCurrent("stale replica advertisement")
	// A replica that never saw the fenced owner still cannot remove it with a
	// legacy withdrawal whose wall-clock timestamp happens to be newer.
	c := makeReplica(NewRedisRegistryStore(client, "cluster-test"), "c")
	c.byInt["stream"] = &cachedEntry{entry: StreamEntry{TenantID: "tenant", InternalName: "stream", Locations: map[string]Location{"peer": {IsLiveNow: true}}}, cached: time.Now()}
	c.UpsertFederatedSource("peer", identity, Location{IsLiveNow: false})
	requireCurrent("stale replica withdrawal")
}

// An outage admission is a legacy owner (no lease epoch) that the cell's
// database serializes after the fenced owner it replaces. Its later revision
// must replace the withdrawn fenced owner on every replica and in every peer
// cell, and a delayed write of that fenced owner must not resurrect it.
func TestOutageLegacyOwnerReplacesWithdrawnFencedOwner(t *testing.T) {
	store, client, _ := newTestRedis(t)
	makeReplica := func(store *RedisRegistryStore, id string) *StreamRegistry {
		r := NewStreamRegistry(nil, "local", time.Minute)
		r.redisStore = store
		r.instanceID = id
		return r
	}
	a := makeReplica(store, "a")
	ctx := t.Context()
	fenced := PublisherRuntimeBinding{TenantID: "tenant", RuntimeName: "live+stream_fw1p_0123456789abcdef0123456789abcdef", LayoutVersion: 1, LeaseEpoch: 4, ClusterID: "local"}
	if _, _, applied, err := a.projectSourceWithPriorGenerationContext(ctx, "stream", "node", 11, "trigger-1", "fenced", 1, fenced); err != nil || !applied {
		t.Fatalf("fenced projection: applied=%v err=%v", applied, err)
	}
	if applied, err := a.PublishSourceInactiveContext(ctx, "stream", "node", "fenced", 2); err != nil || !applied {
		t.Fatalf("fenced withdrawal: applied=%v err=%v", applied, err)
	}
	outage := PublisherRuntimeBinding{TenantID: "tenant", RuntimeName: "live+stream", ClusterID: "local"}
	if _, _, applied, err := a.projectSourceWithPriorGenerationContext(ctx, "stream", "node", 12, "trigger-2", "outage", 3, outage); err != nil || !applied {
		t.Fatalf("outage projection: applied=%v err=%v", applied, err)
	}
	entry, found, err := store.GetSource(ctx, "stream")
	got := entry.Locations["local"]
	if err != nil || !found || !got.SourceActive || got.SourceGeneration != "outage" || got.SourceLeaseEpoch != 0 || got.SourceRevision != 3 {
		t.Fatalf("shared store kept the withdrawn fenced owner over the outage owner: found=%v loc=%+v err=%v", found, got, err)
	}

	// A replica that cached the withdrawal merges the outage owner from the shared store.
	b := makeReplica(NewRedisRegistryStore(client, "cluster-test"), "b")
	withdrawn := Location{ClusterID: "local", OwnerNodeID: "node", SourceGeneration: "fenced", SourceLeaseEpoch: 4, SourceRevision: 2}
	b.byInt["stream"] = &cachedEntry{entry: StreamEntry{TenantID: "tenant", InternalName: "stream", Locations: map[string]Location{"local": withdrawn}}, cached: time.Now()}
	merged := mergeStreamEntry(b.byInt["stream"].entry, entry)
	if loc := merged.Locations["local"]; !loc.SourceActive || loc.SourceGeneration != "outage" || loc.SourceLeaseEpoch != 0 {
		t.Fatalf("replica merge kept the withdrawn fenced owner: %+v", loc)
	}
	// A delayed write of the ended fenced owner orders before the outage owner.
	stale := Location{ClusterID: "local", OwnerNodeID: "node", SourceActive: true, SourceGeneration: "fenced", SourceLeaseEpoch: 4, SourceRevision: 1, SourceRuntimeName: fenced.RuntimeName}
	if loc := mergeLocationRevisioned(got, stale); !loc.SourceActive || loc.SourceGeneration != "outage" || loc.SourceLeaseEpoch != 0 {
		t.Fatalf("delayed fenced write resurrected the ended owner: %+v", loc)
	}

	// A peer cell orders the same advertisements the same way.
	peer := NewStreamRegistry(nil, "remote", time.Minute)
	identity := StreamEntry{TenantID: "tenant", InternalName: "stream"}
	peer.UpsertFederatedSource("cell-a", identity, Location{IsLiveNow: true, SourceActive: true, OwnerNodeID: "node", SourceGeneration: "fenced", SourceLeaseEpoch: 4, SourceRevision: 1})
	peer.UpsertFederatedSource("cell-a", identity, Location{OwnerNodeID: "node", SourceGeneration: "fenced", SourceLeaseEpoch: 4, SourceRevision: 2})
	peer.UpsertFederatedSource("cell-a", identity, Location{IsLiveNow: true, SourceActive: true, OwnerNodeID: "node", SourceGeneration: "outage", SourceRevision: 3})
	peer.UpsertFederatedSource("cell-a", identity, Location{IsLiveNow: true, SourceActive: true, OwnerNodeID: "node", SourceGeneration: "fenced", SourceLeaseEpoch: 4, SourceRevision: 1})
	peerEntry, ok := peer.lookup(peer.byInt, "stream")
	if loc := peerEntry.Locations["cell-a"]; !ok || !loc.SourceActive || loc.SourceGeneration != "outage" || loc.SourceLeaseEpoch != 0 {
		t.Fatalf("peer kept the withdrawn fenced owner over the outage owner: found=%v loc=%+v", ok, loc)
	}
}

func TestLegacyWithdrawalCannotRemoveFencedPublisher(t *testing.T) {
	r := NewStreamRegistry(nil, "local", time.Minute)
	identity := StreamEntry{TenantID: "tenant", InternalName: "stream"}
	r.UpsertFederatedSource("peer", identity, Location{SourceLeaseEpoch: 1, SourceRevision: 1, SourceGeneration: "gen", OwnerNodeID: "node", SourceActive: true, IsLiveNow: true})
	r.withdrawFederatedSource("peer", "stream")
	entry, found := r.lookup(r.byInt, "stream")
	if !found || entry.Locations["peer"].SourceLeaseEpoch != 1 {
		t.Fatal("racing legacy withdrawal removed the fenced publisher")
	}
}
