package graph

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"

	"frameworks/api_gateway/internal/loaders"
	gwmiddleware "frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clips"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// artifactPlacement reads the analytics overlay after checking the caller's scope.
func artifactPlacement(ctx context.Context, requestID string) (*periscopepb.ArtifactState, error) {
	if err := gwmiddleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	if gwmiddleware.IsDemoMode(ctx) || requestID == "" {
		return nil, nil
	}
	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant context is required")
	}
	lds := loaders.FromContext(ctx)
	if lds == nil || lds.ArtifactLifecycle == nil {
		return nil, status.Error(codes.Unavailable, "artifact placement is unavailable")
	}
	return lds.ArtifactLifecycle.Load(ctx, tenantID, requestID)
}

func (r *clipResolver) getLifecycleData(ctx context.Context, requestID string) (*periscopepb.ArtifactState, error) {
	return artifactPlacement(ctx, requestID)
}

func (r *dVRRequestResolver) getLifecycleData(ctx context.Context, requestID string) (*periscopepb.ArtifactState, error) {
	return artifactPlacement(ctx, requestID)
}

// resolveArtifactPlaybackID resolves an artifact playbackId from a content type + hash.
// Best-effort: returns nil on lookup errors or missing mappings.
func (r *Resolver) resolveArtifactPlaybackID(ctx context.Context, contentType, hash string) *string {
	if hash == "" || gwmiddleware.IsDemoMode(ctx) || r == nil || r.Resolver == nil || r.Clients == nil || r.Clients.Commodore == nil {
		return nil
	}
	if !clips.ValidateClipHash(hash) {
		if r.Logger != nil {
			r.Logger.WithField("content_type", contentType).WithField("hash", hash).Debug("Skipping artifact playback resolution: invalid hash format")
		}
		return nil
	}

	var logger logging.Logger
	if r.Logger != nil {
		logger = r.Logger
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
			resp, err := r.Clients.Commodore.ResolveClipHash(ctx, hash)
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
			resp, err := r.Clients.Commodore.ResolveDVRHash(ctx, hash)
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
			resp, err := r.Clients.Commodore.ResolveVodHash(ctx, hash)
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
			if resp, err := r.Clients.Commodore.ResolveClipHash(ctx, hash); err == nil && resp.Found && resp.PlaybackId != "" {
				return resp.PlaybackId, nil
			}
			if resp, err := r.Clients.Commodore.ResolveDVRHash(ctx, hash); err == nil && resp.Found && resp.PlaybackId != "" {
				return resp.PlaybackId, nil
			}
			if resp, err := r.Clients.Commodore.ResolveVodHash(ctx, hash); err == nil && resp.Found && resp.PlaybackId != "" {
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
