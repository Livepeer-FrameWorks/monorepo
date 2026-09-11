package resolvers

import (
	"context"
	"slices"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type placementAPIError interface {
	model.MediaPlacementPolicyResult
	model.MediaPlacementOptionsResult
	model.MediaPlacementPreviewResult
	model.MediaPlacementReviewResult
	model.MediaPlacementChangeResult
	model.MediaPlacementLegacyPinsResult
	model.MediaCapacityConsentResult
	model.MediaCapacityConsentChangeResult
}

func (r *Resolver) placementAccess(ctx context.Context, manage bool) placementAPIError {
	if middleware.IsDemoMode(ctx) {
		return placementDemoAccess(ctx, manage)
	}
	permission, action := "placement:read", authz.ActionReadMediaPlacement
	if manage {
		permission, action = "placement:write", authz.ActionManageMediaPlacement
	}
	if ctxkeys.GetTenantID(ctx) == "" || ctxkeys.GetUserID(ctx) == "" {
		return &model.AuthError{Message: "Tenant authentication is required."}
	}
	if ctxkeys.GetAuthType(ctx) == "api_token" && !slices.ContainsFunc(ctxkeys.GetPermissions(ctx), func(value string) bool { return strings.TrimSpace(value) == permission }) {
		return &model.AuthError{Message: "The API token does not include the required placement scope."}
	}
	if err := middleware.RequireTenantAction(ctx, permission, action, ctxkeys.GetTenantID(ctx)); err != nil {
		return &model.AuthError{Message: "This identity cannot access the requested placement operation."}
	}
	if r == nil || r.Clients == nil || r.Clients.Commodore == nil {
		return placementFailure(status.Error(codes.Unavailable, "placement service unavailable"))
	}
	return nil
}

func (r *Resolver) DoMediaPlacementPolicy(ctx context.Context, scope model.MediaPlacementScopeInput) (model.MediaPlacementPolicyResult, error) {
	if denied := r.placementAccess(ctx, false); denied != nil {
		return denied, nil
	}
	wire, err := placementScopeInput(&scope)
	if err != nil {
		return placementInvalidInput(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var response *placementpb.PolicyState
	if middleware.IsDemoMode(ctx) {
		response, err = demo.GenerateMediaPlacementPolicy(wire)
	} else {
		response, err = r.Clients.Commodore.GetMediaPlacementPolicy(ctx, &placementpb.GetPolicyRequest{Scope: wire})
	}
	if err != nil {
		return placementFailure(err), nil
	}
	if !proto.Equal(response.GetScope(), wire) {
		return placementFailure(status.Error(codes.Internal, "placement scope mismatch")), nil
	}
	out, err := placementPolicyOutput(response)
	if err != nil {
		return placementFailure(err), nil
	}
	return out, nil
}

func (r *Resolver) DoReviewMediaPlacementChange(ctx context.Context, input model.ReviewMediaPlacementChangeInput) (model.MediaPlacementReviewResult, error) {
	if denied := r.placementAccess(ctx, !middleware.IsDemoMode(ctx)); denied != nil {
		return denied, nil
	}
	wire, err := placementReviewInput(input)
	if err != nil {
		return placementInvalidInput(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var response *placementpb.Review
	if middleware.IsDemoMode(ctx) {
		response, err = demo.GenerateMediaPlacementReview(wire, time.Now().UTC())
	} else {
		response, err = r.Clients.Commodore.ReviewMediaPlacementChange(ctx, wire)
	}
	if err != nil {
		return placementFailure(err), nil
	}
	out, err := placementReviewOutput(response)
	if err != nil {
		return placementFailure(err), nil
	}
	return out, nil
}

func (r *Resolver) DoApplyMediaPlacementChange(ctx context.Context, input model.ApplyMediaPlacementChangeInput) (model.MediaPlacementChangeResult, error) {
	if denied := r.placementAccess(ctx, true); denied != nil {
		return denied, nil
	}
	change, err := placementReviewInput(model.ReviewMediaPlacementChangeInput{Scope: input.Scope, ExpectedRevision: input.ExpectedRevision, ExpectedParentRevision: input.ExpectedParentRevision, Updates: input.Updates})
	if err != nil {
		return placementInvalidInput(err), nil
	}
	request := &placementpb.ApplyChangeRequest{Change: change, ReviewToken: input.ReviewToken, IdempotencyKey: input.IdempotencyKey, AcknowledgedWarningIds: append([]string(nil), input.AcknowledgedWarningIds...)}
	if validationErr := placement.ValidateApplyChangeRequest(request); validationErr != nil {
		return placementInvalidInput(validationErr), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	response, err := r.Clients.Commodore.ApplyMediaPlacementChange(ctx, request)
	if err != nil {
		return placementFailure(err), nil
	}
	if !proto.Equal(response.GetScope(), change.GetScope()) || response.GetIdempotencyKey() != input.IdempotencyKey || response.GetRevision() != change.GetExpectedRevision()+1 || response.GetParentRevision() != change.GetExpectedParentRevision() {
		return placementFailure(status.Error(codes.Internal, "placement change mismatch")), nil
	}
	out, err := placementChangeOutput(response)
	if err != nil {
		return placementFailure(err), nil
	}
	return out, nil
}

func (r *Resolver) DoMediaPlacementChange(ctx context.Context, scope model.MediaPlacementScopeInput, idempotencyKey string) (model.MediaPlacementChangeResult, error) {
	if denied := r.placementAccess(ctx, false); denied != nil {
		return denied, nil
	}
	wire, err := placementScopeInput(&scope)
	if err != nil {
		return placementInvalidInput(err), nil
	}
	if middleware.IsDemoMode(ctx) {
		return placementFailure(status.Error(codes.NotFound, "read-only demo has no saved changes")), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	response, err := r.Clients.Commodore.GetMediaPlacementChange(ctx, &placementpb.GetChangeRequest{Scope: wire, IdempotencyKey: idempotencyKey})
	if err != nil {
		return placementFailure(err), nil
	}
	if !proto.Equal(response.GetScope(), wire) || response.GetIdempotencyKey() != idempotencyKey {
		return placementFailure(status.Error(codes.Internal, "placement change mismatch")), nil
	}
	out, err := placementChangeOutput(response)
	if err != nil {
		return placementFailure(err), nil
	}
	return out, nil
}

func placementDemoAccess(ctx context.Context, manage bool) placementAPIError {
	if err := ctx.Err(); err != nil {
		return placementFailure(status.FromContextError(err).Err())
	}
	if manage {
		return &model.MediaPlacementError{Code: model.MediaPlacementErrorCodeUnsupported,
			Message: "This demo is read-only. Review and preview are simulated; no changes are saved.", Fields: []*model.MediaPlacementFieldError{}}
	}
	return nil
}

func placementInvalidInput(err error) *model.MediaPlacementError {
	return &model.MediaPlacementError{Code: model.MediaPlacementErrorCodeInvalidInput, Message: "Invalid placement change.", Fields: []*model.MediaPlacementFieldError{{Path: "input", Message: err.Error()}}}
}

// Service diagnostics never cross the public GraphQL boundary. The typed code
// drives reload, re-review and idempotency recovery without parsing messages.
func placementFailure(err error) placementAPIError {
	out := &model.MediaPlacementError{Code: model.MediaPlacementErrorCodeUnavailable, Message: "Placement state is unavailable. Recover the same change before retrying a write.", Fields: []*model.MediaPlacementFieldError{}}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return &model.AuthError{Message: "This identity cannot access the requested placement operation."}
	case codes.NotFound:
		return &model.NotFoundError{Message: "Placement scope or change was not found.", ResourceType: "media_placement"}
	case codes.InvalidArgument:
		out.Code, out.Message = model.MediaPlacementErrorCodeInvalidInput, "The placement change is invalid. Check the rules and required acknowledgements."
	case codes.Aborted:
		out.Code, out.Message = model.MediaPlacementErrorCodeRevisionConflict, "Placement changed. Reload and review again."
	case codes.AlreadyExists:
		out.Code, out.Message = model.MediaPlacementErrorCodeIdempotencyConflict, "This idempotency key belongs to another change."
	case codes.FailedPrecondition:
		out.Code, out.Message = model.MediaPlacementErrorCodeStaleReview, "The review changed or expired. Review again before applying."
	case codes.Unimplemented:
		out.Code, out.Message = model.MediaPlacementErrorCodeUnsupported, "This placement operation is not supported."
	case codes.ResourceExhausted:
		out.Code, out.Message = model.MediaPlacementErrorCodeRateLimited, "Placement requests are temporarily limited."
	}
	return out
}
