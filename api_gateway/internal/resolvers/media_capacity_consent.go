package resolvers

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (r *Resolver) capacityConsentAccess(ctx context.Context, manage bool) placementAPIError {
	if middleware.IsDemoMode(ctx) {
		return placementDemoAccess(ctx, manage)
	}
	if (ctxkeys.GetAuthType(ctx) != "jwt" && ctxkeys.GetAuthType(ctx) != "api_token") || ctxkeys.GetTenantID(ctx) == "" || ctxkeys.GetUserID(ctx) == "" {
		return &model.AuthError{Message: "Tenant user authentication is required."}
	}
	permission, action := "placement:read", authz.ActionReadMediaPlacement
	if manage {
		permission, action = "placement:write", authz.ActionManageMediaPlacement
	}
	if ctxkeys.GetAuthType(ctx) == "api_token" && !slices.ContainsFunc(ctxkeys.GetPermissions(ctx), func(value string) bool { return strings.TrimSpace(value) == permission }) {
		return &model.AuthError{Message: "The API token does not include the required placement scope."}
	}
	// Capacity consent requires tenant ownership, without a platform-operator override.
	decision := authz.Default.Can(ctx, authz.Identity{UserID: ctxkeys.GetUserID(ctx), TenantID: ctxkeys.GetTenantID(ctx), Role: ctxkeys.GetRole(ctx)}, action, authz.Resource{OwnerTenantID: ctxkeys.GetTenantID(ctx)})
	if !decision.Allow {
		return &model.AuthError{Message: "This identity cannot manage the requested capacity consent."}
	}
	if r == nil || r.Clients == nil || r.Clients.Quartermaster == nil {
		return placementFailure(status.Error(codes.Unavailable, "capacity consent owner unavailable"))
	}
	return nil
}

func (r *Resolver) DoClusterMediaConsent(ctx context.Context, clusterID string) (model.MediaCapacityConsentResult, error) {
	if denied := r.capacityConsentAccess(ctx, false); denied != nil {
		return denied, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var response *quartermasterpb.ClusterMediaConsentState
	var err error
	if middleware.IsDemoMode(ctx) {
		response, err = demo.GenerateMediaCapacityConsent(clusterID)
	} else {
		response, err = r.Clients.Quartermaster.GetClusterMediaConsent(ctx, &quartermasterpb.GetClusterMediaConsentRequest{ClusterId: clusterID})
	}
	if err != nil {
		return placementFailure(err), nil
	}
	if response == nil || response.GetClusterId() != clusterID || response.GetConsent() == nil || response.GetConsent().GetRevision() > math.MaxInt64 {
		return placementFailure(status.Error(codes.Internal, "invalid capacity consent response")), nil
	}
	rollout, err := placementRolloutOutput(response.GetRollout())
	if err != nil {
		return placementFailure(err), nil
	}
	consent := response.GetConsent()
	return &model.MediaCapacityConsent{ClusterID: clusterID, Revision: strconv.FormatUint(consent.GetRevision(), 10), AllowIngest: consent.GetAllowIngest(), AllowServe: consent.GetAllowServe(), AllowExternalSource: consent.GetAllowExternalSource(), CanManage: response.GetCanManage(), Rollout: rollout}, nil
}

func (r *Resolver) DoReviewClusterMediaConsentChange(ctx context.Context, input model.ReviewMediaCapacityConsentInput) (model.MediaPlacementReviewResult, error) {
	if denied := r.capacityConsentAccess(ctx, !middleware.IsDemoMode(ctx)); denied != nil {
		return denied, nil
	}
	request, err := capacityConsentInput(input)
	if err != nil {
		return placementInvalidInput(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var response *placementpb.Review
	if middleware.IsDemoMode(ctx) {
		response, err = demo.GenerateMediaCapacityReview(request, time.Now().UTC())
	} else {
		response, err = r.Clients.Quartermaster.ReviewClusterMediaConsentChange(ctx, request)
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

func (r *Resolver) DoApplyClusterMediaConsentChange(ctx context.Context, input model.ApplyMediaCapacityConsentInput) (model.MediaCapacityConsentChangeResult, error) {
	if denied := r.capacityConsentAccess(ctx, true); denied != nil {
		return denied, nil
	}
	change, err := capacityConsentInput(model.ReviewMediaCapacityConsentInput{ClusterID: input.ClusterID, ExpectedRevision: input.ExpectedRevision, AllowIngest: input.AllowIngest, AllowServe: input.AllowServe, AllowExternalSource: input.AllowExternalSource})
	if err != nil {
		return placementInvalidInput(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	response, err := r.Clients.Quartermaster.ApplyClusterMediaConsentChange(ctx, &quartermasterpb.ApplyClusterMediaConsentRequest{Change: change, ReviewToken: input.ReviewToken, IdempotencyKey: input.IdempotencyKey, AcknowledgedWarningIds: append([]string(nil), input.AcknowledgedWarningIds...)})
	if err != nil {
		return placementFailure(err), nil
	}
	if response.GetRevision() != change.GetExpectedRevision()+1 {
		return placementFailure(status.Error(codes.Internal, "capacity consent revision mismatch")), nil
	}
	out, err := capacityConsentChangeOutput(response, input.ClusterID, input.IdempotencyKey)
	if err != nil {
		return placementFailure(err), nil
	}
	return out, nil
}

func (r *Resolver) DoClusterMediaConsentChange(ctx context.Context, clusterID, key string) (model.MediaCapacityConsentChangeResult, error) {
	if denied := r.capacityConsentAccess(ctx, false); denied != nil {
		return denied, nil
	}
	if middleware.IsDemoMode(ctx) {
		return placementFailure(status.Error(codes.NotFound, "read-only demo has no saved changes")), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	response, err := r.Clients.Quartermaster.GetClusterMediaConsentChange(ctx, &quartermasterpb.GetClusterMediaConsentChangeRequest{ClusterId: clusterID, IdempotencyKey: key})
	if err != nil {
		return placementFailure(err), nil
	}
	out, err := capacityConsentChangeOutput(response, clusterID, key)
	if err != nil {
		return placementFailure(err), nil
	}
	return out, nil
}

func capacityConsentInput(in model.ReviewMediaCapacityConsentInput) (*quartermasterpb.ReviewClusterMediaConsentRequest, error) {
	revision, err := placementRevisionInput(in.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if revision == math.MaxInt64 || in.ClusterID == "" || len(in.ClusterID) > 255 || strings.TrimSpace(in.ClusterID) != in.ClusterID {
		return nil, fmt.Errorf("invalid capacity consent scope or exhausted revision")
	}
	return &quartermasterpb.ReviewClusterMediaConsentRequest{ClusterId: in.ClusterID, ExpectedRevision: revision, AllowIngest: in.AllowIngest, AllowServe: in.AllowServe, AllowExternalSource: in.AllowExternalSource}, nil
}

func capacityConsentChangeOutput(in *quartermasterpb.ClusterMediaConsentChange, clusterID, key string) (*model.MediaCapacityConsentChange, error) {
	if in == nil || in.GetClusterId() != clusterID || in.GetIdempotencyKey() != key || in.GetRevision() == 0 || in.GetRevision() > math.MaxInt64 || in.GetCreatedAt() == nil || !in.GetCreatedAt().IsValid() {
		return nil, fmt.Errorf("invalid capacity consent change response")
	}
	rollout, err := placementRolloutOutput(in.GetRollout())
	if err != nil {
		return nil, err
	}
	return &model.MediaCapacityConsentChange{ClusterID: clusterID, IdempotencyKey: key, Revision: strconv.FormatUint(in.GetRevision(), 10), Digest: in.GetDigest(), CreatedAt: in.GetCreatedAt().AsTime(), Rollout: rollout}, nil
}

func (r *Resolver) DoMediaPlacementLegacyPins(ctx context.Context, streamID string) (model.MediaPlacementLegacyPinsResult, error) {
	if denied := r.placementAccess(ctx, false); denied != nil {
		return denied, nil
	}
	if middleware.IsDemoMode(ctx) {
		if err := demo.ValidateMediaPlacementScope(&placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: streamID}); err != nil {
			return placementFailure(err), nil
		}
		return &model.MediaPlacementLegacyPins{StreamID: streamID, ClusterIds: []string{}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	stream, err := r.Clients.Commodore.GetStream(ctx, streamID)
	if err != nil {
		return placementFailure(err), nil
	}
	if stream == nil || stream.GetStreamId() != streamID {
		return placementFailure(status.Error(codes.Internal, "legacy placement scope mismatch")), nil
	}
	pins := append([]string{}, stream.GetPullSource().GetAllowedClusterIds()...)
	return &model.MediaPlacementLegacyPins{StreamID: streamID, ClusterIds: pins, CurrentlyEnforced: len(pins) > 0}, nil
}
