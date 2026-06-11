package handlers

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"

	"google.golang.org/grpc"
)

func resetFanOutState(t *testing.T) {
	t.Helper()
	prev := fanOutShared
	fanOutShared = balancer.NewSharedFanOut(fanOutMemoTTL)
	t.Cleanup(func() { fanOutShared = prev })
}

// slowCountingFederationServer answers QueryStream after a delay, counting
// calls — the seam for proving singleflight dedup, the memo, and that the
// shared fan-out is detached from the triggering caller's cancellation.
type slowCountingFederationServer struct {
	foghornfederationpb.UnimplementedFoghornFederationServer
	calls atomic.Int32
	delay time.Duration
}

func (s *slowCountingFederationServer) QueryStream(_ context.Context, _ *foghornfederationpb.QueryStreamRequest) (*foghornfederationpb.QueryStreamResponse, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return &foghornfederationpb.QueryStreamResponse{
		Candidates: []*foghornfederationpb.EdgeCandidate{
			{NodeId: "edge-1", BaseUrl: "https://e1", BwAvailable: 1000, RamUsed: 1, RamMax: 2},
		},
	}, nil
}

func setupFanOutStack(t *testing.T, srv *slowCountingFederationServer) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	foghornfederationpb.RegisterFoghornFederationServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()

	pool := foghorn.NewPool(foghorn.PoolConfig{Logger: logging.NewLogger(), AllowInsecure: true, Timeout: 5 * time.Second})
	fedClient := federation.NewFederationClient(federation.FederationClientConfig{Pool: pool, Logger: logging.NewLogger(), Timeout: 5 * time.Second})

	prevFed, prevPeer, prevClusterID := federationClient, peerManager, clusterID
	federationClient = fedClient
	peerManager = fixedPeerResolverFedArms{addr: lis.Addr().String()}
	clusterID = "cluster-local"
	t.Cleanup(func() {
		federationClient = prevFed
		peerManager = prevPeer
		clusterID = prevClusterID
		_ = pool.Close()
		grpcSrv.Stop()
		_ = lis.Close()
	})
}

func TestQueryStreamFanOutShared_SingleFlightAndMemo(t *testing.T) {
	resetFanOutState(t)
	srv := &slowCountingFederationServer{delay: 100 * time.Millisecond}
	setupFanOutStack(t, srv)

	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-remote"}}

	// N concurrent cold resolves for the same stream → exactly one fan-out,
	// every caller gets the shared candidate set.
	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cands := queryStreamFanOutShared(context.Background(), "s1", "tenant-1", 0, 0, peers)
			results[i] = len(cands)
		}(i)
	}
	wg.Wait()
	if got := srv.calls.Load(); got != 1 {
		t.Fatalf("QueryStream calls = %d, want 1 (singleflight dedup)", got)
	}
	for i, n := range results {
		if n != 1 {
			t.Fatalf("caller %d got %d candidates, want shared result of 1", i, n)
		}
	}

	// Sequential call within the memo TTL → still no extra fan-out.
	if cands := queryStreamFanOutShared(context.Background(), "s1", "tenant-1", 0, 0, peers); len(cands) != 1 {
		t.Fatalf("memoized call got %d candidates", len(cands))
	}
	if got := srv.calls.Load(); got != 1 {
		t.Fatalf("QueryStream calls after memo hit = %d, want 1", got)
	}

	// A different stream is a different key → its own fan-out.
	_ = queryStreamFanOutShared(context.Background(), "s2", "tenant-1", 0, 0, peers)
	if got := srv.calls.Load(); got != 2 {
		t.Fatalf("QueryStream calls after second stream = %d, want 2", got)
	}
}

// The shared fan-out must be detached from the triggering viewer's
// cancellation: a first request abandoned mid-resolve would otherwise fail
// every peer RPC and memoize an empty candidate set for the whole window.
func TestQueryStreamFanOutShared_DetachedFromCallerCancellation(t *testing.T) {
	resetFanOutState(t)
	srv := &slowCountingFederationServer{delay: 50 * time.Millisecond}
	setupFanOutStack(t, srv)

	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-remote"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller already gone before the fan-out even starts

	if cands := queryStreamFanOutShared(ctx, "s1", "tenant-1", 0, 0, peers); len(cands) != 1 {
		t.Fatalf("canceled caller got %d candidates, want 1 (fan-out must be detached)", len(cands))
	}
	// And the memoized result for followers is the real candidate set.
	if cands := queryStreamFanOutShared(context.Background(), "s1", "tenant-1", 0, 0, peers); len(cands) != 1 {
		t.Fatalf("follower got %d candidates from memo, want 1", len(cands))
	}
	if got := srv.calls.Load(); got != 1 {
		t.Fatalf("QueryStream calls = %d, want 1", got)
	}
}

// The memo caches empty results too: a stream no peer has must not re-fan-out
// on every request during the memo window.
func TestQueryStreamFanOutShared_MemoizesEmptyResult(t *testing.T) {
	resetFanOutState(t)

	// No federationClient/peerManager → queryStreamFanOut returns nil
	// immediately; the wrapper must still memoize that emptiness.
	prevFed, prevPeer := federationClient, peerManager
	federationClient = nil
	peerManager = nil
	t.Cleanup(func() {
		federationClient = prevFed
		peerManager = prevPeer
	})

	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-remote"}}
	if cands := queryStreamFanOutShared(context.Background(), "s-empty", "tenant-1", 0, 0, peers); cands != nil {
		t.Fatalf("expected nil candidates, got %+v", cands)
	}
	if !fanOutShared.Memoized("tenant-1/s-empty") {
		t.Fatal("empty fan-out result was not memoized")
	}
}
