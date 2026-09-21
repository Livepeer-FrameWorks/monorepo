package grpc

import (
	"context"
	"slices"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type mediaPlacementCapacityRead func(context.Context, string, *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error)

type mediaPreviewCell struct {
	id       string
	clusters []string
}

type mediaPreviewInventory struct {
	peers             map[string]*clusterpb.TenantClusterPeer
	cells             []mediaPreviewCell
	clusters          []string
	entitlementDigest string
	expiresAt         time.Time
}

func mediaPreviewEntitlement(tenantID string, entitlement *quartermasterpb.GetTenantEntitlementResponse, now time.Time) (*mediaPreviewInventory, error) {
	invalid := status.Error(codes.Unavailable, "preview entitlement is incomplete")
	if entitlement == nil || now.IsZero() || len(entitlement.GetAllowedClusterIds()) > 4096 || len(entitlement.GetEffectiveAccess()) != len(entitlement.GetAllowedClusterIds()) {
		return nil, invalid
	}
	allowed, seen := map[string]bool{}, map[string]bool{}
	for _, id := range entitlement.GetAllowedClusterIds() {
		if !placementOptionID(id) || allowed[id] {
			return nil, invalid
		}
		allowed[id] = true
	}
	out := &mediaPreviewInventory{peers: map[string]*clusterpb.TenantClusterPeer{}, expiresAt: now.Add(30 * time.Second)}
	cells := map[string][]string{}
	var bound []*clusterpb.TenantClusterPeer
	for _, peer := range entitlement.GetEffectiveAccess() {
		if peer == nil || !allowed[peer.GetClusterId()] || seen[peer.GetClusterId()] || !peer.GetAccessActive() || peer.GetSubscriptionStatus() != "active" {
			return nil, invalid
		}
		seen[peer.GetClusterId()] = true
		if expiry := peer.GetAccessExpiresAt(); expiry != nil {
			if !expiry.IsValid() || !now.Before(expiry.AsTime()) {
				return nil, invalid
			}
			if expiry.AsTime().Before(out.expiresAt) {
				out.expiresAt = expiry.AsTime()
			}
		}
		if peer.GetClusterType() != "edge" {
			continue
		}
		if !placementOptionID(peer.GetControlCellId()) || len(peer.GetClusterName()) > 255 || peer.GetRegionId() != "" && !placementOptionID(peer.GetRegionId()) {
			return nil, invalid
		}
		copy := proto.CloneOf(peer)
		out.peers[peer.GetClusterId()] = copy
		out.clusters = append(out.clusters, peer.GetClusterId())
		bound = append(bound, copy)
		cells[peer.GetControlCellId()] = append(cells[peer.GetControlCellId()], peer.GetClusterId())
	}
	if len(cells) > 64 {
		return nil, status.Error(codes.ResourceExhausted, "preview cell census exceeds bounds")
	}
	if len(bound) != 0 {
		var err error
		out.entitlementDigest, err = placement.CommercialEntitlementDigest(tenantID, bound)
		if err != nil {
			return nil, invalid
		}
	}
	slices.Sort(out.clusters)
	for id, clusters := range cells {
		slices.Sort(clusters)
		out.cells = append(out.cells, mediaPreviewCell{id: id, clusters: clusters})
	}
	slices.SortFunc(out.cells, func(a, b mediaPreviewCell) int {
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
	return out, nil
}

func (s *CommodoreServer) readPreviewCapacity(ctx context.Context, clusterID string, query *placementpb.CapacityPreviewQuery) (*placementpb.CapacityPreviewObservation, error) {
	if s.placementCapacitySource != nil {
		return s.placementCapacitySource(ctx, clusterID, query)
	}
	if s.foghornPool == nil || s.quartermasterClient == nil {
		return nil, status.Error(codes.Unavailable, "preview cell transport is unavailable")
	}
	client, err := s.resolveFoghornForCluster(ctx, clusterID, query.GetTenantId())
	if err != nil {
		return nil, err
	}
	return client.ObserveMediaPlacementCapacity(ctx, query)
}

func (s *CommodoreServer) collectPreviewCapacity(ctx context.Context, preview *mediaPlacementPreviewContext, inventory *mediaPreviewInventory, verb placementpb.Verb) (placement.CapacitySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	rows := make([]placement.CapacitySnapshot, len(inventory.cells))
	ok := make([]bool, len(rows))
	var group errgroup.Group
	group.SetLimit(4)
	for index, cell := range inventory.cells {
		group.Go(func() error {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			query := &placementpb.CapacityPreviewQuery{TenantId: preview.snapshot.Scope.TenantID, ControlCellId: cell.id, ClusterIds: slices.Clone(cell.clusters), Verb: verb, Protocol: preview.protocol, InternalName: preview.internalName}
			response, err := s.readPreviewCapacity(ctx, cell.clusters[0], query)
			if err != nil {
				return err
			}
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			row, err := placement.DecodeCapacityPreviewObservation(query, response, time.Now().UTC())
			if err != nil {
				return err
			}
			for _, id := range cell.clusters {
				if !proto.Equal(row.Consents[id], inventory.peers[id].GetMediaConsent()) {
					return status.Error(codes.Unavailable, "preview cell consent changed")
				}
			}
			for _, candidate := range row.Candidates {
				peer := inventory.peers[candidate.ClusterID]
				verbs := placement.ConsentVerbs(peer.GetMediaConsent())
				allowed := slices.Clone(candidate.AllowedVerbs)
				slices.Sort(allowed)
				if candidate.OwnerTenantID != peer.GetOwnerTenantId() || candidate.Official != (peer.GetClusterClass() == "platform_official") || candidate.Region != peer.GetRegionId() || !slices.Equal(allowed, verbs) {
					return status.Error(codes.Unavailable, "preview cell permission differs from current entitlement")
				}
			}
			rows[index], ok[index] = row, true
			return nil
		})
	}
	waitErr := group.Wait()
	out := placement.CapacitySnapshot{Complete: waitErr == nil, ObservedAt: time.Now().UTC(), ExpiresAt: inventory.expiresAt}
	seen := map[string]bool{}
	for index, row := range rows {
		if !ok[index] {
			out.Complete = false
			continue
		}
		out.Complete = out.Complete && row.Complete
		if row.ObservedAt.Before(out.ObservedAt) {
			out.ObservedAt = row.ObservedAt
		}
		if row.ExpiresAt.Before(out.ExpiresAt) {
			out.ExpiresAt = row.ExpiresAt
		}
		for _, candidate := range row.Candidates {
			if seen[candidate.NodeID] {
				return placement.CapacitySnapshot{}, status.Error(codes.Unavailable, "preview node identity is inconsistent")
			}
			seen[candidate.NodeID] = true
			out.Candidates = append(out.Candidates, candidate)
			if len(out.Candidates) > 4096 {
				return placement.CapacitySnapshot{}, status.Error(codes.ResourceExhausted, "preview node census exceeds bounds")
			}
		}
	}
	return out, nil
}
