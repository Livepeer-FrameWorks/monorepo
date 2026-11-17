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
