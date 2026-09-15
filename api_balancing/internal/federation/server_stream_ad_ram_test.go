package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"

	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// A StreamAdvertisement's per-edge fields must survive into the registry's
// EdgeCandidates, read back through the same accessor the placement readers
// use. Identity fields drive relay-source selection; the capacity fields ride
// along and currently have no consumer.
func TestHandleStreamAdvertisement_MapsRAMIntoEdgeCandidates(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})

	prev := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	t.Cleanup(func() { control.StreamRegistryInstance = prev })

	// The sender's CLUSTER_ID is "cluster-b" but its control cell is "cell-b",
	// and the edge sits in the virtual cluster "virtual-b". The location must be
	// filed under the control cell, because that is what every consumer of the
	// key compares against.
	channelCell := ""
	srv.handleStreamAdvertisement(context.Background(), "cluster-b", &foghornfederationpb.StreamAdvertisement{
		InternalName:  "s1",
		TenantId:      "tenant-1",
		ControlCellId: "cell-b",
		IsLive:        true,
		Timestamp:     time.Now().Unix(),
		Edges: []*foghornfederationpb.PeerStreamEdge{
			{NodeId: "edge-1", ClusterId: "virtual-b", BaseUrl: "https://e1", DtscUrl: "dtsc://e1/live+s1", BwAvailable: 1000, RamUsed: 256, RamMax: 1024, Playable: true,
				IsOrigin: true, SourceGeneration: "generation", SourceRevision: 9007199254740993, SourceObservedAt: 1800000000, DtscObservedAt: 1800000001},
		},
	}, &channelCell)

	entry, lookupErr := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), "s1")
	if lookupErr != nil {
		t.Fatalf("advertisement did not reach the registry: %v", lookupErr)
	}
	if _, filedUnderCluster := entry.Locations["cluster-b"]; filedUnderCluster {
		t.Fatalf("location filed under the sender's CLUSTER_ID instead of its control cell: %+v", entry.Locations)
	}
	if len(entry.Locations["cell-b"].EdgeCandidates) != 1 {
		t.Fatalf("locations = %+v, want one edge under cell-b", entry.Locations)
	}
	c := entry.Locations["cell-b"].EdgeCandidates[0]
	if c.RAMUsed != 256 || c.RAMMax != 1024 {
		t.Fatalf("RAM fields lost in ad mapping: %+v", c)
	}
	if c.NodeID != "edge-1" || c.DTSCURL != "dtsc://e1/live+s1" {
		t.Fatalf("edge fields wrong: %+v", c)
	}
	if c.ClusterID != "virtual-b" || !c.Playable || c.SourceGeneration != "generation" || c.SourceRevision != 9007199254740993 || c.SourceObservedAt != 1800000000 || c.DTSCObservedAt != 1800000001 {
		t.Fatalf("virtual cluster or exact publisher revision lost: %+v", c)
	}
}

// An advertisement that does not name its control cell is dropped rather than
// filed under the sender's CLUSTER_ID. Guessing the namespace is what strands
// cross-cell serving: every consumer of the location key compares it against a
// control cell, so a location filed under a physical or virtual cluster id
// matches nothing and the cell silently refuses instead of degrading.
func TestHandleStreamAdvertisementRequiresControlCell(t *testing.T) {
	previous := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "local-cell", time.Minute)
	t.Cleanup(func() { control.StreamRegistryInstance = previous })

	if applyRegistryStreamAdvertisement(&foghornfederationpb.StreamAdvertisement{
		InternalName: "stream", TenantId: "tenant", IsLive: true, Timestamp: time.Now().Unix(),
		Edges: []*foghornfederationpb.PeerStreamEdge{{NodeId: "edge", IsOrigin: true, RamMax: 100}},
	}) {
		t.Fatal("advertisement without a control cell was accepted")
	}
	if _, lookupErr := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), "stream"); lookupErr == nil {
		t.Fatal("dropped advertisement still reached the registry")
	}
}

func TestHandleStreamAdvertisementRejectsInvalidPublisherBindings(t *testing.T) {
	previous := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "local", time.Minute)
	t.Cleanup(func() { control.StreamRegistryInstance = previous })
	applyRegistryStreamAdvertisement(&foghornfederationpb.StreamAdvertisement{
		InternalName: "stream", TenantId: "tenant", ControlCellId: "peer", IsLive: true, Timestamp: time.Now().Unix(),
		Edges: []*foghornfederationpb.PeerStreamEdge{
			nil,
			{NodeId: "replica", SourceGeneration: "gen", SourceRevision: 1},
			{NodeId: "bad-revision", IsOrigin: true, SourceGeneration: "gen"},
			{NodeId: "missing-generation", IsOrigin: true, SourceRevision: 1},
			{NodeId: "legacy", IsOrigin: true, RamMax: 100},
		},
	})
	entry, lookupErr := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), "stream")
	if lookupErr != nil {
		t.Fatalf("advertisement did not reach the registry: %v", lookupErr)
	}
	edges := entry.Locations["peer"].EdgeCandidates
	if len(edges) != 1 || edges[0].NodeID != "legacy" || edges[0].SourceGeneration != "" || edges[0].SourceRevision != 0 || edges[0].ClusterID != "" {
		t.Fatalf("malformed binding accepted or legacy identity invented: %+v", edges)
	}
}

// stubCellAuthority answers the peer -> control cell question the way the peer
// manager does from Quartermaster/Commodore peer records.
type stubCellAuthority struct {
	cells map[string]string
}

func (s *stubCellAuthority) GetPeerAddr(string) string { return "" }

func (s *stubCellAuthority) PeerControlCell(clusterID string) (string, bool) {
	cell, ok := s.cells[clusterID]
	return cell, ok
}

// The registry key is an authority claim: withdrawing the last location for a
// stream tombstones its whole registry entry. PeerChannel identity is
// self-asserted, so a peer must not be able to name a cell it does not belong to.
func TestHandleStreamAdvertisement_RefusesForeignControlCell(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:      testLogger(),
		ClusterID:   "cluster-a",
		Cache:       cache,
		PeerManager: &stubCellAuthority{cells: map[string]string{"cluster-b": "cell-b"}},
	})

	prev := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	t.Cleanup(func() { control.StreamRegistryInstance = prev })

	channelCell := ""
	srv.handleStreamAdvertisement(context.Background(), "cluster-b", &foghornfederationpb.StreamAdvertisement{
		InternalName:  "victim",
		TenantId:      "tenant-1",
		ControlCellId: "cell-victim",
		IsLive:        true,
		Timestamp:     time.Now().Unix(),
	}, &channelCell)

	if _, err := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), "victim"); err == nil {
		t.Fatal("advertisement naming a cell the peer does not belong to reached the registry")
	}
	if channelCell != "" {
		t.Fatalf("a refused advertisement pinned the channel cell to %q", channelCell)
	}
}

// Without positive knowledge the cell is accepted — discovery has to work before
// a membership exists — but it is pinned, so one channel cannot file under one
// cell and then withdraw another's.
func TestHandleStreamAdvertisement_PinsControlCellToChannel(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})

	prev := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	t.Cleanup(func() { control.StreamRegistryInstance = prev })

	channelCell := ""
	first := &foghornfederationpb.StreamAdvertisement{
		InternalName: "s1", TenantId: "tenant-1", ControlCellId: "cell-b",
		IsLive: true, Timestamp: time.Now().Unix(),
	}
	srv.handleStreamAdvertisement(context.Background(), "cluster-b", first, &channelCell)
	if channelCell != "cell-b" {
		t.Fatalf("first advertisement did not pin the channel cell: %q", channelCell)
	}
	if _, err := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), "s1"); err != nil {
		t.Fatalf("unknown-peer advertisement was refused: %v", err)
	}

	// Same channel, different cell: a withdrawal aimed at another cell's records.
	srv.handleStreamAdvertisement(context.Background(), "cluster-b", &foghornfederationpb.StreamAdvertisement{
		InternalName: "s2", TenantId: "tenant-1", ControlCellId: "cell-other",
		IsLive: true, Timestamp: time.Now().Unix(),
	}, &channelCell)

	if _, err := control.StreamRegistryInstance.ResolveSourceByInternalName(context.Background(), "s2"); err == nil {
		t.Fatal("advertisement changing the control cell mid-channel reached the registry")
	}
	if channelCell != "cell-b" {
		t.Fatalf("channel cell moved to %q", channelCell)
	}
}

// A peer must never write this Foghorn's own slot in the Locations map. The
// local publisher projection lives at the registry's LocalLocationKey, and
// UpsertFederatedSource replaces a location wholesale rather than merging, so an
// advertisement filed there drops the source activity and pull bookkeeping the
// local slot carries — and an offline one withdraws it, tombstoning the entry.
// PeerChannel already refuses a caller claiming our cluster id; the cell an
// advertisement names needs the same refusal, including the honest-mistake case
// of a cluster provisioned with another's control cell.
func TestHandleStreamAdvertisement_RefusesOwnIdentity(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:        testLogger(),
		ClusterID:     "cluster-a",
		ControlCellID: "cell-a",
		Cache:         cache,
	})

	prev := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	control.StreamRegistryInstance = registry
	t.Cleanup(func() { control.StreamRegistryInstance = prev })

	// Seed a local publisher projection through the production admission path.
	registry.UpsertLocalSource(control.StreamEntry{
		TenantID: "tenant-1", InternalName: "mine", PlaybackID: "pb-1",
	})
	if _, applied, err := registry.ProjectSource("mine", "node-local", 42, "trigger-uuid", "gen", 1); err != nil || !applied {
		t.Fatalf("seed local source: applied=%v err=%v", applied, err)
	}

	for _, claimed := range []string{"cluster-a", "cell-a"} {
		channelCell := ""
		srv.handleStreamAdvertisement(context.Background(), "cluster-b", &foghornfederationpb.StreamAdvertisement{
			InternalName:  "mine",
			TenantId:      "tenant-1",
			ControlCellId: claimed,
			IsLive:        false,
			Timestamp:     time.Now().Unix(),
		}, &channelCell)

		entry, err := registry.ResolveSourceByInternalName(context.Background(), "mine")
		if err != nil {
			t.Fatalf("claiming %q withdrew the local source entirely: %v", claimed, err)
		}
		local, ok := entry.Locations[registry.LocalLocationKey()]
		if !ok {
			t.Fatalf("claiming %q deleted the local location: %+v", claimed, entry.Locations)
		}
		if local.OwnerNodeID != "node-local" || !local.SourceActive {
			t.Fatalf("claiming %q overwrote the local publisher projection: %+v", claimed, local)
		}
	}

	// Positive control: the same withdrawal, aimed at a cell that is genuinely a
	// peer's, does take effect. Without this the assertions above would also hold
	// if withdrawals simply never worked.
	peerChannelCell := ""
	srv.handleStreamAdvertisement(context.Background(), "cluster-b", &foghornfederationpb.StreamAdvertisement{
		InternalName: "mine", TenantId: "tenant-1", ControlCellId: "cell-b",
		IsLive: true, Timestamp: time.Now().Unix(),
	}, &peerChannelCell)
	entry, err := registry.ResolveSourceByInternalName(context.Background(), "mine")
	if err != nil {
		t.Fatalf("peer advertisement did not reach the registry: %v", err)
	}
	if _, ok := entry.Locations["cell-b"]; !ok {
		t.Fatalf("peer location was never filed: %+v", entry.Locations)
	}
	srv.handleStreamAdvertisement(context.Background(), "cluster-b", &foghornfederationpb.StreamAdvertisement{
		InternalName: "mine", TenantId: "tenant-1", ControlCellId: "cell-b",
		IsLive: false, Timestamp: time.Now().Unix(),
	}, &peerChannelCell)
	entry, err = registry.ResolveSourceByInternalName(context.Background(), "mine")
	if err != nil {
		t.Fatalf("withdrawing the peer location removed the entry: %v", err)
	}
	if _, ok := entry.Locations["cell-b"]; ok {
		t.Fatalf("withdrawal did not take effect, so the refusals above prove nothing: %+v", entry.Locations)
	}
}
