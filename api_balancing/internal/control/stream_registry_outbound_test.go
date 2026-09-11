package control

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func testOutbound(node string) OutboundPull {
	return OutboundPull{TenantID: "tenant", DestClusterID: "destination", DestNodeID: node, SourceNodeID: "source-node", DTSCURL: "dtsc://source/live+stream"}
}

func TestOutboundAcceptanceAndExpiryAreAttemptFenced(t *testing.T) {
	store, _, _ := newTestRedis(t)
	ctx := context.Background()
	replica := func(id string) *StreamRegistry {
		r := NewStreamRegistry(nil, "cluster-test", time.Minute)
		r.redisStore, r.instanceID = store, id
		return r
	}
	first, err := replica("first").RecordOutboundPull(ctx, "stream", testOutbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := replica("retry").RecordOutboundPull(ctx, "stream", testOutbound("edge"))
	if err != nil || first.AttemptID != retry.AttemptID || !first.CreatedAt.Equal(retry.CreatedAt) {
		t.Fatalf("retry replaced attempt: %+v %v", retry, err)
	}
	// Equality must retain a renewal even when both writers share a clock tick.
	if clearErr := replica("expiry").ClearOutboundPull(ctx, "stream", first.DestClusterID, first.DestNodeID, first.AttemptID, retry.UpdatedAt); !errors.Is(clearErr, ErrReplicationConflict) {
		t.Fatalf("expiry erased renewal: %v", clearErr)
	}
	if clearErr := replica("clear").ClearOutboundPull(ctx, "stream", first.DestClusterID, first.DestNodeID, first.AttemptID, time.Time{}); clearErr != nil {
		t.Fatal(clearErr)
	}
	replacement, err := replica("replacement").RecordOutboundPull(ctx, "stream", testOutbound("edge"))
	if err != nil || replacement.AttemptID == first.AttemptID {
		t.Fatalf("replacement lacks new attempt: %+v %v", replacement, err)
	}
	if err := replica("late").ClearOutboundPull(ctx, "stream", first.DestClusterID, first.DestNodeID, first.AttemptID, time.Time{}); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("late completion erased replacement: %v", err)
	}
	conflict := replacement
	conflict.SourceNodeID = "another-source"
	if _, err := replica("conflict").RecordOutboundPull(ctx, "stream", conflict); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("same attempt changed source: %v", err)
	}
	conflict = replacement
	conflict.TenantID = "another-tenant"
	if _, err := replica("tenant").RecordOutboundPull(ctx, "stream", conflict); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("cross-tenant handoff: %v", err)
	}
}

func TestOutboundConcurrentWritersAndStaleSnapshots(t *testing.T) {
	store, _, _ := newTestRedis(t)
	ctx := context.Background()
	replica := func(id string) *StreamRegistry {
		r := NewStreamRegistry(nil, "cluster-test", time.Minute)
		r.redisStore, r.instanceID = store, id
		return r
	}
	var workers sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		workers.Go(func() {
			_, err := replica(fmt.Sprintf("writer-%d", i)).RecordOutboundPull(ctx, "stream", testOutbound(fmt.Sprintf("edge-%d", i)))
			results <- err
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	stale, found, err := store.GetSource(ctx, "stream")
	if err != nil || !found || len(stale.Locations["cluster-test"].OutboundPullers) != 8 {
		t.Fatalf("lost concurrent handoffs: %+v %v", stale, err)
	}
	first := stale.Locations["cluster-test"].OutboundPullers[0]
	if clearErr := replica("clear").ClearOutboundPull(ctx, "stream", first.DestClusterID, first.DestNodeID, first.AttemptID, time.Time{}); clearErr != nil {
		t.Fatal(clearErr)
	}
	loc := stale.Locations["cluster-test"]
	loc.UpdatedAt = time.Now().Add(time.Hour)
	stale.Locations["cluster-test"] = loc
	if applied, setErr := store.SetSourceRevisioned(ctx, stale, RegistryChange{Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: "stream"}, 0); setErr != nil || !applied {
		t.Fatalf("stale metadata: %t %v", applied, setErr)
	}
	latest, _, err := store.GetSource(ctx, "stream")
	if err != nil || len(latest.Locations["cluster-test"].OutboundPullers) != 7 || latest.Locations["cluster-test"].OutboundRevision <= stale.Locations["cluster-test"].OutboundRevision {
		t.Fatalf("stale snapshot resurrected cleared handoff: %+v %v", latest, err)
	}
	merged := mergeStreamEntry(latest, stale)
	if len(merged.Locations["cluster-test"].OutboundPullers) != 7 {
		t.Fatal("changelog merge lost outbound revision")
	}
	for _, pull := range latest.Locations["cluster-test"].OutboundPullers {
		if clearErr := replica("clear-all").ClearOutboundPull(ctx, "stream", pull.DestClusterID, pull.DestNodeID, pull.AttemptID, time.Time{}); clearErr != nil {
			t.Fatal(clearErr)
		}
	}
	if applied, setErr := store.SetSourceRevisioned(ctx, stale, RegistryChange{Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: "stream"}, 0); setErr != nil || !applied {
		t.Fatal(setErr)
	}
	empty, _, err := store.GetSource(ctx, "stream")
	if err != nil || len(empty.Locations["cluster-test"].OutboundPullers) != 0 || empty.Locations["cluster-test"].OutboundRevision == 0 {
		t.Fatalf("empty set lost anti-replay fence: %+v %v", empty, err)
	}
}

func TestOutboundWriteFailureCannotBecomeLocallyAccepted(t *testing.T) {
	store, _, server := newTestRedis(t)
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.redisStore = store
	server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.RecordOutboundPull(ctx, "stream", testOutbound("edge")); err == nil {
		t.Fatal("unpersisted handoff accepted")
	}
	if len(r.Snapshot()) != 0 {
		t.Fatal("failed handoff leaked into local state")
	}
}

func TestOutboundGenerationMustMatchActiveSource(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	pull := testOutbound("edge")
	pull.SourceGeneration = "generation"
	pull.SourceRevision = 1
	if _, err := r.RecordOutboundPull(context.Background(), "stream", pull); !errors.Is(err, ErrReplicationConflict) {
		t.Fatalf("unproven source generation accepted: %v", err)
	}
}

func TestOutboundSweeperPersistsExpiryWithoutErasingFreshDestinations(t *testing.T) {
	store, _, _ := newTestRedis(t)
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.redisStore = store
	ctx := context.Background()
	for _, node := range []string{"expired", "fresh"} {
		if _, err := r.RecordOutboundPull(ctx, "stream", testOutbound(node)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.mutateOutbound(ctx, "stream", func(_ *StreamEntry, loc *Location) error {
		for i := range loc.OutboundPullers {
			if loc.OutboundPullers[i].DestNodeID == "expired" {
				loc.OutboundPullers[i].UpdatedAt = time.Now().Add(-time.Hour)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.SweepStaleLocations(time.Minute)
	latest, found, err := store.GetSource(ctx, "stream")
	if err != nil || !found {
		t.Fatal(err)
	}
	pulls := latest.Locations["cluster-test"].OutboundPullers
	if len(pulls) != 1 || pulls[0].DestNodeID != "fresh" {
		t.Fatalf("expiry lost live destinations or was not persisted: %+v", pulls)
	}
}

func TestRegistryCASPreservesFullInt64RevisionOrdering(t *testing.T) {
	for _, revision := range []int64{1<<53 + 1, math.MaxInt64} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			store, _, _ := newTestRedis(t)
			ctx := context.Background()
			entry := StreamEntry{InternalName: "stream"}
			change := RegistryChange{Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: "stream", SourceRevision: revision}
			if applied, err := store.SetSourceRevisioned(ctx, entry, change, revision); err != nil || !applied {
				t.Fatalf("initial revision: %t %v", applied, err)
			}
			change.SourceRevision--
			if applied, err := store.SetSourceRevisioned(ctx, entry, change, revision-1); err != nil || applied {
				t.Fatalf("older write accepted: %t %v", applied, err)
			}
			change.Operation = RegistryOpDelete
			if applied, err := store.DeleteSourceRevisioned(ctx, "stream", change, revision-1); err != nil || applied {
				t.Fatalf("older delete accepted: %t %v", applied, err)
			}
			if got, err := store.SourceRevision(ctx, "stream"); err != nil || got != revision {
				t.Fatalf("revision changed: %d %v", got, err)
			}
		})
	}
}

func TestRegistryCASRejectsCorruptIdentityAndWatermark(t *testing.T) {
	store, _, engine := newTestRedis(t)
	ctx := context.Background()
	if err := engine.Set(store.keySource("stream"), `{"InternalName":"other-stream"}`); err != nil {
		t.Fatal(err)
	}
	r := NewStreamRegistry(nil, "cluster-test", time.Minute)
	r.redisStore = store
	if _, err := r.RecordOutboundPull(ctx, "stream", testOutbound("edge")); err == nil {
		t.Fatal("corrupt snapshot changed a different identity")
	}
	engine.Del(store.keySource("stream"))
	if err := engine.Set(store.keySourceRevision("stream"), "01"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordOutboundPull(ctx, "stream", testOutbound("edge")); err == nil {
		t.Fatal("noncanonical watermark accepted")
	}
}
