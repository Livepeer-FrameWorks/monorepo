package control

import (
	"context"
	"slices"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func nodePolicySourcePair(now time.Time, ingest *pb.Rules) localauthority.PlacementPair {
	return localauthority.PlacementPair{
		Tenant: localauthority.TenantSnapshot{Version: 2, SourceReady: true, IngestReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.TenantAuthority{
			SchemaVersion: sharedauthority.NodePlacementSchemaVersion, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, MediaPlacement: &pb.PolicySet{Revision: 1, Ingest: ingest},
			EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "source", ControlCellId: "cell", ClusterClass: "tenant_private", OwnerTenantId: "tenant",
				SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowIngest: true, AllowServe: true}}},
		}},
		Object: localauthority.MediaObjectSnapshot{AuthorityID: "live_stream:stream", Version: 4, SourceReady: true, IngestReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.MediaObjectAuthority{
			SchemaVersion: sharedauthority.NodePlacementSchemaVersion, TenantId: "tenant", InternalName: "internal", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{}, PlacementTenantRevision: 1,
			Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}},
		}},
	}
}

func allowNodes(nodes ...string) *pb.Rules {
	return &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Allow: &pb.SelectorSet{Any: []*pb.Selector{{ClusterIds: []string{"source"}, NodeIds: nodes}}}}}
}

func TestSourceDialNodePlacementFollowsSignedIngestPolicy(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name          string
		mutate        func(*localauthority.PlacementPair)
		cluster, node string
		want          SourceDialPlacement
	}{
		{"listed node", nil, "source", "node-1", SourceDialPermitted},
		{"unlisted node", nil, "source", "node-2", SourceDialDenied},
		{"unknown node identity", nil, "source", "", SourceDialUnavailable},
		{"cluster without a grant", nil, "elsewhere", "node-1", SourceDialDenied},
		{"owner withholds ingest consent", func(p *localauthority.PlacementPair) {
			p.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowIngest = false
		}, "source", "node-1", SourceDialDenied},
		{"billing suspended", func(p *localauthority.PlacementPair) {
			p.Tenant.Authority.BillingDecision = mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
		}, "source", "node-1", SourceDialDenied},
		{"source not ready", func(p *localauthority.PlacementPair) { p.Tenant.SourceReady = false }, "source", "node-1", SourceDialUnavailable},
		{"schema one carries no policy", func(p *localauthority.PlacementPair) {
			p.Tenant.Authority.SchemaVersion, p.Object.Authority.SchemaVersion = 1, 1
			p.Tenant.Authority.MediaPlacement, p.Object.Authority.MediaPlacement = nil, nil
		}, "source", "node-2", SourceDialPermitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			pair := nodePolicySourcePair(now, allowNodes("node-1"))
			if test.mutate != nil {
				test.mutate(&pair)
			}
			if got := SourceDialNodePlacement(pair, test.cluster, test.node, now); got != test.want {
				t.Fatalf("SourceDialNodePlacement = %d, want %d", got, test.want)
			}
		})
	}
}

func TestManagedNodeElectionSkipsPolicyDeniedNodes(t *testing.T) {
	now := time.Now()
	row := &commodorepb.ManagedStreamRow{TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "mist_native", AllowedClusterIds: []string{"source"}, PlacementCount: 1}
	nodes := []eligibleNode{{nodeID: "node-1", clusterID: "source"}, {nodeID: "node-2", clusterID: "source"}}

	f := &managedPlacementFixture{t: t, pair: nodePolicySourcePair(now, allowNodes("node-2"))}
	got, status := filterManagedNodesByPlacement(context.Background(), f, row, nodes, now)
	if status != placementOK || !slices.Equal(got, []eligibleNode{{nodeID: "node-2", clusterID: "source"}}) {
		t.Fatalf("election census = %v (%v), want only the permitted node", got, status)
	}
	for _, node := range placementPickWithCluster(row.GetStreamId(), got, 1) {
		if node.nodeID != "node-2" {
			t.Fatalf("stream elected onto policy-denied node %q", node.nodeID)
		}
	}

	f.pair = nodePolicySourcePair(now, &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Allow: &pb.SelectorSet{Any: []*pb.Selector{{Regions: []string{"eu"}}}}}})
	if got, status := filterManagedNodesByPlacement(context.Background(), f, row, nodes, now); status != placementTransient || got != nil {
		t.Fatalf("unknown region facts produced an election census: %v (%v)", got, status)
	}

	f.pair = nodePolicySourcePair(now, nil)
	f.pair.Tenant.Authority.SchemaVersion, f.pair.Object.Authority.SchemaVersion = 1, 1
	f.pair.Tenant.Authority.MediaPlacement, f.pair.Object.Authority.MediaPlacement = nil, nil
	if got, status := filterManagedNodesByPlacement(context.Background(), f, row, nodes, now); status != placementOK || len(got) != 2 {
		t.Fatalf("schema-1 pair narrowed the election census: %v (%v)", got, status)
	}
}
