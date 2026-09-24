package balancer

import (
	"context"
	"math"
	"slices"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type PlacementCapacityOwner interface {
	GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error)
	GetMediaPlacementInventory(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error)
}

// PlacementCapacityObserver has only owner reads and a telemetry snapshot. It
// does not depend on active policy, source preparation, reservations or receipts.
type PlacementCapacityObserver struct {
	CellID   string
	Owner    func() PlacementCapacityOwner
	Snapshot func() *state.BalancerSnapshot
	Now      func() time.Time
}

// PlacementPreviewInventory binds registered telemetry to fresh owner permissions,
// without asserting source presence or granting media admission.
type PlacementPreviewInventory struct {
	Inventory             *PlacementInventorySnapshot
	Clusters              map[string]PlacementClusterFacts
	ObservedAt, ExpiresAt time.Time
}

func (observer *PlacementCapacityObserver) ObserveInventory(ctx context.Context, req *placementpb.CapacityPreviewQuery) (*PlacementPreviewInventory, error) {
	if err := placement.ValidateCapacityPreviewQuery(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid capacity preview query")
	}
	if observer == nil || observer.CellID == "" || observer.Owner == nil || observer.Snapshot == nil {
		return nil, status.Error(codes.Unavailable, "capacity preview is unavailable")
	}
	if req.GetControlCellId() != observer.CellID {
		return nil, status.Error(codes.PermissionDenied, "capacity preview cell differs")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	owner := observer.Owner()
	if owner == nil {
		return nil, status.Error(codes.Unavailable, "capacity owner is unavailable")
	}
	entitlement, err := owner.GetTenantEntitlement(ctx, req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "capacity entitlement is unavailable")
	}
	verb := placement.Serve
	if req.GetVerb() == placementpb.Verb_VERB_INGEST {
		verb = placement.Ingest
	}
	facts, expiry, err := capacityPreviewFacts(req, entitlement, verb, observer.now())
	if err != nil {
		return nil, err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	readAt := observer.now()
	membership, err := owner.GetMediaPlacementInventory(ctx, &quartermasterpb.GetMediaPlacementInventoryRequest{TenantId: req.GetTenantId(), ControlCellId: req.GetControlCellId(), ClusterIds: slices.Clone(req.GetClusterIds())})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "capacity inventory is unavailable: %v", err)
	}
	now := observer.now()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if !now.Before(expiry) {
		return nil, status.Error(codes.Unavailable, "capacity entitlement expired")
	}
	joined, err := ReconcilePlacementInventory(req.GetTenantId(), PlacementCell{ID: observer.CellID, ClusterIDs: req.GetClusterIds()}, membership, observer.Snapshot(), readAt)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "capacity inventory is inconsistent: %v", err)
	}
	if joined.ExpiresAt.Before(expiry) {
		expiry = joined.ExpiresAt
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if !observer.now().Before(expiry) {
		return nil, status.Error(codes.Unavailable, "capacity observation expired")
	}
	return &PlacementPreviewInventory{Inventory: joined, Clusters: facts, ObservedAt: now, ExpiresAt: expiry}, nil
}

func (observer *PlacementCapacityObserver) Observe(ctx context.Context, req *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	observed, err := observer.ObserveInventory(ctx, req)
	if err != nil {
		return nil, err
	}
	verb := placement.Serve
	if req.GetVerb() == placementpb.Verb_VERB_INGEST {
		verb = placement.Ingest
	}
	now, expiry := observed.ObservedAt, observed.ExpiresAt
	observation, err := observed.Inventory.ObserveCapacity(PlacementObservationRequest{TenantID: req.GetTenantId(), Verb: verb, Protocol: req.GetProtocol(), InternalName: req.GetInternalName(), Now: now, Clusters: observed.Clusters})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "capacity observation is inconsistent")
	}
	if observation.ExpiresAt.Before(expiry) {
		expiry = observation.ExpiresAt
	}
	out := &placementpb.CapacityPreviewObservation{TenantId: req.GetTenantId(), ControlCellId: observer.CellID, ClusterIds: slices.Clone(req.GetClusterIds()), Verb: req.GetVerb(), Protocol: req.GetProtocol(), InternalName: req.GetInternalName(), Complete: observation.Complete, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(expiry)}
	slices.Sort(out.ClusterIds)
	out.ClusterConsents = make(map[string]*placementpb.CapacityConsent, len(observed.Clusters))
	for id, facts := range observed.Clusters {
		out.ClusterConsents[id] = &placementpb.CapacityConsent{Revision: facts.ConsentRevision, AllowIngest: slices.Contains(facts.AllowedVerbs, placement.Ingest), AllowServe: slices.Contains(facts.AllowedVerbs, placement.Serve), AllowExternalSource: facts.AllowExternalSource}
	}
	for _, candidate := range observation.Candidates {
		wire, encodeErr := placement.CandidateToProto(candidate, verb)
		if encodeErr != nil {
			return nil, status.Error(codes.Unavailable, "capacity observation cannot be encoded")
		}
		out.Candidates = append(out.Candidates, wire)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if !observer.now().Before(expiry) {
		return nil, status.Error(codes.Unavailable, "capacity observation expired")
	}
	return out, nil
}

func (observer *PlacementCapacityObserver) now() time.Time {
	if observer.Now != nil {
		return observer.Now().UTC()
	}
	return time.Now().UTC()
}

func capacityPreviewFacts(req *placementpb.CapacityPreviewQuery, entitlement *quartermasterpb.GetTenantEntitlementResponse, verb placement.Verb, now time.Time) (map[string]PlacementClusterFacts, time.Time, error) {
	invalid := status.Error(codes.Unavailable, "capacity entitlement is inconsistent")
	if now.IsZero() || entitlement == nil || len(entitlement.GetAllowedClusterIds()) > 4096 || len(entitlement.GetEffectiveAccess()) != len(entitlement.GetAllowedClusterIds()) {
		return nil, time.Time{}, invalid
	}
	allowed := make(map[string]bool)
	for _, id := range entitlement.GetAllowedClusterIds() {
		if id == "" || allowed[id] {
			return nil, time.Time{}, invalid
		}
		allowed[id] = true
	}
	requested := make(map[string]bool)
	for _, id := range req.GetClusterIds() {
		requested[id] = true
	}
	seen := make(map[string]bool)
	facts := make(map[string]PlacementClusterFacts)
	expiry := now.Add(placementObservationLifetime)
	for _, peer := range entitlement.GetEffectiveAccess() {
		if peer == nil || !allowed[peer.GetClusterId()] || seen[peer.GetClusterId()] || !peer.GetAccessActive() || peer.GetSubscriptionStatus() != "active" || peer.GetAccessSource() < clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER || peer.GetAccessSource() > clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE {
			return nil, time.Time{}, invalid
		}
		seen[peer.GetClusterId()] = true
		if peer.GetAccessSource() == clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER && peer.GetOwnerTenantId() != req.GetTenantId() || peer.GetAccessSource() == clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER && peer.GetClusterClass() != "platform_official" {
			return nil, time.Time{}, invalid
		}
		until := expiry
		if stamp := peer.GetAccessExpiresAt(); stamp != nil {
			if !stamp.IsValid() || !now.Before(stamp.AsTime()) {
				return nil, time.Time{}, invalid
			}
			if stamp.AsTime().Before(until) {
				until = stamp.AsTime()
			}
		}
		if !requested[peer.GetClusterId()] {
			continue
		}
		consent := peer.GetMediaConsent()
		if peer.GetControlCellId() != req.GetControlCellId() || peer.GetClusterType() != "edge" {
			return nil, time.Time{}, status.Error(codes.PermissionDenied, "capacity cluster is outside this cell")
		}
		if consent == nil || consent.GetRevision() > math.MaxInt64 || len(consent.ProtoReflect().GetUnknown()) != 0 {
			return nil, time.Time{}, invalid
		}
		grant := &mediapb.TenantClusterGrant{OwnerTenantId: peer.GetOwnerTenantId(), ClusterClass: peer.GetClusterClass(), RegionId: peer.GetRegionId(), MediaConsent: consent}
		value, err := placementGrantFacts(grant, nil, verb, until)
		if err != nil {
			return nil, time.Time{}, invalid
		}
		facts[peer.GetClusterId()] = value
		if until.Before(expiry) {
			expiry = until
		}
	}
	if len(facts) != len(requested) {
		return nil, time.Time{}, status.Error(codes.PermissionDenied, "capacity clusters are not authorized")
	}
	return facts, expiry, nil
}
