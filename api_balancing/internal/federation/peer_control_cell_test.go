package federation

import (
	"context"
	"testing"
	"time"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func TestControlCellAddressUsesExplicitIdentityAcrossReplicas(t *testing.T) {
	cache, redisServer := setupTestCache(t)
	writer := newTestPeerManager(t, "local-cluster", cache, false)
	writer.peerDiscovery = &fakeClusterPeerDiscovery{resp: &quartermasterpb.ListPeersResponse{Peers: []*quartermasterpb.PeerCluster{
		{ClusterId: "eu-virtual", ControlCellId: "eu-cell", FoghornAddr: "b.example:18019", SharedTenantIds: []string{"tenant"}},
		{ClusterId: "eu-second-virtual", ControlCellId: "eu-cell", FoghornAddr: "a.example:18019", SharedTenantIds: []string{"tenant"}},
		{ClusterId: "eu-cell", FoghornAddr: "collision.example:18019", SharedTenantIds: []string{"tenant"}},
	}}}
	writer.refreshPeers()
	reader := newTestPeerManager(t, "local-cluster", cache, false)
	if err := reader.loadPeerAddressesFromRedis(); err != nil {
		t.Fatal(err)
	}
	for _, manager := range []*PeerManager{writer, reader} {
		if got := manager.GetControlCellAddr("eu-cell"); got != "a.example:18019" {
			t.Fatalf("canonical cell address = %q", got)
		}
		if manager.GetPeerAddr("eu-cell") != "collision.example:18019" || manager.GetControlCellAddr("eu-virtual") != "" || manager.GetControlCellAddr(" eu-cell") != "" {
			t.Fatal("canonical and cluster namespaces were conflated")
		}
	}
	redisServer.FastForward(peerAddrTTL + time.Second)
	if err := reader.loadPeerAddressesFromRedis(); err != nil {
		t.Fatal(err)
	}
	if got := reader.GetControlCellAddr("eu-cell"); got != "" {
		t.Fatalf("expired cell mapping retained: %q", got)
	}
}

func TestControlCellAddressFollowsSelectedAddressAuthority(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	pm := newTestPeerManager(t, "local", cache, false)
	for _, cellID := range []string{"old-cell", "replacement-cell", ""} {
		if err := cache.PublishPeerHints(ctx, "quartermaster", map[string]PeerHint{
			"virtual": {Addr: cellID + "host:18019", ControlCellID: cellID, AlwaysOn: true},
		}); err != nil {
			t.Fatal(err)
		}
		if err := pm.loadPeerAddressesFromRedis(); err != nil {
			t.Fatal(err)
		}
		if cellID != "old-cell" && pm.GetControlCellAddr("old-cell") != "" {
			t.Fatal("cell reassignment retained old alias")
		}
		if cellID != "" && pm.GetControlCellAddr(cellID) != cellID+"host:18019" {
			t.Fatal("cell identity separated from current address")
		}
		if cellID == "" && pm.GetControlCellAddr("replacement-cell") != "" {
			t.Fatal("old-version discovery retained canonical identity")
		}
	}
	if err := cache.PublishPeerHints(ctx, "invalid", map[string]PeerHint{"virtual": {Addr: "host:18019", ControlCellID: " padded", AlwaysOn: true}}); err == nil {
		t.Fatal("invalid canonical identity published")
	}
}

func TestPlacementPullRequiresCanonicalSourceAddress(t *testing.T) {
	for _, mapping := range []string{"known", "missing", "unconfigured"} {
		t.Run(mapping, func(t *testing.T) {
			destination, media, _, _, fed, request := pushRuntimeFixture(t)
			media.Arrange.LocalSource = &FederationServer{clusterID: "eu-cell", controlCellID: "us-cell"}
			media.Arrange.PeerResolver = &fakePeerResolver{addrs: map[string]string{"eu-cell": "wrong-cluster-namespace:18019"}}
			media.Arrange.CellAddress = nil
			if mapping != "unconfigured" {
				media.Arrange.CellAddress = func(cellID string) string {
					if cellID != "eu-cell" {
						t.Fatal("source lookup did not use canonical cell")
					}
					if mapping == "known" {
						return "canonical-source:18019"
					}
					return ""
				}
			}
			response, err := destination.PreparePlacement(context.Background(), request)
			if mapping == "known" {
				if err != nil || response == nil || len(fed.calls) != 1 || fed.addresses[0] != "canonical-source:18019" || fed.peerIDs[0] != "eu-cell" {
					t.Fatalf("canonical source preparation failed: %v, %v", response, err)
				}
			} else if err == nil || response != nil || len(fed.calls) != 0 {
				t.Fatalf("missing cell address fell back to cluster lookup: %v, %v", response, err)
			}
		})
	}
}

func TestControlCellAdmissionImportReplacesIdentityWithAddress(t *testing.T) {
	pm := newTestPeerManager(t, "local", nil, false)
	pm.quartermasterHints = map[string]PeerHint{"virtual": {Addr: "first:18019", ControlCellID: "first-cell", AlwaysOn: true}}
	pm.mu.Lock()
	pm.importAdmissionPeerHintsLocked(map[string]PeerHint{"virtual": {Addr: "ignored:18019", AlwaysOn: true}})
	pm.mu.Unlock()
	if pm.GetControlCellAddr("first-cell") != "first:18019" {
		t.Fatal("admission import omitted authoritative identity")
	}
	pm.mu.Lock()
	pm.quartermasterHints["virtual"] = PeerHint{Addr: "replacement:18019", ControlCellID: "replacement-cell", AlwaysOn: true}
	pm.importAdmissionPeerHintsLocked(map[string]PeerHint{"virtual": {Addr: "ignored:18019", AlwaysOn: true}})
	pm.mu.Unlock()
	if pm.GetControlCellAddr("first-cell") != "" || pm.GetControlCellAddr("replacement-cell") != "replacement:18019" {
		t.Fatal("incremental import separated address from cell identity")
	}
	pm.mu.Lock()
	pm.quartermasterHints["virtual"] = PeerHint{Addr: "older-writer:18019", AlwaysOn: true}
	pm.importAdmissionPeerHintsLocked(map[string]PeerHint{"virtual": {Addr: "ignored:18019", AlwaysOn: true}})
	pm.mu.Unlock()
	if pm.GetControlCellAddr("replacement-cell") != "" {
		t.Fatal("import retained canonical alias without current evidence")
	}
}

func TestControlCellHintAggregationKeepsIdentityWithWinningAddress(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	if err := cache.PublishPeerHints(ctx, "membership", map[string]PeerHint{
		"virtual": {Addr: "weak:18019", ControlCellID: "weak-cell", Tenants: []string{"tenant"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, cellID := range []string{"authoritative-cell", ""} {
		if err := cache.PublishPeerHints(ctx, "quartermaster", map[string]PeerHint{
			"virtual": {Addr: "strong:18019", ControlCellID: cellID, AlwaysOn: true, Tenants: []string{"tenant"}},
		}); err != nil {
			t.Fatal(err)
		}
		hints, err := cache.GetPeerAddresses(ctx)
		if err != nil || hints["virtual"].Addr != "strong:18019" || hints["virtual"].ControlCellID != cellID {
			t.Fatalf("hint mixed identity from another address authority: %+v, %v", hints, err)
		}
	}
}
