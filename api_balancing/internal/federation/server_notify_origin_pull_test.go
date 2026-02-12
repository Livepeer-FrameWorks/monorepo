package federation

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	return server, cache, mr
}

func setLiveStreamState(t *testing.T, streamName, nodeID, tenantID, baseURL string) {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	sm.SetNodeInfo(nodeID, baseURL, true, nil, nil, "", "", nil)
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

	ack, err := server.NotifyOriginPull(svcAuthCtx(), &pb.OriginPullNotification{
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

func TestNotifyOriginPullFailsWhenCacheUnavailable(t *testing.T) {
	server, _, mr := testFederationServerWithCache(t)
	setLiveStreamState(t, "tenantA+stream", "source-1", "tenant-a", "edge-a.example.com")
	mr.Close()

	ack, err := server.NotifyOriginPull(svcAuthCtx(), &pb.OriginPullNotification{
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
		t.Fatalf("expected cache failure to be rejected")
	}
}
