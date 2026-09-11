package federation

import (
	"context"
	"slices"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PlacementPushSourceObserver reads only this cell's registered publisher. It
// does not synthesize active authority, distribute URLs or start media.
type PlacementPushSourceObserver struct {
	Inventory      *balancer.PlacementCapacityObserver
	Registry       PlacementSourceRegistry
	RegistryCellID string
}

func (observer *PlacementPushSourceObserver) Observe(ctx context.Context, req *placementpb.PushSourcePreviewQuery) (*placementpb.PushSourcePreviewObservation, error) {
	if err := placement.ValidatePushSourcePreviewQuery(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid push source preview")
	}
	if observer == nil || observer.Inventory == nil || observer.Registry == nil {
		return nil, status.Error(codes.Unavailable, "push source observation is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	joined, err := observer.Inventory.ObserveInventory(ctx, &placementpb.CapacityPreviewQuery{TenantId: req.TenantId, ControlCellId: req.ControlCellId, ClusterIds: []string{req.ClusterId}, Verb: placementpb.Verb_VERB_INGEST, Protocol: "whip", InternalName: req.InternalName})
	if err != nil {
		return nil, err
	}
	facts := joined.Clusters[req.ClusterId]
	if !slices.Contains(facts.AllowedVerbs, placement.Ingest) {
		return nil, status.Error(codes.PermissionDenied, "source owner denies ingest")
	}
	now := joined.ObservedAt
	// The reader judges evidence freshness against its own clock, which has to be
	// the same clock the rest of the preview path reads rather than wall time.
	reader := &LivePushPlacementPaths{CellID: req.ControlCellId, RegistryCellID: observer.RegistryCellID, Registry: observer.Registry,
		Snapshot: func() *state.BalancerSnapshot { return joined.Inventory.Snapshot }, Now: observer.Inventory.Now}
	source, err := reader.publisher(ctx, placementSourceContext{tenantID: req.TenantId, internalName: req.InternalName, grants: map[string]balancer.PlacementSourceGrant{req.ClusterId: {CellID: req.ControlCellId, AllowIngest: true}}})
	if err != nil || source == nil || source.cellID != req.ControlCellId || source.clusterID != req.ClusterId {
		return nil, status.Error(codes.Unavailable, "current registered publisher is unavailable")
	}
	expiry := minPlacementExpiry(joined.ExpiresAt, source.expiresAt)
	out := &placementpb.PushSourcePreviewObservation{Scope: proto.CloneOf(req), NodeId: source.nodeID, Generation: source.generation, Revision: uint64(source.revision), PullAvailable: source.dtscURL != "", ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(expiry)}
	out.Consent = &placementpb.CapacityConsent{Revision: facts.ConsentRevision, AllowIngest: true, AllowServe: slices.Contains(facts.AllowedVerbs, placement.Serve), AllowExternalSource: facts.AllowExternalSource}
	current := time.Now().UTC()
	if observer.Inventory.Now != nil {
		current = observer.Inventory.Now().UTC()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if err := placement.ValidatePushSourcePreviewObservation(req, out, current); err != nil {
		return nil, status.Error(codes.Unavailable, "publisher observation expired or inconsistent")
	}
	return out, nil
}
