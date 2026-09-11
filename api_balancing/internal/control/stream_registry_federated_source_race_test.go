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
