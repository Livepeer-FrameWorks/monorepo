package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// hash32 is a 32-hex-char artifact hash. ResolveContent's hash-candidate
// normalization arm (isArtifactHashCandidate) only fires on inputs of exactly
// this shape, so the DVR-registry-row path must be driven with one.
const hash32 = "abcdef0123456789abcdef0123456789"

// TestResolveContent_ArtifactPlacementRouting pins the cross-cluster-vs-local
// routing DECISION that ResolveContent stamps onto an artifact resolution: when a
// local edge holds the artifact bytes (warm cache hit), the resolution is pinned
// to that node (FixedNode/FixedNodeID); when no local node holds them, FixedNode
// stays empty so downstream serve picks any storage edge. This is the branch the
// grpc/handler tests could not reach because it requires both the Commodore fake
// AND a seeded state.Manager + load balancer.
func TestResolveContent_ArtifactPlacementRouting(t *testing.T) {
	ctx := context.Background()

	t.Run("warm local node pins FixedNode (local routing)", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		lat, lon := 52.0, 5.0
		sm.SetNodeInfo("warm1", "https://warm1.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
		sm.TouchNode("warm1", true)
		// StoredArtifact.ClipHash carries the hash for any artifact type.
		sm.SetNodeArtifacts("warm1", []*ipcpb.StoredArtifact{{ClipHash: "warmhash"}})

		prevLB := loadBalancerInstance
		loadBalancerInstance = &fakeLoadBalancer{nodes: map[string]state.NodeState{
			"warm1": {NodeID: "warm1", BaseURL: "https://warm1.example.com"},
		}}
		t.Cleanup(func() { loadBalancerInstance = prevLB })

		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{
					Found: true, ContentType: "vod", InternalName: "v1",
					TenantId: "t1", StreamId: "s1", ArtifactHash: "warmhash",
				}, nil
			},
		})

		res, err := ResolveContent(ctx, "pb-warm")
		if err != nil {
			t.Fatal(err)
		}
		// DECISION: the artifact is warm on warm1, so routing is pinned there.
		if res.FixedNode != "https://warm1.example.com" {
			t.Fatalf("warm artifact must pin FixedNode to the holding edge, got %q", res.FixedNode)
		}
		if res.FixedNodeID != "warm1" {
			t.Fatalf("FixedNodeID must resolve via load balancer host->id, got %q", res.FixedNodeID)
		}
		if res.InternalName != "vod+v1" || res.ContentType != "vod" {
			t.Fatalf("vod artifact must carry vod+ prefix, got %+v", res)
		}
	})

	t.Run("no warm node leaves FixedNode empty (serve picks any edge)", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		// No node holds "coldhash".

		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{
					Found: true, ContentType: "clip", InternalName: "c1",
					TenantId: "t1", ArtifactHash: "coldhash",
				}, nil
			},
		})

		res, err := ResolveContent(ctx, "pb-cold")
		if err != nil {
			t.Fatal(err)
		}
		// DECISION: cold artifact is not pinned — front door leaves placement open.
		if res.FixedNode != "" || res.FixedNodeID != "" {
			t.Fatalf("cold artifact must not pin a node, got node=%q id=%q", res.FixedNode, res.FixedNodeID)
		}
		// Clip still shares the vod+ Mist namespace.
		if res.InternalName != "vod+c1" || res.ContentType != "clip" {
			t.Fatalf("clip artifact must carry vod+ prefix, got %+v", res)
		}
	})
}

// TestResolveContent_DVRRegistryHashArm pins the DVR-registry-row normalization
// arm of ResolveContent: a 32-hex-char artifact hash that ISN'T a playback id (so
// ResolveArtifactPlaybackID misses) but resolves via ResolveDVRHash is normalized
// to the DVR's public playback_id and dvr+<internal_name>, inheriting requires_auth
// from the artifact's playback policy (ResolveArtifactInternalName). The web viewer
// opens DVR rows by hash, so this arm protects that entry path.
func TestResolveContent_DVRRegistryHashArm(t *testing.T) {
	ctx := context.Background()

	t.Run("hash resolves to dvr playback id with inherited auth", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)

		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			// Not a playback id -> artifact playback lookup misses.
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
			},
			dvrHash: func(_ context.Context, req *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				if req.GetDvrHash() != hash32 {
					t.Errorf("dvr hash lookup must use the raw input hash, got %q", req.GetDvrHash())
				}
				return &commodorepb.ResolveDVRHashResponse{
					Found: true, PlaybackId: "dvr-public-pb", InternalName: "recstream",
					TenantId: "t-dvr", StreamId: "s-dvr",
				}, nil
			},
			// Playback policy for the DVR internal name says auth-required.
			artifactInternal: func(_ context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
				if req.GetInternalName() != "recstream" {
					t.Errorf("policy lookup must use the DVR internal name, got %q", req.GetInternalName())
				}
				return &commodorepb.ResolveArtifactInternalNameResponse{Found: true, RequiresAuth: true}, nil
			},
		})

		res, err := ResolveContent(ctx, hash32)
		if err != nil {
			t.Fatal(err)
		}
		// DECISION: hash is normalized to the public playback id, dvr+ prefix,
		// parent tenant/stream, and auth inherited from the artifact policy.
		if res.ContentType != "dvr" {
			t.Fatalf("hash must resolve as dvr, got %q", res.ContentType)
		}
		if res.ContentId != "dvr-public-pb" {
			t.Fatalf("ContentId must be normalized to the public playback id, got %q", res.ContentId)
		}
		if res.InternalName != "dvr+recstream" {
			t.Fatalf("internal name must be dvr+<internal>, got %q", res.InternalName)
		}
		if res.TenantId != "t-dvr" || res.StreamId != "s-dvr" {
			t.Fatalf("tenant/stream must come from the DVR registry, got %+v", res)
		}
		if !res.RequiresAuth {
			t.Fatal("requires_auth must be inherited from the artifact playback policy (true)")
		}
	})

	t.Run("hash with no public playback id falls back to the input hash", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)

		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
			},
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				// No PlaybackId -> ContentId falls back to the raw hash input.
				return &commodorepb.ResolveDVRHashResponse{Found: true, InternalName: "rec2", TenantId: "t2"}, nil
			},
		})

		res, err := ResolveContent(ctx, hash32)
		if err != nil {
			t.Fatal(err)
		}
		// DECISION: with no minted public id, the resolution addresses by the
		// raw input hash rather than inventing one.
		if res.ContentId != hash32 {
			t.Fatalf("ContentId must fall back to the input hash, got %q", res.ContentId)
		}
		if res.InternalName != "dvr+rec2" {
			t.Fatalf("internal name must be dvr+rec2, got %q", res.InternalName)
		}
	})
}

// TestResolveContent_LiveIngestModeRouting pins the live arm of ResolveContent:
// when neither artifact nor DVR-hash lookups hit, a live playback_id resolves to a
// live ContentResolution that carries the ingest mode and cluster peers verbatim
// from Commodore (free with every resolve). IngestMode drives downstream
// RoutingInternalName (pull+ rewrite), so it must survive the resolution. This is
// the live success branch, distinct from the existing test which omits ingest
// mode and cluster context.
func TestResolveContent_LiveIngestModeRouting(t *testing.T) {
	ctx := context.Background()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "peer-a"}, {ClusterId: "peer-b"}}
	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		// Live input is not a 32-hex hash, so the dvr-hash arm is skipped entirely;
		// artifact lookup misses; playback lookup hits.
		playbackID: func(_ context.Context, _ *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
			return &commodorepb.ResolvePlaybackIDResponse{
				InternalName: "pullstream", TenantId: "t-live", StreamId: "s-live",
				IngestMode: "pull", ClusterPeers: peers, RequiresAuth: true,
			}, nil
		},
	})

	res, err := ResolveContent(ctx, "pb-live-pull")
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != "live" {
		t.Fatalf("playback-id resolution must be live, got %q", res.ContentType)
	}
	// DECISION: ingest mode survives so RoutingInternalName can rewrite pull+.
	if res.IngestMode != "pull" {
		t.Fatalf("ingest mode must be carried through, got %q", res.IngestMode)
	}
	if got := res.RoutingInternalName(); got != "pull+pullstream" {
		t.Fatalf("pull ingest must yield pull+ routing name, got %q", got)
	}
	// DECISION: tenant cluster context is enriched onto every live resolve.
	if len(res.ClusterPeers) != 2 {
		t.Fatalf("cluster peers must be carried through, got %d", len(res.ClusterPeers))
	}
	if !res.RequiresAuth {
		t.Fatal("requires_auth must be carried from Commodore")
	}
}

// TestResolveArtifactByHash_ClipAndVodArms pins the clip and vod arms of
// ResolveArtifactByHash (the internal artifact-hash entry, no legacy playback): a
// clip hash stamps content_type=clip + vod+<internal> + stream id; a vod hash
// stamps content_type=vod + vod+<internal> with NO stream id (VODs aren't bound to
// a source stream). The arms are ordered clip→dvr→vod, so each must win only on
// its own hit. Wave 1-2 covered only the dvr arm.
func TestResolveArtifactByHash_ClipAndVodArms(t *testing.T) {
	ctx := context.Background()

	t.Run("clip hit stamps clip + stream id", func(t *testing.T) {
		_, _, _ = setupArtifactTestDeps(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			clipHash: func(_ context.Context, _ *commodorepb.ResolveClipHashRequest) (*commodorepb.ResolveClipHashResponse, error) {
				return &commodorepb.ResolveClipHashResponse{
					Found: true, InternalName: "clipinternal", TenantId: "tc", StreamId: "sc",
				}, nil
			},
		})
		got, err := ResolveArtifactByHash(ctx, hash32)
		if err != nil {
			t.Fatal(err)
		}
		// DECISION: clip arm wins, carries source stream id, vod+ namespace.
		if got.ContentType != "clip" || got.InternalName != "vod+clipinternal" {
			t.Fatalf("clip arm = %+v", got)
		}
		if got.StreamID != "sc" || got.TenantID != "tc" || !got.IsVod {
			t.Fatalf("clip arm must carry stream/tenant and IsVod, got %+v", got)
		}
	})

	t.Run("vod hit stamps vod with no stream binding", func(t *testing.T) {
		_, _, _ = setupArtifactTestDeps(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			// clip + dvr miss; vod hits.
			vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
				return &commodorepb.ResolveVodHashResponse{
					Found: true, InternalName: "vodinternal", TenantId: "tv",
				}, nil
			},
		})
		got, err := ResolveArtifactByHash(ctx, hash32)
		if err != nil {
			t.Fatal(err)
		}
		// DECISION: vod arm wins; the vod registry has no source-stream field, so
		// the resolved target carries no StreamID binding (unlike clip/dvr).
		if got.ContentType != "vod" || got.InternalName != "vod+vodinternal" {
			t.Fatalf("vod arm = %+v", got)
		}
		if got.StreamID != "" {
			t.Fatalf("vod must not bind a source stream id, got %q", got.StreamID)
		}
		if got.TenantID != "tv" || !got.IsVod {
			t.Fatalf("vod arm must carry tenant + IsVod, got %+v", got)
		}
	})
}
