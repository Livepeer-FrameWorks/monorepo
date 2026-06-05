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
	goredis "github.com/redis/go-redis/v9"
)

func testFederationServerWithCache(t *testing.T) (*FederationServer, *RemoteEdgeCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cache := NewRemoteEdgeCache(client, "cluster-a", logging.NewLogger())
	server := NewFederationServer(FederationServerConfig{
		Logger:    logging.NewLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})
	// Install a per-test stream registry so NotifyOriginPull can record
	// outbound pulls. Tests that want to exercise the registry-unavailable
	// path clear it explicitly via control.SetStreamRegistry(nil).
	prior := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() {
		control.SetStreamRegistry(prior)
		_ = client.Close()
		mr.Close()
	})
	return server, cache, mr
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
	if got, want := ack.GetDtscUrl(), "dtsc://edge-a.example.com:4200/frameworks-demo"; got != want {
		t.Fatalf("DTSC URL = %q, want %q", got, want)
	}
}
