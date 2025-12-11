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
	IsVod     bool
	// TenantID associated with the stream/artifact.
	TenantID string
	// ContentType indicates the artifact type: "clip", "dvr", or "live"
	ContentType string
}

// ResolveStream determines the target stream name and node constraint for a given input.
// Input can be: Internal Name, View Key, or Artifact Hash.
// This unifies resolution logic across HTTP handlers and Mist triggers.
func ResolveStream(ctx context.Context, input string) (*StreamTarget, error) {
	// 1. Optimization: If already internal name, pass through.
	// This handles cases where Mist or an internal service requests a stream by its canonical name.
	if strings.HasPrefix(input, "live+") || strings.HasPrefix(input, "vod+") {
		return &StreamTarget{InternalName: input}, nil
	}

	// 2. Check VOD Artifacts (In-Memory Snapshot).
	// This covers Clip Hashes and DVR Hashes. If we find an artifact, it is stored on specific nodes.
	// We return the host of one such node so the viewer can be redirected directly to the storage source.
	if host, _ := state.DefaultManager().FindNodeByArtifactHash(input); host != "" {
		target := &StreamTarget{
			InternalName: "vod+" + input,
			FixedNode:    host,
			IsVod:        true,
		}

		// Resolve TenantID and ContentType from DB for VOD
		if db != nil {
			var tid string
			// Check clips first
			err := db.QueryRowContext(ctx, "SELECT tenant_id FROM foghorn.clips WHERE clip_hash = $1", input).Scan(&tid)
			if err == nil {
				target.TenantID = tid
				target.ContentType = "clip"
			} else {
				// Check DVR
				err = db.QueryRowContext(ctx, "SELECT tenant_id FROM foghorn.dvr_requests WHERE request_hash = $1", input).Scan(&tid)
				if err == nil {
					target.TenantID = tid
					target.ContentType = "dvr"
				}
			}
		}

		return target, nil
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
