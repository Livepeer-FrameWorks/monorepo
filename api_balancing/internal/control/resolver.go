package control

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// CommodoreClient holds the reference to the commodore gRPC client for resolution.
// This should be set during application initialization (e.g. in handlers.Init).
var CommodoreClient *commodore.GRPCClient

// SetCommodoreClient updates the Commodore client reference for resolution.
func SetCommodoreClient(client *commodore.GRPCClient) {
	CommodoreClient = client
}

// StreamTarget describes the resolution result.
type StreamTarget struct {
	InternalName string
	StreamID     string
	// FixedNode is set if the stream is pinned to a specific node (e.g. VOD artifact).
	// If empty, the stream is dynamic/live and can be served by any capable edge.
	FixedNode string
	// FixedNodeID is the node ID corresponding to FixedNode
	FixedNodeID string
	IsVod       bool
	// TenantID associated with the stream/artifact.
	TenantID string
	// ContentType indicates the artifact type: "clip", "dvr", or "live"
	ContentType string
	// NeedsDefrost indicates the artifact is synced to S3 but not cached locally.
	// Caller should trigger defrost and return 202 Accepted with Retry-After.
	NeedsDefrost bool
	// S3URL is the S3 location if NeedsDefrost is true
	S3URL             string
	ClusterPeers      []*pb.TenantClusterPeer // Tenant's cluster context from Commodore
	RequiresAuth      bool
	RequiresAuthKnown bool
}

// ResolveStream determines the target stream name and node constraint for a given input.
// Input can be: Internal Name, View Key, or Artifact Playback ID.
// This unifies resolution logic across HTTP handlers and Mist triggers.
func ResolveStream(ctx context.Context, input string) (*StreamTarget, error) {
	// 1. Already canonical internal name (live+ / vod+)
	// Use mist.ExtractInternalName for generic prefix stripping
	if strings.HasPrefix(input, "live+") {
		target := &StreamTarget{InternalName: input}
		if CommodoreClient != nil {
			internal := mist.ExtractInternalName(input)
			if internal != "" {
				if resp, err := CommodoreClient.ResolveInternalName(ctx, internal); err == nil {
					target.TenantID = resp.TenantId
					target.StreamID = resp.StreamId
					target.ContentType = "live"
					target.ClusterPeers = resp.ClusterPeers
					target.RequiresAuth = resp.GetRequiresAuth()
					target.RequiresAuthKnown = true
				}
			}
		}
		return target, nil
	}

	if strings.HasPrefix(input, "vod+") {
		artifactInternal := mist.ExtractInternalName(input)
		target := &StreamTarget{InternalName: input, IsVod: true}
		if CommodoreClient != nil && artifactInternal != "" {
			if resp, err := CommodoreClient.ResolveArtifactInternalName(ctx, artifactInternal); err == nil && resp.Found {
				target.TenantID = resp.TenantId
				target.StreamID = resp.StreamId
				target.ContentType = resp.ContentType
				target.ClusterPeers = resp.ClusterPeers
				target.RequiresAuth = resp.GetRequiresAuth()
				target.RequiresAuthKnown = true
				applyArtifactPlacement(ctx, resp.ArtifactHash, target)
			}
		}
		return target, nil
	}

	// Canonical "dvr+<dvr_internal_name>" — the rolling-DVR playback
	// surface of an actively recording stream. Chapter playback flows
	// through the chapter artifact's public playback_id (resolved by
	// the artifact-playback path below); a dvr+ token whose body is
	// NOT a DVR artifact internal name is treated as not-found so the
	// legacy dvr+<chapter_id> shape does not silently masquerade as a
	// DVR target.
	if strings.HasPrefix(input, "dvr+") {
		token := strings.TrimPrefix(input, "dvr+")
		if CommodoreClient != nil && token != "" {
			if resp, err := CommodoreClient.ResolveArtifactInternalName(ctx, token); err == nil && resp.Found && resp.GetContentType() == "dvr" {
				target := &StreamTarget{
					InternalName: input,
					ContentType:  "dvr",
					// Live-type transport (local manifest on origin, DTSC
					// pull cross-node), not relay/S3 — IsVod stays false.
					IsVod:             false,
					TenantID:          resp.TenantId,
					StreamID:          resp.StreamId,
					ClusterPeers:      resp.ClusterPeers,
					RequiresAuth:      resp.GetRequiresAuth(),
					RequiresAuthKnown: true,
				}
				applyArtifactPlacement(ctx, resp.ArtifactHash, target)
				return target, nil
			}
		}
		// Sentinel empty target + error: callers branch on InternalName=="" to
		// mean "not found" without nil-checking the target pointer.
		return &StreamTarget{}, fmt.Errorf("dvr+ token does not resolve to a DVR artifact internal name")
	}

	// 2a. Chapter playback ID (Commodore-minted public ID for a chapter
	// VOD artifact). Resolve this before the generic VOD registry path
	// because chapter artifacts inherit auth + stream context from the
	// parent DVR, not from a standalone VOD policy row.
	if resp, ok := resolveChapterArtifactPlaybackResp(ctx, input); ok {
		target := &StreamTarget{
			InternalName:      "vod+" + resp.InternalName,
			IsVod:             true,
			TenantID:          resp.TenantId,
			StreamID:          resp.StreamId,
			ContentType:       resp.ContentType,
			ClusterPeers:      resp.ClusterPeers,
			RequiresAuth:      resp.GetRequiresAuth(),
			RequiresAuthKnown: true,
		}
		applyArtifactPlacement(ctx, resp.ArtifactHash, target)
		return target, nil
	}

	// 2b. Artifact playback ID (clip / dvr / vod). DVR rewrites to
	// dvr+<dvr_internal_name> — its own Mist stream identity, distinct
	// from vod+. Active DVR is served from the recording origin's
	// rolling artefacts (local manifest on origin, DTSC pull on other
	// edges); finalized DVR resolves to the latest playable chapter
	// via the relay → S3 path. Clips and VODs share vod+<internal_name>.
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolveArtifactPlaybackID(ctx, input); err == nil && resp.Found {
			prefix := "vod+"
			isVod := true
			if resp.GetContentType() == "dvr" {
				prefix = "dvr+"
				isVod = false
			}
			target := &StreamTarget{
				InternalName:      prefix + resp.InternalName,
				IsVod:             isVod,
				TenantID:          resp.TenantId,
				StreamID:          resp.StreamId,
				ContentType:       resp.ContentType,
				ClusterPeers:      resp.ClusterPeers,
				RequiresAuth:      resp.GetRequiresAuth(),
				RequiresAuthKnown: true,
			}
			applyArtifactPlacement(ctx, resp.ArtifactHash, target)
			return target, nil
		}

		if isArtifactHashCandidate(input) {
			if target := resolveArtifactHashStreamTarget(ctx, input); target != nil {
				return target, nil
			}
		}
	}

	// 3. Live view keys (playback_id) — internal name shape is ingest-mode aware.
	// push streams      → live+<internal>      (wildcard adapter; resolved at PUSH_REWRITE)
	// pull streams      → pull+<internal>      (wildcard adapter; STREAM_SOURCE resolves upstream)
	// mist_native       → <internal> (bare)    (concrete Mist config; literal source set by sidecar Apply)
	// Bare names are reserved for concrete configs; new ingest modes must
	// not introduce a fourth `<word>+` prefix without explicit design review.
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolvePlaybackID(ctx, input); err == nil {
			var internalName string
			switch resp.GetIngestMode() {
			case "pull":
				internalName = "pull+" + resp.InternalName
			case "mist_native":
				internalName = resp.InternalName
			default:
				internalName = "live+" + resp.InternalName
			}
			return &StreamTarget{
				InternalName:      internalName,
				IsVod:             false,
				TenantID:          resp.TenantId,
				StreamID:          resp.StreamId,
				ContentType:       "live",
				ClusterPeers:      resp.ClusterPeers,
				RequiresAuth:      resp.GetRequiresAuth(),
				RequiresAuthKnown: true,
			}, nil
		}
	}

	// 4. Nothing matched — stream does not exist.
	return &StreamTarget{}, nil
}

func resolveArtifactHashStreamTarget(ctx context.Context, artifactHash string) *StreamTarget {
	if CommodoreClient == nil || artifactHash == "" {
		return nil
	}

	if resp, err := CommodoreClient.ResolveDVRHash(ctx, artifactHash); err == nil && resp.GetFound() {
		requiresAuth, requiresKnown, clusterPeers := resolveArtifactPolicy(ctx, resp.GetInternalName())
		target := &StreamTarget{
			InternalName:      "dvr+" + resp.GetInternalName(),
			IsVod:             false,
			TenantID:          resp.GetTenantId(),
			StreamID:          resp.GetStreamId(),
			ContentType:       "dvr",
			ClusterPeers:      clusterPeers,
			RequiresAuth:      requiresAuth,
			RequiresAuthKnown: requiresKnown,
		}
		applyArtifactPlacement(ctx, artifactHash, target)
		return target
	}

	if resp, err := CommodoreClient.ResolveClipHash(ctx, artifactHash); err == nil && resp.GetFound() {
		requiresAuth, requiresKnown, clusterPeers := resolveArtifactPolicy(ctx, resp.GetInternalName())
		target := &StreamTarget{
			InternalName:      "vod+" + resp.GetInternalName(),
			IsVod:             true,
			TenantID:          resp.GetTenantId(),
			StreamID:          resp.GetStreamId(),
			ContentType:       "clip",
			ClusterPeers:      clusterPeers,
			RequiresAuth:      requiresAuth,
			RequiresAuthKnown: requiresKnown,
		}
		applyArtifactPlacement(ctx, artifactHash, target)
		return target
	}

	if resp, err := CommodoreClient.ResolveVodHash(ctx, artifactHash); err == nil && resp.GetFound() {
		requiresAuth, requiresKnown, clusterPeers := resolveArtifactPolicy(ctx, resp.GetInternalName())
		target := &StreamTarget{
			InternalName:      "vod+" + resp.GetInternalName(),
			IsVod:             true,
			TenantID:          resp.GetTenantId(),
			ContentType:       "vod",
			ClusterPeers:      clusterPeers,
			RequiresAuth:      requiresAuth,
			RequiresAuthKnown: requiresKnown,
		}
		applyArtifactPlacement(ctx, artifactHash, target)
		return target
	}

	return nil
}

func resolveArtifactPolicy(ctx context.Context, artifactInternalName string) (bool, bool, []*pb.TenantClusterPeer) {
	if CommodoreClient == nil || artifactInternalName == "" {
		return false, false, nil
	}
	resp, err := CommodoreClient.ResolveArtifactInternalName(ctx, artifactInternalName)
	if err != nil || resp == nil || !resp.GetFound() {
		return false, false, nil
	}
	return resp.GetRequiresAuth(), true, resp.GetClusterPeers()
}

// ResolveArtifactByHash resolves an artifact hash to tenant/content context and placement.
// Intended for internal use only (no legacy playback support).
func ResolveArtifactByHash(ctx context.Context, artifactHash string) (*StreamTarget, error) {
	if artifactHash == "" {
		return &StreamTarget{}, nil
	}

	target := &StreamTarget{
		IsVod: true,
	}

	applyArtifactPlacement(ctx, artifactHash, target)

	// Resolve TenantID and ContentType via Commodore (business registry owner)
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolveClipHash(ctx, artifactHash); err == nil && resp.Found {
			target.TenantID = resp.TenantId
			target.StreamID = resp.StreamId
			target.ContentType = "clip"
			if resp.InternalName != "" {
				target.InternalName = "vod+" + resp.InternalName
			}
			return target, nil
		}
		if resp, err := CommodoreClient.ResolveDVRHash(ctx, artifactHash); err == nil && resp.Found {
			target.TenantID = resp.TenantId
			target.StreamID = resp.StreamId
			target.ContentType = "dvr"
			if resp.InternalName != "" {
				target.InternalName = "vod+" + resp.InternalName
			}
			return target, nil
		}
		if resp, err := CommodoreClient.ResolveVodHash(ctx, artifactHash); err == nil && resp.Found {
			target.TenantID = resp.TenantId
			target.ContentType = "vod"
			if resp.InternalName != "" {
				target.InternalName = "vod+" + resp.InternalName
			}
			return target, nil
		}
	}

	return target, nil
}

func applyArtifactPlacement(ctx context.Context, artifactHash string, target *StreamTarget) {
	if target == nil || artifactHash == "" {
		return
	}

	if manager := state.DefaultManager(); manager != nil {
		nodes := manager.FindNodesByArtifactHash(artifactHash)
		if len(nodes) > 0 {
			best := nodes[0]
			for _, n := range nodes[1:] {
				if n.Score < best.Score {
					best = n
				}
			}
			target.FixedNode = best.Host
			target.FixedNodeID = best.NodeID
			return
		}
	}

	// Cache Miss: No local nodes have the artifact - check if synced to S3
	if artifactRepo != nil {
		if info, err := artifactRepo.GetArtifactSyncInfo(ctx, artifactHash); err == nil && info != nil && info.SyncStatus == "synced" && info.S3URL != "" {
			target.NeedsDefrost = true
			target.S3URL = info.S3URL
			if target.ContentType == "" {
				target.ContentType = info.ArtifactType
			}

			// Pick any storage node for defrost (prefer one that's healthy and has storage capability)
			if loadBalancerInstance != nil {
				for _, node := range loadBalancerInstance.GetNodes() {
					if node.CapStorage && node.IsHealthy {
						target.FixedNodeID = node.NodeID
						if baseURL, err := loadBalancerInstance.GetNodeByID(node.NodeID); err == nil {
							target.FixedNode = baseURL
						}
						break
					}
				}
			}
		}
	}
}
