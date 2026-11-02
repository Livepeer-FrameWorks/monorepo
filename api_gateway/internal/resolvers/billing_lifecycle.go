package resolvers

import (
	"fmt"
	"strings"

	"frameworks/api_gateway/graph/model"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// billingErrorDomain is the ErrorInfo domain Purser uses for refusals a
// tenant can act on.
const billingErrorDomain = "billing.frameworks.network"

// FreeFallbackReason maps Purser's free_fallback_reason.
func FreeFallbackReason(obj *purserpb.FreeFallbackStatus) (model.FreeFallbackReason, error) {
	switch obj.GetReason() {
	case "payment_failed":
		return model.FreeFallbackReasonPaymentFailed, nil
	case "cancelled":
		return model.FreeFallbackReasonCancelled, nil
	}
	return "", fmt.Errorf("unknown free fallback reason %q", obj.GetReason())
}

// FreeFallbackStage maps Purser's free fallback stage.
func FreeFallbackStage(obj *purserpb.FreeFallbackStatus) (model.FreeFallbackStage, error) {
	switch obj.GetStage() {
	case "scheduled":
		return model.FreeFallbackStageScheduled, nil
	case "applying":
		return model.FreeFallbackStageApplying, nil
	case "moved":
		return model.FreeFallbackStageMoved, nil
	}
	return "", fmt.Errorf("unknown free fallback stage %q", obj.GetStage())
}

// PendingTierState maps Purser's pending_tier_state; empty when no downgrade is staged.
func PendingTierState(obj *purserpb.BillingStatusResponse) *model.PendingTierState {
	var state model.PendingTierState
	switch obj.GetPendingTierState() {
	case "scheduled":
		state = model.PendingTierStateScheduled
	case "final":
		state = model.PendingTierStateFinal
	case "applying":
		state = model.PendingTierStateApplying
	default:
		return nil
	}
	return &state
}

// billingMutationError is the ValidationError for a failed billing mutation.
// A typed refusal keeps its reason and message; a precondition or invalid
// argument keeps Purser's message under fallbackCode; anything else shows only
// the fallback text, never transport or internal detail.
func billingMutationError(err error, fallback, fallbackCode, field string) *model.ValidationError {
	if refusal, ok := billingRefusal(err, field); ok {
		return refusal
	}
	message := fallback
	if st, isStatus := status.FromError(err); isStatus && (st.Code() == codes.FailedPrecondition || st.Code() == codes.InvalidArgument) && strings.TrimSpace(st.Message()) != "" {
		message = st.Message()
	}
	out := &model.ValidationError{Message: message}
	if fallbackCode != "" {
		out.Code = &fallbackCode
	}
	if field != "" {
		out.Field = &field
	}
	return out
}

// billingRefusal turns a typed Purser refusal into the ValidationError a
// tenant sees: the reason is the code and Purser's public message the text.
// ok is false for any other error, which the caller must not show verbatim.
func billingRefusal(err error, field string) (*model.ValidationError, bool) {
	st, isStatus := status.FromError(err)
	if !isStatus {
		return nil, false
	}
	for _, detail := range st.Details() {
		info, isInfo := detail.(*errdetails.ErrorInfo)
		if !isInfo || info.GetDomain() != billingErrorDomain || info.GetReason() == "" {
			continue
		}
		message := strings.TrimSpace(info.GetMetadata()["public_message"])
		if message == "" {
			message = st.Message()
		}
		reason := info.GetReason()
		refusal := &model.ValidationError{Message: message, Code: &reason}
		if field != "" {
			refusal.Field = &field
		}
		return refusal, true
	}
	return nil, false
}
