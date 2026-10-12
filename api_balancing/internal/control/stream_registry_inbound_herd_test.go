package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2/server"
)

// inboundHerdReplicas builds control replicas that share one durable store, as
// the Foghorn replicas of a cell do.
func inboundHerdReplicas(store *RedisRegistryStore, count int) []*StreamRegistry {
	replicas := make([]*StreamRegistry, count)
	for i := range replicas {
		replica := NewStreamRegistry(nil, "cluster-test", time.Minute)
		replica.redisStore, replica.instanceID = store, fmt.Sprintf("replica-%d", i)
		replicas[i] = replica
	}
	return replicas
}

func registryChangelogLength(t *testing.T, store *RedisRegistryStore) int64 {
	t.Helper()
	length, err := store.client.XLen(context.Background(), store.changelog.Key()).Result()
	if err != nil {
		t.Fatal(err)
	}
	return length
}

// A viewer herd on one stream and destination node hands out the same physical
// pull to every viewer. Each preparation marks the pull placement-required and
// retains request context; once the pull carries both, a further preparation
// has nothing to change and must not compete for the stream's record.
func TestInboundPullViewerHerdMutatesAtMostOnce(t *testing.T) {
	store, _, _ := newTestRedis(t)
	assertInboundHerdMutatesAtMostOnce(t, store)
}

func assertInboundHerdMutatesAtMostOnce(t *testing.T, store *RedisRegistryStore) {
	t.Helper()
	ctx := context.Background()
	replicas := inboundHerdReplicas(store, 6)
	pull, err := replicas[0].RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	if err = replicas[0].RequireInboundPlacement(ctx, "stream", pull); err != nil {
		t.Fatal(err)
	}
	before := registryChangelogLength(t, store)

	const viewers = 240
	var wg sync.WaitGroup
	errs := make(chan error, 2*viewers)
	start := make(chan struct{})
	for viewer := range viewers {
		wg.Add(1)
		go func(replica *StreamRegistry) {
			defer wg.Done()
			<-start
			if requireErr := replica.RequireInboundPlacement(ctx, "stream", pull); requireErr != nil {
				errs <- fmt.Errorf("require placement: %w", requireErr)
			}
			if demandErr := replica.RecordInboundPlacementDemand(ctx, "stream", pull, "viewer-context", nil); demandErr != nil {
				errs <- fmt.Errorf("retain demand: %w", demandErr)
			}
		}(replicas[viewer%len(replicas)])
	}
	close(start)
	wg.Wait()
	close(errs)
	failed := 0
	var first error
	for herdErr := range errs {
		if first == nil {
			first = herdErr
		}
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d of %d herd mutations failed, first: %v", failed, 2*viewers, first)
	}
	if writes := registryChangelogLength(t, store) - before; writes > 1 {
		t.Fatalf("herd wrote the stream record %d times, want at most 1", writes)
	}
	current, found, err := replicas[1].CurrentInboundPull(ctx, "stream", "edge")
	if err != nil || !found || current.PlacementDemand != "viewer-context" || !current.PlacementRequired || current.AttemptID != pull.AttemptID {
		t.Fatalf("herd lost the retained pull state: %+v, %v", current, err)
	}
}

// Viewers of one pull carry different request context (their location). The
// caller decides when stored context already serves the request; a demand it
// accepts is kept rather than rewritten by every viewer.
func TestInboundPlacementDemandKeepsEquivalentStoredContext(t *testing.T) {
	store, _, _ := newTestRedis(t)
	ctx := context.Background()
	replicas := inboundHerdReplicas(store, 3)
	pull, err := replicas[0].RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	if err = replicas[0].RecordInboundPlacementDemand(ctx, "stream", pull, "viewer-0", nil); err != nil {
		t.Fatal(err)
	}
	before := registryChangelogLength(t, store)
	keep := func(stored string) bool { return stored == "viewer-0" }
	var wg sync.WaitGroup
	var failures sync.Map
	for viewer := 1; viewer <= 90; viewer++ {
		wg.Add(1)
		go func(replica *StreamRegistry, demand string) {
			defer wg.Done()
			if demandErr := replica.RecordInboundPlacementDemand(ctx, "stream", pull, demand, keep); demandErr != nil {
				failures.Store(demand, demandErr)
			}
		}(replicas[viewer%len(replicas)], fmt.Sprintf("viewer-%d", viewer))
	}
	wg.Wait()
	failures.Range(func(demand, failure any) bool {
		t.Errorf("%s: %v", demand, failure)
		return true
	})
	if writes := registryChangelogLength(t, store) - before; writes != 0 {
		t.Fatalf("equivalent demands wrote the stream record %d times", writes)
	}
	if err = replicas[2].RecordInboundPlacementDemand(ctx, "stream", pull, "viewer-new", func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	current, _, err := replicas[0].CurrentInboundPull(ctx, "stream", "edge")
	if err != nil || current.PlacementDemand != "viewer-new" {
		t.Fatalf("non-equivalent demand was not retained: %+v, %v", current, err)
	}
}

// Contention that outlasts the compare-and-set retries is a retryable state,
// distinguishable from a fenced conflict or a storage failure.
func TestInboundPullContentionExhaustionIsRetryable(t *testing.T) {
	store, _, mr := newTestRedis(t)
	ctx := context.Background()
	registry := inboundHerdReplicas(store, 1)[0]
	pull, err := registry.RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	// Every compare-and-set reports that another writer changed the record first.
	mr.Server().SetPreHook(func(peer *server.Peer, command string, _ ...string) bool {
		if command == "EVAL" || command == "EVALSHA" {
			peer.WriteInt(2)
			return true
		}
		return false
	})
	err = registry.RecordInboundPlacementDemand(ctx, "stream", pull, "viewer-context", nil)
	if !errors.Is(err, ErrReplicationContention) {
		t.Fatalf("exhausted contention = %v, want ErrReplicationContention", err)
	}
	if errors.Is(err, ErrReplicationConflict) {
		t.Fatal("contention was reported as a superseded attempt")
	}
}
