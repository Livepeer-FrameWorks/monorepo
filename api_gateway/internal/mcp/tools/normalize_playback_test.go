package tools

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/google/uuid"
)

func svc(fake *clientstest.FakeCommodore) *clients.ServiceClients {
	return &clients.ServiceClients{Commodore: fake}
}

// notFoundResolvers returns a fake whose whole raw-canonicalization chain
// (playback_id resolvers, then clip/DVR/VOD hash resolvers) reports not-found, so a
// test can override just the one arm that should hit.
func notFoundResolvers() *clientstest.FakeCommodore {
	return &clientstest.FakeCommodore{
		ResolveArtifactPlaybackIDFn: func(_ context.Context, _ string) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
		},
		ResolvePlaybackIDFn: func(_ context.Context, _ string) (*commodorepb.ResolvePlaybackIDResponse, error) {
			return &commodorepb.ResolvePlaybackIDResponse{}, nil
		},
		ResolveChapterPlaybackIDFn: func(_ context.Context, _ string) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
			return &commodorepb.ResolveChapterPlaybackIDResponse{Found: false}, nil
		},
		ResolveClipHashFn: func(_ context.Context, _ string) (*commodorepb.ResolveClipHashResponse, error) {
			return &commodorepb.ResolveClipHashResponse{Found: false}, nil
		},
		ResolveDVRHashFn: func(_ context.Context, _ string) (*commodorepb.ResolveDVRHashResponse, error) {
			return &commodorepb.ResolveDVRHashResponse{Found: false}, nil
		},
		ResolveVodHashFn: func(_ context.Context, _ string) (*commodorepb.ResolveVodHashResponse, error) {
			return &commodorepb.ResolveVodHashResponse{Found: false}, nil
		},
	}
}

// NormalizePlaybackContent maps any MCP content_id (raw playback_id / artifact hash,
// or a Stream/Clip/VodAsset global ID) to the CANONICAL public playback_id + owner.
// Artifacts have a distinct playback_id (≠ hash), and x402 viewer:// resolves only
// playback_id — so hashes must be canonicalized, not passed through.
func TestNormalizePlaybackContent(t *testing.T) {
	t.Run("empty input errors", func(t *testing.T) {
		if _, _, err := NormalizePlaybackContent(context.Background(), "", svc(&clientstest.FakeCommodore{})); err == nil {
			t.Fatal("expected error for empty content_id")
		}
	})

	t.Run("nil commodore errors", func(t *testing.T) {
		if _, _, err := NormalizePlaybackContent(context.Background(), "pb", &clients.ServiceClients{}); err == nil {
			t.Fatal("expected error for nil commodore")
		}
	})

	t.Run("raw playback_id passes through and resolves owner", func(t *testing.T) {
		fake := notFoundResolvers()
		fake.ResolveArtifactPlaybackIDFn = func(_ context.Context, pid string) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			if pid != "pb_clip" {
				t.Fatalf("ResolveArtifactPlaybackID got %q", pid)
			}
			return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, TenantId: "t-clip"}, nil
		}
		pid, owner, err := NormalizePlaybackContent(context.Background(), "pb_clip", svc(fake))
		if err != nil || pid != "pb_clip" || owner != "t-clip" {
			t.Fatalf("got pid=%q owner=%q err=%v; want pb_clip / t-clip", pid, owner, err)
		}
	})

	t.Run("raw artifact hash canonicalizes to playback_id", func(t *testing.T) {
		fake := notFoundResolvers()
		fake.ResolveClipHashFn = func(_ context.Context, h string) (*commodorepb.ResolveClipHashResponse, error) {
			if h != "cliphash" {
				t.Fatalf("ResolveClipHash got %q", h)
			}
			return &commodorepb.ResolveClipHashResponse{Found: true, PlaybackId: "pb_clip", TenantId: "t-clip"}, nil
		}
		pid, owner, err := NormalizePlaybackContent(context.Background(), "cliphash", svc(fake))
		if err != nil || pid != "pb_clip" || owner != "t-clip" {
			t.Fatalf("got pid=%q owner=%q err=%v; want pb_clip / t-clip (hash canonicalized)", pid, owner, err)
		}
	})

	t.Run("DVR chapter playback_id resolves owner (distinct resolver)", func(t *testing.T) {
		fake := notFoundResolvers()
		fake.ResolveChapterPlaybackIDFn = func(_ context.Context, pid string) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
			if pid != "pb_chapter" {
				t.Fatalf("ResolveChapterPlaybackID got %q", pid)
			}
			return &commodorepb.ResolveChapterPlaybackIDResponse{Found: true, TenantId: "t-chapter"}, nil
		}
		pid, owner, err := NormalizePlaybackContent(context.Background(), "pb_chapter", svc(fake))
		if err != nil || pid != "pb_chapter" || owner != "t-chapter" {
			t.Fatalf("got pid=%q owner=%q err=%v; want pb_chapter / t-chapter", pid, owner, err)
		}
	})

	t.Run("Stream global ID → playback_id, no owner split (caller is owner)", func(t *testing.T) {
		fake := &clientstest.FakeCommodore{
			GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
				if id != "s-uuid" {
					t.Fatalf("GetStream got %q", id)
				}
				return &commodorepb.Stream{PlaybackId: "pb_stream"}, nil
			},
		}
		relay := globalid.Encode(globalid.TypeStream, "s-uuid")
		pid, owner, err := NormalizePlaybackContent(context.Background(), relay, svc(fake))
		if err != nil || pid != "pb_stream" || owner != "" {
			t.Fatalf("got pid=%q owner=%q err=%v; want pb_stream / \"\"", pid, owner, err)
		}
	})

	t.Run("Stream global ID with no playback_id errors", func(t *testing.T) {
		fake := &clientstest.FakeCommodore{
			GetStreamFn: func(_ context.Context, _ string) (*commodorepb.Stream, error) {
				return &commodorepb.Stream{}, nil
			},
		}
		relay := globalid.Encode(globalid.TypeStream, "s-uuid")
		if _, _, err := NormalizePlaybackContent(context.Background(), relay, svc(fake)); err == nil {
			t.Fatal("expected error when stream has no playback id")
		}
	})

	t.Run("VodAsset UUID global ID → playback_id + owner", func(t *testing.T) {
		vid := uuid.New().String()
		fake := &clientstest.FakeCommodore{
			ResolveVodIDFn: func(_ context.Context, id string) (*commodorepb.ResolveVodIDResponse, error) {
				if id != vid {
					t.Fatalf("ResolveVodID got %q", id)
				}
				return &commodorepb.ResolveVodIDResponse{Found: true, PlaybackId: "pb_vod", TenantId: "t-vod"}, nil
			},
		}
		relay := globalid.Encode(globalid.TypeVodAsset, vid)
		pid, owner, err := NormalizePlaybackContent(context.Background(), relay, svc(fake))
		if err != nil || pid != "pb_vod" || owner != "t-vod" {
			t.Fatalf("got pid=%q owner=%q err=%v; want pb_vod / t-vod", pid, owner, err)
		}
	})

	t.Run("VodAsset UUID cross-tenant is hidden", func(t *testing.T) {
		vid := uuid.New().String()
		fake := &clientstest.FakeCommodore{
			ResolveVodIDFn: func(_ context.Context, _ string) (*commodorepb.ResolveVodIDResponse, error) {
				return &commodorepb.ResolveVodIDResponse{Found: true, PlaybackId: "pb_vod", TenantId: "t-owner"}, nil
			},
		}
		relay := globalid.Encode(globalid.TypeVodAsset, vid)
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "t-other")
		if _, _, err := NormalizePlaybackContent(ctx, relay, svc(fake)); err == nil {
			t.Fatal("expected not-found for cross-tenant VOD relay ID")
		}
	})

	t.Run("VodAsset hash-form global ID canonicalizes to playback_id", func(t *testing.T) {
		fake := notFoundResolvers()
		fake.ResolveVodHashFn = func(_ context.Context, h string) (*commodorepb.ResolveVodHashResponse, error) {
			if h != "vodhash" {
				t.Fatalf("ResolveVodHash got %q", h)
			}
			return &commodorepb.ResolveVodHashResponse{Found: true, PlaybackId: "pb_vod", TenantId: "t-vod"}, nil
		}
		relay := globalid.Encode(globalid.TypeVodAsset, "vodhash") // non-UUID → hash form
		pid, owner, err := NormalizePlaybackContent(context.Background(), relay, svc(fake))
		if err != nil || pid != "pb_vod" || owner != "t-vod" {
			t.Fatalf("got pid=%q owner=%q err=%v; want pb_vod / t-vod", pid, owner, err)
		}
	})

	t.Run("Clip global ID canonicalizes clip_hash → playback_id + owner", func(t *testing.T) {
		fake := notFoundResolvers()
		fake.ResolveClipHashFn = func(_ context.Context, h string) (*commodorepb.ResolveClipHashResponse, error) {
			if h != "cliphash" {
				t.Fatalf("ResolveClipHash got %q", h)
			}
			return &commodorepb.ResolveClipHashResponse{Found: true, PlaybackId: "pb_clip", TenantId: "t-clip"}, nil
		}
		relay := globalid.Encode(globalid.TypeClip, "cliphash")
		pid, owner, err := NormalizePlaybackContent(context.Background(), relay, svc(fake))
		if err != nil || pid != "pb_clip" || owner != "t-clip" {
			t.Fatalf("got pid=%q owner=%q err=%v; want pb_clip / t-clip (not the hash)", pid, owner, err)
		}
	})

	t.Run("unsupported global ID type errors", func(t *testing.T) {
		relay := globalid.Encode(globalid.TypeCluster, "c1")
		if _, _, err := NormalizePlaybackContent(context.Background(), relay, svc(&clientstest.FakeCommodore{})); err == nil {
			t.Fatal("expected error for unsupported content ID type")
		}
	})

	t.Run("unresolvable raw content_id fails closed", func(t *testing.T) {
		// A raw stream UUID / internal_name / typo matches none of the playback
		// resolvers → must error, not silently forward (schema forbids these).
		fake := notFoundResolvers()
		if _, _, err := NormalizePlaybackContent(context.Background(), "not-a-playback-id", svc(fake)); err == nil {
			t.Fatal("expected error for unresolvable raw content_id")
		}
	})
}
