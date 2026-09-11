package federation

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func localOriginPullFixture(t *testing.T) (*FederationServer, *ArrangeOriginPullDeps, ArrangeOriginPullRequest) {
	t.Helper()
	server, cache, _ := testFederationServerWithCache(t)
	server.controlCellID = "cell-a"
	server.allowFederationMutations = false
	server.isServedCluster = func(cluster string) bool { return cluster == "source-private" || cluster == "dest-private" }
	setLiveStreamState(t, "stream", "source-1", "tenant-a", "https://source.example")
	sm := state.DefaultManager()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeConnectionInfo(context.Background(), "source-1", "", "tenant-a", "source-private", nil)
	sm.SetNodeInfo("dest-1", "https://dest.example", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "dest-1", "", "tenant-a", "dest-private", nil)
	sm.TouchNode("dest-1", true)
	sm.SetProbeVerified("dest-1", true)
	if _, applied, err := control.StreamRegistryInstance.ProjectSource("stream", "source-1", 1, "trigger", "generation", 7); err != nil || !applied {
		t.Fatalf("publisher projection failed: %v", err)
	}
	deps := &ArrangeOriginPullDeps{Cache: cache, Registry: control.StreamRegistryInstance, LocalSource: server, InstanceID: "local-test", Logger: server.logger}
	req := ArrangeOriginPullRequest{
		TenantID: "tenant-a", InternalName: "stream", RemoteCluster: "cell-a",
		Remote:        &federationpb.EdgeCandidate{NodeId: "source-1", ClusterId: "source-private", SourceGeneration: "generation", SourceRevision: 7},
		DestClusterID: "dest-private", DestNodeID: "dest-1", DestNodeBaseURL: "https://dest.example",
	}
	return server, deps, req
}

func TestArrangeLocalOriginNeedsNeitherPeerAddressNorFederationCredential(t *testing.T) {
	server, deps, req := localOriginPullFixture(t)
	ctx := context.Background()
	var bound []PlacementPullBinding
	req.DestinationFence = 9007199254740993
	req.BindPull = func(pull *PlacementPullBinding) error {
		bound = append(bound, *pull)
		return nil
	}
	first, err := deps.ArrangeOriginPull(ctx, req)
	if err != nil || first == nil || first.SourceCellID != "cell-a" || first.SourceMediaClusterID != "source-private" || len(bound) != 1 {
		t.Fatalf("local preparation required federation or lost cell identity: %+v, %v", first, err)
	}
	second, err := deps.ArrangeOriginPull(ctx, req)
	if err != nil || !second.Reused || first.AttemptID != second.AttemptID || len(bound) != 2 || bound[0] != bound[1] {
		t.Fatalf("warm local preparation replaced physical pull: %+v, %v", second, err)
	}
	entries := control.StreamRegistryInstance.Snapshot()
	if len(entries) != 1 || len(entries[0].Locations["cluster-a"].OutboundPullers) != 1 || len(entries[0].Locations["cluster-a"].InboundPulls) != 1 {
		t.Fatalf("local handoff lost legacy registry namespace or tracking: %+v", entries)
	}
	notification := &federationpb.OriginPullNotification{StreamName: "stream", TenantId: "tenant-a", SourceNodeId: "source-1", DestNodeId: "dest-1", DestClusterId: "dest-private"}
	if ack, callErr := server.NotifyOriginPull(ctx, notification); ack != nil || status.Code(callErr) != codes.PermissionDenied {
		t.Fatalf("local path opened anonymous federation: %+v, %v", ack, callErr)
	}
	if ack, callErr := server.NotifyOriginPull(svcAuthCtx(), notification); callErr != nil || ack.GetAccepted() {
		t.Fatalf("local path enabled disabled federation: %+v, %v", ack, callErr)
	}
	state.DefaultManager().SetProbeVerified(req.DestNodeID, false)
	if result, reuseErr := deps.ArrangeOriginPull(ctx, req); reuseErr == nil || result != nil || len(bound) != 2 {
		t.Fatalf("warm local pull bypassed destination health: %+v, %v", result, reuseErr)
	}
}

func TestLocalOriginPreparationRefusesForeignOrUnregisteredDestination(t *testing.T) {
	for _, change := range []string{"foreign-cell", "foreign-source", "foreign-destination", "wrong-node-cluster", "unknown-node", "unhealthy", "unverified", "maintenance", "self-pull"} {
		t.Run(change, func(t *testing.T) {
			server, _, req := localOriginPullFixture(t)
			notification := &federationpb.OriginPullNotification{
				StreamName: req.InternalName, TenantId: req.TenantID, SourceCellId: req.RemoteCluster,
				SourceClusterId: req.Remote.ClusterId, SourceNodeId: req.Remote.NodeId, DestClusterId: req.DestClusterID, DestNodeId: req.DestNodeID,
				SourceGeneration: "generation", SourceRevision: 7, AttemptId: uuid.NewString(),
			}
			sm := state.DefaultManager()
			switch change {
			case "foreign-cell":
				notification.SourceCellId = "other-cell"
			case "foreign-source":
				notification.SourceClusterId = "other-source"
			case "foreign-destination":
				notification.DestClusterId = "other-destination"
			case "wrong-node-cluster":
				notification.DestClusterId = "source-private"
			case "unknown-node":
				notification.DestNodeId = "unknown"
			case "unhealthy":
				sm.TouchNode(req.DestNodeID, false)
			case "unverified":
				sm.SetProbeVerified(req.DestNodeID, false)
			case "maintenance":
				if err := sm.SetNodeOperationalMode(context.Background(), req.DestNodeID, state.NodeModeMaintenance, "test"); err != nil {
					t.Fatal(err)
				}
			case "self-pull":
				notification.DestNodeId = notification.SourceNodeId
			}
			if ack, err := server.prepareLocalOriginPull(context.Background(), notification); err == nil || ack.GetAccepted() {
				t.Fatalf("invalid local handoff accepted: %+v, %v", ack, err)
			}
			for _, entry := range control.StreamRegistryInstance.Snapshot() {
				if len(entry.Locations["cluster-a"].OutboundPullers) != 0 {
					t.Fatal("invalid local handoff recorded outbound pull")
				}
			}
		})
	}
}

func TestNotifyOriginPullUsesControlCellIdentityNotRegistryNamespace(t *testing.T) {
	server, _, req := localOriginPullFixture(t)
	server.allowFederationMutations = true
	notification := &federationpb.OriginPullNotification{
		StreamName: req.InternalName, TenantId: req.TenantID, SourceCellId: req.RemoteCluster,
		SourceClusterId: req.Remote.ClusterId, SourceNodeId: req.Remote.NodeId, DestClusterId: req.DestClusterID, DestNodeId: req.DestNodeID,
		SourceGeneration: "generation", SourceRevision: 7, AttemptId: uuid.NewString(),
	}
	ack, err := server.NotifyOriginPull(svcAuthCtx(), notification)
	if err != nil || !ack.GetAccepted() || ack.GetSourceCellId() != "cell-a" || ack.GetSourceClusterId() != "source-private" {
		t.Fatalf("source cell was confused with registry namespace: %+v, %v", ack, err)
	}
	legacy := proto.CloneOf(notification)
	legacy.SourceCellId = "cluster-a"
	if legacyAck, callErr := server.NotifyOriginPull(svcAuthCtx(), legacy); callErr != nil || !legacyAck.GetAccepted() || legacyAck.GetSourceCellId() != legacy.SourceCellId {
		t.Fatalf("configured peer alias lost its exact source binding: %+v, %v", legacyAck, callErr)
	}
	wrong := proto.CloneOf(notification)
	wrong.SourceCellId = "unrelated-cell"
	if wrongAck, callErr := server.NotifyOriginPull(svcAuthCtx(), wrong); callErr != nil || wrongAck.GetAccepted() {
		t.Fatalf("unconfigured source cell accepted: %+v, %v", wrongAck, callErr)
	}
}
