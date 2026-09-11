package federation

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"

	"context"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func testFederationServerWithCache(t *testing.T) (*FederationServer, *RemoteEdgeCache, *miniredis.Miniredis) {
	t.Helper()
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "source-pull-test-secret")
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cache := NewRemoteEdgeCache(client, "cluster-a", logging.NewLogger())
	server := NewFederationServer(FederationServerConfig{
		Logger:                   logging.NewLogger(),
		ClusterID:                "cluster-a",
		Cache:                    cache,
		AllowFederationMutations: true,
	})
	// Install a per-test stream registry so NotifyOriginPull can record
	// outbound pulls. Tests that want to exercise the registry-unavailable
	// path clear it explicitly via control.SetStreamRegistry(nil).
	prior := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	if _, _, err := registry.EnableRedisSync(context.Background(), control.NewRedisRegistryStore(client, "cluster-a"), "test-source", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	control.SetStreamRegistry(registry)
	t.Cleanup(func() {
		registry.DisableRedisSync()
		control.SetStreamRegistry(prior)
		_ = client.Close()
		mr.Close()
	})
	return server, cache, mr
}

func TestNotifyOriginPullRejectsOutboundPersistenceFailure(t *testing.T) {
	server, _, engine := testFederationServerWithCache(t)
	setLiveStreamState(t, "stream", "source-1", "tenant-a", "https://edge-a.example.com")
	engine.SetError("registry writes unavailable")
	t.Cleanup(func() { engine.SetError("") })
	ctx, cancel := context.WithTimeout(svcAuthCtx(), 100*time.Millisecond)
	defer cancel()
	ack, err := server.NotifyOriginPull(ctx, &foghornfederationpb.OriginPullNotification{
		StreamName: "stream", SourceNodeId: "source-1", DestClusterId: "cluster-b", DestNodeId: "dest-1", TenantId: "tenant-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.GetAccepted() || ack.GetDtscUrl() != "" {
		t.Fatalf("untracked pull acknowledged: %v", ack)
	}
	if len(control.StreamRegistryInstance.Snapshot()) != 0 {
		t.Fatal("failed source handoff became locally tracked")
	}
}

func TestNotifyOriginPullRejectsAnotherStreamsNode(t *testing.T) {
	server, _, _ := testFederationServerWithCache(t)
	setLiveStreamState(t, "stream", "source-1", "tenant-a", "https://edge-a.example.com")
	state.DefaultManager().SetNodeInfo("other-node", "https://other.example.com", true, nil, nil, "", "", map[string]any{"DTSC": "dtsc://HOST/$"})
	ack, err := server.NotifyOriginPull(svcAuthCtx(), &foghornfederationpb.OriginPullNotification{
		StreamName: "stream", SourceNodeId: "other-node", DestClusterId: "cluster-b", DestNodeId: "dest-1", TenantId: "tenant-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.GetAccepted() {
		t.Fatal("node-level source binding was skipped")
	}
}

func TestNotifyOriginPullBindsAcknowledgementToConfirmedPublisher(t *testing.T) {
	server, _, _ := testFederationServerWithCache(t)
	setLiveStreamState(t, "stream", "source-1", "tenant-a", "https://edge-a.example.com")
	state.DefaultManager().SetNodeConnectionInfo(context.Background(), "source-1", "", "tenant-a", "virtual-source", nil)
	registry := control.StreamRegistryInstance
	if _, applied, err := registry.ProjectSource("stream", "source-1", 1, "trigger", "generation-1", 7); err != nil || !applied {
		t.Fatalf("project publisher: applied=%v err=%v", applied, err)
	}
	req := &foghornfederationpb.OriginPullNotification{
		StreamName: "stream", SourceNodeId: "source-1", DestClusterId: "cluster-b", DestNodeId: "dest-1", TenantId: "tenant-a",
		SourceGeneration: "generation-1", SourceRevision: 7, AttemptId: uuid.NewString(),
		SourceCellId: "cluster-a", SourceClusterId: "virtual-source",
	}
	ack, err := server.NotifyOriginPull(svcAuthCtx(), req)
	if err != nil || !ack.GetAccepted() {
		t.Fatalf("confirmed publisher refused: %v %v", ack, err)
	}
	bound := ArrangeOriginPullRequest{TenantID: req.TenantId, SourceGeneration: req.SourceGeneration, SourceRevision: req.SourceRevision, AttemptID: req.AttemptId,
		RemoteCluster: "cluster-a", Remote: &foghornfederationpb.EdgeCandidate{NodeId: req.SourceNodeId, ClusterId: "virtual-source"}}
	if !originPullAckMatches(bound, req.DestClusterId, req.DestNodeId, ack) {
		t.Fatalf("acknowledgement lost source/destination identity: %v", ack)
	}
	legacy := proto.CloneOf(req)
	legacy.SourceCellId, legacy.SourceClusterId = "", ""
	legacyAck, legacyErr := server.NotifyOriginPull(svcAuthCtx(), legacy)
	if legacyErr != nil || !legacyAck.GetAccepted() || legacyAck.GetSourceCellId() != "" || legacyAck.GetSourceClusterId() != "" {
		t.Fatalf("unbound request acquired an unsolicited binding: %+v, %v", legacyAck, legacyErr)
	}
	entries := registry.Snapshot()
	if len(entries) != 1 || len(entries[0].Locations["cluster-a"].OutboundPullers) != 1 || entries[0].Locations["cluster-a"].OutboundPullers[0].SourceMediaClusterID != "virtual-source" {
		t.Fatal("unbound request erased validated source-side membership")
	}
	if _, applied, projectErr := registry.ProjectSource("stream", "source-1", 2, "trigger-2", "generation-2", 8); projectErr != nil || !applied {
		t.Fatalf("replace publisher: applied=%v err=%v", applied, projectErr)
	}
	stale, staleErr := server.NotifyOriginPull(svcAuthCtx(), req)
	if staleErr != nil || stale.GetAccepted() || stale.GetDtscUrl() != "" {
		t.Fatalf("stale source generation acknowledged: %v %v", stale, staleErr)
	}
	req.SourceGeneration, req.SourceRevision, req.AttemptId = "generation-2", 8, uuid.NewString()
	current, currentErr := server.NotifyOriginPull(svcAuthCtx(), req)
	if currentErr != nil || !current.GetAccepted() || current.GetSourceRevision() != 8 || current.GetAttemptId() != req.AttemptId {
		t.Fatalf("replacement publisher refused: %v %v", current, currentErr)
	}
}

func TestNotifyOriginPullRejectsWrongOrUnknownVirtualSource(t *testing.T) {
	for _, scenario := range []string{"wrong_cell", "wrong_cluster", "unknown_membership", "missing_cell", "missing_cluster", "auto_source"} {
		t.Run(scenario, func(t *testing.T) {
			server, _, _ := testFederationServerWithCache(t)
			setLiveStreamState(t, "stream", "source", "tenant", "https://edge.example")
			if scenario != "unknown_membership" {
				state.DefaultManager().SetNodeConnectionInfo(context.Background(), "source", "", "tenant", "media", nil)
			}
			req := &foghornfederationpb.OriginPullNotification{StreamName: "stream", SourceNodeId: "source", TenantId: "tenant",
				DestClusterId: "destination", DestNodeId: "edge", AttemptId: uuid.NewString(), SourceCellId: "cluster-a", SourceClusterId: "media"}
			switch scenario {
			case "wrong_cell":
				req.SourceCellId = "another-cell"
			case "wrong_cluster":
				req.SourceClusterId = "another-media"
			case "unknown_membership":
			case "missing_cell":
				req.SourceCellId = ""
			case "missing_cluster":
				req.SourceClusterId = ""
			case "auto_source":
				req.SourceNodeId = ""
			}
			ack, err := server.NotifyOriginPull(svcAuthCtx(), req)
			if ack.GetAccepted() || ack.GetDtscUrl() != "" || (err != nil && status.Code(err) != codes.InvalidArgument) {
				t.Fatalf("incorrect source binding accepted: %+v, %v", ack, err)
			}
			if len(control.StreamRegistryInstance.Snapshot()) != 0 {
				t.Fatal("invalid source binding created outbound state")
			}
		})
	}
}

func TestNotifyOriginPullRejectsMalformedGenerationBeforeSourceSelection(t *testing.T) {
	for name, mutate := range map[string]func(*foghornfederationpb.OriginPullNotification){
		"missing_source":      func(r *foghornfederationpb.OriginPullNotification) { r.SourceNodeId = "" },
		"missing_destination": func(r *foghornfederationpb.OriginPullNotification) { r.DestNodeId = "" },
		"missing_attempt":     func(r *foghornfederationpb.OriginPullNotification) { r.AttemptId = "" },
		"missing_revision":    func(r *foghornfederationpb.OriginPullNotification) { r.SourceRevision = 0 },
		"missing_generation":  func(r *foghornfederationpb.OriginPullNotification) { r.SourceGeneration = "" },
		"control_character":   func(r *foghornfederationpb.OriginPullNotification) { r.SourceGeneration = "generation\x00one" },
	} {
		t.Run(name, func(t *testing.T) {
			server, _, _ := testFederationServerWithCache(t)
			req := &foghornfederationpb.OriginPullNotification{
				StreamName: "stream", SourceNodeId: "source-1", DestClusterId: "cluster-b", DestNodeId: "dest-1", TenantId: "tenant-a",
				SourceGeneration: "generation", SourceRevision: 7, AttemptId: uuid.NewString(),
			}
			mutate(req)
			if ack, err := server.NotifyOriginPull(svcAuthCtx(), req); status.Code(err) != codes.InvalidArgument || ack != nil {
				t.Fatalf("malformed source binding reached selection: %v %v", ack, err)
			}
			if len(control.StreamRegistryInstance.Snapshot()) != 0 {
				t.Fatal("malformed source request created outbound state")
			}
		})
	}
}

func setLiveStreamState(t *testing.T, streamName, nodeID, tenantID, baseURL string) {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	sm.SetNodeInfo(nodeID, baseURL, true, nil, nil, "", "", map[string]any{
		"DTSC": "dtsc://HOST/$",
	})
	if err := sm.UpdateStreamFromBuffer(streamName, streamName, nodeID, tenantID, "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}
}

func svcAuthCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}

func TestNotifyOriginPullRejectsTenantMismatch(t *testing.T) {
	server, _, _ := testFederationServerWithCache(t)
	setLiveStreamState(t, "tenantA+stream", "source-1", "tenant-a", "edge-a.example.com")

	ack, err := server.NotifyOriginPull(svcAuthCtx(), &foghornfederationpb.OriginPullNotification{
		StreamName:    "tenantA+stream",
		SourceNodeId:  "source-1",
		DestClusterId: "cluster-b",
		DestNodeId:    "dest-1",
		TenantId:      "tenant-b",
	})
	if err != nil {
		t.Fatalf("NotifyOriginPull error: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatalf("expected tenant mismatch to be rejected")
	}
}

// TestNotifyOriginPullFailsWhenRegistryUnavailable preserves the intent
// of the prior cache-availability check: if we can't durably track the
// outbound pull, reject the request rather than silently acking a handoff
// we can't observe. The handoff record moved from federation cache to
// the unified stream registry; the rejection now fires when the registry
// singleton is unset.
func TestNotifyOriginPullFailsWhenRegistryUnavailable(t *testing.T) {
	server, _, _ := testFederationServerWithCache(t)
	setLiveStreamState(t, "tenantA+stream", "source-1", "tenant-a", "edge-a.example.com")

	priorRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(nil)
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })

	ack, err := server.NotifyOriginPull(svcAuthCtx(), &foghornfederationpb.OriginPullNotification{
		StreamName:    "tenantA+stream",
		SourceNodeId:  "source-1",
		DestClusterId: "cluster-b",
		DestNodeId:    "dest-1",
		TenantId:      "tenant-a",
	})
	if err != nil {
		t.Fatalf("NotifyOriginPull error: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatalf("expected rejection when registry unavailable")
	}
}

func TestNotifyOriginPullKeepsBareMistNativeStreamName(t *testing.T) {
	server, _, _ := testFederationServerWithCache(t)
	setLiveStreamState(t, "frameworks-demo", "source-1", "tenant-a", "https://edge-a.example.com")

	ack, err := server.NotifyOriginPull(svcAuthCtx(), &foghornfederationpb.OriginPullNotification{
		StreamName:    "frameworks-demo",
		SourceNodeId:  "source-1",
		DestClusterId: "cluster-b",
		DestNodeId:    "dest-1",
		TenantId:      "tenant-a",
	})
	if err != nil {
		t.Fatalf("NotifyOriginPull error: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatalf("expected origin pull accepted, reason=%q", ack.GetReason())
	}
	// A managed Mist-native source keeps its bare runtime name; the accepted
	// pull's per-destination credential rides alongside it, never in the path.
	if got, want := control.SourcePullBaseURL(ack.GetDtscUrl()), "dtsc://edge-a.example.com:4200/frameworks-demo"; got != want {
		t.Fatalf("DTSC URL = %q, want %q", got, want)
	}
	if control.SourcePullCredential(ack.GetDtscUrl()) == "" {
		t.Fatalf("accepted pull carried no destination credential: %q", ack.GetDtscUrl())
	}
}
