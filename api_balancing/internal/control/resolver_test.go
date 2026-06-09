package control

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/state"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// fakeLoadBalancer is a minimal LoadBalancerInterface for placement tests: it
// returns a fixed node set and resolves base URLs from it.
type fakeLoadBalancer struct {
	nodes map[string]state.NodeState
}

func (f *fakeLoadBalancer) GetNodes() map[string]state.NodeState { return f.nodes }
func (f *fakeLoadBalancer) GetNodeByID(nodeID string) (string, error) {
	for _, n := range f.nodes {
		if n.NodeID == nodeID {
			return n.BaseURL, nil
		}
	}
	return "", errors.New("not found")
}
func (f *fakeLoadBalancer) GetNodeIDByHost(host string) string {
	for _, n := range f.nodes {
		if n.BaseURL == host {
			return n.NodeID
		}
	}
	return ""
}

// TestResolveStream pins the central resolution contract: input shape →
// canonical Mist stream name + auth/tenant context. The prefix and ingest-mode
// branches encode load-bearing routing invariants (live+/pull+/bare-native,
// vod+ vs dvr+, hash fallback), so a regression here misroutes playback.
func TestResolveStream(t *testing.T) {
	ctx := context.Background()
	const hashInput = "abcdef0123456789abcdef0123456789" // 32 hex chars

	t.Run("live+ propagates commodore context", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			internalName: func(_ context.Context, req *commodorepb.ResolveInternalNameRequest) (*commodorepb.ResolveInternalNameResponse, error) {
				if req.GetInternalName() != "abc" {
					t.Errorf("expected stripped internal name abc, got %q", req.GetInternalName())
				}
				return &commodorepb.ResolveInternalNameResponse{TenantId: "t1", StreamId: "s1", RequiresAuth: true}, nil
			},
		})
		got, err := ResolveStream(ctx, "live+abc")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.InternalName != "live+abc" || got.TenantID != "t1" || got.StreamID != "s1" ||
			got.ContentType != "live" || !got.RequiresAuth || !got.RequiresAuthKnown {
			t.Fatalf("unexpected target: %+v", got)
		}
	})

	t.Run("vod+ resolves artifact and is VOD", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactInternal: func(_ context.Context, _ *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
				return &commodorepb.ResolveArtifactInternalNameResponse{Found: true, TenantId: "t1", ContentType: "vod", ArtifactHash: hashInput}, nil
			},
		})
		got, err := ResolveStream(ctx, "vod+name")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.InternalName != "vod+name" || !got.IsVod || got.TenantID != "t1" {
			t.Fatalf("unexpected target: %+v", got)
		}
	})

	t.Run("dvr+ resolves only when content type is dvr", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactInternal: func(_ context.Context, _ *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
				return &commodorepb.ResolveArtifactInternalNameResponse{Found: true, TenantId: "t1", ContentType: "dvr"}, nil
			},
		})
		got, err := ResolveStream(ctx, "dvr+token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.InternalName != "dvr+token" || got.IsVod || got.ContentType != "dvr" {
			t.Fatalf("unexpected target: %+v", got)
		}
	})

	t.Run("dvr+ token that is not a dvr artifact returns sentinel error", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactInternal: func(_ context.Context, _ *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
				// Found, but it's a VOD — the legacy dvr+<chapter_id> shape must
				// not masquerade as a DVR target.
				return &commodorepb.ResolveArtifactInternalNameResponse{Found: true, ContentType: "vod"}, nil
			},
		})
		got, err := ResolveStream(ctx, "dvr+notadvr")
		if err == nil {
			t.Fatal("expected sentinel error for non-dvr token")
		}
		if got.InternalName != "" {
			t.Fatalf("expected empty sentinel target, got %+v", got)
		}
	})

	t.Run("artifact playback id: clip -> vod+, dvr -> dvr+", func(t *testing.T) {
		for _, tc := range []struct {
			contentType string
			wantPrefix  string
			wantVod     bool
		}{
			{"clip", "vod+", true},
			{"dvr", "dvr+", false},
		} {
			startFakeCommodoreServer(t, &fakeCommodoreInternal{
				artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
					return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, InternalName: "art", ContentType: tc.contentType, TenantId: "t1"}, nil
				},
			})
			got, err := ResolveStream(ctx, "pbid")
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.contentType, err)
			}
			if got.InternalName != tc.wantPrefix+"art" || got.IsVod != tc.wantVod || got.ContentType != tc.contentType {
				t.Fatalf("%s: unexpected target: %+v", tc.contentType, got)
			}
		}
	})

	t.Run("hash candidate falls back to hash resolution", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
			},
			dvrHash: func(_ context.Context, req *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				if req.GetDvrHash() != hashInput {
					t.Errorf("hash not forwarded: %q", req.GetDvrHash())
				}
				return &commodorepb.ResolveDVRHashResponse{Found: true, InternalName: "rec", TenantId: "t1", StreamId: "s1"}, nil
			},
		})
		got, err := ResolveStream(ctx, hashInput)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.InternalName != "dvr+rec" || got.ContentType != "dvr" || got.IsVod {
			t.Fatalf("unexpected target: %+v", got)
		}
	})

	t.Run("playback id ingest modes select prefix", func(t *testing.T) {
		for _, tc := range []struct {
			ingestMode string
			want       string
		}{
			{"pull", "pull+rec"},
			{"mist_native", "rec"},
			{"", "live+rec"},
		} {
			startFakeCommodoreServer(t, &fakeCommodoreInternal{
				playbackID: func(_ context.Context, _ *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
					return &commodorepb.ResolvePlaybackIDResponse{InternalName: "rec", IngestMode: tc.ingestMode, TenantId: "t1"}, nil
				},
			})
			got, err := ResolveStream(ctx, "pbid")
			if err != nil {
				t.Fatalf("mode %q: unexpected error: %v", tc.ingestMode, err)
			}
			if got.InternalName != tc.want || got.ContentType != "live" {
				t.Fatalf("mode %q: got %+v, want internal %q", tc.ingestMode, got, tc.want)
			}
		}
	})

	t.Run("nothing matches yields empty target", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			playbackID: func(_ context.Context, _ *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
				return nil, errors.New("not found")
			},
		})
		got, err := ResolveStream(ctx, "ghost")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.InternalName != "" {
			t.Fatalf("expected empty target, got %+v", got)
		}
	})
}

// TestResolveArtifactHashStreamTarget pins the DVR→clip→VOD first-hit ordering
// of hash resolution and the all-miss nil contract.
func TestResolveArtifactHashStreamTarget(t *testing.T) {
	ctx := context.Background()
	const h = "abcdef0123456789abcdef0123456789"

	t.Run("nil client returns nil", func(t *testing.T) {
		prev := CommodoreClient
		CommodoreClient = nil
		t.Cleanup(func() { CommodoreClient = prev })
		if got := resolveArtifactHashStreamTarget(ctx, h); got != nil {
			t.Fatalf("expected nil with no client, got %+v", got)
		}
	})

	t.Run("clip hit when dvr misses", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			clipHash: func(_ context.Context, _ *commodorepb.ResolveClipHashRequest) (*commodorepb.ResolveClipHashResponse, error) {
				return &commodorepb.ResolveClipHashResponse{Found: true, InternalName: "clip1", TenantId: "t1"}, nil
			},
		})
		got := resolveArtifactHashStreamTarget(ctx, h)
		if got == nil || got.InternalName != "vod+clip1" || got.ContentType != "clip" || !got.IsVod {
			t.Fatalf("unexpected target: %+v", got)
		}
	})

	t.Run("all miss returns nil", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		if got := resolveArtifactHashStreamTarget(ctx, h); got != nil {
			t.Fatalf("expected nil when all hash lookups miss, got %+v", got)
		}
	})
}

// TestResolveArtifactPolicy pins the auth/peer enrichment helper: guards return
// the not-known triple, and a found artifact surfaces its requires-auth flag and
// cluster peers so the caller can gate playback.
func TestResolveArtifactPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("empty name guard", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		auth, known, peers := resolveArtifactPolicy(ctx, "")
		if auth || known || peers != nil {
			t.Fatalf("empty name must return (false,false,nil), got (%v,%v,%v)", auth, known, peers)
		}
	})

	t.Run("not found returns not-known", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactInternal: func(_ context.Context, _ *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
				return &commodorepb.ResolveArtifactInternalNameResponse{Found: false}, nil
			},
		})
		auth, known, _ := resolveArtifactPolicy(ctx, "art")
		if auth || known {
			t.Fatalf("not-found must return not-known, got (%v,%v)", auth, known)
		}
	})

	t.Run("found surfaces requires-auth", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactInternal: func(_ context.Context, _ *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
				return &commodorepb.ResolveArtifactInternalNameResponse{Found: true, RequiresAuth: true}, nil
			},
		})
		auth, known, _ := resolveArtifactPolicy(ctx, "art")
		if !auth || !known {
			t.Fatalf("found requires-auth must return (true,true), got (%v,%v)", auth, known)
		}
	})
}

// TestApplyArtifactPlacement pins the placement decision: among warm local
// holders the idlest (highest-score) node wins; on a cache miss the synced S3
// row pins a storage-capable healthy edge for read-through; a miss with no
// synced row leaves the target unpinned (dynamic).
func TestApplyArtifactPlacement(t *testing.T) {
	ctx := context.Background()
	const h = "abcdef0123456789abcdef0123456789"

	t.Run("nil target and empty hash are no-ops", func(t *testing.T) {
		applyArtifactPlacement(ctx, h, nil) // must not panic
		tgt := &StreamTarget{}
		applyArtifactPlacement(ctx, "", tgt)
		if tgt.FixedNode != "" || tgt.FixedNodeID != "" {
			t.Fatalf("empty hash must not pin a node, got %+v", tgt)
		}
	})

	t.Run("warm node picks idlest holder (highest score)", func(t *testing.T) {
		// Score is an idleness scale (CPUScore = WeightCPU - load term), so the
		// idler holder has the higher combined score — same direction as the
		// main balancer's rate().
		_, _, _ = setupArtifactTestDeps(t) // resets state manager + swaps deps
		sm := state.DefaultManager()
		for _, n := range []struct {
			id   string
			cpu  float64
			host string
		}{{"n-busy", 90, "https://busy"}, {"n-idle", 5, "https://idle"}} {
			sm.SetNodeInfo(n.id, n.host, true, nil, nil, "", "", nil)
			sm.UpdateNodeMetrics(n.id, struct {
				CPU                  float64
				RAMMax               float64
				RAMCurrent           float64
				UpSpeed              float64
				DownSpeed            float64
				BWLimit              float64
				CapIngest            bool
				CapEdge              bool
				CapStorage           bool
				CapProcessing        bool
				Roles                []string
				StorageCapacityBytes uint64
				StorageUsedBytes     uint64
				ProcessingClasses    map[string]state.ClassCapacity
			}{CPU: n.cpu})
			sm.TouchNode(n.id, true)
			sm.SetProbeVerified(n.id, true)
			sm.SetNodeArtifacts(n.id, []*ipcpb.StoredArtifact{{ClipHash: h}})
		}

		tgt := &StreamTarget{}
		applyArtifactPlacement(ctx, h, tgt)
		if tgt.FixedNodeID != "n-idle" {
			t.Fatalf("expected idlest holder n-idle, got %q (%+v)", tgt.FixedNodeID, tgt)
		}
	})

	t.Run("cache miss pins synced storage edge", func(t *testing.T) {
		_, _, repo := setupArtifactTestDeps(t)
		repo.getArtifactSyncInfoFn = func(_ context.Context, hash string) (*state.ArtifactSyncInfo, error) {
			return &state.ArtifactSyncInfo{ArtifactHash: hash, ArtifactType: "vod", SyncStatus: "synced", S3URL: "s3://b/k"}, nil
		}
		prevLB := loadBalancerInstance
		loadBalancerInstance = &fakeLoadBalancer{nodes: map[string]state.NodeState{
			"edge": {NodeID: "edge", BaseURL: "https://edge", CapStorage: true, IsHealthy: true},
		}}
		t.Cleanup(func() { loadBalancerInstance = prevLB })

		tgt := &StreamTarget{}
		applyArtifactPlacement(ctx, h, tgt)
		if tgt.FixedNodeID != "edge" || tgt.FixedNode != "https://edge" || tgt.ContentType != "vod" {
			t.Fatalf("expected synced edge placement, got %+v", tgt)
		}
	})

	t.Run("cache miss with no synced row leaves target unpinned", func(t *testing.T) {
		_, _, repo := setupArtifactTestDeps(t)
		repo.getArtifactSyncInfoFn = func(_ context.Context, _ string) (*state.ArtifactSyncInfo, error) {
			return nil, nil // not synced
		}
		tgt := &StreamTarget{}
		applyArtifactPlacement(ctx, h, tgt)
		if tgt.FixedNode != "" || tgt.FixedNodeID != "" {
			t.Fatalf("unsynced miss must leave target dynamic, got %+v", tgt)
		}
	})
}

// TestResolveArtifactByHash pins the internal-only resolver: empty hash guard
// and the clip/dvr/vod hit chain that stamps tenant + content type.
func TestResolveArtifactByHash(t *testing.T) {
	ctx := context.Background()
	const h = "abcdef0123456789abcdef0123456789"

	t.Run("empty hash guard", func(t *testing.T) {
		got, err := ResolveArtifactByHash(ctx, "")
		if err != nil || got.InternalName != "" || got.TenantID != "" {
			t.Fatalf("empty hash must return empty target, got %+v err %v", got, err)
		}
	})

	t.Run("dvr hit stamps content type", func(t *testing.T) {
		_, _, _ = setupArtifactTestDeps(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				return &commodorepb.ResolveDVRHashResponse{Found: true, InternalName: "rec", TenantId: "t1"}, nil
			},
		})
		got, err := ResolveArtifactByHash(ctx, h)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ContentType != "dvr" || got.TenantID != "t1" || got.InternalName != "vod+rec" || !got.IsVod {
			t.Fatalf("unexpected target: %+v", got)
		}
	})
}
