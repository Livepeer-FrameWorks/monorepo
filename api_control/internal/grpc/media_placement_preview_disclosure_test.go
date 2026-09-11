package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

const (
	disclosureTenant = "10000000-0000-4000-8000-000000000091"
	otherTenant      = "10000000-0000-4000-8000-000000000092"
)

// disclosureInventory holds three clusters whose node ids must be treated
// differently: one the previewing tenant owns, one another tenant owns, and one
// platform-official cluster.
func disclosureInventory(t *testing.T) (*mediaPlacementPreviewContext, *mediaPreviewInventory, placement.CapacitySnapshot, mediaPreviewEvaluation) {
	t.Helper()
	now := time.Now().UTC()
	peers := []*clusterpb.TenantClusterPeer{
		{ClusterId: "own-eu", ClusterName: "My EU", ControlCellId: "eu-cell", ClusterType: "edge", ClusterClass: "tenant_private", OwnerTenantId: disclosureTenant, RegionId: "eu", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER},
		{ClusterId: "rented-eu", ClusterName: "Rented EU", ControlCellId: "eu-cell", ClusterType: "edge", ClusterClass: "third_party_marketplace", OwnerTenantId: otherTenant, RegionId: "eu", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION},
		{ClusterId: "official-us", ClusterName: "Official US", ControlCellId: "us-cell", ClusterType: "edge", ClusterClass: "platform_official", RegionId: "us", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER},
	}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{}
	for _, peer := range peers {
		peer.AccessActive, peer.SubscriptionStatus = true, "active"
		peer.MediaConsent = &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}
		entitlement.AllowedClusterIds = append(entitlement.AllowedClusterIds, peer.ClusterId)
		entitlement.EffectiveAccess = append(entitlement.EffectiveAccess, peer)
	}
	inventory, err := mediaPreviewEntitlement(disclosureTenant, entitlement, now)
	if err != nil {
		t.Fatal(err)
	}

	capacity := placement.CapacitySnapshot{ObservedAt: now, ExpiresAt: now.Add(20 * time.Second)}
	decision := mediaPreviewEvaluation{requiresPull: map[string]bool{}}
	for _, peer := range peers {
		nodeID := peer.ClusterId + "-node"
		capacity.Candidates = append(capacity.Candidates, placement.Candidate{
			TenantID: disclosureTenant, ClusterID: peer.ClusterId, NodeID: nodeID,
			OwnerTenantID: peer.OwnerTenantId, Official: peer.ClusterClass == "platform_official",
			Region: peer.RegionId, ObservedAt: now, ExpiresAt: now.Add(20 * time.Second),
			Capacity: placement.CapacityAvailable,
		})
		// Selected comes from Choices, the considered rows from Assessments;
		// both go through the same disclosure decision.
		decision.Choices = append(decision.Choices, placement.CapacityChoice{NodeID: nodeID, ClusterID: peer.ClusterId, GroupID: "g1"})
		decision.Assessments = append(decision.Assessments, placement.Assessment{NodeID: nodeID, ClusterID: peer.ClusterId, GroupID: "g1", Reason: placement.Reason("considered")})
	}
	preview := &mediaPlacementPreviewContext{
		snapshot: placementpolicy.Snapshot{Scope: placementpolicy.Scope{TenantID: disclosureTenant, Kind: "tenant", ID: disclosureTenant}},
		verb:     placement.Serve, protocol: "hls",
	}
	return preview, inventory, capacity, decision
}

func disclosureContext(authType, role string, platformOperator bool, permissions ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, disclosureTenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
	if platformOperator {
		ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	}
	return ctx
}

// disclosedNodes collects every node id the projection put on the wire, across
// the selected candidate and the considered list.
func disclosedNodes(out *placementpb.Preview) map[string]bool {
	seen := map[string]bool{}
	visit := func(row *placementpb.PreviewCandidate) {
		if row != nil && row.GetNodeId() != "" {
			seen[row.GetNodeId()] = true
		}
	}
	visit(out.GetSelected())
	for _, row := range out.GetCandidates() {
		visit(row)
	}
	return seen
}

// A node id names a specific machine in someone's private infrastructure. The
// preview explains a placement decision, so it may reveal which CLUSTERS were
// considered — that is the tenant's own policy surface — but a node id is only
// disclosed for infrastructure the caller is entitled to inspect.
func TestMediaPlacementPreviewNodeDisclosure(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want map[string]bool
	}{
		{
			// Owner: their own cluster's nodes, and nothing else's.
			name: "tenant admin sees only its own clusters",
			ctx:  disclosureContext("jwt", "admin", false),
			want: map[string]bool{"own-eu-node": true},
		},
		{
			// A rented cluster is usable without being inspectable; the
			// marketplace seller's node ids are not the renter's to see.
			name: "rented and official nodes stay hidden",
			ctx:  disclosureContext("jwt", "admin", false),
			want: map[string]bool{"own-eu-node": true},
		},
		{
			name: "platform operator sees every node",
			ctx:  disclosureContext("jwt", "admin", true),
			want: map[string]bool{"own-eu-node": true, "rented-eu-node": true, "official-us-node": true},
		},
		{
			// A delegated token is not the user. Without an explicit
			// infrastructure:read grant it discloses nothing, even though the
			// human behind it could see their own nodes in the UI.
			name: "api token without infrastructure:read sees nothing",
			ctx:  disclosureContext("api_token", "admin", false),
			want: map[string]bool{},
		},
		{
			name: "api token with infrastructure:read sees its own clusters",
			ctx:  disclosureContext("api_token", "admin", false, "infrastructure:read"),
			want: map[string]bool{"own-eu-node": true},
		},
		{
			// A member cannot read private infrastructure at all.
			name: "tenant member sees nothing",
			ctx:  disclosureContext("jwt", "member", false),
			want: map[string]bool{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preview, inventory, capacity, decision := disclosureInventory(t)
			req := &placementpb.PreviewRequest{Verb: placementpb.Verb_VERB_SERVE}
			out := projectMediaPlacementPreview(tc.ctx, req, preview, inventory, capacity, decision, time.Now().UTC())

			got := disclosedNodes(out)
			for node := range tc.want {
				if !got[node] {
					t.Errorf("node %q was withheld from a caller entitled to it", node)
				}
			}
			for node := range got {
				if !tc.want[node] {
					t.Errorf("node %q was disclosed to a caller not entitled to it", node)
				}
			}

			// Whatever the disclosure decision, the explanation itself must
			// still be produced — withholding node ids must not silently drop
			// the candidate rows the operator needs to understand the outcome.
			if len(out.GetCandidates()) == 0 && out.GetSelected() == nil {
				t.Fatal("projection produced no candidate rows at all")
			}
		})
	}
}

// Cluster identity is not private: the preview has to say which clusters were
// considered and why, or it cannot explain the tenant's own policy back to them.
func TestMediaPlacementPreviewAlwaysNamesClusters(t *testing.T) {
	preview, inventory, capacity, decision := disclosureInventory(t)
	req := &placementpb.PreviewRequest{Verb: placementpb.Verb_VERB_SERVE}
	out := projectMediaPlacementPreview(disclosureContext("jwt", "member", false), req, preview, inventory, capacity, decision, time.Now().UTC())

	if len(disclosedNodes(out)) != 0 {
		t.Fatal("a member was shown node ids")
	}
	named := map[string]bool{}
	if row := out.GetSelected(); row != nil {
		named[row.GetClusterId()] = true
	}
	for _, row := range out.GetCandidates() {
		named[row.GetClusterId()] = true
	}
	for _, cluster := range []string{"own-eu", "rented-eu", "official-us"} {
		if !named[cluster] {
			t.Errorf("cluster %q was withheld, so the preview cannot explain the decision", cluster)
		}
	}
}
