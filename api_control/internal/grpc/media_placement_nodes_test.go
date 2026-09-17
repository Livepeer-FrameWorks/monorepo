package grpc

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nodePlacementOwner struct {
	unusedMediaAuthorityTenantSource
	tenantID string
}

func (o nodePlacementOwner) GetTenant(_ context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error) {
	return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: tenantID, IsActive: true}}, nil
}

func (o nodePlacementOwner) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	return &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{{
		ClusterId: "media-a", ClusterType: "edge", AccessActive: true, SubscriptionStatus: "active",
		AccessSource:  clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
		OwnerTenantId: o.tenantID, ClusterClass: "platform_official", ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-b"},
	}}}, nil
}

type nodePlacementBilling struct {
	unusedMediaAuthorityBillingSource
}

func (nodePlacementBilling) GetTenantBillingStatus(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
	return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"}, nil
}

func (nodePlacementBilling) GetTenantAdmissionStatus(context.Context, string) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	return &purserpb.GetTenantAdmissionStatusResponse{TierLevel: 1}, nil
}

// Every media cell the tenant authority targets, including eligible serving
// cells beyond the control cell, must attest node placement.
func TestCheckTenantPlacementNodesRequiresEveryTargetCellAttestation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	systemTenant := tenants.SystemTenantID.String()
	server := &CommodoreServer{
		db: db, foghornCandidateNext: map[string]int{},
		authorityTenantSource: nodePlacementOwner{tenantID: systemTenant}, authorityBillingSource: nodePlacementBilling{},
	}
	checker := &PlacementNodeChecker{server: server}
	columns := []string{"cell_id", "max_schema_version", "enforcement_ready", "live_replicas", "attested_at"}
	capabilityQuery := regexp.QuoteMeta("FROM commodore.media_cell_placement_capabilities")

	// cell-b attests schema 2 only, before and after the direct refresh, which
	// cannot reach a Foghorn without service discovery.
	for range 2 {
		mock.ExpectQuery(capabilityQuery).WillReturnRows(sqlmock.NewRows(columns).
			AddRow("cell-a", int32(3), true, int32(1), time.Now()).AddRow("cell-b", int32(2), true, int32(1), time.Now()))
	}
	anywhere := []placementpolicy.PlacementNode{{NodeID: "node-anywhere", ClusterIDs: []string{"media-a"}}}
	err = checker.CheckTenantPlacementNodes(context.Background(), systemTenant, anywhere)
	if !placement.IsNodePlacementNotReady(err) || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unattested serving cell did not refuse with the node placement reason: %v", err)
	}

	mock.ExpectQuery(capabilityQuery).WillReturnRows(sqlmock.NewRows(columns).
		AddRow("cell-a", int32(3), true, int32(1), time.Now()).AddRow("cell-b", int32(3), true, int32(1), time.Now()))
	if err := checker.CheckTenantPlacementNodes(context.Background(), systemTenant, anywhere); err != nil {
		t.Fatalf("system tenant with every cell attesting was refused: %v", err)
	}

	if err := checker.CheckTenantPlacementNodes(context.Background(), systemTenant, nil); err != nil {
		t.Fatalf("no node IDs needs no readiness: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type pairedNodeOwner struct {
	unusedMediaAuthorityTenantSource
	tenantID string
}

func (o pairedNodeOwner) GetTenant(_ context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error) {
	return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: tenantID, IsActive: true}}, nil
}

func (o pairedNodeOwner) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	return &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: pairedNodePeers(o.tenantID)}, nil
}

func pairedNodePeers(tenantID string) []*clusterpeerpb.TenantClusterPeer {
	var peers []*clusterpeerpb.TenantClusterPeer
	for _, cluster := range []string{"media-a", "media-b"} {
		peers = append(peers, &clusterpeerpb.TenantClusterPeer{
			ClusterId: cluster, ClusterType: "edge", AccessActive: true, SubscriptionStatus: "active",
			AccessSource:  clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
			OwnerTenantId: tenantID, ClusterClass: "private", ControlCellId: "cell-a",
		})
	}
	return peers
}

type pairedNodeInventory struct{}

func (pairedNodeInventory) GetMediaPlacementInventory(_ context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	return &quartermasterpb.MediaPlacementInventory{
		TenantId: req.GetTenantId(), ControlCellId: req.GetControlCellId(), ClusterIds: req.GetClusterIds(), Complete: true,
		Nodes: []*quartermasterpb.MediaPlacementInventoryNode{{ClusterId: "media-a", NodeId: "node-a1"}, {ClusterId: "media-b", NodeId: "node-b1"}},
	}, nil
}

// A node listed with clusters in the same selector (a source-location cluster
// entry or a reviewed rule) must belong to one of them; avoided nodes, deny
// selectors and cluster-less selectors only need an owned node.
func TestPlacementNodesMustBelongToTheirListedCluster(t *testing.T) {
	const tenantID = "10000000-0000-4000-8000-0000000000b1"
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &CommodoreServer{
		db: db, logger: logrus.New(), foghornCandidateNext: map[string]int{},
		authorityTenantSource: pairedNodeOwner{tenantID: tenantID}, authorityBillingSource: nodePlacementBilling{},
		placementInventorySource: pairedNodeInventory{},
		quartermasterClient: startPullSourceQuartermasterFake(t, &pullSourceQuartermasterFake{
			entitlementByTenant: map[string][]*clusterpeerpb.TenantClusterPeer{tenantID: pairedNodePeers(tenantID)},
		}),
	}
	expectReady := func() {
		mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_cell_placement_capabilities")).WillReturnRows(
			sqlmock.NewRows([]string{"cell_id", "max_schema_version", "enforcement_ready", "live_replicas", "attested_at"}).
				AddRow("cell-a", int32(3), true, int32(1), time.Now()))
	}
	ctx := context.Background()
	mispaired := `node "node-b1" belongs to cluster "media-b"`

	location := func(clusterID string, nodeIDs, avoid []string) *commodorepb.StreamSourceLocation {
		return &commodorepb.StreamSourceLocation{
			Mode:         commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED,
			Clusters:     []*commodorepb.SourceLocationCluster{{ClusterId: clusterID, NodeIds: nodeIDs}},
			AvoidNodeIds: avoid,
		}
	}
	expectReady()
	_, err = server.prepareStreamPlacement(ctx, tenantID, location("media-a", []string{"node-b1"}, nil), nil, false)
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), mispaired) {
		t.Fatalf("source location pairing media-a with a media-b node: %v", err)
	}
	expectReady()
	if _, err = server.prepareStreamPlacement(ctx, tenantID, location("media-a", []string{"node-a1"}, []string{"node-b1"}), nil, false); err != nil {
		t.Fatalf("source location with a paired node and an avoided owned node: %v", err)
	}

	facts, err := server.mediaPlacementOwnerFacts(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	selector := func(clusters []string, node string) *placementpb.Selector {
		return &placementpb.Selector{ClusterIds: clusters, NodeIds: []string{node}}
	}
	mispairedRule := &placementpb.PolicySet{Serve: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
		Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{selector([]string{"media-a"}, "node-a1"), selector([]string{"media-a"}, "node-b1")}},
	}}}
	expectReady()
	if err := server.checkMediaPlacementNodeSelectors(ctx, tenantID, mispairedRule, facts); status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), mispaired) {
		t.Fatalf("reviewed rule pairing media-a with a media-b node: %v", err)
	}
	accepted := &placementpb.PolicySet{Serve: &placementpb.Rules{SchemaVersion: 1,
		Constraints: &placementpb.Constraints{
			Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{selector([]string{"media-a", "media-b"}, "node-b1"), selector(nil, "node-a1")}},
			Deny:  []*placementpb.Selector{selector([]string{"media-a"}, "node-b1")},
		},
		Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "near", Match: selector([]string{"media-b"}, "node-b1")}}},
	}}
	expectReady()
	if err := server.checkMediaPlacementNodeSelectors(ctx, tenantID, accepted, facts); err != nil {
		t.Fatalf("reviewed rule with paired, cluster-less and denied nodes: %v", err)
	}
	unowned := &placementpb.PolicySet{Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{selector(nil, "node-foreign")}}}}
	expectReady()
	if err := server.checkMediaPlacementNodeSelectors(ctx, tenantID, unowned, facts); status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "not in a media cluster this tenant owns") {
		t.Fatalf("avoided node outside owned clusters: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
