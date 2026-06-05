package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Bridge → Commodore retention policy mutations + query. All operations
// are tenant-scoped via JWT; Commodore enforces tenant isolation
// server-side. Cost-affecting: changes here resize the tenant's storage
// bill for future / existing recordings.

// DoMediaRetentionPolicy reads the tenant per-class defaults plus the
// effective horizon the cascade resolves to today for each class (no
// per-stream context).
func (r *Resolver) DoMediaRetentionPolicy(ctx context.Context) (*model.MediaRetentionPolicy, error) {
	if err := middleware.RequirePermission(ctx, "billing:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demoMediaRetentionPolicy(), nil
	}
	resp, err := r.Clients.Commodore.GetMediaRetentionPolicy(ctx, &commodorepb.GetMediaRetentionPolicyRequest{})
	if err != nil {
		r.Logger.WithError(err).Error("MediaRetentionPolicy: Commodore.GetMediaRetentionPolicy failed")
		return nil, fmt.Errorf("read retention policy: %w", err)
	}
	return mediaRetentionPolicyFromProto(resp), nil
}

// DoSetMediaRetentionPolicy writes a tenant per-class default. The input
// must target a single asset class (VOD, DVR, or CLIP). clear=true NULLs
// the column so the tenant inherits the system default.
func (r *Resolver) DoSetMediaRetentionPolicy(ctx context.Context, input model.SetMediaRetentionPolicyInput) (model.SetMediaRetentionPolicyResult, error) {
	if err := middleware.RequirePermission(ctx, "billing:write"); err != nil {
		return nil, err
	}

	target := protoTargetType(input.TargetType)
	if target == commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED {
		return &model.ValidationError{
			Message: "targetType must be VOD, DVR, or CLIP",
			Field:   strPtr("targetType"),
		}, nil
	}

	clear := false
	if input.Clear != nil {
		clear = *input.Clear
	}
	days := int32(0)
	if input.Days != nil {
		days = int32(*input.Days)
	}

	if !clear {
		if input.Days == nil {
			return &model.ValidationError{
				Message: "days is required unless clear=true",
				Field:   strPtr("days"),
			}, nil
		}
		if days < 0 {
			return &model.ValidationError{
				Message: "days must be >= 0 (0 = no auto-expire)",
				Field:   strPtr("days"),
			}, nil
		}
	}
	if middleware.IsDemoMode(ctx) {
		return demoMediaRetentionPolicy(), nil
	}

	resp, err := r.Clients.Commodore.SetMediaRetentionPolicy(ctx, &commodorepb.SetMediaRetentionPolicyRequest{
		TargetType: target,
		Days:       days,
		Clear:      clear,
	})
	if err != nil {
		if vErr := mapInvalidArgument(err); vErr != nil {
			return vErr, nil
		}
		r.Logger.WithError(err).Error("SetMediaRetentionPolicy failed")
		return nil, fmt.Errorf("set retention policy: %w", err)
	}
	return mediaRetentionPolicyFromProto(resp.GetPolicy()), nil
}

// DoSetStreamRetentionOverrides writes per-stream DVR/clip retention
// overrides. clearXxxOverride=true on a class takes precedence over a
// value on that class. 0 = keep forever (clamped to the tier cap on
// Free); >0 = days.
func (r *Resolver) DoSetStreamRetentionOverrides(ctx context.Context, input model.SetStreamRetentionOverridesInput) (model.SetStreamRetentionOverridesResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if input.StreamID == "" {
		return &model.ValidationError{
			Message: "streamId is required",
			Field:   strPtr("streamId"),
		}, nil
	}

	req := &commodorepb.SetStreamRetentionOverridesRequest{
		StreamId: input.StreamID,
	}
	if input.DvrRetentionDaysOverride != nil {
		v := int32(*input.DvrRetentionDaysOverride)
		req.DvrRetentionDaysOverride = &v
	}
	if input.ClipRetentionDaysOverride != nil {
		v := int32(*input.ClipRetentionDaysOverride)
		req.ClipRetentionDaysOverride = &v
	}
	if input.ClearDvrRetentionOverride != nil {
		req.ClearDvrRetentionOverride = *input.ClearDvrRetentionOverride
	}
	if input.ClearClipRetentionOverride != nil {
		req.ClearClipRetentionOverride = *input.ClearClipRetentionOverride
	}
	if req.DvrRetentionDaysOverride == nil && req.ClipRetentionDaysOverride == nil &&
		!req.ClearDvrRetentionOverride && !req.ClearClipRetentionOverride {
		return &model.ValidationError{
			Message: "at least one override or clear flag must be set",
		}, nil
	}
	if middleware.IsDemoMode(ctx) {
		out := &model.StreamRetentionOverrides{StreamID: input.StreamID}
		if v := input.DvrRetentionDaysOverride; v != nil {
			out.DvrRetentionDaysOverride = v
		}
		if v := input.ClipRetentionDaysOverride; v != nil {
			out.ClipRetentionDaysOverride = v
		}
		return out, nil
	}

	resp, err := r.Clients.Commodore.SetStreamRetentionOverrides(ctx, req)
	if err != nil {
		if vErr := mapInvalidArgument(err); vErr != nil {
			return vErr, nil
		}
		if nfErr := mapNotFound(err); nfErr != nil {
			return nfErr, nil
		}
		if authErr := mapPermissionDenied(err); authErr != nil {
			return authErr, nil
		}
		r.Logger.WithError(err).Error("SetStreamRetentionOverrides failed")
		return nil, fmt.Errorf("set stream retention overrides: %w", err)
	}
	out := &model.StreamRetentionOverrides{StreamID: resp.GetStreamId()}
	if v := resp.DvrRetentionDaysOverride; v != nil {
		iv := int(*v)
		out.DvrRetentionDaysOverride = &iv
	}
	if v := resp.ClipRetentionDaysOverride; v != nil {
		iv := int(*v)
		out.ClipRetentionDaysOverride = &iv
	}
	return out, nil
}

// DoUpdateMediaRetention applies a per-asset retention override.
// retentionDays = 0 means "keep forever" (Commodore writes NULL
// retention_until; Foghorn's RetentionJob skips the artifact).
func (r *Resolver) DoUpdateMediaRetention(ctx context.Context, input model.UpdateMediaRetentionInput) (model.UpdateMediaRetentionResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if input.TargetID == "" {
		return &model.ValidationError{
			Message: "targetId is required",
			Field:   strPtr("targetId"),
		}, nil
	}
	if input.RetentionDays == nil && input.RetentionUntil == nil {
		return &model.ValidationError{
			Message: "either retentionDays or retentionUntil must be set",
		}, nil
	}
	if input.RetentionDays != nil && input.RetentionUntil != nil {
		return &model.ValidationError{
			Message: "retentionDays and retentionUntil are mutually exclusive",
			Field:   strPtr("retentionDays"),
		}, nil
	}
	if input.RetentionDays != nil && *input.RetentionDays < 0 {
		return &model.ValidationError{
			Message: "retentionDays must be >= 0 (0 = keep forever)",
			Field:   strPtr("retentionDays"),
		}, nil
	}
	if middleware.IsDemoMode(ctx) {
		return demoEffectiveRetention(input.RetentionDays), nil
	}
	req := &commodorepb.UpdateAssetRetentionRequest{
		TargetType: protoTargetType(input.TargetType),
		TargetId:   input.TargetID,
	}
	if input.RetentionUntil != nil {
		req.RetentionUntil = timestamppb.New(*input.RetentionUntil)
	}
	if input.RetentionDays != nil {
		v := int32(*input.RetentionDays)
		req.RetentionDays = &v
	}
	resp, err := r.Clients.Commodore.UpdateAssetRetention(ctx, req)
	if err != nil {
		if vErr := mapInvalidArgument(err); vErr != nil {
			return vErr, nil
		}
		if nfErr := mapNotFound(err); nfErr != nil {
			return nfErr, nil
		}
		if authErr := mapPermissionDenied(err); authErr != nil {
			return authErr, nil
		}
		if preErr := mapFailedPrecondition(err); preErr != nil {
			return preErr, nil
		}
		r.Logger.WithError(err).Error("UpdateAssetRetention failed")
		return nil, fmt.Errorf("update asset retention: %w", err)
	}
	return effectiveRetentionFromProto(resp), nil
}

// DoResetMediaRetentionOverride clears a per-asset override and
// recomputes the horizon from the per-class cascade (which itself
// consults per-stream and tenant default).
func (r *Resolver) DoResetMediaRetentionOverride(ctx context.Context, input model.ResetMediaRetentionOverrideInput) (model.UpdateMediaRetentionResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if input.TargetID == "" {
		return &model.ValidationError{
			Message: "targetId is required",
			Field:   strPtr("targetId"),
		}, nil
	}
	if middleware.IsDemoMode(ctx) {
		return demoEffectiveRetention(nil), nil
	}
	resp, err := r.Clients.Commodore.ResetAssetRetention(ctx, &commodorepb.ResetAssetRetentionRequest{
		TargetType: protoTargetType(input.TargetType),
		TargetId:   input.TargetID,
	})
	if err != nil {
		if vErr := mapInvalidArgument(err); vErr != nil {
			return vErr, nil
		}
		if nfErr := mapNotFound(err); nfErr != nil {
			return nfErr, nil
		}
		if authErr := mapPermissionDenied(err); authErr != nil {
			return authErr, nil
		}
		if preErr := mapFailedPrecondition(err); preErr != nil {
			return preErr, nil
		}
		r.Logger.WithError(err).Error("ResetAssetRetention failed")
		return nil, fmt.Errorf("reset asset retention: %w", err)
	}
	return effectiveRetentionFromProto(resp), nil
}

// DoDVRRequestEffectiveRetention is the field resolver for
// DVRRequest.effectiveRetention. Returns nil while the recording is active
// (no horizon yet) or when ExpiresAt isn't set (artifact kept forever).
func (r *Resolver) DoDVRRequestEffectiveRetention(ctx context.Context, info *sharedpb.DVRInfo) (*model.EffectiveRetention, error) {
	if info == nil || info.ExpiresAt == nil {
		return nil, nil
	}
	until := info.ExpiresAt.AsTime()
	dur := time.Until(until)
	days := int((dur + 24*time.Hour - 1) / (24 * time.Hour))
	if days < 0 {
		days = 0
	}
	return &model.EffectiveRetention{
		RetentionDays:  days,
		RetentionUntil: &until,
		Source:         RetentionSourceFromString(info.GetRetentionSource()),
	}, nil
}

func mediaRetentionPolicyFromProto(p *commodorepb.GetMediaRetentionPolicyResponse) *model.MediaRetentionPolicy {
	if p == nil {
		return nil
	}
	out := &model.MediaRetentionPolicy{
		Bounds:                     p.GetBounds(),
		EffectiveVodRetentionDays:  int(p.GetEffectiveVodRetentionDays()),
		EffectiveDvrRetentionDays:  int(p.GetEffectiveDvrRetentionDays()),
		EffectiveClipRetentionDays: int(p.GetEffectiveClipRetentionDays()),
	}
	if v := p.DefaultVodRetentionDays; v != nil {
		iv := int(*v)
		out.DefaultVodRetentionDays = &iv
	}
	if v := p.DefaultDvrRetentionDays; v != nil {
		iv := int(*v)
		out.DefaultDvrRetentionDays = &iv
	}
	if v := p.DefaultClipRetentionDays; v != nil {
		iv := int(*v)
		out.DefaultClipRetentionDays = &iv
	}
	if u := p.GetUpdatedBy(); u != "" {
		out.UpdatedBy = &u
	}
	if ts := p.GetUpdatedAt(); ts != nil {
		t := ts.AsTime()
		out.UpdatedAt = &t
	}
	return out
}

func demoMediaRetentionPolicy() *model.MediaRetentionPolicy {
	now := time.Now()
	updatedBy := "demo"
	return &model.MediaRetentionPolicy{
		Bounds:                     &commodorepb.MediaRetentionBounds{MaxRecordingRetentionDays: 90},
		UpdatedBy:                  &updatedBy,
		UpdatedAt:                  &now,
		EffectiveVodRetentionDays:  0,
		EffectiveDvrRetentionDays:  30,
		EffectiveClipRetentionDays: 30,
	}
}

func effectiveRetentionFromProto(p *commodorepb.UpdateAssetRetentionResponse) *model.EffectiveRetention {
	if p == nil {
		return nil
	}
	out := &model.EffectiveRetention{
		RetentionDays: int(p.GetRetentionDays()),
		Source:        RetentionSourceFromString(p.GetSource()),
	}
	if ts := p.GetRetentionUntil(); ts != nil {
		t := ts.AsTime()
		out.RetentionUntil = &t
	}
	return out
}

func demoEffectiveRetention(retentionDays *int) *model.EffectiveRetention {
	days := 30
	if retentionDays != nil {
		days = *retentionDays
	}
	until := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	out := &model.EffectiveRetention{
		RetentionDays: days,
		Source:        model.RetentionSourcePerAssetOverride,
	}
	if days > 0 {
		out.RetentionUntil = &until
	}
	return out
}

// protoTargetType translates the GraphQL string enum to the wire-side proto
// enum. Unsupported targets fall through to UNSPECIFIED so Commodore returns
// a clear InvalidArgument instead of silently coercing.
func protoTargetType(t model.MediaRetentionTarget) commodorepb.MediaRetentionTarget {
	switch t {
	case model.MediaRetentionTargetDvr:
		return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR
	case model.MediaRetentionTargetClip:
		return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP
	case model.MediaRetentionTargetVod:
		return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD
	default:
		return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED
	}
}

func RetentionSourceFromString(s string) model.RetentionSource {
	switch s {
	case "tenant_default":
		return model.RetentionSourceTenantDefault
	case "per_asset_override":
		return model.RetentionSourcePerAssetOverride
	default:
		return model.RetentionSourceTierEntitlement
	}
}

// mapInvalidArgument converts Commodore's InvalidArgument gRPC errors into
// a ValidationError union member.
func mapInvalidArgument(err error) *model.ValidationError {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		return nil
	}
	msg := st.Message()
	field := guessFieldFromMessage(msg)
	v := &model.ValidationError{Message: msg}
	if field != "" {
		v.Field = strPtr(field)
	}
	return v
}

func mapNotFound(err error) *model.NotFoundError {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		return nil
	}
	return &model.NotFoundError{Message: st.Message()}
}

// mapPermissionDenied maps PermissionDenied gRPC errors to AuthError union
// members. Commodore returns this for tenant-mismatch on cross-tenant
// probes; surfacing the structured error lets clients distinguish "not
// yours" from transport failures.
func mapPermissionDenied(err error) *model.AuthError {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		return nil
	}
	return &model.AuthError{Message: st.Message()}
}

// mapFailedPrecondition maps routeability / lifecycle preconditions to
// ValidationError so clients can surface data issues without a generic 500.
func mapFailedPrecondition(err error) *model.ValidationError {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return nil
	}
	msg := st.Message()
	v := &model.ValidationError{Message: msg}
	if field := guessFieldFromMessage(msg); field != "" {
		v.Field = strPtr(field)
	}
	return v
}

// guessFieldFromMessage extracts the first identifier-shaped token from an
// error message so ValidationError.field gives the client a hint when
// possible. Order matters — longer candidates first so substrings don't
// shadow them.
func guessFieldFromMessage(msg string) string {
	candidates := []string{
		"default_vod_retention_days",
		"default_dvr_retention_days",
		"default_clip_retention_days",
		"dvr_retention_days_override",
		"clip_retention_days_override",
		"retention_days",
		"retention_until",
		"target_id",
		"target_type",
		"stream_id",
		"days",
	}
	for _, candidate := range candidates {
		if strings.Contains(msg, candidate) {
			return camelCase(candidate)
		}
	}
	return ""
}

func camelCase(snake string) string {
	parts := strings.Split(snake, "_")
	if len(parts) == 0 {
		return snake
	}
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}
