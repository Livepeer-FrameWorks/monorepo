package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"

	"github.com/google/uuid"
)

func decodeStreamID(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("stream_id is required")
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeStream {
			return "", fmt.Errorf("invalid stream relay ID type: %s", typ)
		}
		return id, nil
	}
	return input, nil
}

// NormalizePlaybackContent maps an MCP `content_id` to the CANONICAL public viewer
// playback_id AND the content owner's tenant (for access-control / x402 attribution).
//
// Artifacts (clip/DVR/VOD) have a distinct public playback_id, separate from their
// storage hash (clip_hash/vod_hash/dvr_hash). The viewer resolve path tolerates a
// hash as an alias, but x402 viewer:// settlement resolves ONLY playback_id, so we
// canonicalize every accepted form to the playback_id here.
//
//	raw            → playback_id if it is one; else an artifact hash → its playback_id
//	Stream global  → the stream's playback_id via GetStream (auth-scoped: caller is owner)
//	VodAsset global → the VOD's playback_id via ResolveVodID (UUID form) or the hash path
//	Clip global    → the clip_hash decoded, then canonicalized to its playback_id
//
// A raw internal stream UUID / stream_id / internal_name is NOT a viewer identifier
// and is not accepted. ownerTenantID is best-effort ("" when unestablished); a Stream
// global ID returns "" because GetStream already scopes to the authenticated owner.
func NormalizePlaybackContent(ctx context.Context, input string, clients *clients.ServiceClients) (playbackID, ownerTenantID string, err error) {
	if input == "" {
		return "", "", fmt.Errorf("content_id is required")
	}
	if clients == nil || clients.Commodore == nil {
		return "", "", fmt.Errorf("playback resolver unavailable")
	}

	typ, id, ok := globalid.Decode(input)
	if !ok {
		// Raw playback_id or artifact hash — canonicalize to the playback_id.
		return canonicalizeRawPlayback(ctx, input, clients)
	}

	switch typ {
	case globalid.TypeStream:
		// Stream global IDs encode the internal UUID; the viewer path needs the
		// playback_id. GetStream is auth-scoped, so this succeeds only for the owner
		// (caller == owner, so no owner/caller billing split is needed → owner "").
		stream, gErr := clients.Commodore.GetStream(ctx, id)
		if gErr != nil {
			return "", "", fmt.Errorf("failed to resolve stream relay ID: %w", gErr)
		}
		if stream.GetPlaybackId() == "" {
			return "", "", fmt.Errorf("stream has no playback id")
		}
		return stream.GetPlaybackId(), "", nil
	case globalid.TypeVodAsset:
		if _, pErr := uuid.Parse(id); pErr == nil {
			resp, rErr := clients.Commodore.ResolveVodID(ctx, id)
			if rErr != nil {
				return "", "", fmt.Errorf("failed to resolve VOD relay ID: %w", rErr)
			}
			if resp == nil || !resp.Found {
				return "", "", fmt.Errorf("VOD asset not found")
			}
			// A VodAsset Relay ID is a management handle: don't let one tenant probe
			// another's VOD via it (public playback still works via the playback_id).
			callerTenant := ctxkeys.GetTenantID(ctx)
			if callerTenant != "" && resp.TenantId != "" && resp.TenantId != callerTenant {
				return "", "", fmt.Errorf("VOD asset not found")
			}
			return resp.PlaybackId, resp.TenantId, nil
		}
		// Hash-form VOD global ID → canonicalize the vod_hash to its playback_id.
		return canonicalizeRawPlayback(ctx, id, clients)
	case globalid.TypeClip:
		// Clip global IDs encode the clip_hash → canonicalize to the playback_id.
		return canonicalizeRawPlayback(ctx, id, clients)
	default:
		return "", "", fmt.Errorf("unsupported content ID type for playback: %s", typ)
	}
}

// canonicalizeRawPlayback maps a raw playback_id or artifact hash to the canonical
// public playback_id + owner tenant. A playback_id (live/artifact/chapter) is
// returned unchanged; an artifact hash is resolved to its playback_id via the hash
// resolvers. All of these resolvers are public (no auth), matching public viewer
// playback. A value that matches none of them fails closed with an error (it is not
// a viewer-resolvable identifier).
func canonicalizeRawPlayback(ctx context.Context, input string, clients *clients.ServiceClients) (playbackID, ownerTenantID string, err error) {
	c := clients.Commodore
	// Already a playback_id? Try the playback_id-keyed resolvers: artifact
	// (clip/dvr/vod), then live stream, then DVR chapter (a distinct public
	// playback_id class with its own resolver, not covered by the artifact one).
	if r, e := c.ResolveArtifactPlaybackID(ctx, input); e == nil && r.Found && r.TenantId != "" {
		return input, r.TenantId, nil
	}
	if r, e := c.ResolvePlaybackID(ctx, input); e == nil && r.TenantId != "" {
		return input, r.TenantId, nil
	}
	if r, e := c.ResolveChapterPlaybackID(ctx, input); e == nil && r.Found && r.TenantId != "" {
		return input, r.TenantId, nil
	}
	// Otherwise it may be an artifact hash — canonicalize to its playback_id.
	if r, e := c.ResolveClipHash(ctx, input); e == nil && r.Found && r.PlaybackId != "" {
		return r.PlaybackId, r.TenantId, nil
	}
	if r, e := c.ResolveDVRHash(ctx, input); e == nil && r.Found && r.PlaybackId != "" {
		return r.PlaybackId, r.TenantId, nil
	}
	if r, e := c.ResolveVodHash(ctx, input); e == nil && r.Found && r.PlaybackId != "" {
		return r.PlaybackId, r.TenantId, nil
	}
	// The resolvers above cover the complete set of viewer-resolvable identifiers
	// (live/artifact/chapter playback_id + clip/DVR/VOD hash — keep this in sync with
	// Commodore's ResolveViewerEndpoint). An input that misses all of them is not a
	// playback identifier (or the content no longer exists), so fail closed rather
	// than forward a value the viewer path and x402 can't resolve. This honors the
	// schema contract that raw stream UUIDs / stream_ids / internal_names are not
	// accepted. The message stays neutral because a transient resolver error is
	// indistinguishable here from not-found (both surface the same way downstream).
	return "", "", fmt.Errorf("content_id could not be resolved to a playback identifier (use a public playback_id)")
}

func resolveVodIdentifier(ctx context.Context, input string, clients *clients.ServiceClients) (string, error) {
	if input == "" {
		return "", fmt.Errorf("invalid artifact hash")
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeVodAsset {
			return "", fmt.Errorf("invalid VOD relay ID type: %s", typ)
		}
		if _, err := uuid.Parse(id); err == nil {
			if clients == nil || clients.Commodore == nil {
				return "", fmt.Errorf("VOD resolver unavailable")
			}
			resp, err := clients.Commodore.ResolveVodID(ctx, id)
			if err != nil {
				return "", fmt.Errorf("failed to resolve VOD relay ID: %w", err)
			}
			if resp == nil || !resp.Found {
				return "", fmt.Errorf("VOD asset not found")
			}
			callerTenant := ctxkeys.GetTenantID(ctx)
			if callerTenant != "" && resp.TenantId != "" && resp.TenantId != callerTenant {
				return "", fmt.Errorf("VOD asset not found")
			}
			return resp.VodHash, nil
		}
		return id, nil
	}
	return input, nil
}
