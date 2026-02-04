package graph

import (
	"context"
	"strings"

	"frameworks/api_gateway/internal/loaders"
	gwmiddleware "frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	pb "frameworks/pkg/proto"
)

// getLifecycleData fetches artifact lifecycle data from the ArtifactLifecycleLoader.
// Used by clip resolvers to get processing status, file paths, etc.
func (r *clipResolver) getLifecycleData(ctx context.Context, requestID string) *pb.ArtifactState {
	if requestID == "" {
		return nil
	}

	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return nil
	}

	lds := loaders.FromContext(ctx)
	if lds == nil || lds.ArtifactLifecycle == nil {
		return nil
	}

	state, err := lds.ArtifactLifecycle.Load(ctx, tenantID, requestID)
	if err != nil {
		return nil
	}

	return state
}

// getLifecycleData fetches artifact lifecycle data from the ArtifactLifecycleLoader.
// Used by DVR resolvers to get processing status, file paths, etc.
func (r *dVRRequestResolver) getLifecycleData(ctx context.Context, requestID string) *pb.ArtifactState {
	if requestID == "" {
		return nil
	}

	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return nil
	}

	lds := loaders.FromContext(ctx)
	if lds == nil || lds.ArtifactLifecycle == nil {
		return nil
	}

	state, err := lds.ArtifactLifecycle.Load(ctx, tenantID, requestID)
	if err != nil {
		return nil
	}

	return state
}

// resolveArtifactPlaybackID resolves an artifact playbackId from a content type + hash.
// Best-effort: returns nil on lookup errors or missing mappings.
func (r *Resolver) resolveArtifactPlaybackID(ctx context.Context, contentType, hash string) *string {
	if hash == "" || gwmiddleware.IsDemoMode(ctx) || r == nil || r.Resolver == nil || r.Resolver.Clients == nil || r.Resolver.Clients.Commodore == nil {
		return nil
	}

	var logger logging.Logger
	if r.Resolver.Logger != nil {
		logger = r.Resolver.Logger
	}

	lds := loaders.FromContext(ctx)
	memo := (*loaders.Memoizer)(nil)
	if lds != nil {
		memo = lds.Memo
	}

	key := "artifact_playback:" + strings.ToLower(contentType) + ":" + hash
	lookup := func() (interface{}, error) {
		ct := strings.ToLower(contentType)
		switch ct {
		case "clip":
			resp, err := r.Resolver.Clients.Commodore.ResolveClipHash(ctx, hash)
			if err != nil {
				if logger != nil {
					logger.WithField("content_type", ct).WithField("hash", hash).WithError(err).Debug("Commodore clip hash resolution failed")
				}
				return nil, err
			}
			if !resp.Found || resp.PlaybackId == "" {
				if logger != nil {
					logger.WithField("content_type", ct).WithField("hash", hash).WithField("found", resp.Found).Debug("Commodore clip hash not found")
				}
				return nil, nil
			}
			return resp.PlaybackId, nil
		case "dvr":
			resp, err := r.Resolver.Clients.Commodore.ResolveDVRHash(ctx, hash)
			if err != nil {
				if logger != nil {
					logger.WithField("content_type", ct).WithField("hash", hash).WithError(err).Debug("Commodore DVR hash resolution failed")
				}
				return nil, err
			}
			if !resp.Found || resp.PlaybackId == "" {
				if logger != nil {
					logger.WithField("content_type", ct).WithField("hash", hash).WithField("found", resp.Found).Debug("Commodore DVR hash not found")
				}
				return nil, nil
			}
			return resp.PlaybackId, nil
		case "vod":
			resp, err := r.Resolver.Clients.Commodore.ResolveVodHash(ctx, hash)
			if err != nil {
				if logger != nil {
					logger.WithField("content_type", ct).WithField("hash", hash).WithError(err).Debug("Commodore VOD hash resolution failed")
				}
				return nil, err
			}
			if !resp.Found || resp.PlaybackId == "" {
				if logger != nil {
					logger.WithField("content_type", ct).WithField("hash", hash).WithField("found", resp.Found).Debug("Commodore VOD hash not found")
				}
				return nil, nil
			}
			return resp.PlaybackId, nil
		default:
			if resp, err := r.Resolver.Clients.Commodore.ResolveClipHash(ctx, hash); err == nil && resp.Found && resp.PlaybackId != "" {
				return resp.PlaybackId, nil
			}
			if resp, err := r.Resolver.Clients.Commodore.ResolveDVRHash(ctx, hash); err == nil && resp.Found && resp.PlaybackId != "" {
				return resp.PlaybackId, nil
			}
			if resp, err := r.Resolver.Clients.Commodore.ResolveVodHash(ctx, hash); err == nil && resp.Found && resp.PlaybackId != "" {
				return resp.PlaybackId, nil
			}
			if logger != nil {
				logger.WithField("content_type", contentType).WithField("hash", hash).Debug("Commodore hash resolution failed for unknown content type")
			}
			return nil, nil
		}
	}

	if memo == nil {
		val, err := lookup()
		if err != nil || val == nil {
			return nil
		}
		if s, ok := val.(string); ok && s != "" {
			return &s
		}
		return nil
	}

	val, err := memo.GetOrLoad(key, lookup)
	if err != nil || val == nil {
		return nil
	}
	s, ok := val.(string)
	if !ok || s == "" {
		return nil
	}
	return &s
}

// formatMetricName formats a metric key for display in invoice line items
func formatMetricName(metric string) string {
	names := map[string]string{
		"viewer_hours":     "Delivered Minutes",
		"storage_gb_hours": "Storage (GB-hours)",
		"bandwidth_gb":     "Bandwidth (GB)",
		"ingest_hours":     "Ingest Hours",
		"transcode_hours":  "Transcode Hours",
	}
	if name, ok := names[metric]; ok {
		return name
	}
	return metric
}
