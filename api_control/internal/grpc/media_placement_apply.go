package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *CommodoreServer) ApplyMediaPlacementChange(ctx context.Context, req *placementpb.ApplyChangeRequest) (*placementpb.Change, error) {
	change := req.GetChange()
	scope, err := mediaPlacementScope(ctx, change.GetScope(), true)
	if err != nil {
		return nil, err
	}
	if validationErr := placement.ValidateApplyChangeRequest(req); validationErr != nil {
		return nil, status.Error(codes.InvalidArgument, validationErr.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	store := placementpolicy.NewStore(s.db)
	input := placementpolicy.ApplyInput{Scope: scope, ActorID: ctxkeys.GetUserID(ctx), IdempotencyKey: req.GetIdempotencyKey(),
		ExpectedRevision: change.GetExpectedRevision(), ExpectedParentRevision: change.GetExpectedParentRevision(), AcknowledgedWarnings: req.GetAcknowledgedWarningIds(),
	}
	if recovered, found, recoverErr := recoverCommittedMediaPlacement(ctx, store, input, change); found {
		return recovered, recoverErr
	}
	if !s.mediaPlacementReviewReady() {
		return nil, status.Error(codes.Unavailable, "placement review authority is unavailable")
	}
	public, ok := s.mediaAuthorityPrivateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, status.Error(codes.Unavailable, "placement review key is unavailable")
	}
	trust := map[string]ed25519.PublicKey{s.mediaAuthorityKeyID: public}
	original, err := placement.SignedReviewBinding(req.GetReviewToken(), trust)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	if original.ActorID != input.ActorID || original.TenantID != scope.TenantID || original.ScopeKind != scope.Kind || original.ScopeID != scope.ID ||
		original.ExpectedRevision != input.ExpectedRevision || original.ExpectedParentRevision != input.ExpectedParentRevision {
		return nil, mediaPlacementError(placement.ErrStaleReview)
	}
	input.ReviewDigest, err = placement.ReviewDigest(original)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	snapshot, err := store.Read(ctx, scope)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	if snapshot.Own.GetRevision() != input.ExpectedRevision || snapshot.Parent.GetRevision() != input.ExpectedParentRevision {
		if recovered, found, recoverErr := recoverCommittedMediaPlacement(ctx, store, input, change); found {
			return recovered, recoverErr
		}
		return nil, mediaPlacementError(placementpolicy.ErrRevisionConflict)
	}
	input.Policy, err = placement.ApplyUpdates(snapshot.Own, change.GetUpdates())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	factsDigest, err := s.mediaPlacementReviewContext(ctx, scope.TenantID)
	if err != nil {
		if recovered, found, recoverErr := recoverCommittedMediaPlacement(ctx, store, input, change); found {
			return recovered, recoverErr
		}
		return nil, mediaPlacementError(err)
	}
	observedAt := time.Now()
	receipt, err := store.Apply(ctx, input, func(locked placementpolicy.Snapshot) error {
		if time.Since(observedAt) > 5*time.Second {
			return placement.ErrStaleReview
		}
		binding, _, reviewErr := buildMediaPlacementReview(input.ActorID, locked, input.Policy, factsDigest)
		if reviewErr != nil {
			return reviewErr
		}
		if verifyErr := placement.VerifyReview(req.GetReviewToken(), trust, binding, time.Now()); verifyErr != nil {
			return verifyErr
		}
		if ackErr := placement.ValidateReviewAcknowledgements(binding, req.GetAcknowledgedWarningIds()); ackErr != nil {
			return status.Error(codes.InvalidArgument, "acknowledge the exact reviewed warnings before applying")
		}
		return nil
	})
	if err != nil {
		if _, recognized := status.FromError(err); recognized {
			return nil, err
		}
		return nil, mediaPlacementError(err)
	}
	return mediaPlacementChangeResponse(change.GetScope(), receipt)
}

// A committed retry authenticates against the immutable command, not today's
// signing key or owner-service availability. Caller authorization is mandatory;
// no new command passes this path because its callback always refuses a write.
func recoverCommittedMediaPlacement(ctx context.Context, store *placementpolicy.Store, input placementpolicy.ApplyInput, change *placementpb.ReviewChangeRequest) (*placementpb.Change, bool, error) {
	previous, err := store.Change(ctx, input.Scope, input.IdempotencyKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, mediaPlacementError(err)
	}
	if previous.ActorID != input.ActorID || uint64(previous.Revision) != input.ExpectedRevision+1 || uint64(previous.ParentRevision) != input.ExpectedParentRevision {
		return nil, true, mediaPlacementError(placementpolicy.ErrIdempotencyConflict)
	}
	base, err := placementpolicy.PreviousPolicy(previous)
	if err != nil {
		return nil, true, mediaPlacementError(err)
	}
	input.Policy, err = placement.ApplyUpdates(base, change.GetUpdates())
	if err != nil {
		return nil, true, status.Error(codes.InvalidArgument, err.Error())
	}
	input.ReviewDigest = previous.ReviewDigest
	receipt, err := store.Apply(ctx, input, func(placementpolicy.Snapshot) error { return placement.ErrStaleReview })
	if err != nil {
		return nil, true, mediaPlacementError(err)
	}
	response, err := mediaPlacementChangeResponse(change.GetScope(), receipt)
	return response, true, err
}
