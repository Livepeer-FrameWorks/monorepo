package resolvers

import (
	"context"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

// DoTestPlaybackAccess wraps Commodore.TestPlaybackAccess. The mutation
// shape (rather than a query) is deliberate: webhook policies fire a real
// outbound HTTPS request to the customer URL when fireWebhook=true.
//
// Tenant ownership of the playback target is validated server-side in
// Commodore before forwarding to the owning Foghorn; this resolver doesn't
// re-check.
func (r *Resolver) DoTestPlaybackAccess(ctx context.Context, input model.TestPlaybackAccessInput) (model.TestPlaybackAccessResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	playback := deref(input.PlaybackID)
	internal := deref(input.InternalName)
	switch {
	case playback == "" && internal == "":
		return &model.ValidationError{
			Message: "exactly one of playbackId or internalName is required",
			Field:   strPtr("playbackId"),
		}, nil
	case playback != "" && internal != "":
		return &model.ValidationError{
			Message: "exactly one of playbackId or internalName may be provided",
			Field:   strPtr("internalName"),
		}, nil
	}
	if middleware.IsDemoMode(ctx) {
		return &model.PlaybackAccessDecision{
			Allowed:    true,
			PolicyType: "PUBLIC",
			Reason:     strPtr("demo playback access allowed"),
		}, nil
	}

	resp, err := r.Clients.Commodore.TestPlaybackAccess(ctx, &foghorncontrolpb.TestPlaybackAccessRequest{
		PlaybackId:   playback,
		InternalName: internal,
		ViewerToken:  deref(input.ViewerToken),
		ViewerIp:     deref(input.ViewerIP),
		RequestUrl:   deref(input.RequestURL),
		Connector:    deref(input.Connector),
		SessionId:    deref(input.SessionID),
		FireWebhook:  input.FireWebhook != nil && *input.FireWebhook,
	})
	if err != nil {
		if vErr := mapInvalidArgument(err); vErr != nil {
			return vErr, nil
		}
		if nfErr := mapNotFound(err); nfErr != nil {
			return nfErr, nil
		}
		if aErr := mapPermissionDenied(err); aErr != nil {
			return aErr, nil
		}
		r.Logger.WithError(err).Error("TestPlaybackAccess: Commodore RPC failed")
		return nil, fmt.Errorf("test playback access: %w", err)
	}

	out := &model.PlaybackAccessDecision{
		Allowed:    resp.GetAllowed(),
		PolicyType: resp.GetPolicyType(),
	}
	if v := resp.GetReason(); v != "" {
		out.Reason = strPtr(v)
	}
	if v := resp.GetDetail(); v != "" {
		out.Detail = strPtr(v)
	}
	if v := resp.GetKid(); v != "" {
		out.Kid = strPtr(v)
	}
	if v := resp.GetClaimsJson(); v != "" {
		out.ClaimsJSON = strPtr(v)
	}
	if v := resp.GetWebhookStatus(); v != 0 {
		i := int(v)
		out.WebhookStatus = &i
	}
	if v := resp.GetWebhookLatencyMs(); v != 0 {
		i := int(v)
		out.WebhookLatencyMs = &i
	}
	if v := resp.GetResolvedInternalName(); v != "" {
		out.ResolvedInternalName = strPtr(v)
	}
	return out, nil
}
