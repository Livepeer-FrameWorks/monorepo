package grpc

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mediaPlacementInventorySource interface {
	GetMediaPlacementInventory(ctx context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error)
}

// checkMediaPlacementNodeSelectors admits node IDs in a policy only when every
// cell serving the tenant attests node placement (schema 3), and only for nodes
// of media clusters the tenant owns, each inside a cluster its selector names.
// The system tenant may name any node.
func (s *CommodoreServer) checkMediaPlacementNodeSelectors(ctx context.Context, tenantID string, next *placementpb.PolicySet, facts mediaPlacementOwnerFacts) error {
	return s.checkPlacementNodes(ctx, tenantID, placementpolicy.PolicySetPlacementNodes(next), facts)
}

// PlacementNodeChecker applies Commodore's node selector gate outside the gRPC
// service, for writers such as bootstrap that bypass placement reviews.
type PlacementNodeChecker struct {
	server *CommodoreServer
}

// PlacementNodeCheckerConfig carries the owner, capability and Foghorn
// discovery dependencies the gate reads.
type PlacementNodeCheckerConfig struct {
	DB                  *sql.DB
	Logger              logging.Logger
	QuartermasterClient *qmclient.GRPCClient
	PurserClient        *purserclient.GRPCClient
	FoghornPool         *foghornclient.FoghornPool
	// SystemTenantID is the deployment's Quartermaster-owned system tenant.
	// The zero value means the reserved tenants.SystemTenantID.
	SystemTenantID uuid.UUID
}

func NewPlacementNodeChecker(cfg PlacementNodeCheckerConfig) *PlacementNodeChecker {
	server := &CommodoreServer{
		db:                   cfg.DB,
		logger:               cfg.Logger,
		foghornPool:          cfg.FoghornPool,
		quartermasterClient:  cfg.QuartermasterClient,
		purserClient:         cfg.PurserClient,
		systemTenantID:       cfg.SystemTenantID,
		foghornCandidateNext: make(map[string]int),
	}
	if cfg.QuartermasterClient != nil {
		server.authorityTenantSource = cfg.QuartermasterClient
		server.placementInventorySource = cfg.QuartermasterClient
	}
	if cfg.PurserClient != nil {
		server.authorityBillingSource = cfg.PurserClient
	}
	return &PlacementNodeChecker{server: server}
}

// CheckTenantPlacementNodes reports whether tenantID may name nodes in
// placement rules. A refusal because a serving cell does not attest node
// placement carries placement.ReasonNodePlacementNotReady.
func (c *PlacementNodeChecker) CheckTenantPlacementNodes(ctx context.Context, tenantID string, nodes []placementpolicy.PlacementNode) error {
	return c.server.checkTenantPlacementNodes(ctx, tenantID, nodes)
}

// checkTenantPlacementNodes reads the tenant's owner facts and applies the node
// selector gate to nodes declared outside a policy.
func (s *CommodoreServer) checkTenantPlacementNodes(ctx context.Context, tenantID string, nodes []placementpolicy.PlacementNode) error {
	if len(nodes) == 0 {
		return nil
	}
	facts, err := s.mediaPlacementOwnerFacts(ctx, tenantID)
	if err != nil {
		return err
	}
	return s.checkPlacementNodes(ctx, tenantID, nodes, facts)
}

// checkPlacementNodes requires node placement attestation from every target
// cell, then, for tenants other than the system tenant, that every node belongs
// to an owned media cluster and, when its selector names clusters, to one of
// those clusters. The system tenant's node IDs are validated against the
// manifest inventory when bootstrap renders them.
func (s *CommodoreServer) checkPlacementNodes(ctx context.Context, tenantID string, nodes []placementpolicy.PlacementNode, facts mediaPlacementOwnerFacts) error {
	if len(nodes) == 0 {
		return nil
	}
	if s.db == nil {
		return status.Error(codes.Unavailable, "placement state is unavailable")
	}
	queries := commodoredb.New(s.db)
	ready, err := placementCellsReadyFor(ctx, queries, facts.targets, sharedauthority.NodePlacementSchemaVersion)
	if err != nil {
		return status.Error(codes.Unavailable, "placement capability state is unavailable")
	}
	if !ready {
		// A cell that upgraded since its last acknowledgement is asked directly;
		// an unreachable cell stays unattested.
		_ = s.refreshPlacementCellCapabilities(ctx, facts.targets) //nolint:errcheck // re-read below decides
		if ready, err = placementCellsReadyFor(ctx, queries, facts.targets, sharedauthority.NodePlacementSchemaVersion); err != nil {
			return status.Error(codes.Unavailable, "placement capability state is unavailable")
		}
	}
	if !ready {
		return placement.NodePlacementNotReadyError("node placement rules require every media cell serving this tenant to support node placement")
	}
	if s.isSystemPlacementTenant(tenantID) {
		return nil
	}
	owned, err := s.ownedPlacementNodes(ctx, tenantID, facts.entitlement)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		clusterID, ok := owned[node.NodeID]
		if !ok {
			return status.Errorf(codes.InvalidArgument, "node %q is not in a media cluster this tenant owns", node.NodeID)
		}
		if len(node.ClusterIDs) != 0 && !slices.Contains(node.ClusterIDs, clusterID) {
			return status.Errorf(codes.InvalidArgument, "node %q belongs to cluster %q, not to a cluster listed with it (%s)", node.NodeID, clusterID, strings.Join(node.ClusterIDs, ", "))
		}
	}
	return nil
}

// isSystemPlacementTenant reports whether tenantID is a reserved platform
// identity or the deployment's system tenant.
func (s *CommodoreServer) isSystemPlacementTenant(tenantID string) bool {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return false
	}
	if id == tenants.ServiceAccountUserID || id == tenants.AnonymousTenantID {
		return true
	}
	systemTenantID := s.systemTenantID
	if systemTenantID == uuid.Nil {
		systemTenantID = tenants.SystemTenantID
	}
	return id == systemTenantID
}

// ownedPlacementNodes maps node ID to cluster ID for every registered node of
// the edge clusters the tenant owns, read from Quartermaster's placement
// inventory per control cell. An incomplete or mismatched inventory is
// unavailable rather than a smaller set of nodes.
func (s *CommodoreServer) ownedPlacementNodes(ctx context.Context, tenantID string, entitlement *quartermasterpb.GetTenantEntitlementResponse) (map[string]string, error) {
	unavailable := status.Error(codes.Unavailable, "placement node inventory is unavailable")
	byCell := map[string][]string{}
	for _, peer := range entitlement.GetEffectiveAccess() {
		if peer.GetOwnerTenantId() != tenantID || peer.GetClusterType() != "edge" || !peer.GetAccessActive() {
			continue
		}
		if peer.GetControlCellId() == "" || peer.GetClusterId() == "" {
			return nil, unavailable
		}
		byCell[peer.GetControlCellId()] = append(byCell[peer.GetControlCellId()], peer.GetClusterId())
	}
	owned := map[string]string{}
	if len(byCell) == 0 {
		return owned, nil
	}
	if s.placementInventorySource == nil {
		return nil, unavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cells := make([]string, 0, len(byCell))
	for cell := range byCell {
		cells = append(cells, cell)
	}
	slices.Sort(cells)
	for _, cell := range cells {
		clusters := sortedUnique(byCell[cell])
		inventory, err := s.placementInventorySource.GetMediaPlacementInventory(ctx, &quartermasterpb.GetMediaPlacementInventoryRequest{TenantId: tenantID, ControlCellId: cell, ClusterIds: clusters})
		if err != nil || inventory.GetTenantId() != tenantID || inventory.GetControlCellId() != cell || !inventory.GetComplete() || !slices.Equal(sortedUnique(inventory.GetClusterIds()), clusters) {
			return nil, unavailable
		}
		for _, node := range inventory.GetNodes() {
			if node.GetNodeId() == "" || !slices.Contains(clusters, node.GetClusterId()) {
				return nil, unavailable
			}
			if previous, seen := owned[node.GetNodeId()]; seen && previous != node.GetClusterId() {
				return nil, unavailable
			}
			owned[node.GetNodeId()] = node.GetClusterId()
		}
	}
	return owned, nil
}
