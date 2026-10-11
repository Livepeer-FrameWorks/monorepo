package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// withMembership answers discovery from a membership snapshot holding the
// fixture's inventory plus nodes, and counts every Quartermaster inventory
// read the discovery still makes.
func withMembership(t *testing.T, f *discoveryFixture, extra ...*quartermasterpb.MediaPlacementInventoryNode) (*balancer.PlacementMembership, *int) {
	t.Helper()
	membership := balancer.NewPlacementMembership(balancer.PlacementMembershipConfig{CellID: "us-cell", Now: func() time.Time { return f.now }})
	snapshot := &quartermasterpb.PlacementMembership{
		ControlCellId: "us-cell", Revision: "rev-1", Complete: true, ObservedAt: timestamppb.New(f.now.Add(-time.Hour)),
		Clusters: []*quartermasterpb.PlacementMembershipCluster{{ClusterId: "empty", IsActive: true}, {ClusterId: "us", IsActive: true}, {ClusterId: "eu", IsActive: true}},
		Nodes:    append(append([]*quartermasterpb.MediaPlacementInventoryNode(nil), f.inventory.Nodes...), extra...),
	}
	if err := membership.Apply(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.discovery.Membership = membership
	f.discovery.Inventory = placementInventoryReaderFunc(func(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
		calls++
		return nil, status.Error(codes.Unavailable, "quartermaster is down")
	})
	// Paths follow whatever inventory discovery joined.
	f.discovery.Paths = placementPathReaderFunc(func(_ context.Context, _ balancer.PlacementAuthority, _ *placementpb.CandidateQuery, snapshot *state.BalancerSnapshot) (PlacementPathObservation, error) {
		paths := f.paths
		paths.Paths = map[string]balancer.PlacementNodePath{}
		for _, node := range snapshot.Nodes {
			paths.Paths[node.NodeID] = balancer.PlacementNodePath{Presence: placement.Absent, SourceFeasible: true}
		}
		return paths, nil
	})
	return membership, &calls
}

func TestDiscovery_NoQuartermasterCallPerRequest(t *testing.T) {
	f := newDiscoveryFixture(t)
	_, calls := withMembership(t, f)
	for range 100 {
		response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
		if err != nil || !response.GetComplete() || len(response.GetCandidates()) != 12 {
			t.Fatalf("snapshot discovery = %+v, %v", response, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("100 queries made %d Quartermaster inventory calls, want 0", *calls)
	}
}

// Without a snapshot (none received, or Quartermaster predates membership)
// discovery reads inventory from Quartermaster per request.
func TestDiscovery_WithoutSnapshotReadsQuartermasterPerRequest(t *testing.T) {
	f := newDiscoveryFixture(t)
	calls := 0
	live := f.discovery.Inventory
	f.discovery.Membership = balancer.NewPlacementMembership(balancer.PlacementMembershipConfig{CellID: "us-cell"})
	f.discovery.Inventory = placementInventoryReaderFunc(func(ctx context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
		calls++
		return live.GetMediaPlacementInventory(ctx, req)
	})
	for range 3 {
		if _, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("fallback inventory calls = %d, want one per query", calls)
	}
}

func TestDiscovery_QMDownUsesSnapshot(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.discovery.Inventory = placementInventoryReaderFunc(func(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
		return nil, status.Error(codes.Unavailable, "quartermaster is down")
	})
	if _, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query); status.Code(err) != codes.Unavailable {
		t.Fatalf("without a snapshot a Quartermaster outage must refuse: %v", err)
	}
	_, calls := withMembership(t, f)
	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || !response.GetComplete() || len(response.GetCandidates()) != 12 {
		t.Fatalf("Quartermaster outage refused discovery despite a snapshot: %+v, %v", response, err)
	}
	// The snapshot's own read instant is an hour old; freshness is the request's.
	if !response.GetObservedAt().AsTime().Equal(f.now) || !response.GetExpiresAt().AsTime().After(f.now) {
		t.Fatalf("snapshot age leaked into the observation envelope: %+v", response)
	}
	if *calls != 0 {
		t.Fatalf("snapshot discovery called Quartermaster %d times", *calls)
	}
}

func TestDiscovery_GrantRevocationExcludesClusterWithoutQM(t *testing.T) {
	f := newDiscoveryFixture(t)
	_, calls := withMembership(t, f, &quartermasterpb.MediaPlacementInventoryNode{ClusterId: "empty", NodeId: "private-node", AdmissionEnabled: true})
	f.snapshot.Nodes = append(f.snapshot.Nodes, f.snapshot.Nodes[0])
	f.snapshot.Nodes[len(f.snapshot.Nodes)-1].NodeID, f.snapshot.Nodes[len(f.snapshot.Nodes)-1].ClusterID = "private-node", "empty"
	clustersOf := func(response *placementpb.CandidateObservation) map[string]int {
		out := map[string]int{}
		for _, candidate := range response.GetCandidates() {
			out[candidate.GetClusterId()]++
		}
		return out
	}
	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || clustersOf(response)["empty"] != 1 || clustersOf(response)["us"] != 12 {
		t.Fatalf("granted clusters before revocation = %v, %v", clustersOf(response), err)
	}

	// Commodore delivers a tenant authority without the private grant. The
	// snapshot still lists the cluster; the signed grants alone remove it.
	grants := f.pair.Tenant.Authority.EffectiveClusterGrants
	f.pair.Tenant.Authority.EffectiveClusterGrants = grants[:1]
	if _, refused := f.discovery.QueryPlacementCandidates(context.Background(), f.query); status.Code(refused) != codes.FailedPrecondition {
		t.Fatalf("a query naming the revoked cluster = %v", refused)
	}
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.query.ClusterIds, f.query.PolicyDigest = []string{"us"}, authority.PolicyDigest
	response, err = f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || clustersOf(response)["empty"] != 0 || clustersOf(response)["us"] != 12 {
		t.Fatalf("revoked cluster stayed in placement: %v, %v", clustersOf(response), err)
	}
	if *calls != 0 {
		t.Fatalf("revocation consulted Quartermaster %d times", *calls)
	}
}

// A granted cluster the membership snapshot does not list is refused exactly
// as Quartermaster's inventory refuses it, without falling back to it.
func TestDiscovery_ClusterMissingFromSnapshotRefusedAsQuartermasterDoes(t *testing.T) {
	quartermaster := newDiscoveryFixture(t)
	quartermaster.discovery.Inventory = placementInventoryReaderFunc(func(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
		return nil, status.Error(codes.PermissionDenied, "requested placement inventory is not authorized")
	})
	_, want := quartermaster.discovery.QueryPlacementCandidates(context.Background(), quartermaster.query)

	f := newDiscoveryFixture(t)
	membership, calls := withMembership(t, f)
	moved := &quartermasterpb.PlacementMembership{
		ControlCellId: "us-cell", Revision: "rev-2", Complete: true, ObservedAt: timestamppb.New(f.now),
		Clusters: []*quartermasterpb.PlacementMembershipCluster{{ClusterId: "us", IsActive: true}},
		Nodes:    f.inventory.Nodes,
	}
	if err := membership.Apply(context.Background(), moved); err != nil {
		t.Fatal(err)
	}
	_, got := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if got == nil || status.Code(got) != status.Code(want) || status.Convert(got).Message() != status.Convert(want).Message() {
		t.Fatalf("missing cluster refusal = %v, Quartermaster refusal = %v", got, want)
	}
	if *calls != 0 {
		t.Fatalf("a refused snapshot read fell back to Quartermaster %d times", *calls)
	}
}
