package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// streamedPeerDiscovery answers the periodic read and the subscription from the
// same fixture, so a test can prove which one applied a peer set.
type streamedPeerDiscovery struct {
	list    *quartermasterpb.ListPeersResponse
	updates chan *quartermasterpb.ListPeersResponse
	opened  chan string
	openErr error
}

func (d *streamedPeerDiscovery) ListPeers(context.Context, string) (*quartermasterpb.ListPeersResponse, error) {
	return d.list, nil
}

func (d *streamedPeerDiscovery) WatchPeers(ctx context.Context, clusterID string) (quartermasterpb.ClusterService_WatchPeersClient, error) {
	if d.openErr != nil {
		return nil, d.openErr
	}
	select {
	case d.opened <- clusterID:
	default:
	}
	return &fakePeerStream{ctx: ctx, updates: d.updates}, nil
}

type fakePeerStream struct {
	grpc.ClientStream
	ctx     context.Context
	updates chan *quartermasterpb.ListPeersResponse
}

func (s *fakePeerStream) Recv() (*quartermasterpb.ListPeersResponse, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case resp, ok := <-s.updates:
		if !ok {
			return nil, errors.New("peer stream closed")
		}
		return resp, nil
	}
}

func (s *fakePeerStream) Context() context.Context     { return s.ctx }
func (s *fakePeerStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakePeerStream) Trailer() metadata.MD         { return nil }
func (s *fakePeerStream) CloseSend() error             { return nil }
func (s *fakePeerStream) SendMsg(any) error            { return nil }
func (s *fakePeerStream) RecvMsg(any) error            { return nil }

func peerWatchManager(t *testing.T, discovery clusterPeerDiscovery) *PeerManager {
	t.Helper()
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "us", cache, true)
	pm.peerDiscovery = discovery
	return pm
}

func TestPeerSubscriptionAppliesChangesWithoutWaitingForReconciliation(t *testing.T) {
	discovery := &streamedPeerDiscovery{
		list:    &quartermasterpb.ListPeersResponse{},
		updates: make(chan *quartermasterpb.ListPeersResponse, 1),
		opened:  make(chan string, 1),
	}
	pm := peerWatchManager(t, discovery)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.watchPeers(ctx)

	select {
	case clusterID := <-discovery.opened:
		if clusterID != "us" {
			t.Fatalf("subscription named %q, want the leader's own cluster", clusterID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader never subscribed for peer changes")
	}

	discovery.updates <- &quartermasterpb.ListPeersResponse{Peers: []*quartermasterpb.PeerCluster{
		{ClusterId: "eu", FoghornAddr: "eu-foghorn:18019", ControlCellId: "eu-cell", SharedTenantIds: []string{"tenant"}},
	}}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pm.mu.RLock()
		hint, found := pm.quartermasterHints["eu"]
		pm.mu.RUnlock()
		if found {
			if hint.Addr != "eu-foghorn:18019" || hint.ControlCellID != "eu-cell" || !hint.AlwaysOn {
				t.Fatalf("streamed peer applied incorrectly: %+v", hint)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("streamed peer change never reached the leader's hints")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPeerSubscriptionStopsWhenLeadershipMoves(t *testing.T) {
	discovery := &streamedPeerDiscovery{
		list:    &quartermasterpb.ListPeersResponse{},
		updates: make(chan *quartermasterpb.ListPeersResponse, 2),
		opened:  make(chan string, 1),
	}
	pm := peerWatchManager(t, discovery)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { pm.watchPeers(ctx); close(done) }()
	<-discovery.opened

	// A replica that has lost the lease must stop republishing the cell's
	// authoritative contribution, even with a stream still open.
	pm.mu.Lock()
	pm.isLeader, pm.leaderReady = false, false
	pm.mu.Unlock()
	discovery.updates <- &quartermasterpb.ListPeersResponse{Peers: []*quartermasterpb.PeerCluster{
		{ClusterId: "eu", FoghornAddr: "eu-foghorn:18019", ControlCellId: "eu-cell"},
	}}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription kept running after leadership moved")
	}
	pm.mu.RLock()
	_, found := pm.quartermasterHints["eu"]
	pm.mu.RUnlock()
	if found {
		t.Fatal("a non-leader applied a streamed peer change")
	}
}

func TestPeerDiscoveryWithoutStreamingKeepsReconciliation(t *testing.T) {
	// A discovery source that cannot stream must not stall the leader: the
	// subscription returns immediately and the periodic path still applies.
	pm := peerWatchManager(t, &fakeClusterPeerDiscovery{resp: &quartermasterpb.ListPeersResponse{Peers: []*quartermasterpb.PeerCluster{
		{ClusterId: "eu", FoghornAddr: "eu-foghorn:18019", ControlCellId: "eu-cell"},
	}}})
	done := make(chan struct{})
	go func() { pm.watchPeers(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a non-streaming discovery source blocked the subscription goroutine")
	}
	pm.refreshPeers()
	pm.mu.RLock()
	_, found := pm.quartermasterHints["eu"]
	pm.mu.RUnlock()
	if !found {
		t.Fatal("periodic reconciliation stopped applying peers")
	}
}
