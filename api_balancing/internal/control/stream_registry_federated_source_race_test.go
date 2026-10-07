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
