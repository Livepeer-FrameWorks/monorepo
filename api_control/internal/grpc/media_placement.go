package grpc

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *CommodoreServer) GetMediaPlacementPolicy(ctx context.Context, req *placementpb.GetPolicyRequest) (*placementpb.PolicyState, error) {
	scope, err := mediaPlacementScope(ctx, req.GetScope(), false)
	if err != nil {
		return nil, err
	}
	snapshot, err := placementpolicy.NewStore(s.db).Read(ctx, scope)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	rollout, err := mediaPlacementRollout(snapshot.Status)
	if err != nil {
		return nil, err
	}
	private := authz.Default.Can(ctx, authz.Identity{
		UserID: ctxkeys.GetUserID(ctx), TenantID: ctxkeys.GetTenantID(ctx), Role: ctxkeys.GetRole(ctx), PlatformOperator: ctxkeys.IsPlatformOperator(ctx),
	}, authz.ActionReadPrivateInfrastructure, authz.Resource{OwnerTenantID: scope.TenantID}).Allow
	if ctxkeys.GetAuthType(ctx) == "api_token" && !hasDelegatedPermission(ctxkeys.GetPermissions(ctx), "infrastructure:read") {
		private = false
	}
	var updatedAt *timestamppb.Timestamp
	if !snapshot.UpdatedAt.IsZero() {
		updatedAt = timestamppb.New(snapshot.UpdatedAt)
	}
	rolloutState := &placementpb.Rollout{Status: rollout, ExistingSessionsRetained: true, UpdatedAt: updatedAt}
	s.attachPlacementRollout(ctx, scope, rolloutState)
	return &placementpb.PolicyState{
		Scope: req.GetScope(), Own: snapshot.Own, Inherited: snapshot.Parent, Active: snapshot.Active,
		ActiveParentRevision: snapshot.ActiveParentRevision,
		Rollout:              rolloutState,
		Actions:              &placementpb.Actions{CanRead: true, CanPreview: true, CanManage: requireMediaPlacementAccess(ctx, scope.TenantID, true) == nil, CanInspectPrivateCandidates: private},
		Features:             &placementpb.Features{SchemaVersion: placement.SchemaVersion, GeographicSpillover: true, SupportedPresets: []string{"closest_available", "my_clusters_first", "my_clusters_only", "no_official"}},
	}, nil
}

func (s *CommodoreServer) GetMediaPlacementChange(ctx context.Context, req *placementpb.GetChangeRequest) (*placementpb.Change, error) {
	scope, err := mediaPlacementScope(ctx, req.GetScope(), false)
	if err != nil {
		return nil, err
	}
	receipt, err := placementpolicy.NewStore(s.db).Change(ctx, scope, req.GetIdempotencyKey())
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	response, err := mediaPlacementChangeResponse(req.GetScope(), receipt)
	if err != nil {
		return nil, err
	}
	s.attachPlacementRollout(ctx, scope, response.GetRollout())
	return response, nil
}

func mediaPlacementChangeResponse(scope *placementpb.Scope, receipt commodoredb.CommodoreMediaPlacementChange) (*placementpb.Change, error) {
	rollout, err := mediaPlacementRollout(receipt.RolloutStatus)
	if err != nil {
		return nil, err
	}
	return &placementpb.Change{
		Scope: scope, IdempotencyKey: receipt.IdempotencyKey, Revision: uint64(receipt.Revision), ParentRevision: uint64(receipt.ParentRevision),
		Digest: receipt.PolicyDigest, CreatedAt: timestamppb.New(receipt.CreatedAt),
		Rollout: &placementpb.Rollout{Status: rollout, ExistingSessionsRetained: true, UpdatedAt: timestamppb.New(receipt.UpdatedAt)},
	}, nil
}

func mediaPlacementScope(ctx context.Context, scope *placementpb.Scope, manage bool) (placementpolicy.Scope, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return placementpolicy.Scope{}, err
	}
	if err := requireMediaPlacementAccess(ctx, tenantID, manage); err != nil {
		return placementpolicy.Scope{}, err
	}
	result := placementpolicy.Scope{TenantID: tenantID}
	switch scope.GetKind() {
	case placementpb.ScopeKind_SCOPE_KIND_TENANT:
		if scope.GetStreamId() != "" {
			return result, status.Error(codes.InvalidArgument, "tenant placement scope cannot include a stream")
		}
		result.Kind, result.ID = "tenant", tenantID
	case placementpb.ScopeKind_SCOPE_KIND_STREAM:
		result.Kind, result.ID = "stream", scope.GetStreamId()
	default:
		return result, status.Error(codes.InvalidArgument, "tenant or stream placement scope is required")
	}
	if err := result.Validate(); err != nil {
		return result, status.Error(codes.InvalidArgument, err.Error())
	}
	return result, nil
}

func mediaPlacementRollout(value string) (placementpb.RolloutStatus, error) {
	switch value {
	case "not_configured":
		return placementpb.RolloutStatus_ROLLOUT_STATUS_NOT_CONFIGURED, nil
	case "pending":
		return placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING, nil
	case "effective":
		return placementpb.RolloutStatus_ROLLOUT_STATUS_EFFECTIVE, nil
	case "blocked":
		return placementpb.RolloutStatus_ROLLOUT_STATUS_BLOCKED, nil
	case "superseded":
		return placementpb.RolloutStatus_ROLLOUT_STATUS_SUPERSEDED, nil
	default:
		return 0, status.Error(codes.Internal, "unknown placement rollout state")
	}
}

func mediaPlacementError(err error) error {
	switch {
	case errors.Is(err, placementpolicy.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, "invalid placement input")
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, placementpolicy.ErrNotFound):
		return status.Error(codes.NotFound, "placement scope or change not found")
	case errors.Is(err, placementpolicy.ErrRevisionConflict):
		return status.Error(codes.Aborted, "placement revision changed; reload and review again")
	case errors.Is(err, placementpolicy.ErrIdempotencyConflict):
		return status.Error(codes.AlreadyExists, "idempotency key belongs to another placement change")
	case errors.Is(err, placement.ErrStaleReview):
		return status.Error(codes.FailedPrecondition, "placement review changed or expired")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return status.FromContextError(err).Err()
	default:
		return status.Error(codes.Unavailable, "placement state is unavailable")
	}
}
