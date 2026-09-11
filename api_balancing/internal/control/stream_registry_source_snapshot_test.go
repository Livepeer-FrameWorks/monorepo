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
