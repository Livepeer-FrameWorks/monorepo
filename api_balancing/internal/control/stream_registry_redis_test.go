package control

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) (*RedisRegistryStore, goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	return NewRedisRegistryStore(client, "cluster-test"), client, mr
}

func TestRedisRegistryStore_RoundTripsSource(t *testing.T) {
	store, _, _ := newTestRedis(t)

	entry := StreamEntry{
		StreamID:        "stream-1",
		TenantID:        "tenant-1",
		PlaybackID:      "frameworks-demo",
		InternalName:    "60546679b497415db2338cd5cae54992",
		IngestMode:      IngestMistNative,
		RuntimeName:     "60546679b497415db2338cd5cae54992",
		OriginClusterID: "cluster-test",
		Locations: map[string]Location{
			"cluster-test": {
				ClusterID: "cluster-test",
				IsOrigin:  true,
				IsLiveNow: true,
			},
		},
	}
	if err := store.SetSource(entry); err != nil {
		t.Fatal(err)
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
	if err := store.SetSource(prior); err != nil {
		t.Fatal(err)
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
