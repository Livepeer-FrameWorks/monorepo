package balancer

import (
	"context"
	"reflect"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type capacityOwnerFixture struct {
	t                                *testing.T
	entitlement                      *quartermasterpb.GetTenantEntitlementResponse
	inventory                        *quartermasterpb.MediaPlacementInventory
	entitlementCalls, inventoryCalls int
	afterEntitlement                 func()
}

func (owner *capacityOwnerFixture) GetTenantEntitlement(ctx context.Context, tenant string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	owner.t.Helper()
	owner.entitlementCalls++
	if tenant != owner.inventory.GetTenantId() {
		owner.t.Fatal("capacity observer substituted tenant")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
		owner.t.Fatal("capacity owner read was not bounded")
	}
	if owner.afterEntitlement != nil {
		owner.afterEntitlement()
	}
	return proto.CloneOf(owner.entitlement), nil
}

func (owner *capacityOwnerFixture) GetMediaPlacementInventory(_ context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	owner.inventoryCalls++
	if req.GetTenantId() != owner.inventory.GetTenantId() || req.GetControlCellId() != owner.inventory.GetControlCellId() || len(req.GetClusterIds()) != len(owner.inventory.GetClusterIds()) {
		owner.t.Fatal("capacity observer changed inventory scope")
	}
	return proto.CloneOf(owner.inventory), nil
}

func capacityObserverFixture(t *testing.T) (*PlacementCapacityObserver, *placementpb.CapacityPreviewQuery, *capacityOwnerFixture) {
	t.Helper()
	cell, inventory, observation := inventoryFixture()
	owner := &capacityOwnerFixture{t: t, inventory: inventory, entitlement: &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: cell.ClusterIDs}}
	for _, id := range cell.ClusterIDs {
		owner.entitlement.EffectiveAccess = append(owner.entitlement.EffectiveAccess, &clusterpb.TenantClusterPeer{
			ClusterId: id, ClusterType: "edge", ClusterClass: "tenant_private", OwnerTenantId: observation.TenantID,
			ControlCellId: cell.ID, RegionId: "eu", AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
			MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true},
		})
	}
	observer := &PlacementCapacityObserver{CellID: cell.ID, Owner: func() PlacementCapacityOwner { return owner }, Snapshot: func() *state.BalancerSnapshot { return observation.Snapshot }, Now: func() time.Time { return observation.Now }}
	query := &placementpb.CapacityPreviewQuery{TenantId: observation.TenantID, ControlCellId: cell.ID, ClusterIds: cell.ClusterIDs, Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls"}
	return observer, query, owner
}

func TestCapacityObserverReadsCompleteAuthorizedInventoryWithoutSource(t *testing.T) {
	observer, query, owner := capacityObserverFixture(t)
	before := proto.CloneOf(owner.entitlement)
	out, err := observer.Observe(context.Background(), query)
	if err != nil || !out.GetComplete() || len(out.GetCandidates()) != 2 || owner.entitlementCalls != 1 || owner.inventoryCalls != 1 {
		t.Fatalf("capacity observation: %+v %v", out, err)
	}
	if !reflect.DeepEqual(out.GetClusterIds(), []string{"empty", "private"}) || !proto.Equal(before, owner.entitlement) || !out.GetExpiresAt().AsTime().Equal(observer.now().Add(30*time.Second)) {
		t.Fatal("capacity observation changed census, owner facts or membership expiry")
	}
	for _, candidate := range out.GetCandidates() {
		if candidate.GetSourceFeasible() || candidate.GetPresence() != placementpb.Presence_PRESENCE_UNSPECIFIED || candidate.GetCommercialFacts() != nil || candidate.GetRegion() != "eu" {
			t.Fatalf("capacity preview invented source/price or lost authorized region: %+v", candidate)
		}
		if candidate.GetNodeId() == "missing" && candidate.GetCapacity() == placementpb.Capacity_CAPACITY_AVAILABLE {
			t.Fatal("missing registered member became available")
		}
	}
}

func TestCapacityObserverRejectsChangedAuthorityBeforeInventory(t *testing.T) {
	for _, scenario := range []string{"cell", "missing grant", "duplicate grant", "inactive", "expired", "unknown source", "moved cluster", "missing consent", "future consent", "unknown class", "wrong owner provenance", "wrong tier provenance"} {
		t.Run(scenario, func(t *testing.T) {
			observer, query, owner := capacityObserverFixture(t)
			peer := owner.entitlement.EffectiveAccess[0]
			switch scenario {
			case "wrong owner provenance":
				peer.OwnerTenantId = "other"
			case "wrong tier provenance":
				peer.AccessSource = clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER
			case "cell":
				query.ControlCellId = "other"
			case "missing grant":
				owner.entitlement.EffectiveAccess = owner.entitlement.EffectiveAccess[:1]
			case "duplicate grant":
				owner.entitlement.EffectiveAccess[1] = peer
			case "inactive":
				peer.AccessActive = false
			case "expired":
				peer.AccessExpiresAt = timestamppb.New(observer.now())
			case "unknown source":
				peer.AccessSource = 999
			case "moved cluster":
				peer.ControlCellId = "other"
			case "missing consent":
				peer.MediaConsent = nil
			case "future consent":
				peer.MediaConsent.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			case "unknown class":
				peer.ClusterClass = "unknown"
			}
			if _, err := observer.Observe(context.Background(), query); err == nil || owner.inventoryCalls != 0 {
				t.Fatalf("invalid authority reached inventory: %v calls=%d", err, owner.inventoryCalls)
			}
		})
	}
}

func TestCapacityObserverPreservesCancellationAndOwnerExpiry(t *testing.T) {
	observer, query, owner := capacityObserverFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	owner.afterEntitlement = cancel
	if _, err := observer.Observe(ctx, query); status.Code(err) != codes.Canceled || owner.inventoryCalls != 0 {
		t.Fatalf("cancelled preview continued owner reads: %v", err)
	}
	owner.afterEntitlement = nil
	owner.entitlement.EffectiveAccess[0].AccessExpiresAt = timestamppb.New(observer.now().Add(5 * time.Second))
	out, err := observer.Observe(context.Background(), query)
	if err != nil || !out.GetExpiresAt().AsTime().Equal(observer.now().Add(5*time.Second)) {
		t.Fatalf("preview outlived access grant: %+v %v", out, err)
	}
}
