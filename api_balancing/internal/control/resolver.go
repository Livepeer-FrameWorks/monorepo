package control

import (
	"context"
	"strings"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/clients/commodore"
)

// CommodoreClient holds the reference to the commodore gRPC client for resolution.
// This should be set during application initialization (e.g. in handlers.Init).
var CommodoreClient *commodore.GRPCClient

// StreamTarget describes the resolution result.
type StreamTarget struct {
	InternalName string
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
	S3URL string
}

// ResolveStream determines the target stream name and node constraint for a given input.
// Input can be: Internal Name, View Key, or Artifact Hash.
// This unifies resolution logic across HTTP handlers and Mist triggers.
func ResolveStream(ctx context.Context, input string) (*StreamTarget, error) {
	// 1. Optimization: If already internal name, pass through.
	// This handles cases where Mist or an internal service requests a stream by its canonical name.
	if strings.HasPrefix(input, "live+") || strings.HasPrefix(input, "vod+") {
		target := &StreamTarget{InternalName: input}

		// Even if already canonical, enrich tenant context for analytics/billing.
		if CommodoreClient != nil {
			// live+ streams: internal name is the suffix after live+
			if strings.HasPrefix(input, "live+") {
				internal := strings.TrimPrefix(input, "live+")
				if internal != "" {
					if resp, err := CommodoreClient.ResolveInternalName(ctx, internal); err == nil {
						target.TenantID = resp.TenantId
						target.ContentType = "live"
					}
				}
			}

			// vod+ streams: suffix is artifact hash (clip or dvr)
			if strings.HasPrefix(input, "vod+") {
				hash := strings.TrimPrefix(input, "vod+")
				target.IsVod = true

				// If we have local artifact placement info, pin to a node (same selection logic as below).
				nodes := state.DefaultManager().FindNodesByArtifactHash(hash)
				if len(nodes) > 0 {
					best := nodes[0]
					for _, n := range nodes[1:] {
						if n.Score < best.Score {
							best = n
						}
					}
					target.FixedNode = best.Host
					target.FixedNodeID = best.NodeID
				}

				// Resolve tenant + type via Commodore registry.
				if resp, err := CommodoreClient.ResolveClipHash(ctx, hash); err == nil && resp.Found {
					target.TenantID = resp.TenantId
					target.ContentType = "clip"
				} else if resp, err := CommodoreClient.ResolveDVRHash(ctx, hash); err == nil && resp.Found {
					target.TenantID = resp.TenantId
					target.ContentType = "dvr"
				}
			}
		}

		return target, nil
	}

	// 2. Check VOD Artifacts (In-Memory Snapshot).
	// This covers Clip Hashes and DVR Hashes. If we find an artifact, it is stored on specific nodes.
	// We return the host of the best node (lowest score) for load balancing.
	nodes := state.DefaultManager().FindNodesByArtifactHash(input)
	if len(nodes) > 0 {
		// Pick the best node (lowest score)
		best := nodes[0]
		for _, n := range nodes[1:] {
			if n.Score < best.Score {
				best = n
			}
		}

		target := &StreamTarget{
			InternalName: "vod+" + input,
			FixedNode:    best.Host,
			FixedNodeID:  best.NodeID,
			IsVod:        true,
		}

		// Resolve TenantID and ContentType via Commodore (business registry owner)
		// foghorn.artifacts has no tenant_id - use Commodore for tenant context
		if CommodoreClient != nil {
			// Try clip first
			if resp, err := CommodoreClient.ResolveClipHash(ctx, input); err == nil && resp.Found {
				target.TenantID = resp.TenantId
				target.ContentType = "clip"
			} else {
				// Try DVR
				if resp, err := CommodoreClient.ResolveDVRHash(ctx, input); err == nil && resp.Found {
					target.TenantID = resp.TenantId
					target.ContentType = "dvr"
				}
			}
		}

		return target, nil
	}

	// 2b. Cache Miss: No local nodes have the artifact - check if synced to S3
	if artifactRepo != nil {
		if info, err := artifactRepo.GetArtifactSyncInfo(ctx, input); err == nil && info != nil && info.SyncStatus == "synced" && info.S3URL != "" {
			// Artifact is synced to S3 but no local cache - need defrost
			target := &StreamTarget{
				InternalName: "vod+" + input,
				IsVod:        true,
				NeedsDefrost: true,
				S3URL:        info.S3URL,
				ContentType:  info.ArtifactType,
			}

			// Get tenant info from Commodore (business registry owner)
			// foghorn.artifacts has no tenant_id - use Commodore for tenant context
			if CommodoreClient != nil {
				if info.ArtifactType == "clip" {
					if resp, err := CommodoreClient.ResolveClipHash(ctx, input); err == nil && resp.Found {
						target.TenantID = resp.TenantId
					}
				} else if info.ArtifactType == "dvr" {
					if resp, err := CommodoreClient.ResolveDVRHash(ctx, input); err == nil && resp.Found {
						target.TenantID = resp.TenantId
					}
				}
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

			return target, nil
		}
	}

	// 3. Check Live View Keys (Commodore).

	// If it's not an artifact, check if it's a view key for a live stream.

	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolvePlaybackID(ctx, input); err == nil {
			// Success: We found a live stream.
			// Prepend "live+" so MistServer matches the "live+$" wildcard configuration.
			return &StreamTarget{
				InternalName: "live+" + resp.InternalName,
				IsVod:        false,
				TenantID:     resp.TenantId,
				ContentType:  "live",
			}, nil
		}
	}

	// 4. Fallback: Assume input is the name.
	// If all resolution fails, we treat the input as the requested stream name.
	return &StreamTarget{InternalName: input}, nil
}
