package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type pushPreviewOwner struct {
	entitlement *quartermasterpb.GetTenantEntitlementResponse
	inventory   *quartermasterpb.MediaPlacementInventory
}

func (owner *pushPreviewOwner) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	return proto.CloneOf(owner.entitlement), nil
}
func (owner *pushPreviewOwner) GetMediaPlacementInventory(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	return proto.CloneOf(owner.inventory), nil
}

func pushPreviewFixture(t *testing.T) (*PlacementPushSourceObserver, *placementpb.PushSourcePreviewQuery, *pushPreviewOwner, *livePathRegistry, *state.BalancerSnapshot, time.Time) {
	t.Helper()
	f := newDiscoveryFixture(t)
	f.snapshot.Nodes = f.snapshot.Nodes[:1]
	node := &f.snapshot.Nodes[0]
	node.Outputs["DTSC"] = "dtsc://HOST:14200/$"
	node.Streams = map[string]state.BalancerStreamSummary{"internal": {TenantID: "tenant", Status: "live", BufferState: "FULL", Inputs: 1, ObservedAt: f.now}}
	registry := &livePathRegistry{entry: control.StreamEntry{TenantID: "tenant", InternalName: "internal", IngestMode: control.IngestPush, Locations: map[string]control.Location{"us-cell": {SourceActive: true, OwnerNodeID: node.NodeID, SourceGeneration: "generation", SourceRevision: 9}}}}
	owner := &pushPreviewOwner{entitlement: &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"us"}, EffectiveAccess: []*clusterpb.TenantClusterPeer{{ClusterId: "us", ControlCellId: "us-cell", ClusterType: "edge", ClusterClass: "platform_official", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, MediaConsent: &placementpb.CapacityConsent{AllowIngest: true}}}}, inventory: &quartermasterpb.MediaPlacementInventory{TenantId: "tenant", ControlCellId: "us-cell", ClusterIds: []string{"us"}, Complete: true, ObservedAt: timestamppb.New(f.now), Nodes: []*quartermasterpb.MediaPlacementInventoryNode{{NodeId: node.NodeID, ClusterId: "us", AdmissionEnabled: true}}}}
	observer := &PlacementPushSourceObserver{Inventory: &balancer.PlacementCapacityObserver{CellID: "us-cell", Owner: func() balancer.PlacementCapacityOwner { return owner }, Snapshot: func() *state.BalancerSnapshot { return f.snapshot }, Now: func() time.Time { return f.now }}, Registry: registry}
	return observer, &placementpb.PushSourcePreviewQuery{TenantId: "tenant", ControlCellId: "us-cell", ClusterId: "us", InternalName: "internal"}, owner, registry, f.snapshot, f.now
}

func TestPushSourcePreviewRequiresRegisteredCurrentPublisher(t *testing.T) {
	for _, scenario := range []string{"ok", "registry alias", "no listener", "stale listener", "stale buffer", "future buffer", "replica", "no inputs", "foreign tenant", "managed", "withdrawn", "missing owner", "unregistered", "disabled member", "incomplete membership", "denied ingest", "revoked", "wrong cell", "wrong stream", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			observer, req, owner, registry, snapshot, now := pushPreviewFixture(t)
			node := &snapshot.Nodes[0]
			stream := node.Streams["internal"]
			loc := registry.entry.Locations["us-cell"]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "registry alias":
				observer.RegistryCellID = "legacy-cell"
				registry.entry.Locations = map[string]control.Location{"legacy-cell": loc}
			case "no listener":
				delete(node.Outputs, "DTSC")
			case "stale listener":
				node.OutputsObservedAt = now.Add(-time.Minute)
			case "stale buffer":
				stream.ObservedAt = now.Add(-time.Minute)
			case "future buffer":
				stream.ObservedAt = now.Add(time.Second)
			case "replica":
				stream.Replicated = true
			case "no inputs":
				stream.Inputs = 0
			case "foreign tenant":
				stream.TenantID = "other"
			case "managed":
				registry.entry.IngestMode = control.IngestPull
			case "withdrawn":
				loc.SourceActive = false
			case "missing owner":
				loc.OwnerNodeID = "other"
			case "unregistered":
				owner.inventory.Nodes = nil
			case "disabled member":
				owner.inventory.Nodes[0].AdmissionEnabled = false
			case "incomplete membership":
				owner.inventory.Complete = false
			case "denied ingest":
				owner.entitlement.EffectiveAccess[0].MediaConsent.AllowIngest = false
			case "revoked":
				owner.entitlement.EffectiveAccess[0].AccessActive = false
			case "wrong cell":
				req.ControlCellId = "other"
			case "wrong stream":
				req.InternalName = "other"
			case "cancelled":
				cancel()
			}
			node.Streams["internal"] = stream
			if scenario != "registry alias" {
				registry.entry.Locations["us-cell"] = loc
			}
			out, err := observer.Observe(ctx, req)
			valid := scenario == "ok" || scenario == "registry alias" || scenario == "no listener" || scenario == "stale listener"
			if !valid {
				if err == nil || out != nil {
					t.Fatalf("%s invented publisher evidence: %+v %v", scenario, out, err)
				}
				return
			}
			if err != nil || placement.ValidatePushSourcePreviewObservation(req, out, now) != nil || out.GetNodeId() != node.NodeID || out.GetRevision() != 9 || out.GetGeneration() != "generation" || out.GetPullAvailable() != (scenario == "ok" || scenario == "registry alias") {
				t.Fatalf("source evidence lost scope or presence: %+v %v", out, err)
			}
		})
	}
}

func TestPushSourcePreviewExpiryIncludesMembershipAndAccess(t *testing.T) {
	observer, req, owner, _, _, now := pushPreviewFixture(t)
	owner.entitlement.EffectiveAccess[0].AccessExpiresAt = timestamppb.New(now.Add(2 * time.Second))
	out, err := observer.Observe(context.Background(), req)
	if err != nil || !out.GetExpiresAt().AsTime().Equal(now.Add(2*time.Second)) {
		t.Fatalf("access lifetime extended: %+v %v", out, err)
	}
	owner.entitlement.EffectiveAccess[0].AccessExpiresAt = nil
	owner.inventory.ObservedAt = timestamppb.New(now.Add(-29 * time.Second))
	out, err = observer.Observe(context.Background(), req)
	if err != nil || !out.GetExpiresAt().AsTime().Equal(now.Add(time.Second)) {
		t.Fatalf("membership lifetime extended: %+v %v", out, err)
	}
	var absent *PlacementPushSourceObserver
	if _, err := absent.Observe(context.Background(), req); status.Code(err) != codes.Unavailable {
		t.Fatal("missing observer succeeded")
	}
}
