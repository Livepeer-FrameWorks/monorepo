package grpc

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *CommodoreServer) PreviewMediaPlacement(ctx context.Context, req *placementpb.PreviewRequest) (*placementpb.Preview, error) {
	scope, err := mediaPlacementScope(ctx, req.GetScope(), false)
	if err != nil {
		return nil, err
	}
	if validationErr := placement.ValidatePreviewRequest(req); validationErr != nil {
		return nil, status.Error(codes.InvalidArgument, validationErr.Error())
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "preview policy owner is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	preview, err := s.prepareMediaPlacementPreview(ctx, scope, req)
	if err != nil {
		return nil, err
	}
	authority, entitlement, err := s.mediaPlacementOwnerContext(ctx, scope.TenantID)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	if authority.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || authority.GetBillingDecision() != mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return nil, status.Error(codes.FailedPrecondition, "tenant is not currently admitted for media")
	}
	inventory, err := mediaPreviewEntitlement(scope.TenantID, entitlement, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	var capacity placement.CapacitySnapshot
	var quote *placementpb.CommercialQuoteResponse
	var source *placementpb.PushSourcePreviewObservation
	var quoteRequired bool
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		var callErr error
		capacity, callErr = s.collectPreviewCapacity(groupCtx, preview, inventory, req.GetVerb())
		return callErr
	})
	group.Go(func() error {
		quoteRequired = placement.RequiresCommercialFacts(preview.policy) && len(inventory.clusters) != 0
		if !quoteRequired {
			return nil
		}
		quote = s.collectPreviewQuote(groupCtx, preview, inventory, req.GetVerb())
		return nil
	})
	group.Go(func() error {
		source = s.collectPreviewPushSource(groupCtx, preview, inventory)
		return nil
	})
	if waitErr := group.Wait(); waitErr != nil {
		return nil, waitErr
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if quoteRequired && quote == nil {
		capacity.Complete = false
	}
	if quote != nil {
		if quote.GetExpiresAt().AsTime().Before(capacity.ExpiresAt) {
			capacity.ExpiresAt = quote.GetExpiresAt().AsTime()
		}
		facts := map[string]*placementpb.CommercialFacts{}
		for _, cluster := range quote.GetClusters() {
			facts[cluster.GetClusterId()] = cluster.GetFacts()
		}
		for index, candidate := range capacity.Candidates {
			wire, encodeErr := placement.CandidateToProto(candidate, preview.verb)
			if encodeErr != nil {
				return nil, status.Error(codes.Unavailable, "preview observation cannot be compared")
			}
			wire.CommercialFacts = facts[candidate.ClusterID]
			decoded, decodeErr := placement.CandidateFromProto(wire, preview.verb)
			if decodeErr != nil {
				return nil, status.Error(codes.Unavailable, "preview pricing is inconsistent")
			}
			capacity.Candidates[index] = decoded
		}
	}
	if !preview.activeUntil.IsZero() && preview.activeUntil.Before(capacity.ExpiresAt) {
		capacity.ExpiresAt = preview.activeUntil
	}
	if source != nil {
		if source.GetExpiresAt().AsTime().Before(capacity.ExpiresAt) {
			capacity.ExpiresAt = source.GetExpiresAt().AsTime()
		}
		if source.GetObservedAt().AsTime().Before(capacity.ObservedAt) {
			capacity.ObservedAt = source.GetObservedAt().AsTime()
		}
	}
	for index := range capacity.Candidates {
		if capacity.Candidates[index].ExpiresAt.After(capacity.ExpiresAt) {
			capacity.Candidates[index].ExpiresAt = capacity.ExpiresAt
		}
	}
	// Recheck persisted intent and publisher ownership after potentially slow cell reads.
	checked := proto.CloneOf(req)
	checked.ExpectedRevision = proto.Uint64(preview.snapshot.Own.GetRevision())
	checked.ExpectedParentRevision = proto.Uint64(preview.snapshot.Parent.GetRevision())
	current, err := s.prepareMediaPlacementPreview(ctx, scope, checked)
	if err != nil {
		return nil, err
	}
	if current.digest != preview.digest || current.internalName != preview.internalName || current.activeCluster != preview.activeCluster || current.sourceCluster != preview.sourceCluster || preview.content != nil && (current.content == nil || current.content.Own.GetRevision() != preview.content.Own.GetRevision()) {
		return nil, mediaPlacementError(placementpolicy.ErrRevisionConflict)
	}
	now := time.Now().UTC()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if !now.Before(capacity.ExpiresAt) {
		return nil, status.Error(codes.Unavailable, "preview observations expired")
	}
	var location *placement.Coordinates
	if geo := req.GetCoordinates(); geo != nil {
		location = &placement.Coordinates{Latitude: geo.GetLatitude(), Longitude: geo.GetLongitude()}
	}
	decision, err := evaluateMediaPreview(placement.Request{TenantID: scope.TenantID, Verb: preview.verb, Now: now, Location: location, Policy: preview.policy, Candidates: capacity.Candidates, Complete: capacity.Complete, ActiveIngestClusterID: preview.activeCluster}, preview.verb == placement.Serve && preview.internalName != "", source, inventory)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "preview evidence cannot be evaluated")
	}
	out := projectMediaPlacementPreview(ctx, req, preview, inventory, capacity, decision, now)
	if len(out.GetCandidates()) > 4096 || proto.Size(out) > 8<<20 {
		return nil, status.Error(codes.ResourceExhausted, "preview explanations exceed bounds")
	}
	return out, nil
}

func (s *CommodoreServer) collectPreviewQuote(ctx context.Context, preview *mediaPlacementPreviewContext, inventory *mediaPreviewInventory, verb placementpb.Verb) *placementpb.CommercialQuoteResponse {
	if s.authorityCommercialSource == nil || preview.policy == nil {
		return nil
	}
	request := &placementpb.CommercialQuoteRequest{TenantId: preview.snapshot.Scope.TenantID, ObjectId: preview.snapshot.Scope.Kind + ":" + preview.snapshot.Scope.ID, Verb: verb, PolicyRevision: preview.snapshot.Own.GetRevision(), ParentRevision: preview.snapshot.Parent.GetRevision(), PolicyDigest: preview.digest, ClusterIds: inventory.clusters}
	seen := map[string]bool{}
	for _, group := range preview.policy.Groups {
		if group.Order != placement.PriceFirst {
			continue
		}
		key := group.PriceCurrency + "\x00" + group.PriceUnit
		if !seen[key] {
			request.Bases = append(request.Bases, &placementpb.ComparisonBasis{Currency: group.PriceCurrency, Unit: group.PriceUnit})
			seen[key] = true
		}
	}
	canonical, _, err := placement.CanonicalCommercialQuoteRequest(request)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	quote, err := s.authorityCommercialSource.GetMediaPlacementQuote(ctx, canonical)
	if err != nil || ctx.Err() != nil || placement.ValidateCommercialQuoteResponse(canonical, quote, time.Now().UTC()) != nil || quote.GetEntitlementDigest() != inventory.entitlementDigest {
		return nil
	}
	return quote
}

func projectMediaPlacementPreview(ctx context.Context, req *placementpb.PreviewRequest, preview *mediaPlacementPreviewContext, inventory *mediaPreviewInventory, capacity placement.CapacitySnapshot, decision mediaPreviewEvaluation, now time.Time) *placementpb.Preview {
	out := &placementpb.Preview{Scope: proto.CloneOf(req.GetScope()), Verb: req.GetVerb(), Revision: preview.snapshot.Own.GetRevision(), ParentRevision: preview.snapshot.Parent.GetRevision(), Digest: preview.digest, Reason: string(decision.Reason), Complete: decision.Complete, ObservedAt: timestamppb.New(capacity.ObservedAt), ExpiresAt: timestamppb.New(capacity.ExpiresAt), ActiveIngestClusterId: preview.activeCluster, SourceEvaluated: decision.sourceEvaluated}
	private := authz.Default.Can(ctx, authz.Identity{UserID: ctxkeys.GetUserID(ctx), TenantID: preview.snapshot.Scope.TenantID, Role: ctxkeys.GetRole(ctx), PlatformOperator: ctxkeys.IsPlatformOperator(ctx)}, authz.ActionReadPrivateInfrastructure, authz.Resource{OwnerTenantID: preview.snapshot.Scope.TenantID}).Allow
	if ctxkeys.GetAuthType(ctx) == "api_token" && !hasDelegatedPermission(ctxkeys.GetPermissions(ctx), "infrastructure:read") {
		private = false
	}
	byNode := map[string]placement.Candidate{}
	for _, candidate := range capacity.Candidates {
		byNode[candidate.NodeID] = candidate
	}
	explain := func(nodeID, clusterID, groupID, reason string, distance *float64) *placementpb.PreviewCandidate {
		peer := inventory.peers[clusterID]
		name := peer.GetClusterName()
		if name == "" {
			name = clusterID
		}
		row := &placementpb.PreviewCandidate{ClusterId: clusterID, ClusterName: name, Region: peer.GetRegionId(), GroupId: groupID, Reason: reason, DistanceKm: distance, RequiresSourcePull: decision.requiresPull[nodeID+"\x00"+groupID]}
		if private && (peer.GetOwnerTenantId() == preview.snapshot.Scope.TenantID || ctxkeys.IsPlatformOperator(ctx)) {
			row.NodeId = nodeID
		}
		if preview.policy != nil {
			for _, group := range preview.policy.Groups {
				if group.ID != groupID {
					continue
				}
				if price := placement.PreviewComparisonPrice(byNode[nodeID], group, now); price != nil {
					row.Price = &placementpb.PreviewPrice{AmountMicros: price.AmountMicros, Currency: price.Currency, Unit: price.Unit, Revision: price.Revision, ExpiresAt: timestamppb.New(price.ExpiresAt)}
				}
			}
		}
		return row
	}
	if len(decision.Choices) != 0 {
		choice := decision.Choices[0]
		out.Selected = explain(choice.NodeID, choice.ClusterID, choice.GroupID, "selected", choice.DistanceKM)
	}
	seen := map[string]*placementpb.PreviewCandidate{}
	for _, assessment := range decision.Assessments {
		row := explain(assessment.NodeID, assessment.ClusterID, assessment.GroupID, string(assessment.Reason), assessment.DistanceKM)
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t", row.ClusterId, row.NodeId, row.GroupId, row.Reason, row.RequiresSourcePull)
		if previous := seen[key]; previous != nil {
			if row.DistanceKm != nil && (previous.DistanceKm == nil || *row.DistanceKm < *previous.DistanceKm) {
				previous.DistanceKm = row.DistanceKm
			}
			continue
		}
		seen[key] = row
		out.Candidates = append(out.Candidates, row)
	}
	for _, transition := range decision.Transitions {
		out.Transitions = append(out.Transitions, &placementpb.PreviewTransition{FromGroup: transition.FromGroup, Reason: string(transition.Reason)})
	}
	return out
}
