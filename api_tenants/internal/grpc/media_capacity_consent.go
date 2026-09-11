package grpc

import (
	"context"
	"crypto/ed25519"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func consentScope(ctx context.Context, clusterID string, manage bool) (quartermasterdb.MediaConsentScope, error) {
	scope := quartermasterdb.MediaConsentScope{TenantID: ctxkeys.GetTenantID(ctx), ClusterID: clusterID}
	if (ctxkeys.GetAuthType(ctx) != "jwt" && ctxkeys.GetAuthType(ctx) != "api_token") || ctxkeys.GetUserID(ctx) == "" || scope.TenantID == "" {
		return scope, status.Error(codes.Unauthenticated, "tenant user identity is required")
	}
	permission, action := "placement:read", authz.ActionReadMediaPlacement
	if manage {
		permission, action = "placement:write", authz.ActionManageMediaPlacement
	}
	if ctxkeys.GetAuthType(ctx) == "api_token" && !hasAPITokenPermission(ctxkeys.GetPermissions(ctx), permission) {
		return scope, status.Error(codes.PermissionDenied, "API token lacks placement permission")
	}
	decision := authz.Default.Can(ctx, authz.Identity{UserID: ctxkeys.GetUserID(ctx), TenantID: scope.TenantID, Role: ctxkeys.GetRole(ctx)}, action, authz.Resource{OwnerTenantID: scope.TenantID})
	if !decision.Allow {
		return scope, status.Error(codes.PermissionDenied, "tenant owner/admin permission is required to manage consent")
	}
	if err := scope.Validate(); err != nil {
		return scope, consentError(err)
	}
	return scope, nil
}

func (s *QuartermasterServer) consentReviewReady() bool {
	return placementInventoryID(s.consentReviewKeyID) && len(s.consentReviewPrivateKey) == ed25519.PrivateKeySize
}

func (s *QuartermasterServer) GetClusterMediaConsent(ctx context.Context, req *quartermasterpb.GetClusterMediaConsentRequest) (*quartermasterpb.ClusterMediaConsentState, error) {
	scope, err := consentScope(ctx, req.GetClusterId(), false)
	if err != nil {
		return nil, err
	}
	if len(req.ProtoReflect().GetUnknown()) != 0 {
		return nil, consentError(quartermasterdb.ErrConsentInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	consent, err := quartermasterdb.NewMediaConsentStore(s.db).Read(ctx, scope)
	if err != nil {
		return nil, consentError(err)
	}
	_, manageErr := consentScope(ctx, req.GetClusterId(), true)
	return &quartermasterpb.ClusterMediaConsentState{
		ClusterId: scope.ClusterID, Consent: &placementpb.CapacityConsent{Revision: consent.Revision, AllowIngest: consent.AllowIngest, AllowServe: consent.AllowServe, AllowExternalSource: consent.AllowExternalSource},
		CanManage: manageErr == nil && s.consentReviewReady(), Rollout: consentRollout(consent.Revision),
	}, nil
}

func (s *QuartermasterServer) GetClusterMediaConsentChange(ctx context.Context, req *quartermasterpb.GetClusterMediaConsentChangeRequest) (*quartermasterpb.ClusterMediaConsentChange, error) {
	scope, err := consentScope(ctx, req.GetClusterId(), false)
	if err != nil {
		return nil, err
	}
	if len(req.ProtoReflect().GetUnknown()) != 0 {
		return nil, consentError(quartermasterdb.ErrConsentInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	change, err := quartermasterdb.NewMediaConsentStore(s.db).Change(ctx, scope, req.GetIdempotencyKey())
	if err != nil {
		return nil, consentError(err)
	}
	return consentChangeResponse(change), nil
}

func (s *QuartermasterServer) ReviewClusterMediaConsentChange(ctx context.Context, req *quartermasterpb.ReviewClusterMediaConsentRequest) (*placementpb.Review, error) {
	scope, err := consentScope(ctx, req.GetClusterId(), true)
	if err != nil {
		return nil, err
	}
	if validationErr := validateConsentChange(req); validationErr != nil {
		return nil, validationErr
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	current, err := quartermasterdb.NewMediaConsentStore(s.db).Read(ctx, scope)
	if err != nil {
		return nil, consentError(err)
	}
	if current.Revision != req.GetExpectedRevision() {
		return nil, consentError(quartermasterdb.ErrConsentRevisionConflict)
	}
	if !s.consentReviewReady() {
		return nil, status.Error(codes.Unavailable, "capacity consent review signer is unavailable")
	}
	binding, review, err := buildConsentReview(scope, ctxkeys.GetUserID(ctx), current, consentNext(current.ClusterRecordID, req))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	review.ReviewToken, err = placement.IssueReview(s.consentReviewKeyID, s.consentReviewPrivateKey, binding, now)
	if err != nil {
		return nil, consentError(err)
	}
	review.ExpiresAt = timestamppb.New(now.Add(placement.ReviewLifetime))
	return review, nil
}

func (s *QuartermasterServer) ApplyClusterMediaConsentChange(ctx context.Context, req *quartermasterpb.ApplyClusterMediaConsentRequest) (*quartermasterpb.ClusterMediaConsentChange, error) {
	change := req.GetChange()
	scope, err := consentScope(ctx, change.GetClusterId(), true)
	if err != nil {
		return nil, err
	}
	if validationErr := validateConsentChange(change); validationErr != nil {
		return nil, validationErr
	}
	if len(req.ProtoReflect().GetUnknown()) != 0 || len(req.GetReviewToken()) > 16384 || !consentAPIIdentifier(req.GetIdempotencyKey(), 128) || len(req.GetAcknowledgedWarningIds()) > 128 {
		return nil, consentError(quartermasterdb.ErrConsentInvalidInput)
	}
	for _, warning := range req.GetAcknowledgedWarningIds() {
		if !consentAPIIdentifier(warning, 128) {
			return nil, consentError(quartermasterdb.ErrConsentInvalidInput)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	store := quartermasterdb.NewMediaConsentStore(s.db)
	input := quartermasterdb.MediaConsentApply{Scope: scope, ExpectedRevision: change.GetExpectedRevision(), ActorID: ctxkeys.GetUserID(ctx), IdempotencyKey: req.GetIdempotencyKey(), AcknowledgedWarnings: req.GetAcknowledgedWarningIds()}
	if recovered, found, recoverErr := recoverConsentChange(ctx, store, input, change); found {
		return recovered, recoverErr
	}
	current, err := store.Read(ctx, scope)
	if err != nil {
		return nil, consentError(err)
	}
	if current.Revision != input.ExpectedRevision {
		if recovered, found, recoverErr := recoverConsentChange(ctx, store, input, change); found {
			return recovered, recoverErr
		}
		return nil, consentError(quartermasterdb.ErrConsentRevisionConflict)
	}
	if !s.consentReviewReady() {
		return nil, status.Error(codes.Unavailable, "capacity consent review signer is unavailable")
	}
	public, ok := s.consentReviewPrivateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, status.Error(codes.Unavailable, "capacity consent review signer is unavailable")
	}
	trust := map[string]ed25519.PublicKey{s.consentReviewKeyID: public}
	input.Consent = consentNext(current.ClusterRecordID, change)
	binding, _, err := buildConsentReview(scope, input.ActorID, current, input.Consent)
	if err != nil {
		return nil, err
	}
	input.ReviewDigest, err = placement.ReviewDigest(binding)
	if err != nil {
		return nil, consentError(err)
	}
	receipt, err := store.Apply(ctx, input, func(locked quartermasterdb.MediaConsent) error {
		lockedBinding, _, reviewErr := buildConsentReview(scope, input.ActorID, locked, input.Consent)
		if reviewErr != nil {
			return reviewErr
		}
		lockedDigest, digestErr := placement.ReviewDigest(lockedBinding)
		if digestErr != nil || lockedDigest != input.ReviewDigest {
			return placement.ErrStaleReview
		}
		if verifyErr := placement.VerifyReview(req.GetReviewToken(), trust, lockedBinding, time.Now()); verifyErr != nil {
			return verifyErr
		}
		if ackErr := placement.ValidateReviewAcknowledgements(lockedBinding, req.GetAcknowledgedWarningIds()); ackErr != nil {
			return status.Error(codes.InvalidArgument, "acknowledge the exact reviewed warnings before applying")
		}
		return nil
	})
	if err != nil {
		return nil, consentError(err)
	}
	return consentChangeResponse(receipt), nil
}

func recoverConsentChange(ctx context.Context, store *quartermasterdb.MediaConsentStore, input quartermasterdb.MediaConsentApply, change *quartermasterpb.ReviewClusterMediaConsentRequest) (*quartermasterpb.ClusterMediaConsentChange, bool, error) {
	previous, err := store.Change(ctx, input.Scope, input.IdempotencyKey)
	if errors.Is(err, quartermasterdb.ErrConsentNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, consentError(err)
	}
	if previous.ActorID != input.ActorID || previous.Revision <= 0 || uint64(previous.Revision) != input.ExpectedRevision+1 {
		return nil, true, consentError(quartermasterdb.ErrConsentIdempotencyConflict)
	}
	input.Consent, input.ReviewDigest = consentNext(previous.ClusterRecordID, change), previous.ReviewDigest
	// Only the store's immutable retry path may succeed without today's signing key.
	receipt, err := store.Apply(ctx, input, func(quartermasterdb.MediaConsent) error { return placement.ErrStaleReview })
	if err != nil {
		return nil, true, consentError(err)
	}
	return consentChangeResponse(receipt), true, nil
}

func buildConsentReview(scope quartermasterdb.MediaConsentScope, actor string, before, after quartermasterdb.MediaConsent) (placement.ReviewBinding, *placementpb.Review, error) {
	if before.Revision >= math.MaxInt64 || after.Revision != before.Revision+1 || before.ClusterRecordID != after.ClusterRecordID {
		return placement.ReviewBinding{}, nil, consentError(quartermasterdb.ErrConsentRevisionConflict)
	}
	contextDigest, err := quartermasterdb.MediaConsentDigest(scope, before)
	if err != nil {
		return placement.ReviewBinding{}, nil, consentError(err)
	}
	digest, err := quartermasterdb.MediaConsentDigest(scope, after)
	if err != nil {
		return placement.ReviewBinding{}, nil, consentError(err)
	}
	binding := placement.ReviewBinding{TenantID: scope.TenantID, ActorID: actor, ScopeKind: "cluster_consent", ScopeID: scope.ClusterID, ExpectedRevision: before.Revision, PolicyDigest: digest, ContextDigest: contextDigest}
	review := &placementpb.Review{Digest: digest, Impact: &placementpb.Impact{Complete: false, ExistingSessionsRetained: true}}
	for _, field := range []struct {
		path, label   string
		before, after bool
	}{
		{"allowIngest", "Accept new ingest connections", before.AllowIngest, after.AllowIngest},
		{"allowServe", "Accept new viewer connections", before.AllowServe, after.AllowServe},
		{"allowExternalSource", "Source content from other clusters", before.AllowExternalSource, after.AllowExternalSource},
	} {
		if field.before == field.after {
			continue
		}
		review.Differences = append(review.Differences, &placementpb.Difference{Path: field.path, Label: field.label, Before: strconv.FormatBool(field.before), After: strconv.FormatBool(field.after)})
		id := "consent_" + field.path + "_restricted"
		message := "Disabling this permission can prevent new connections or content delivery for tenants using this cluster."
		if field.after {
			id, message = "consent_"+field.path+"_expanded", "Enabling this permission allows existing entitled tenants to use this capacity; it does not grant new access."
		}
		binding.RequiredWarnings = append(binding.RequiredWarnings, id)
		review.Warnings = append(review.Warnings, &placementpb.Warning{Id: id, Message: message, AcknowledgementRequired: true})
	}
	if len(review.Differences) == 0 {
		return placement.ReviewBinding{}, nil, status.Error(codes.InvalidArgument, "consent change has no effect")
	}
	return binding, review, nil
}

func validateConsentChange(req *quartermasterpb.ReviewClusterMediaConsentRequest) error {
	if req == nil || len(req.ProtoReflect().GetUnknown()) != 0 || req.GetExpectedRevision() >= math.MaxInt64 {
		return consentError(quartermasterdb.ErrConsentInvalidInput)
	}
	return nil
}

func consentNext(recordID string, req *quartermasterpb.ReviewClusterMediaConsentRequest) quartermasterdb.MediaConsent {
	return quartermasterdb.MediaConsent{ClusterRecordID: recordID, Revision: req.GetExpectedRevision() + 1, AllowIngest: req.GetAllowIngest(), AllowServe: req.GetAllowServe(), AllowExternalSource: req.GetAllowExternalSource()}
}

func consentRollout(revision uint64) *placementpb.Rollout {
	state := placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING
	if revision == 0 {
		state = placementpb.RolloutStatus_ROLLOUT_STATUS_NOT_CONFIGURED
	}
	return &placementpb.Rollout{Status: state, ExistingSessionsRetained: true}
}

func consentChangeResponse(change quartermasterdb.QuartermasterMediaCapacityConsentChange) *quartermasterpb.ClusterMediaConsentChange {
	return &quartermasterpb.ClusterMediaConsentChange{ClusterId: change.ClusterID, IdempotencyKey: change.IdempotencyKey, Revision: uint64(change.Revision), Digest: change.ConsentDigest, CreatedAt: timestamppb.New(change.CreatedAt), Rollout: consentRollout(uint64(change.Revision))}
}

func consentAPIIdentifier(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func consentError(err error) error {
	switch {
	case errors.Is(err, quartermasterdb.ErrConsentInvalidInput):
		return status.Error(codes.InvalidArgument, "invalid capacity consent input")
	case errors.Is(err, quartermasterdb.ErrConsentNotFound):
		return status.Error(codes.NotFound, "owned capacity or change not found")
	case errors.Is(err, quartermasterdb.ErrConsentRevisionConflict):
		return status.Error(codes.Aborted, "capacity consent changed; reload and review again")
	case errors.Is(err, quartermasterdb.ErrConsentIdempotencyConflict):
		return status.Error(codes.AlreadyExists, "idempotency key was used for a different consent command")
	case errors.Is(err, placement.ErrStaleReview):
		return status.Error(codes.FailedPrecondition, "consent review is stale or does not match this command")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "consent request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "consent request deadline exceeded")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Unavailable, "capacity consent operation is unavailable")
}
