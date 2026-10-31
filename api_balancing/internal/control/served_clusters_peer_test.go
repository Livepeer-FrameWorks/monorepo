package control

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

type peerAssignmentRegistry struct {
	quartermasterpb.UnimplementedServiceRegistryServiceServer
	calls           atomic.Int64
	privateAssigned atomic.Bool
}

func (r *peerAssignmentRegistry) ListServiceClusterAssignments(context.Context, *quartermasterpb.ListServiceClusterAssignmentsRequest) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error) {
	r.calls.Add(1)
	clusters := []string{"cell-a"}
	if r.privateAssigned.Load() {
		clusters = append(clusters, "private-a")
	}
	return &quartermasterpb.ListServiceClusterAssignmentsResponse{ClusterIds: clusters}, nil
}

// The node holder learns an authenticated identity before its sibling's periodic
// assignment refresh. Node replication must wake that refresh, not grant the
// node's claimed cluster directly or fetch assignments inside viewer requests.
func TestServedClustersRefreshFromPeerNodeIdentity(t *testing.T) {
	resetServedClusters(t)
	setInstanceID(t, "sibling-a")
	prevLocal := localClusterID
	localClusterID = "cell-a"
	t.Cleanup(func() { localClusterID = prevLocal })

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	registry := &peerAssignmentRegistry{}
	server := grpc.NewServer()
	quartermasterpb.RegisterServiceRegistryServiceServer(server, registry)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr: listener.Addr().String(), AllowInsecure: true,
		Logger: logging.NewLogger(), ServiceToken: "assignment-test", Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prevClient := servedClustersClient.Swap(client)
	t.Cleanup(func() { servedClustersClient.Store(prevClient) })

	redis := miniredis.RunT(t)
	holder := state.NewStreamStateManager()
	sibling := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	for id, manager := range map[string]*state.StreamStateManager{"holder-a": holder, "sibling-a": sibling} {
		redisClient := goredis.NewClient(&goredis.Options{Addr: redis.Addr()})
		t.Cleanup(func() { _ = redisClient.Close() })
		if err := manager.EnableRedisSync(context.Background(), state.NewRedisStateStore(redisClient, "cell-a"), id, logging.NewLogger()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(manager.Shutdown)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		StartServedClustersRefresh(ctx, time.Hour, logging.NewLogger())
	}()
	t.Cleanup(func() { cancel(); <-done })

	waitFor := func(message string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ready() {
			if time.Now().After(deadline) {
				t.Fatal(message)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("assignment refresh worker did not initialize", func() bool { return isServedCluster("cell-a") })
	if isServedCluster("private-a") {
		t.Fatal("private cluster was assigned before Quartermaster created it")
	}
	registry.privateAssigned.Store(true)
	holder.SetNodeConnectionInfo(context.Background(), "private-node", "", "tenant-a", "private-a", nil)
	waitFor("sibling received authenticated node identity but never refreshed its private-cluster assignment", func() bool {
		return isServedCluster("private-a")
	})
	if node := sibling.GetNodeState("private-node"); node == nil || node.ClusterID != "private-a" {
		t.Fatalf("private identity did not arrive through the cell changelog: %+v", node)
	}

	beforeForeign := registry.calls.Load()
	holder.SetNodeConnectionInfo(context.Background(), "foreign-node", "", "tenant-b", "foreign-b", nil)
	waitFor("foreign identity did not request an authoritative assignment check", func() bool {
		return registry.calls.Load() > beforeForeign
	})
	for range 20 {
		holder.TouchNode("foreign-node", true)
		time.Sleep(10 * time.Millisecond)
	}
	if got := registry.calls.Load() - beforeForeign; got != 1 {
		t.Fatalf("unknown cluster heartbeats bypassed refresh throttling: calls=%d want=1", got)
	}
	cancel()
	<-done
	if isServedCluster("foreign-b") {
		t.Fatal("node identity conferred an assignment Quartermaster never granted")
	}
	beforeViewer := registry.calls.Load()
	deps := &PlaybackDependencies{LocalClusterID: "cell-a"}
	if isRemoteOriginCluster("private-a", deps) {
		t.Fatal("private DVR origin is still misclassified as another cell")
	}
	if !isRemoteOriginCluster("foreign-b", deps) {
		t.Fatal("another cell's origin became local")
	}
	if got := registry.calls.Load(); got != beforeViewer {
		t.Fatalf("viewer classification called Quartermaster: %d new calls", got-beforeViewer)
	}
}

func TestServedClusterRefreshHintCooldown(t *testing.T) {
	hints := servedClusterRefreshHints{last: make(map[string]time.Time)}
	requests := make(chan struct{}, 1)
	now := time.Now()
	hints.request("unknown", now, requests)
	<-requests
	hints.request("unknown", now.Add(time.Second), requests)
	if len(requests) != 0 {
		t.Fatal("repeat hint bypassed cooldown")
	}
	hints.request("other", now.Add(time.Second), requests)
	if len(requests) != 1 {
		t.Fatal("unrelated cluster lost first discovery hint")
	}
	hints.request("queue-full", now.Add(time.Second), requests)
	<-requests
	hints.request("queue-full", now.Add(2*time.Second), requests)
	if len(requests) != 1 {
		t.Fatal("a dropped hint consumed its cooldown")
	}
	<-requests
	hints.request("unknown", now.Add(2*servedClusterHintMinInterval), requests)
	if len(requests) != 1 {
		t.Fatal("failed discovery cannot retry after cooldown")
	}
	if len(hints.last) != 1 {
		t.Fatalf("expired hint entries retained: %+v", hints.last)
	}
}
