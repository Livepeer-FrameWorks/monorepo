package control

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// Auth identity (requires_auth + cluster_peers) is filled from a peer only when
// the local side hasn't hydrated it; a locally-known bit is not overwritten.
func TestMergeStreamEntry_FillsAuthIdentityFromPeer(t *testing.T) {
	incoming := StreamEntry{
		InternalName:      "s1",
		RequiresAuth:      true,
		RequiresAuthKnown: true,
		ClusterPeers:      []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-B"}},
	}

	filled := mergeStreamEntry(StreamEntry{InternalName: "s1"}, incoming)
	if !filled.RequiresAuthKnown || !filled.RequiresAuth {
		t.Fatalf("peer auth not filled into authless local entry: %+v", filled)
	}
	if len(filled.ClusterPeers) != 1 || filled.ClusterPeers[0].GetClusterId() != "cluster-B" {
		t.Fatalf("peer cluster_peers not filled: %+v", filled.ClusterPeers)
	}

	localKnown := StreamEntry{InternalName: "s1", RequiresAuth: false, RequiresAuthKnown: true}
	if keep := mergeStreamEntry(localKnown, incoming); keep.RequiresAuth {
		t.Fatalf("a locally-known auth bit must not be overwritten by a peer: %+v", keep)
	}
}

func newTestRedis(t *testing.T) (*RedisRegistryStore, goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	return NewRedisRegistryStore(client, "cluster-test"), client, mr
}

func TestMergeStreamEntry_PerLocationNewestWins(t *testing.T) {
	tEarly := time.Unix(100, 0)
	tLate := time.Unix(200, 0)

	// Local view: clusterA freshly admitted (T=late, SourceActive), clusterB stale.
	existing := StreamEntry{
		InternalName: "s1",
		Locations: map[string]Location{
			"A": {ClusterID: "A", UpdatedAt: tLate, SourceActive: true, OwnerNodeID: "nodeA"},
			"B": {ClusterID: "B", UpdatedAt: tEarly},
		},
	}
	// Incoming peer snapshot: fresh for B, but STALE for A (predates the admit).
	incoming := StreamEntry{
		InternalName: "s1",
		Locations: map[string]Location{
			"A": {ClusterID: "A", UpdatedAt: tEarly, SourceActive: false},
			"B": {ClusterID: "B", UpdatedAt: tLate, IsLiveNow: true},
		},
	}

	merged := mergeStreamEntry(existing, incoming)

	// A must keep the fresher local state (not rolled back to SourceActive=false).
	if a := merged.Locations["A"]; !a.SourceActive || a.OwnerNodeID != "nodeA" || !a.UpdatedAt.Equal(tLate) {
		t.Fatalf("location A rolled back by stale snapshot: %+v", a)
	}
	// B must take the fresher incoming state.
	if b := merged.Locations["B"]; !b.IsLiveNow || !b.UpdatedAt.Equal(tLate) {
		t.Fatalf("location B not updated from fresher snapshot: %+v", b)
	}

	// A Location only the incoming side knows is added (union, no tombstones).
	withC := mergeStreamEntry(existing, StreamEntry{
		InternalName: "s1",
		Locations:    map[string]Location{"C": {ClusterID: "C", UpdatedAt: tLate}},
	})
	if _, ok := withC.Locations["C"]; !ok {
		t.Fatal("incoming-only location C should be merged in")
	}
	if a := withC.Locations["A"]; !a.SourceActive {
		t.Fatal("merging an unrelated location must not disturb A")
	}
	// The merge must not mutate the existing entry's map in place.
	if _, leaked := existing.Locations["C"]; leaked {
		t.Fatal("mergeStreamEntry mutated the existing entry's Locations map")
	}
}

func TestRedisRegistryStore_RoundTripsSource(t *testing.T) {
	store, _, _ := newTestRedis(t)

	entry := StreamEntry{
		StreamID:          "stream-1",
		TenantID:          "tenant-1",
		PlaybackID:        "frameworks-demo",
		InternalName:      "60546679b497415db2338cd5cae54992",
		IngestMode:        IngestMistNative,
		RuntimeName:       "60546679b497415db2338cd5cae54992",
		OriginClusterID:   "cluster-test",
		RequiresAuth:      true,
		RequiresAuthKnown: true,
		ClusterPeers:      []*clusterpeerpb.TenantClusterPeer{{ClusterId: "peer-X"}},
		Locations: map[string]Location{
			"cluster-test": {
				ClusterID: "cluster-test",
				IsOrigin:  true,
				IsLiveNow: true,
			},
		},
	}
	change := RegistryChange{InstanceID: "test", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: entry.InternalName}
	if applied, err := store.SetSourceRevisioned(context.Background(), entry, change, 0); err != nil || !applied {
		t.Fatalf("set source: applied=%v err=%v", applied, err)
	}

	sources, err := store.GetAllSources()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := sources[entry.InternalName]
	if !ok {
		t.Fatalf("source not found; got %v", sources)
	}
	if got.RuntimeName != entry.RuntimeName {
		t.Errorf("RuntimeName = %q, want %q", got.RuntimeName, entry.RuntimeName)
	}
	if got.Locations["cluster-test"].IsOrigin != true {
		t.Errorf("Location IsOrigin not round-tripped")
	}
	if !got.RequiresAuth || !got.RequiresAuthKnown {
		t.Errorf("auth identity not round-tripped: %+v", got)
	}
	if len(got.ClusterPeers) != 1 || got.ClusterPeers[0].GetClusterId() != "peer-X" {
		t.Errorf("cluster_peers not round-tripped: %+v", got.ClusterPeers)
	}
}

func TestRedisRegistryStore_SourceTombstoneRejectsStaleWritesAndRehydratesDelete(t *testing.T) {
	store, _, _ := newTestRedis(t)
	const internalName = "revisioned-source"
	entry := StreamEntry{
		InternalName: internalName,
		Locations: map[string]Location{
			"cluster-test": {ClusterID: "cluster-test", SourceActive: true, SourceRevision: 10},
		},
	}
	upsert := RegistryChange{InstanceID: "writer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: 10}
	if applied, err := store.SetSourceRevisioned(context.Background(), entry, upsert, 10); err != nil || !applied {
		t.Fatalf("seed revisioned source: applied=%v err=%v", applied, err)
	}
	// A delete strictly BELOW the watermark is a superseded decision — rejected.
	staleDelete := RegistryChange{InstanceID: "stale", Entity: RegistryEntitySource, Operation: RegistryOpDelete, Key: internalName, SourceRevision: 9}
	if applied, err := store.DeleteSourceRevisioned(context.Background(), internalName, staleDelete, 9); err != nil || applied {
		t.Fatalf("a below-watermark delete removed source ownership: applied=%v err=%v", applied, err)
	}
	// A delete AT the watermark is delete-if-not-superseded: the deleter acted on the latest
	// transition (which left the entry evictable), so it applies. This is the revision every real
	// caller can actually carry — sweeps/withdraws know at most the last written revision, so
	// requiring strictly-higher would make the tombstone path unreachable in production.
	equalDelete := RegistryChange{InstanceID: "writer", Entity: RegistryEntitySource, Operation: RegistryOpDelete, Key: internalName, SourceRevision: 10}
	if applied, err := store.DeleteSourceRevisioned(context.Background(), internalName, equalDelete, 10); err != nil || !applied {
		t.Fatalf("delete at the watermark must apply: applied=%v err=%v", applied, err)
	}
	// The watermark survives the delete, so a DELAYED lower-revision upsert cannot resurrect.
	staleEntry := entry
	staleEntry.Locations = map[string]Location{"cluster-test": {ClusterID: "cluster-test", SourceActive: true, SourceRevision: 9}}
	staleUpsert := RegistryChange{InstanceID: "late", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: 9}
	if applied, err := store.SetSourceRevisioned(context.Background(), staleEntry, staleUpsert, 9); err != nil || applied {
		t.Fatalf("stale upsert after tombstone: applied=%v err=%v", applied, err)
	}

	registry := NewStreamRegistry(nil, "cluster-test", time.Minute)
	projectSourceForTest(t, registry, internalName, "node-stale", 1, "trigger-stale", "generation-stale", 10)
	if _, ok := registry.lookup(registry.byInt, internalName); !ok {
		t.Fatal("test setup did not seed stale in-memory source")
	}
	registry.rehydrateFromRedis(store, logging.NewLogger())
	if _, ok := registry.lookup(registry.byInt, internalName); ok {
		t.Fatal("revisioned Redis tombstone did not remove stale in-memory source during rehydration")
	}
}

func TestStreamRegistry_GenericMutationReconcilesHigherSourceRevision(t *testing.T) {
	store, _, _ := newTestRedis(t)
	const internalName = "reconciled-source"
	latest := StreamEntry{
		InternalName: internalName,
		Locations: map[string]Location{
			"cluster-test": {
				ClusterID: "cluster-test", SourceActive: true, OwnerNodeID: "node-new",
				SourceGeneration: "generation-new", SourceRevision: 10, UpdatedAt: time.Unix(100, 0),
			},
		},
	}
	latestPayload, err := json.Marshal(latest)
	if err != nil {
		t.Fatal(err)
	}
	latestChange := RegistryChange{InstanceID: "winner", Entity: RegistryEntitySource, Operation: RegistryOpUpsert,
		Key: internalName, Payload: latestPayload, SourceRevision: 10}
	if applied, storeErr := store.SetSourceRevisioned(context.Background(), latest, latestChange, 10); storeErr != nil || !applied {
		t.Fatalf("seed latest source: applied=%v err=%v", applied, storeErr)
	}

	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "stale-writer"
	r.mu.Unlock()
	staleMutation := StreamEntry{
		InternalName: internalName,
		Locations: map[string]Location{
			"cluster-test": {
				ClusterID: "cluster-test", SourceActive: true, OwnerNodeID: "node-old",
				SourceGeneration: "generation-old", SourceRevision: 5,
				ReplicatingFrom: "cluster-origin", UpdatedAt: time.Unix(200, 0),
			},
		},
	}
	r.publishUpsertSource(staleMutation)

	durable, found, err := store.GetSource(context.Background(), internalName)
	if err != nil || !found {
		t.Fatalf("read reconciled source: found=%v err=%v", found, err)
	}
	loc := durable.Locations["cluster-test"]
	if loc.SourceRevision != 10 || loc.OwnerNodeID != "node-new" || loc.SourceGeneration != "generation-new" {
		t.Fatalf("source ownership regressed during generic mutation: %+v", loc)
	}
	if loc.ReplicatingFrom != "cluster-origin" {
		t.Fatalf("generic mutation was discarded behind source CAS: %+v", loc)
	}
}

func TestRedisRegistryStore_RoundTripsArtifact(t *testing.T) {
	store, _, _ := newTestRedis(t)

	entry := ArtifactEntry{
		Kind:         ArtifactKindVOD,
		ArtifactHash: "abcd1234",
		InternalName: "vodint1",
		TenantID:     "tenant-1",
		Status:       "ready",
		RuntimeName:  "vod+vodint1",
		HydrationSrc: "sql_artifact",
	}
	if err := store.SetArtifact(entry); err != nil {
		t.Fatal(err)
	}
	arts, err := store.GetAllArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := arts[entry.ArtifactHash]
	if !ok {
		t.Fatalf("artifact not found; got %v", arts)
	}
	if got.RuntimeName != entry.RuntimeName {
		t.Errorf("RuntimeName = %q, want %q", got.RuntimeName, entry.RuntimeName)
	}
}

func TestStreamRegistry_EnableRedisSync_RehydratesOnStartup(t *testing.T) {
	store, _, _ := newTestRedis(t)

	// Seed Redis as if a prior Foghorn instance had written entries.
	prior := StreamEntry{
		StreamID:        "stream-1",
		InternalName:    "60546679b497415db2338cd5cae54992",
		PlaybackID:      "frameworks-demo",
		IngestMode:      IngestMistNative,
		RuntimeName:     "60546679b497415db2338cd5cae54992",
		OriginClusterID: "cluster-test",
		Locations: map[string]Location{
			"cluster-test": {ClusterID: "cluster-test", IsOrigin: true},
		},
		HydratedAt: time.Now(),
	}
	change := RegistryChange{InstanceID: "prior", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: prior.InternalName}
	if applied, err := store.SetSourceRevisioned(context.Background(), prior, change, 0); err != nil || !applied {
		t.Fatalf("seed source: applied=%v err=%v", applied, err)
	}
	priorArt := ArtifactEntry{
		Kind:         ArtifactKindVOD,
		ArtifactHash: "hash-1",
		InternalName: "vodint",
		Status:       "ready",
		RuntimeName:  "vod+vodint",
	}
	if err := store.SetArtifact(priorArt); err != nil {
		t.Fatal(err)
	}

	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	sources, artifacts, err := r.EnableRedisSync(context.Background(), store, "instance-A", logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	if sources != 1 || artifacts != 1 {
		t.Errorf("rehydrate counts (sources=%d artifacts=%d), want (1,1)", sources, artifacts)
	}

	// Source addressable by every key in-memory after rehydrate.
	e, ok := r.lookup(r.byInt, prior.InternalName)
	if !ok {
		t.Fatal("source not in byInt after rehydrate")
	}
	if e.PlaybackID != prior.PlaybackID {
		t.Errorf("PlaybackID = %q", e.PlaybackID)
	}
	// Lookup by playback_id and stream_id also work.
	if _, ok := r.lookup(r.byPlay, prior.PlaybackID); !ok {
		t.Error("missing byPlay index after rehydrate")
	}
	if _, ok := r.lookup(r.byID, prior.StreamID); !ok {
		t.Error("missing byID index after rehydrate")
	}
	// Artifact also addressable.
	if _, ok := r.lookupArtifact(r.artifacts.byHash, priorArt.ArtifactHash); !ok {
		t.Error("artifact not in byHash after rehydrate")
	}
	r.DisableRedisSync()
}

func TestStreamRegistry_CrossInstanceFanout(t *testing.T) {
	// Two Foghorn instances sharing one Redis. Instance A writes; instance
	// B receives the pubsub change and applies it.
	store, _, _ := newTestRedis(t)
	logger := logging.NewLogger()
	ctx := context.Background()

	rA := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-test", time.Minute)
	rB := NewStreamRegistry(nil, "cluster-test", time.Minute)
	if _, _, err := rA.EnableRedisSync(ctx, store, "instance-A", logger); err != nil {
		t.Fatal(err)
	}
	defer rA.DisableRedisSync()
	if _, _, err := rB.EnableRedisSync(ctx, store, "instance-B", logger); err != nil {
		t.Fatal(err)
	}
	defer rB.DisableRedisSync()
	// Give the subscription goroutine a beat to register with miniredis
	// before instance A publishes. Production startup wins on this race
	// because traffic doesn't arrive on the millisecond Foghorn boots,
	// but the test has no such buffer.
	time.Sleep(50 * time.Millisecond)

	// Instance A resolves a stream — write-through publishes to Redis +
	// pubsub. Instance B should observe.
	if _, err := rA.ResolveSourceByInternalName(ctx, "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}

	// Pubsub is async; wait briefly and poll.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := rB.lookup(rB.byInt, "60546679b497415db2338cd5cae54992"); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := rB.lookup(rB.byInt, "60546679b497415db2338cd5cae54992"); !ok {
		t.Fatal("instance B did not see instance A's source upsert via pubsub")
	}
}

func TestStreamRegistry_PublishDoesNotPanicWithoutRedis(t *testing.T) {
	r := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-test", time.Minute)
	// No EnableRedisSync — publish path should be a no-op.
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
}

// The sweep's eviction must fire the durable revisioned delete with the revision the Location held
// BEFORE eviction cleared it. A delete that reads the revision after clearing carries 0, which the
// versioned watermark rejects — no tombstone is emitted, the durable value survives, and the swept
// entry resurrects on the next rehydrate. This drives the REAL caller (SweepStaleLocations →
// publishDeleteSource), not the store API directly.
func TestSweepStaleLocations_EmitsRevisionedDeleteForVersionedSource(t *testing.T) {
	store, _, _ := newTestRedis(t)
	const internalName = "swept-versioned-source"

	// Durable state: the last transition (rev 7) left the source inactive and ownerless — the state
	// that makes the entry evictable.
	durable := StreamEntry{
		InternalName: internalName,
		Locations: map[string]Location{
			"cluster-test": {ClusterID: "cluster-test", SourceRevision: 7},
		},
	}
	seed := RegistryChange{InstanceID: "writer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: 7}
	if applied, err := store.SetSourceRevisioned(context.Background(), durable, seed, 7); err != nil || !applied {
		t.Fatalf("seed durable source: applied=%v err=%v", applied, err)
	}

	// In-memory mirror of the same state, stale past the sweep cutoff.
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "sweeper"
	r.byInt[internalName] = &cachedEntry{
		entry: StreamEntry{
			InternalName: internalName,
			Locations: map[string]Location{
				"cluster-test": {ClusterID: "cluster-test", SourceRevision: 7, UpdatedAt: time.Now().Add(-time.Hour)},
			},
		},
		cached: time.Now(),
	}
	r.mu.Unlock()

	if _, evicted := r.SweepStaleLocations(30 * time.Minute); evicted != 1 {
		t.Fatalf("sweep evicted %d entries, want 1", evicted)
	}

	// The durable value must be GONE — this is the assertion that fails when the delete carries 0.
	if _, found, err := store.GetSource(context.Background(), internalName); err != nil || found {
		t.Fatalf("durable source must be deleted by the sweep's revisioned tombstone (found=%v err=%v)", found, err)
	}
	// The watermark survives as the tombstone, so a delayed lower-revision upsert cannot resurrect.
	if rev, err := store.GetSourceRevision(context.Background(), internalName); err != nil || rev != 7 {
		t.Fatalf("tombstone watermark = %d err=%v, want 7", rev, err)
	}
	stale := durable
	stale.Locations = map[string]Location{"cluster-test": {ClusterID: "cluster-test", SourceActive: true, SourceRevision: 6}}
	lateUpsert := RegistryChange{InstanceID: "late", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: 6}
	if applied, err := store.SetSourceRevisioned(context.Background(), stale, lateUpsert, 6); err != nil || applied {
		t.Fatalf("a stale upsert resurrected the swept source: applied=%v err=%v", applied, err)
	}
	// And a rehydrate must NOT bring the swept entry back.
	r2 := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r2.mu.Lock()
	r2.redisStore = store
	r2.mu.Unlock()
	r2.rehydrateFromRedis(store, logging.NewLogger())
	if _, ok := r2.lookup(r2.byInt, internalName); ok {
		t.Fatal("swept source resurrected on rehydrate despite the revisioned delete")
	}
}

// A federated withdraw evicts an entry whose LOCAL location never existed, so the caller has no
// revision to carry; publishDeleteSource must resolve the durable watermark and delete at it rather
// than emitting a revision-0 delete a versioned watermark would reject.
func TestPublishDeleteSource_FallsBackToDurableWatermark(t *testing.T) {
	store, _, _ := newTestRedis(t)
	const internalName = "withdrawn-federated-source"
	durable := StreamEntry{
		InternalName: internalName,
		Locations: map[string]Location{
			"cluster-test": {ClusterID: "cluster-test", SourceRevision: 4},
		},
	}
	seed := RegistryChange{InstanceID: "writer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: 4}
	if applied, err := store.SetSourceRevisioned(context.Background(), durable, seed, 4); err != nil || !applied {
		t.Fatalf("seed durable source: applied=%v err=%v", applied, err)
	}

	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "withdrawer"
	r.mu.Unlock()
	// The evicted entry carries no local Location (the withdraw removed a peer's), revision unknown.
	r.publishDeleteSource(StreamEntry{InternalName: internalName}, 0)

	if _, found, err := store.GetSource(context.Background(), internalName); err != nil || found {
		t.Fatalf("watermark-fallback delete must remove the durable source (found=%v err=%v)", found, err)
	}
	if rev, err := store.GetSourceRevision(context.Background(), internalName); err != nil || rev != 4 {
		t.Fatalf("tombstone watermark = %d err=%v, want 4", rev, err)
	}
}

// A transiently-failing durable delete must RETAIN the local entry (marked pending) so a later sweep
// retries and eventually settles the tombstone — evicting first would leave nothing to retry from,
// leaking the durable value until it resurrects the entry on a rehydrate. Failure is injected at the
// Redis layer, covering both the watermark lookup and the tombstone script.
func TestSweepStaleLocations_RetainsEntryUntilDurableDeleteSucceeds(t *testing.T) {
	store, _, mr := newTestRedis(t)
	const internalName = "swept-retry-source"

	durable := StreamEntry{
		InternalName: internalName,
		Locations:    map[string]Location{"cluster-test": {ClusterID: "cluster-test", SourceRevision: 7}},
	}
	seed := RegistryChange{InstanceID: "writer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: 7}
	if applied, err := store.SetSourceRevisioned(context.Background(), durable, seed, 7); err != nil || !applied {
		t.Fatalf("seed durable source: applied=%v err=%v", applied, err)
	}

	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "sweeper"
	r.byInt[internalName] = &cachedEntry{
		entry: StreamEntry{
			InternalName: internalName,
			Locations: map[string]Location{
				"cluster-test": {ClusterID: "cluster-test", SourceRevision: 7, UpdatedAt: time.Now().Add(-time.Hour)},
			},
		},
		cached: time.Now(),
	}
	r.mu.Unlock()

	// Redis is down for the first sweep: the delete must fail WITHOUT evicting locally.
	mr.SetError("injected redis outage")
	if _, evicted := r.SweepStaleLocations(30 * time.Minute); evicted != 0 {
		t.Fatalf("sweep evicted %d entries during a failed durable delete, want 0 (retained for retry)", evicted)
	}
	r.mu.RLock()
	ce, present := r.byInt[internalName]
	retained := present && ce.pendingSourceDelete
	r.mu.RUnlock()
	if !retained {
		t.Fatal("entry must be retained and marked pending-delete after a failed durable delete")
	}

	// Redis recovers: the next sweep retries the durable delete and completes the eviction.
	mr.SetError("")
	if _, evicted := r.SweepStaleLocations(30 * time.Minute); evicted != 1 {
		t.Fatalf("retry sweep evicted %d entries, want 1", evicted)
	}
	if _, found, err := store.GetSource(context.Background(), internalName); err != nil || found {
		t.Fatalf("durable source must be deleted after the retry (found=%v err=%v)", found, err)
	}
	if rev, err := store.GetSourceRevision(context.Background(), internalName); err != nil || rev != 7 {
		t.Fatalf("tombstone watermark = %d err=%v, want 7", rev, err)
	}
	r.mu.RLock()
	_, still := r.byInt[internalName]
	r.mu.RUnlock()
	if still {
		t.Fatal("local entry must be evicted once the tombstone is durably published")
	}
}

// A federated withdraw whose durable delete fails must also retain the (marked) entry; the periodic
// sweep then settles it. This injects the failure at the WATERMARK lookup (the withdraw path always
// resolves the watermark, having no local revision).
func TestWithdrawFederatedSource_FailedDeleteRetriedBySweep(t *testing.T) {
	store, _, mr := newTestRedis(t)
	const internalName = "withdrawn-retry-source"

	durable := StreamEntry{
		InternalName: internalName,
		Locations:    map[string]Location{"cluster-test": {ClusterID: "cluster-test", SourceRevision: 4}},
	}
	seed := RegistryChange{InstanceID: "writer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: 4}
	if applied, err := store.SetSourceRevisioned(context.Background(), durable, seed, 4); err != nil || !applied {
		t.Fatalf("seed durable source: applied=%v err=%v", applied, err)
	}

	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "withdrawer"
	r.byInt[internalName] = &cachedEntry{
		entry: StreamEntry{
			InternalName: internalName,
			Locations:    map[string]Location{"peer-B": {ClusterID: "peer-B", UpdatedAt: time.Now()}},
		},
		cached: time.Now(),
	}
	r.mu.Unlock()

	mr.SetError("injected watermark outage")
	r.withdrawFederatedSource("peer-B", internalName)
	r.mu.RLock()
	ce, present := r.byInt[internalName]
	retained := present && ce.pendingSourceDelete && len(ce.entry.Locations) == 0
	r.mu.RUnlock()
	if !retained {
		t.Fatal("withdraw must retain the marked empty entry when the durable delete fails")
	}

	mr.SetError("")
	if _, evicted := r.SweepStaleLocations(30 * time.Minute); evicted != 1 {
		t.Fatalf("sweep retry evicted %d entries, want 1", evicted)
	}
	if _, found, err := store.GetSource(context.Background(), internalName); err != nil || found {
		t.Fatalf("durable source must be deleted after the sweep retry (found=%v err=%v)", found, err)
	}
	if rev, err := store.GetSourceRevision(context.Background(), internalName); err != nil || rev != 4 {
		t.Fatalf("tombstone watermark = %d err=%v, want 4", rev, err)
	}
}
