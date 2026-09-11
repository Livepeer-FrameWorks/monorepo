package grpc

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"
	"strings"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *CommodoreServer) ReviewMediaPlacementChange(ctx context.Context, req *placementpb.ReviewChangeRequest) (*placementpb.Review, error) {
	scope, err := mediaPlacementScope(ctx, req.GetScope(), true)
	if err != nil {
		return nil, err
	}
	if validationErr := placement.ValidateReviewChangeRequest(req); validationErr != nil {
		return nil, status.Error(codes.InvalidArgument, validationErr.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !s.mediaPlacementReviewReady() {
		return nil, status.Error(codes.Unavailable, "placement review authority is unavailable")
	}
	snapshot, err := placementpolicy.NewStore(s.db).Read(ctx, scope)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	if snapshot.Own.GetRevision() != req.GetExpectedRevision() || snapshot.Parent.GetRevision() != req.GetExpectedParentRevision() {
		return nil, mediaPlacementError(placementpolicy.ErrRevisionConflict)
	}
	next, err := placement.ApplyUpdates(snapshot.Own, req.GetUpdates())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	factsDigest, err := s.mediaPlacementReviewContext(ctx, scope.TenantID)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	binding, review, err := buildMediaPlacementReview(ctxkeys.GetUserID(ctx), snapshot, next, factsDigest)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	review.ReviewToken, err = placement.IssueReview(s.mediaAuthorityKeyID, s.mediaAuthorityPrivateKey, binding, now)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	review.ExpiresAt = timestamppb.New(now.Add(placement.ReviewLifetime))
	return review, nil
}

func (s *CommodoreServer) mediaPlacementReviewReady() bool {
	return s.mediaAuthorityEnabled() && len(s.mediaAuthorityPrivateKey) == ed25519.PrivateKeySize
}

// Owner RPCs run before the database write transaction. The apply callback bounds
// their age and locks the local parent/scope revisions; it never holds SQL locks
// across network calls. Final admission still consumes current signed owner facts.
func (s *CommodoreServer) mediaPlacementReviewContext(ctx context.Context, tenantID string) (string, error) {
	authority, entitlement, err := s.mediaPlacementOwnerContext(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return mediaPlacementContextDigest(authority, entitlement)
}

func (s *CommodoreServer) mediaPlacementOwnerContext(ctx context.Context, tenantID string) (*mediaauthoritypb.TenantAuthority, *quartermasterpb.GetTenantEntitlementResponse, error) {
	if s.authorityTenantSource == nil || s.authorityBillingSource == nil {
		return nil, nil, status.Error(codes.Unavailable, "placement owners are unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tenant, err := s.authorityTenantSource.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if tenant.GetTenant() == nil || tenant.GetTenant().GetId() != tenantID {
		return nil, nil, placementpolicy.ErrNotFound
	}
	var entitlement *quartermasterpb.GetTenantEntitlementResponse
	var billing *purserpb.GetTenantBillingStatusResponse
	var admission *purserpb.GetTenantAdmissionStatusResponse
	if tenant.GetTenant().GetIsActive() {
		group, groupCtx := errgroup.WithContext(ctx)
		group.Go(func() error {
			var callErr error
			entitlement, callErr = s.authorityTenantSource.GetTenantEntitlement(groupCtx, tenantID)
			return callErr
		})
		group.Go(func() error {
			var callErr error
			billing, callErr = s.authorityBillingSource.GetTenantBillingStatus(groupCtx, tenantID)
			return callErr
		})
		group.Go(func() error {
			var callErr error
			admission, callErr = s.authorityBillingSource.GetTenantAdmissionStatus(groupCtx, tenantID)
			return callErr
		})
		if groupErr := group.Wait(); groupErr != nil {
			return nil, nil, groupErr
		}
		if entitlement == nil || billing == nil || admission == nil {
			return nil, nil, fmt.Errorf("placement owner returned empty authority")
		}
	}
	authority, _, _, _, err := buildTenantAuthority(tenant.GetTenant(), entitlement, billing, admission, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	return authority, entitlement, nil
}

func mediaPlacementContextDigest(authority *mediaauthoritypb.TenantAuthority, entitlement *quartermasterpb.GetTenantEntitlementResponse) (string, error) {
	// Review binds the owner's observed consent even when the active authority schema
	// cannot yet carry that field. Sorting must not mutate the RPC owner's snapshot.
	entitlement = proto.CloneOf(entitlement)
	if entitlement != nil {
		slices.Sort(entitlement.AllowedClusterIds)
		slices.SortFunc(entitlement.EffectiveAccess, func(a, b *clusterpeerpb.TenantClusterPeer) int {
			return strings.Compare(a.GetClusterId(), b.GetClusterId())
		})
	}
	digest, err := hashProtoMessages(authority, entitlement)
	return strings.TrimPrefix(digest, "sha256:"), err
}

func buildMediaPlacementReview(actorID string, snapshot placementpolicy.Snapshot, next *placementpb.PolicySet, factsDigest string) (placement.ReviewBinding, *placementpb.Review, error) {
	tenant, stream := next, (*placementpb.PolicySet)(nil)
	if snapshot.Scope.Kind == "stream" {
		tenant, stream = snapshot.Parent, next
	}
	digest, err := placement.PolicySetsDigest(tenant, stream)
	if err != nil {
		return placement.ReviewBinding{}, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	differences, err := placement.DescribePolicyChange(snapshot.Own, next)
	if err != nil {
		return placement.ReviewBinding{}, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if len(differences) == 0 {
		return placement.ReviewBinding{}, nil, status.Error(codes.InvalidArgument, "placement change has no effect")
	}
	warnings := mediaPlacementWarnings(snapshot.Own, next)
	binding := placement.ReviewBinding{TenantID: snapshot.Scope.TenantID, ActorID: actorID, ScopeKind: snapshot.Scope.Kind, ScopeID: snapshot.Scope.ID,
		ExpectedRevision: snapshot.Own.GetRevision(), ExpectedParentRevision: snapshot.Parent.GetRevision(), PolicyDigest: digest, ContextDigest: factsDigest,
	}
	for _, warning := range warnings {
		if warning.GetAcknowledgementRequired() {
			binding.RequiredWarnings = append(binding.RequiredWarnings, warning.GetId())
		}
	}
	if _, err := placement.ReviewDigest(binding); err != nil {
		return placement.ReviewBinding{}, nil, mediaPlacementError(err)
	}
	impact := &placementpb.Impact{Complete: false, ExistingSessionsRetained: true}
	if snapshot.Scope.Kind == "stream" {
		impact.AffectedStreams = 1
	}
	return binding, &placementpb.Review{Digest: digest, Differences: differences, Warnings: warnings, Impact: impact}, nil
}

func mediaPlacementWarnings(before, after *placementpb.PolicySet) []*placementpb.Warning {
	var warnings []*placementpb.Warning
	for _, verb := range []string{"ingest", "serve"} {
		old, next := before.GetServe(), after.GetServe()
		if verb == "ingest" {
			old, next = before.GetIngest(), after.GetIngest()
		}
		if proto.Equal(old, next) {
			continue
		}
		if next.GetPreferences() != nil && len(next.GetPreferences().GetGroups()) == 0 || next.GetConstraints().GetAllow() != nil && len(next.GetConstraints().GetAllow().GetAny()) == 0 || slices.ContainsFunc(next.GetConstraints().GetDeny(), placementSelectorMatchesAll) {
			warnings = append(warnings, &placementpb.Warning{Id: verb + "_deny_all", Message: "These rules permit no new " + verb + " destination.", AcknowledgementRequired: true})
		}
		if old != nil && next == nil || old.GetConstraints().GetAllow() != nil && !proto.Equal(old.GetConstraints().GetAllow(), next.GetConstraints().GetAllow()) || removedPlacementDenies(old.GetConstraints().GetDeny(), next.GetConstraints().GetDeny()) {
			warnings = append(warnings, &placementpb.Warning{Id: verb + "_restriction_removed", Message: "Removing or replacing a restriction may expand the capacity used by new " + verb + " connections.", AcknowledgementRequired: true})
		}
		for _, group := range next.GetPreferences().GetGroups() {
			spill := group.GetSpillover()
			if spill == placementpb.Spillover_SPILLOVER_UNSPECIFIED || spill == placementpb.Spillover_SPILLOVER_NEVER {
				continue
			}
			previous := slices.IndexFunc(old.GetPreferences().GetGroups(), func(prior *placementpb.Group) bool {
				return proto.Equal(prior, group)
			})
			if previous < 0 {
				warnings = append(warnings, &placementpb.Warning{Id: verb + "_fallback_changed", Message: "New " + verb + " connections may use a later fallback group under the selected conditions. Its charges can differ; this does not subscribe to new capacity.", AcknowledgementRequired: true})
				break
			}
		}
	}
	return warnings
}

func placementSelectorMatchesAll(selector *placementpb.Selector) bool {
	return len(selector.GetClusterIds()) == 0 && len(selector.GetOwnerIds()) == 0 && len(selector.GetRegions()) == 0 && len(selector.GetClasses()) == 0 && len(selector.GetCharging()) == 0
}

func removedPlacementDenies(before, after []*placementpb.Selector) bool {
	for _, old := range before {
		if !slices.ContainsFunc(after, func(next *placementpb.Selector) bool { return proto.Equal(old, next) }) {
			return true
		}
	}
	return false
}
