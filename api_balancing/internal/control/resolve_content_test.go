package control

import (
	"context"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// ResolveContent is the playback front door: it turns a public content id into a
// typed ContentResolution (clip/dvr/vod/live) via Commodore, stamping the Mist
// namespace prefix onto InternalName so downstream resolvers and USER_NEW policy
// see the same stream identity. This drives every viewer request, so each
// resolution outcome is pinned against the fake Commodore.
func TestResolveContent(t *testing.T) {
	ctx := context.Background()

	t.Run("empty input errors", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		if _, err := ResolveContent(ctx, ""); err == nil {
			t.Fatal("empty input must error")
		}
	})

	t.Run("vod artifact gets vod+ prefix", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{
					Found: true, ContentType: "vod", InternalName: "s1", TenantId: "t1", StreamId: "stream1",
				}, nil
			},
		})
		res, err := ResolveContent(ctx, "pb-vod")
		if err != nil {
			t.Fatal(err)
		}
		if res.ContentType != "vod" || res.InternalName != "vod+s1" || res.TenantId != "t1" {
			t.Fatalf("resolution = %+v", res)
		}
	})

	t.Run("dvr artifact gets dvr+ prefix", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, ContentType: "dvr", InternalName: "s2"}, nil
			},
		})
		res, err := ResolveContent(ctx, "pb-dvr")
		if err != nil || res.ContentType != "dvr" || res.InternalName != "dvr+s2" {
			t.Fatalf("got (%+v, %v)", res, err)
		}
	})

	t.Run("invalid artifact content_type errors", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, ContentType: "hologram", InternalName: "s3"}, nil
			},
		})
		if _, err := ResolveContent(ctx, "pb-bad"); err == nil {
			t.Fatal("unknown content_type must error")
		}
	})

	t.Run("live playback id resolves to live", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			// artifact lookup misses; live lookup hits.
			playbackID: func(_ context.Context, _ *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
				return &commodorepb.ResolvePlaybackIDResponse{InternalName: "live+s4", TenantId: "t4", StreamId: "stream4"}, nil
			},
		})
		res, err := ResolveContent(ctx, "pb-live")
		if err != nil || res.ContentType != "live" || res.InternalName != "live+s4" || res.TenantId != "t4" {
			t.Fatalf("got (%+v, %v)", res, err)
		}
	})

	t.Run("nothing resolves -> not found", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{}) // all lookups miss
		if _, err := ResolveContent(ctx, "pb-ghost"); err == nil {
			t.Fatal("unresolvable content must error")
		}
	})
}
