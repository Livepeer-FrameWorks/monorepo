package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Bridge → Commodore retention policy mutations + query.
// All four operations are tenant-scoped via JWT; Commodore enforces tenant
// isolation server-side. Cost-affecting: changes here resize the tenant's
// storage bill for future / existing recordings.

// DoMediaRetentionPolicy reads the tenant default + entitlement bounds + the
// value the cascade resolves to today.
func (r *Resolver) DoMediaRetentionPolicy(ctx context.Context) (*model.MediaRetentionPolicy, error) {
	if err := middleware.RequirePermission(ctx, "billing:read"); err != nil {
		return nil, err
	}
	resp, err := r.Clients.Commodore.GetMediaRetentionPolicy(ctx, &pb.GetMediaRetentionPolicyRequest{})
	if err != nil {
		r.Logger.WithError(err).Error("MediaRetentionPolicy: Commodore.GetMediaRetentionPolicy failed")
		return nil, fmt.Errorf("read retention policy: %w", err)
	}
	return mediaRetentionPolicyFromProto(resp), nil
}

// DoSetMediaRetentionPolicy upserts the tenant default. Validation against
// tier entitlement happens server-side in Commodore.
func (r *Resolver) DoSetMediaRetentionPolicy(ctx context.Context, input model.SetMediaRetentionPolicyInput) (model.SetMediaRetentionPolicyResult, error) {
	if err := middleware.RequirePermission(ctx, "billing:write"); err != nil {
		return nil, err
	}
	if input.RecordingRetentionDays < 1 {
		return &model.ValidationError{
			Message: "recordingRetentionDays must be at least 1",
			Field:   strPtr("recordingRetentionDays"),
		}, nil
	}
	resp, err := r.Clients.Commodore.SetMediaRetentionPolicy(ctx, &pb.SetMediaRetentionPolicyRequest{
		RecordingRetentionDays: int32(input.RecordingRetentionDays),
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

// DoUpdateMediaRetention applies a per-asset retention override on a
// finalized DVR recording.
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
	req := &pb.UpdateAssetRetentionRequest{
		TargetType: protoTargetType(input.TargetType),
		TargetId:   input.TargetID,
	}
	if input.RetentionUntil != nil {
		req.RetentionUntil = timestamppb.New(*input.RetentionUntil)
	}
	if input.RetentionDays != nil {
		req.RetentionDays = int32(*input.RetentionDays)
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

// DoResetMediaRetentionOverride clears a per-asset override and recomputes
// the horizon from the cascade (tenant default → tier entitlement).
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
	resp, err := r.Clients.Commodore.ResetAssetRetention(ctx, &pb.ResetAssetRetentionRequest{
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
// (no retention horizon yet) or when ExpiresAt isn't set.
func (r *Resolver) DoDVRRequestEffectiveRetention(ctx context.Context, info *pb.DVRInfo) (*model.EffectiveRetention, error) {
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
		RetentionUntil: until,
		Source:         retentionSourceFromString(info.GetRetentionSource()),
	}, nil
}

func mediaRetentionPolicyFromProto(p *pb.GetMediaRetentionPolicyResponse) *model.MediaRetentionPolicy {
	if p == nil {
		return nil
	}
	out := &model.MediaRetentionPolicy{
		EffectiveRecordingRetentionDays: int(p.GetEffectiveRecordingRetentionDays()),
		Bounds:                          p.GetBounds(),
	}
	if p.GetRecordingRetentionDaysSet() {
		v := int(p.GetRecordingRetentionDays())
		out.RecordingRetentionDays = &v
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

func effectiveRetentionFromProto(p *pb.UpdateAssetRetentionResponse) *model.EffectiveRetention {
	if p == nil {
		return nil
	}
	out := &model.EffectiveRetention{
		RetentionDays: int(p.GetRetentionDays()),
		Source:        retentionSourceFromString(p.GetSource()),
	}
	if ts := p.GetRetentionUntil(); ts != nil {
		out.RetentionUntil = ts.AsTime()
	}
	return out
}

// protoTargetType translates the GraphQL string enum to the wire-side proto
// enum. Unsupported targets fall through to UNSPECIFIED so Commodore returns
// a clear InvalidArgument; we don't silently coerce to DVR.
func protoTargetType(t model.MediaRetentionTarget) pb.MediaRetentionTarget {
	switch t {
	case model.MediaRetentionTargetDvr:
		return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR
	case model.MediaRetentionTargetClip:
		return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP
	case model.MediaRetentionTargetVod:
		return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD
	default:
		return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED
	}
}

func retentionSourceFromString(s string) model.RetentionSource {
	switch s {
	case "tenant_default":
		return model.RetentionSourceTenantDefault
	case "per_asset_override":
		return model.RetentionSourcePerAssetOverride
	default:
		return model.RetentionSourceTierEntitlement
	}
}

// mapInvalidArgument converts Commodore's InvalidArgument gRPC errors into a
// ValidationError union member so clients see a structured failure rather
// than a raw RPC error. Returns nil if the error is something else.
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

// mapNotFound maps NotFound gRPC errors to NotFoundError union members.
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
// members. Commodore returns this for tenant-mismatch on cross-tenant probes;
// surfacing the structured error lets clients distinguish "not yours" from
// transport failures.
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
// possible.
func guessFieldFromMessage(msg string) string {
	for _, candidate := range []string{
		"recording_retention_days", "retention_days", "retention_until",
		"target_id", "target_type",
	} {
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
