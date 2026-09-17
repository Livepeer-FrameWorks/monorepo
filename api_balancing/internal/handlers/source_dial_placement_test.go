package handlers

import (
	"net/http"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func nodePolicyPullLookup(upstream string, allowedNodes ...string) pullSourceLookup {
	now := time.Now()
	return pullSourceLookup{
		response:  &commodorepb.ResolvePullSourceByInternalNameResponse{Found: true, Enabled: true, SourceUri: upstream, TenantId: "tenant-a", StreamId: "stream-1"},
		usedLocal: true,
		snapshot: localauthority.SourceSnapshot{
			Tenant: localauthority.TenantSnapshot{Version: 2, SourceReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.TenantAuthority{
				SchemaVersion: sharedauthority.NodePlacementSchemaVersion, TenantId: "tenant-a", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
				MediaPlacement: &pb.PolicySet{Revision: 1, Ingest: &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Allow: &pb.SelectorSet{Any: []*pb.Selector{
					{ClusterIds: []string{"cluster-local"}, NodeIds: allowedNodes},
				}}}}},
				EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "cluster-local", ControlCellId: "cell-a", ClusterClass: "tenant_private", OwnerTenantId: "tenant-a",
					SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowIngest: true, AllowServe: true}}},
			}},
			Object: localauthority.MediaObjectSnapshot{AuthorityID: sharedauthority.LiveStreamAuthorityID("stream-1"), Version: 4, SourceReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.MediaObjectAuthority{
				SchemaVersion: sharedauthority.NodePlacementSchemaVersion, TenantId: "tenant-a", InternalName: "cam1", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{}, PlacementTenantRevision: 1,
				Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream-1", IngestMode: "pull"}},
			}},
		},
	}
}

// A signed ingest policy that names nodes decides which node of an allowed
// cluster may receive the upstream URI: the cluster pin passes for both nodes,
// but only the listed node is handed the upstream.
func TestPullSourceLookupHandsUpstreamOnlyToPolicyPermittedNode(t *testing.T) {
	withSeededBalancer(t)
	withLoggerSourceRes(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	seedSourceNodeCluster("edgeA", "cluster-local")
	const upstream = "https://origin.example.com/live/master.m3u8"

	c, w := newSourceCtxSourceRes("source=pull%2Bcam1")
	servePullSourceLookup(c, "pull+cam1", nodePolicyPullLookup(upstream, "edgeB"), 0, 0, nil, "edgeA", "203.0.113.5", c.Request.Context(), time.Now())
	if w.Code != http.StatusOK || w.Body.String() != control.OfflineNotPlaced {
		t.Fatalf("policy-denied node body = %q (status %d), want %q", w.Body.String(), w.Code, control.OfflineNotPlaced)
	}

	c, w = newSourceCtxSourceRes("source=pull%2Bcam1")
	servePullSourceLookup(c, "pull+cam1", nodePolicyPullLookup(upstream, "edgeA"), 0, 0, nil, "edgeA", "203.0.113.5", c.Request.Context(), time.Now())
	if w.Code != http.StatusOK || w.Body.String() != upstream {
		t.Fatalf("policy-permitted node body = %q (status %d), want the upstream", w.Body.String(), w.Code)
	}
}

func TestRemoteSourceSelectionRefusesPolicyDeniedOrigin(t *testing.T) {
	deniedOrigin := &foghornfederationpb.EdgeCandidate{NodeId: "origin-a", IsOrigin: true, ClusterId: "cluster-remote"}
	permittedOrigin := &foghornfederationpb.EdgeCandidate{NodeId: "origin-b", IsOrigin: true}
	relay := &foghornfederationpb.EdgeCandidate{NodeId: "relay-c"}
	permit := func(clusterID, nodeID string) bool {
		if clusterID != "cluster-remote" {
			t.Fatalf("origin checked against cluster %q", clusterID)
		}
		return nodeID != "origin-a"
	}
	if got := selectRemoteSourceCandidate([]*foghornfederationpb.EdgeCandidate{relay, deniedOrigin}, "cluster-remote", permit); got != nil {
		t.Fatalf("selected %q although its only origin is refused by policy", got.GetNodeId())
	}
	if got := selectRemoteSourceCandidate([]*foghornfederationpb.EdgeCandidate{relay, deniedOrigin, permittedOrigin}, "cluster-remote", permit); got != permittedOrigin {
		t.Fatalf("selected %v, want the permitted origin", got)
	}
	if got := selectRemoteSourceCandidate([]*foghornfederationpb.EdgeCandidate{relay, deniedOrigin}, "cluster-remote", nil); got != deniedOrigin {
		t.Fatalf("selection without a policy changed: %v", got)
	}
}
