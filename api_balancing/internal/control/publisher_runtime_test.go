package control

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

func TestBeginNodeRuntimeRejectsDelayedRelayPreparation(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	const internal, node, tenant = "handoff", "edge", "tenant"
	publisher, err := streamident.BoundRuntimeName(internal, streamident.RuntimePublisher, "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := streamident.BoundRuntimeName(internal, streamident.RuntimeRelay, "faed1d31-13d2-468d-b7b5-ef132d4e3ee9")
	if err != nil {
		t.Fatal(err)
	}
	r := NewStreamRegistry(nil, "cell", time.Minute)
	r.UpsertLocalSource(StreamEntry{TenantID: tenant, InternalName: internal, IngestMode: IngestPush, Locations: map[string]Location{"cell": {OwnerNodeID: node, SourceActive: true, SourceRuntimeName: publisher}}})
	if !r.BeginNodeRuntime(internal, node, tenant, publisher) {
		t.Fatal("current publisher refused readiness binding")
	}
	if err := sm.UpdateStreamFromBuffer(publisher, internal, node, tenant, "FULL", ""); err != nil {
		t.Fatal(err)
	}
	if r.BeginNodeRuntime(internal, node, tenant, relay) {
		t.Fatal("delayed relay preparation replaced publisher readiness")
	}
	inst := sm.GetStreamInstances(internal)[node]
	if inst.RuntimeName != publisher || !inst.Playable {
		t.Fatalf("stale pull damaged current publisher: %+v", inst)
	}
}

func TestPublisherRuntimeCapabilityComesFromAuthenticatedSession(t *testing.T) {
	if PublisherRuntimeLayout(context.Background()) != 0 {
		t.Fatal("unnegotiated caller enabled runtime layout")
	}
	for _, protocol := range []int32{0, 4, 10, 11, 12} {
		want := int32(0)
		if protocol >= 11 {
			want = 1
		}
		if got := PublisherRuntimeLayout(publisherRuntimeContext(t.Context(), protocol)); got != want {
			t.Fatalf("protocol %d: runtime layout %d, want %d", protocol, got, want)
		}
	}
}

func TestPhysicalRuntimeAdmissionReadsSharedPullWhenReplicaCacheLags(t *testing.T) {
	_, client, _ := newTestRedis(t)
	store := NewRedisRegistryStore(client, "cell")
	reader := NewStreamRegistry(nil, "cell", time.Minute)
	reader.redisStore, reader.instanceID = store, "reader"
	reader.UpsertLocalSource(StreamEntry{InternalName: "stream", TenantID: "tenant", IngestMode: IngestPush})
	writer := NewStreamRegistry(nil, "cell", time.Minute)
	writer.redisStore, writer.instanceID = NewRedisRegistryStore(client, "cell"), "writer"
	generation, attempt := "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9", "faed1d31-13d2-468d-b7b5-ef132d4e3ee9"
	source, err := streamident.BoundRuntimeName("stream", streamident.RuntimePublisher, generation)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := streamident.BoundRuntimeName("stream", streamident.RuntimeRelay, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.RecordInboundPull(t.Context(), "stream", InboundPull{TenantID: "tenant", AttemptID: attempt,
		SourceClusterID: "origin", SourceNodeID: "publisher", SourceGeneration: generation, SourceRevision: 1, SourceLeaseEpoch: 1,
		SourceRuntimeName: source, DestRuntimeName: relay, DestNodeID: "edge", DTSCURL: "dtsc://origin/" + source}); err != nil {
		t.Fatal(err)
	}
	if _, found := reader.InboundPullForNode("stream", "edge"); found {
		t.Fatal("reader fixture already has the newer pull cached")
	}
	if allowed, err := reader.AcceptsNodeRuntime(t.Context(), "stream", "edge", relay); err != nil || !allowed {
		t.Fatalf("lagging replica refused the shared prepared runtime: allowed=%v error=%v", allowed, err)
	}
	if allowed, err := reader.AcceptsNodeRuntime(t.Context(), "stream", "other-edge", relay); err != nil || allowed {
		t.Fatalf("another node borrowed the prepared runtime: allowed=%v error=%v", allowed, err)
	}
}

func TestInactivePhysicalPublisherCanReportItsOwnEndWithoutReadmitting(t *testing.T) {
	const runtime = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	r := NewStreamRegistry(nil, "cell", time.Minute)
	loc := Location{OwnerNodeID: "edge", SourceRuntimeName: runtime, SourceLeaseEpoch: 1, SourceActive: false}
	r.UpsertLocalSource(StreamEntry{InternalName: "stream", TenantID: "tenant", IngestMode: IngestPush, Locations: map[string]Location{"cell": loc}})
	if allowed, err := r.AcceptsNodeRuntime(t.Context(), "stream", "edge", runtime, true); err != nil || !allowed {
		t.Fatalf("publisher close suppressed its final end report: allowed=%v error=%v", allowed, err)
	}
	if allowed, err := r.AcceptsNodeRuntime(t.Context(), "stream", "edge", runtime); err != nil || allowed {
		t.Fatalf("terminal permission readmitted an ended source: allowed=%v error=%v", allowed, err)
	}
	r.UpsertFederatedSource("other", StreamEntry{InternalName: "stream", TenantID: "tenant"}, Location{OwnerNodeID: "new-edge", SourceGeneration: "new", SourceLeaseEpoch: 2, SourceRevision: 1, SourceActive: true})
	if allowed, err := r.AcceptsNodeRuntime(t.Context(), "stream", "edge", runtime, true); err != nil || allowed {
		t.Fatalf("old end could retire a globally newer source: allowed=%v error=%v", allowed, err)
	}
}
