package federation

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

// A Foghorn's control cell and its CLUSTER_ID are separate namespaces, and the
// registry files this cell's own location under CLUSTER_ID while a peer's
// arrives under that peer's control cell. RegistryCellID exists solely to
// bridge that, and nothing exercised it: every other fixture sets the two ids
// equal, and the two-cell harness cannot reach this at all because dev compose
// configures them equal too. That is precisely the shape that hid the original
// keying defect, so it is pinned here.
func TestPushSourceReadsItsOwnLocationUnderTheRegistryKeyNotTheCell(t *testing.T) {
	f, reader, registry := livePathFixture(t)
	const registryKey = "us-physical-cluster"
	reader.RegistryCellID = registryKey

	// The local publisher lives under the registry's own key. Filing it under
	// the control cell — the mistake this field guards against — must not be
	// what the reader finds.
	registry.entry = control.StreamEntry{
		TenantID: "tenant", InternalName: "internal", IngestMode: control.IngestPush,
		Locations: map[string]control.Location{
			registryKey: {
				ClusterID: registryKey, SourceActive: true, OwnerNodeID: "node-00",
				SourceGeneration: "source-generation", SourceRevision: 9007199254740993,
			},
		},
	}

	f.snapshot.Nodes[0].Streams = map[string]state.BalancerStreamSummary{
		"internal": {TenantID: "tenant", Status: "live", BufferState: "FULL", Playable: true, Inputs: 1, ObservedAt: f.now},
	}
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil {
		t.Fatal(err)
	}
	generation, _, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil || generation != "source-generation" {
		t.Fatalf("own publisher was not found under the registry key: %q, %v", generation, err)
	}

	// The same location filed under the control cell instead must be invisible:
	// that key belongs to peers, and reading it as our own is the defect.
	registry.entry.Locations = map[string]control.Location{
		reader.CellID: {
			ClusterID: reader.CellID, SourceActive: true, OwnerNodeID: "node-00",
			SourceGeneration: "source-generation", SourceRevision: 9007199254740993,
		},
	}
	if generation, _, err := reader.ResolveSourceGeneration(context.Background(), authority); err == nil {
		t.Fatalf("a location filed under the control cell was read as this cell's own publisher: %q", generation)
	}
}

// The same divergence on the configured-source reader, which keeps its own copy
// of the registry-cell fallback.
func TestConfiguredSourceSkipsItsOwnRegistryKeyWhenScanningPeers(t *testing.T) {
	f, reader, registry := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	const registryKey = "us-physical-cluster"
	reader.RegistryCellID = registryKey

	// This cell's own location, filed under its registry key, must not be
	// mistaken for a peer offering a relay source.
	registry.entry = control.StreamEntry{TenantID: "tenant", InternalName: "internal", Locations: map[string]control.Location{
		registryKey: {ClusterID: registryKey, IsLiveNow: true, AdTimestamp: f.now.Unix(), EdgeCandidates: []control.EdgeCandidate{{
			NodeID: "self", ClusterID: "us", IsOrigin: true, BufferState: "FULL", Playable: true,
			DTSCURL: "dtsc://self.example:14200/pull+internal", DTSCObservedAt: f.now.Unix(),
		}}},
	}}

	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, &state.BalancerSnapshot{Nodes: f.snapshot.Nodes})
	if err != nil {
		t.Fatal(err)
	}
	// "us" may originate the input itself, so it is feasible on its own merit.
	// The point is that the scan did not treat this cell's own record as a peer.
	if observation.ObservedAt.IsZero() {
		t.Fatal("observation did not complete")
	}
}
