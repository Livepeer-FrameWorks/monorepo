package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSourceSnapshotScopesTenantAndDetachesRuntimeEvidence(t *testing.T) {
	r := NewStreamRegistry(nil, "cell", time.Minute)
	r.UpsertLocalSource(StreamEntry{TenantID: "tenant", InternalName: "stream", IngestMode: IngestPush,
		Locations: map[string]Location{"cell": {SourceRevision: 9007199254740993, SourceGeneration: "generation", SourceActive: true,
			EdgeCandidates: []EdgeCandidate{{NodeID: "node", SourceObservedAt: 1800000000, DTSCObservedAt: 1800000001}},
			InboundPulls:   map[string]InboundPull{"destination": {AttemptID: "attempt"}},
		}}})
	for _, identity := range [][2]string{{"other", "stream"}, {"tenant", "missing"}, {"", "stream"}, {"tenant", ""}} {
		if _, found, _ := r.SourceSnapshot(context.Background(), identity[0], identity[1]); found {
			t.Fatalf("foreign or incomplete identity resolved: %v", identity)
		}
	}
	got, found, err := r.SourceSnapshot(context.Background(), "tenant", "stream")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.TenantID != "tenant" || got.IngestMode != IngestPush || got.Locations["cell"].SourceRevision != 9007199254740993 {
		t.Fatalf("lost source identity: %+v, %v", got, found)
	}
	loc := got.Locations["cell"]
	loc.EdgeCandidates[0].SourceObservedAt = 1
	loc.InboundPulls["destination"] = InboundPull{AttemptID: "corrupt"}
	delete(got.Locations, "cell")
	stored, _, _ := r.SourceSnapshot(context.Background(), "tenant", "stream")
	if stored.Locations["cell"].EdgeCandidates[0].SourceObservedAt != 1800000000 || stored.Locations["cell"].InboundPulls["destination"].AttemptID != "attempt" {
		t.Fatal("snapshot aliases registry evidence")
	}
}

func TestSourceSnapshotReadsCurrentSharedOwnershipAcrossReplicas(t *testing.T) {
	store, client, _ := newTestRedis(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	stale := NewStreamRegistry(nil, "cluster-test", time.Minute)
	fresh := NewStreamRegistry(nil, "cluster-test", time.Minute)
	entry := StreamEntry{TenantID: "tenant", InternalName: "stream", IngestMode: IngestPush,
		Locations: map[string]Location{"cluster-test": {SourceActive: true, OwnerNodeID: "publisher", SourceGeneration: "old", SourceRevision: 9007199254740993}}}
	stale.UpsertLocalSource(entry)
	stale.redisStore, fresh.redisStore = store, store
	entry.Locations = cloneLocations(entry.Locations)
	loc := entry.Locations["cluster-test"]
	loc.SourceActive, loc.SourceRevision = false, loc.SourceRevision+1
	entry.Locations["cluster-test"] = loc
	if written, err := store.SetSourceRevisioned(ctx, entry, RegistryChange{}, loc.SourceRevision); err != nil || !written {
		t.Fatalf("write withdrawal: %v, %v", written, err)
	}
	for _, reader := range []*StreamRegistry{stale, fresh} {
		got, found, err := reader.SourceSnapshot(ctx, "tenant", "stream")
		if err != nil || !found || got.Locations["cluster-test"].SourceActive || got.Locations["cluster-test"].SourceRevision != 9007199254740994 {
			t.Fatalf("replica ignored shared withdrawal: %+v, %v, %v", got, found, err)
		}
	}
	stale.mu.RLock()
	stillActive := stale.byInt["stream"].entry.Locations["cluster-test"].SourceActive
	stale.mu.RUnlock()
	if !stillActive {
		t.Fatal("test did not retain a lagging process-local publisher")
	}
}

func TestSourceSnapshotCannotUseCacheWhenSharedEvidenceIsMissingOrInvalid(t *testing.T) {
	for _, scenario := range []string{"missing", "corrupt", "foreign-tenant", "foreign-stream", "outage", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			store, client, server := newTestRedis(t)
			t.Cleanup(func() { _ = client.Close() })
			reader := NewStreamRegistry(nil, "cluster-test", time.Minute)
			entry := StreamEntry{TenantID: "tenant", InternalName: "stream", IngestMode: IngestPush,
				Locations: map[string]Location{"cluster-test": {SourceActive: true, OwnerNodeID: "publisher", SourceGeneration: "old", SourceRevision: 9}}}
			reader.UpsertLocalSource(entry)
			reader.redisStore = store
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "corrupt":
				if err := server.Set(store.keySource("stream"), "{"); err != nil {
					t.Fatal(err)
				}
			case "foreign-tenant", "foreign-stream":
				if scenario == "foreign-tenant" {
					entry.TenantID = "other"
				} else {
					entry.InternalName = "other"
				}
				payload, err := json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
				if err := server.Set(store.keySource("stream"), string(payload)); err != nil {
					t.Fatal(err)
				}
			case "outage":
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			got, found, err := reader.SourceSnapshot(ctx, "tenant", "stream")
			if found || got.TenantID != "" || (scenario != "missing" && err == nil) || (scenario == "canceled" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("shared state failure fell back or leaked identity: %+v, %v, %v", got, found, err)
			}
		})
	}
}

// A replica that never cached the stream ends its never-started admission with
// a revisioned inactive write that knows only the internal name. The shared
// entry keeps the tenant identity another replica bound, so a viewer resolve
// reads an ended publisher rather than an identity conflict.
func TestSourceInactiveFromColdReplicaKeepsSharedIdentity(t *testing.T) {
	store, client, _ := newTestRedis(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	admitting := NewStreamRegistry(nil, "cluster-test", time.Minute)
	cold := NewStreamRegistry(nil, "cluster-test", time.Minute)
	admitting.redisStore, cold.redisStore = store, store
	if _, applied, err := admitting.ProjectSource("stream", "publisher", 1, "trigger", "generation", 7); err != nil || !applied {
		t.Fatalf("project admitted source: %v, %v", applied, err)
	}
	admitting.UpsertLocalSource(StreamEntry{TenantID: "tenant", StreamID: "stream-id", PlaybackID: "public", InternalName: "stream", IngestMode: IngestPush})

	if applied, err := cold.PublishSourceInactive("stream", "publisher", "generation", 8); err != nil || !applied {
		t.Fatalf("cold replica inactive publish: %v, %v", applied, err)
	}

	got, found, err := cold.SourceSnapshot(ctx, "tenant", "stream")
	if err != nil || !found {
		t.Fatalf("source snapshot after cold inactive write: found=%v err=%v", found, err)
	}
	loc := got.Locations["cluster-test"]
	if got.TenantID != "tenant" || got.StreamID != "stream-id" || got.PlaybackID != "public" || got.IngestMode != IngestPush ||
		loc.SourceActive || loc.SourceRevision != 8 {
		t.Fatalf("shared entry after cold inactive write = %+v", got)
	}
}

// An entry no writer has bound to a tenant is no tenant's publisher: the
// resolve reads it as absent, not as an identity conflict.
func TestSourceSnapshotTreatsUnboundEntryAsAbsent(t *testing.T) {
	r := NewStreamRegistry(nil, "cell", time.Minute)
	if _, applied, err := r.ProjectSource("stream", "publisher", 1, "trigger", "generation", 3); err != nil || !applied {
		t.Fatalf("project source: %v, %v", applied, err)
	}
	got, found, err := r.SourceSnapshot(context.Background(), "tenant", "stream")
	if err != nil || found || got.TenantID != "" {
		t.Fatalf("unbound entry = %+v, found=%v, err=%v; want absent without error", got, found, err)
	}
}
