package resolvers

import (
	"context"
	"fmt"
	"sync"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const originalDownloadRate = 20
const originalDownloadBurst = 5

// One limiter per gateway process; keys isolate each tenant's original download grants.
var originalDownloads struct {
	once    sync.Once
	limiter *middleware.RateLimiter
}

func (r *Resolver) DoSetVodOriginalPolicy(ctx context.Context, keep *bool) (model.SetMediaRetentionPolicyResult, error) {
	if err := middleware.RequireTenantAction(ctx, "billing:write", authz.ActionManageBilling, ctxkeys.GetTenantID(ctx)); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		p := demoMediaRetentionPolicy()
		p.KeepVodOriginals = keep
		p.EffectiveKeepVodOriginals = keep == nil || *keep
		return p, nil
	}
	policy, err := r.Clients.Commodore.SetVodOriginalPolicy(ctx, &commodorepb.SetVodOriginalPolicyRequest{KeepVodOriginals: keep})
	if err != nil {
		return nil, fmt.Errorf("set original upload policy: %w", err)
	}
	return mediaRetentionPolicyFromProto(policy), nil
}

func (r *Resolver) DoCreateVodOriginalDownload(ctx context.Context, id string) (model.CreateVodOriginalDownloadResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	rawID, err := globalid.DecodeExpected(id, globalid.TypeVodAsset)
	if err != nil {
		return &model.ValidationError{Message: "Invalid VOD ID", Field: strPtr("id")}, nil //nolint:nilerr // Validation is represented by the GraphQL result union.
	}
	originalDownloads.once.Do(func() {
		originalDownloads.limiter = middleware.NewRateLimiter(middleware.RateLimitConfig{BurstIsCapacity: true})
	})
	if allowed, _, _ := originalDownloads.limiter.Allow("vod-original:"+ctxkeys.GetTenantID(ctx), originalDownloadRate, originalDownloadBurst); !allowed {
		return &model.ValidationError{Message: "Too many original download requests; try again shortly"}, nil
	}
	if middleware.IsDemoMode(ctx) {
		return &model.ValidationError{Message: "Original downloads are unavailable in the demo"}, nil
	}
	resp, err := r.Clients.Commodore.CreateVodOriginalDownload(ctx, &sharedpb.CreateVodOriginalDownloadRequest{ArtifactHash: rawID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return &model.NotFoundError{Message: "VOD not found"}, nil
		}
		if status.Code(err) == codes.FailedPrecondition || status.Code(err) == codes.ResourceExhausted {
			message := status.Convert(err).Message()
			if message == "original_not_kept" {
				message = "No original upload is retained for this video"
			}
			return &model.ValidationError{Message: message}, nil
		}
		return nil, fmt.Errorf("download original: %w", err)
	}
	return &model.VodOriginalDownload{URL: resp.GetUrl(), ExpiresAt: resp.GetExpiresAt().AsTime(), SizeBytes: float64(resp.GetSizeBytes()), Filename: resp.GetFilename()}, nil
}

func (r *Resolver) DoDeleteVodOriginal(ctx context.Context, id string) (model.DeleteVodOriginalResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	rawID, err := globalid.DecodeExpected(id, globalid.TypeVodAsset)
	if err != nil {
		return &model.ValidationError{Message: "Invalid VOD ID", Field: strPtr("id")}, nil //nolint:nilerr // Validation is represented by the GraphQL result union.
	}
	if middleware.IsDemoMode(ctx) {
		return &model.ValidationError{Message: "Original deletion is unavailable in the demo"}, nil
	}
	resp, err := r.Clients.Commodore.DeleteVodOriginal(ctx, &sharedpb.DeleteVodOriginalRequest{ArtifactHash: rawID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return &model.NotFoundError{Message: "VOD not found"}, nil
		}
		if status.Code(err) == codes.FailedPrecondition {
			return &model.ValidationError{Message: status.Convert(err).Message()}, nil
		}
		return nil, fmt.Errorf("delete original: %w", err)
	}
	if !resp.GetSuccess() {
		return &model.ValidationError{Message: "No original upload is retained for this video"}, nil
	}
	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}
