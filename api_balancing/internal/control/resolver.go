package control

import (
	"context"
	"strings"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"
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
	S3URL        string
	ClusterPeers []*pb.TenantClusterPeer // Tenant's cluster context from Commodore
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
				applyArtifactPlacement(ctx, resp.ArtifactHash, target)
			}
		}
		return target, nil
	}

	// 2. Artifact playback ID (clip/dvr/vod)
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolveArtifactPlaybackID(ctx, input); err == nil && resp.Found {
			target := &StreamTarget{
				InternalName: "vod+" + resp.ArtifactInternalName,
				IsVod:        true,
				TenantID:     resp.TenantId,
				StreamID:     resp.StreamId,
				ContentType:  resp.ContentType,
			}
			applyArtifactPlacement(ctx, resp.ArtifactHash, target)
			return target, nil
		}
	}

	// 3. Live view keys (playback_id)
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolvePlaybackID(ctx, input); err == nil {
			return &StreamTarget{
				InternalName: "live+" + resp.InternalName,
				IsVod:        false,
				TenantID:     resp.TenantId,
				StreamID:     resp.StreamId,
				ContentType:  "live",
				ClusterPeers: resp.ClusterPeers,
			}, nil
		}
	}

	// 4. Fallback: assume input is already an internal name.
	return &StreamTarget{InternalName: input}, nil
}

// ResolveArtifactByHash resolves an artifact hash to tenant/content context and placement.
// Intended for internal use only (no legacy playback support).
func ResolveArtifactByHash(ctx context.Context, artifactHash string) (*StreamTarget, error) {
	if artifactHash == "" {
		return &StreamTarget{}, nil
	}

	target := &StreamTarget{
		InternalName: "vod+" + artifactHash,
		IsVod:        true,
	}

	applyArtifactPlacement(ctx, artifactHash, target)

	// Resolve TenantID and ContentType via Commodore (business registry owner)
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolveClipHash(ctx, artifactHash); err == nil && resp.Found {
			target.TenantID = resp.TenantId
			target.StreamID = resp.StreamId
			target.ContentType = "clip"
			return target, nil
		}
		if resp, err := CommodoreClient.ResolveDVRHash(ctx, artifactHash); err == nil && resp.Found {
			target.TenantID = resp.TenantId
			target.StreamID = resp.StreamId
			target.ContentType = "dvr"
			return target, nil
		}
		if resp, err := CommodoreClient.ResolveVodHash(ctx, artifactHash); err == nil && resp.Found {
			target.TenantID = resp.TenantId
			target.ContentType = "vod"
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
