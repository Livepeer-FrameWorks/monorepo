package control

import (
	"sync"
	"testing"
	"time"
)

// Two peers advertising the same stream concurrently must not share the cached
// entry's Locations map with the snapshot that is marshaled outside the lock;
// under -race the aliasing surfaces as a map iteration/write race, in
// production as a fatal runtime error in the federation PeerChannel handler.
func TestUpsertFederatedSourceConcurrentPeersDoNotAliasLocations(t *testing.T) {
	const stream = "60546679b497415db2338cd5cae54992"
	r := NewStreamRegistry(nil, "cluster-local", time.Minute)
	store, _, _ := newTestRedis(t)
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "local"
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, peer := range []string{"cluster-a", "cluster-b", "cluster-c"} {
		wg.Add(1)
		go func(peer string) {
			defer wg.Done()
			// Odd iterations withdraw, even ones advertise; the final iteration is live so
			// every peer must be present at the end.
			for i := 0; i <= 200; i++ {
				r.UpsertFederatedSource(peer, StreamEntry{InternalName: stream, TenantID: "tenant", PlaybackID: "pb"},
					Location{ClusterID: peer, IsLiveNow: i%2 == 0, AdTimestamp: int64(i)})
			}
		}(peer)
	}
	wg.Wait()

	snapshot, found, err := r.SourceSnapshot(t.Context(), "tenant", stream)
	if err != nil || !found {
		t.Fatalf("source snapshot: found=%v err=%v", found, err)
	}
	for _, peer := range []string{"cluster-a", "cluster-b", "cluster-c"} {
		if _, ok := snapshot.Locations[peer]; !ok {
			t.Fatalf("peer %s location missing after concurrent advertisements: %v", peer, snapshot.Locations)
		}
	}
}

// A replica whose cache has not yet applied another replica's advertisement
// must not erase that peer's Location from the shared store when it publishes
// its own peer's advertisement or withdrawal.
func TestFederatedSourceWritesKeepOtherPeersDurableLocation(t *testing.T) {
	const stream = "federated-two-replicas"
	storeA, client, _ := newTestRedis(t)
	storeB := NewRedisRegistryStore(client, "cluster-test")
	replica := func(store *RedisRegistryStore, instance string) *StreamRegistry {
		r := NewStreamRegistry(nil, "cluster-local", time.Minute)
		r.mu.Lock()
		r.redisStore = store
		r.instanceID = instance
		r.mu.Unlock()
		return r
	}
	ra, rb := replica(storeA, "replica-a"), replica(storeB, "replica-b")
	identity := StreamEntry{InternalName: stream, TenantID: "tenant", PlaybackID: "pb"}

	ra.UpsertFederatedSource("cluster-a", identity, Location{IsLiveNow: true, AdTimestamp: 1})
	rb.UpsertFederatedSource("cluster-b", identity, Location{IsLiveNow: true, AdTimestamp: 1})
	ra.UpsertFederatedSource("cluster-a", identity, Location{IsLiveNow: true, AdTimestamp: 2})

	snapshot, found, err := rb.SourceSnapshot(t.Context(), "tenant", stream)
	if err != nil || !found {
		t.Fatalf("source snapshot: found=%v err=%v", found, err)
	}
	if _, ok := snapshot.Locations["cluster-a"]; !ok {
		t.Fatalf("cluster-a location missing: %v", snapshot.Locations)
	}
	if _, ok := snapshot.Locations["cluster-b"]; !ok {
		t.Fatalf("cluster-b location erased by a replica that never cached it: %v", snapshot.Locations)
	}

	// cluster-a held replica A's only cached Location; its withdrawal must remove
	// that Location without deleting the source cluster-b still serves.
	ra.UpsertFederatedSource("cluster-a", identity, Location{IsLiveNow: false, AdTimestamp: 3})
	snapshot, found, err = rb.SourceSnapshot(t.Context(), "tenant", stream)
	if err != nil || !found {
		t.Fatalf("source deleted while cluster-b is live: found=%v err=%v", found, err)
	}
	if _, ok := snapshot.Locations["cluster-a"]; ok {
		t.Fatalf("withdrawn cluster-a location still present: %v", snapshot.Locations)
	}
	if _, ok := snapshot.Locations["cluster-b"]; !ok {
		t.Fatalf("cluster-b location missing after cluster-a withdrew: %v", snapshot.Locations)
	}

	rb.UpsertFederatedSource("cluster-b", identity, Location{IsLiveNow: false, AdTimestamp: 2})
	if _, found, err = rb.SourceSnapshot(t.Context(), "tenant", stream); err != nil || found {
		t.Fatalf("source must be deleted once its last peer withdrew: found=%v err=%v", found, err)
	}
}

// A local-source write publishes only what it owns. A replica whose cache has
// not applied a peer's advertisement must not erase that peer's durable
// Location when it refreshes the stream's identity or stamps its own Location.
func TestLocalSourceWritesKeepPeerDurableLocation(t *testing.T) {
	const stream = "local-write-keeps-peer"
	storeA, client, _ := newTestRedis(t)
	storeB := NewRedisRegistryStore(client, "cluster-test")
	replica := func(store *RedisRegistryStore, instance string) *StreamRegistry {
		r := NewStreamRegistry(nil, "cluster-local", time.Minute)
		r.mu.Lock()
		r.redisStore = store
		r.instanceID = instance
		r.mu.Unlock()
		return r
	}
	ra, rb := replica(storeA, "replica-a"), replica(storeB, "replica-b")
	identity := StreamEntry{InternalName: stream, TenantID: "tenant", PlaybackID: "pb"}

	ra.UpsertLocalSource(identity)
	rb.UpsertFederatedSource("cluster-b", identity, Location{IsLiveNow: true, AdTimestamp: 1})

	requirePeer := func(step string) {
		t.Helper()
		durable, found, err := storeA.GetSource(t.Context(), stream)
		if err != nil || !found {
			t.Fatalf("%s: read durable source: found=%v err=%v", step, found, err)
		}
		if _, ok := durable.Locations["cluster-b"]; !ok {
			t.Fatalf("%s erased peer cluster-b's durable location: %v", step, durable.Locations)
		}
	}
	ra.UpsertLocalSource(StreamEntry{InternalName: stream, TenantID: "tenant", StreamID: "stream-id"})
	requirePeer("identity refresh")
	if _, stamped := ra.MarkSourceOwnerIfUnset(stream, "node-1"); !stamped {
		t.Fatal("owner stamp was not applied")
	}
	requirePeer("owner stamp")
	durable, _, err := storeA.GetSource(t.Context(), stream)
	if err != nil || durable.Locations["cluster-local"].OwnerNodeID != "node-1" || durable.StreamID != "stream-id" {
		t.Fatalf("local writes missing from the durable source: %+v err=%v", durable, err)
	}
}

// The sweeper removes only the Locations it expired, and only while the durable
// copy is no newer than the expired one. A replica whose cache lacks a live
// peer, or holds a Location another replica has since renewed, must not delete
// either from the shared store, including on the retry of a failed removal.
func TestSweepStaleLocationsKeepsLiveDurableLocations(t *testing.T) {
	store, _, mr := newTestRedis(t)
	stale := time.Now().Add(-time.Hour)
	seed := func(name string, locations map[string]Location) {
		t.Helper()
		entry := StreamEntry{InternalName: name, TenantID: "tenant", Locations: locations}
		if applied, err := store.SetSourceRevisioned(t.Context(), entry, RegistryChange{Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: name}, 0); err != nil || !applied {
			t.Fatalf("seed %s: applied=%v err=%v", name, applied, err)
		}
	}
	// "with-peer": the durable source also holds a live cluster-b this replica never cached.
	seed("with-peer", map[string]Location{
		"cluster-a": {ClusterID: "cluster-a", UpdatedAt: stale},
		"cluster-b": {ClusterID: "cluster-b", UpdatedAt: time.Now()},
	})
	// "renewed": another replica renewed cluster-a after this replica's cached copy.
	seed("renewed", map[string]Location{
		"cluster-a": {ClusterID: "cluster-a", UpdatedAt: time.Now()},
	})

	r := NewStreamRegistry(nil, "cluster-local", time.Minute)
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "sweeper"
	for _, name := range []string{"with-peer", "renewed"} {
		r.byInt[name] = &cachedEntry{
			entry:  StreamEntry{InternalName: name, TenantID: "tenant", Locations: map[string]Location{"cluster-a": {ClusterID: "cluster-a", UpdatedAt: stale}}},
			cached: time.Now(),
		}
	}
	r.mu.Unlock()

	// The first pass fails against Redis; the retry pass publishes the retained removals.
	mr.SetError("injected redis outage")
	if _, evicted := r.SweepStaleLocations(30 * time.Minute); evicted != 0 {
		t.Fatalf("sweep evicted %d entries during a failed durable removal, want 0", evicted)
	}
	mr.SetError("")
	if _, evicted := r.SweepStaleLocations(30 * time.Minute); evicted != 2 {
		t.Fatalf("retry sweep evicted %d entries, want 2", evicted)
	}

	withPeer, found, err := store.GetSource(t.Context(), "with-peer")
	if err != nil || !found {
		t.Fatalf("sweep deleted a source a live peer still serves: found=%v err=%v", found, err)
	}
	if _, ok := withPeer.Locations["cluster-b"]; !ok {
		t.Fatalf("sweep erased live peer cluster-b: %v", withPeer.Locations)
	}
	if _, ok := withPeer.Locations["cluster-a"]; ok {
		t.Fatalf("expired cluster-a location still present: %v", withPeer.Locations)
	}
	renewed, found, err := store.GetSource(t.Context(), "renewed")
	if err != nil || !found {
		t.Fatalf("sweep deleted a source whose location another replica renewed: found=%v err=%v", found, err)
	}
	if _, ok := renewed.Locations["cluster-a"]; !ok {
		t.Fatalf("sweep erased renewed cluster-a: %v", renewed.Locations)
	}
}
