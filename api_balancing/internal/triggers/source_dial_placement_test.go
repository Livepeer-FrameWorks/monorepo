package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func nodePolicyPullSnapshot(allowedNodes ...string) localauthority.SourceSnapshot {
	now := time.Now()
	return localauthority.SourceSnapshot{
		Tenant: localauthority.TenantSnapshot{Version: 2, SourceReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.TenantAuthority{
			SchemaVersion: sharedauthority.NodePlacementSchemaVersion, TenantId: "tenant-a", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			MediaPlacement: &pb.PolicySet{Revision: 1, Ingest: &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Allow: &pb.SelectorSet{Any: []*pb.Selector{
				{ClusterIds: []string{"cluster-a"}, NodeIds: allowedNodes},
			}}}}},
			EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "cluster-a", ControlCellId: "cell-a", ClusterClass: "tenant_private", OwnerTenantId: "tenant-a",
				SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowIngest: true, AllowServe: true}}},
		}},
		Object: localauthority.MediaObjectSnapshot{AuthorityID: sharedauthority.LiveStreamAuthorityID("stream-1"), Version: 4, SourceReady: true, ValidUntil: now.Add(time.Hour), Authority: &mediapb.MediaObjectAuthority{
			SchemaVersion: sharedauthority.NodePlacementSchemaVersion, TenantId: "tenant-a", InternalName: "cam1", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{}, PlacementTenantRevision: 1,
			Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream-1", IngestMode: "pull"}},
		}},
	}
}

// STREAM_SOURCE on a node the signed ingest policy excludes must not return the
// balance: template that would make that node dial the upstream; it delegates
// to /source federation instead. The listed node in the same cluster proceeds.
func TestStreamSourcePullPlacementDelegatesPolicyDeniedNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeConnectionInfo(context.Background(), "edge-a", "edge-a.example", "", "cluster-a", nil)
	previousCommodore := control.CommodoreClient
	control.CommodoreClient = nil
	t.Cleanup(func() { control.CommodoreClient = previousCommodore })

	p := NewProcessor(testLogger(), nil, nil, nil, nil)
	resp := &commodorepb.ResolvePullSourceByInternalNameResponse{Found: true, Enabled: true, SourceUri: "https://origin.example.com/live/master.m3u8", TenantId: "tenant-a", StreamId: "stream-1"}
	trigger := func() *ipcpb.MistTrigger { return &ipcpb.MistTrigger{NodeId: "edge-a"} }

	response, abort, err := p.placeResolvedPullSource(context.Background(), "pull+cam1", "cam1", trigger(), resp, nodePolicyPullSnapshot("edge-b"), true)
	if err != nil || abort || response != "" {
		t.Fatalf("policy-denied node = %q abort=%v err=%v, want delegation to /source", response, abort, err)
	}
	response, abort, err = p.placeResolvedPullSource(context.Background(), "pull+cam1", "cam1", trigger(), resp, nodePolicyPullSnapshot("edge-a"), true)
	if err != nil || abort || response == "" {
		t.Fatalf("policy-permitted node = %q abort=%v err=%v, want it placed", response, abort, err)
	}
}
