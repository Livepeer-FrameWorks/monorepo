package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
)

type stubCommodore struct {
	resp *pb.ResolveInternalNameResponse
	err  error

	mu    sync.Mutex
	calls int
}

func (s *stubCommodore) ResolveInternalName(_ context.Context, _ string) (*pb.ResolveInternalNameResponse, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.resp, s.err
}

func (s *stubCommodore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type stubFederation struct {
	respByCluster map[string]*pb.QueryStreamResponse
	errByCluster  map[string]error

	mu    sync.Mutex
	calls map[string]int
}

func (s *stubFederation) QueryStream(_ context.Context, clusterID, _ string, _ *pb.QueryStreamRequest) (*pb.QueryStreamResponse, error) {
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[clusterID]++
	s.mu.Unlock()
	if err, ok := s.errByCluster[clusterID]; ok && err != nil {
		return nil, err
	}
	return s.respByCluster[clusterID], nil
}

func (s *stubFederation) callCount(clusterID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[clusterID]
}

type stubPeerAddrs struct {
	addrs map[string]string
}

func (s stubPeerAddrs) GetPeerAddr(clusterID string) string { return s.addrs[clusterID] }
func (s stubPeerAddrs) GetPeerGeo(_ string) (float64, float64) {
	return 0, 0
}

func newAuthTestLogger() logging.Logger {
	l := logrus.New()
	l.SetLevel(logrus.FatalLevel)
	return logging.Logger(l)
}

func newAuthResolver(t *testing.T) *LivepeerAuthResolver {
	t.Helper()
	return &LivepeerAuthResolver{
		LocalCluster:  "local-cluster",
		PositiveCache: newAuthPositiveCache(15 * time.Second),
		PeerQueryWait: time.Second,
		Logger:        newAuthTestLogger(),
	}
}

func TestLivepeerAuth_LocalStateHitAuthorizesWithoutCommodore(t *testing.T) {
	commod := &stubCommodore{}
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return true }
	r.Commodore = commod

	ok, reason := r.Authorize(context.Background(), "manifest-1")
	if !ok {
		t.Fatalf("expected authorize=true, got reason=%q", reason)
	}
	if commod.callCount() != 0 {
		t.Fatalf("local hit must not call Commodore, got %d calls", commod.callCount())
	}
}

func TestLivepeerAuth_PositiveCacheHitSkipsCommodoreAndPeers(t *testing.T) {
	commod := &stubCommodore{}
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = commod
	r.PositiveCache.add("manifest-cached")

	ok, reason := r.Authorize(context.Background(), "manifest-cached")
	if !ok {
		t.Fatalf("expected cache-backed authorize=true, got reason=%q", reason)
	}
	if commod.callCount() != 0 {
		t.Fatalf("cache hit must not call Commodore, got %d calls", commod.callCount())
	}
}

func TestLivepeerAuth_CommodoreNotFoundReturnsStreamNotFound(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	// Empty TenantId means "Commodore doesn't recognise this manifest".
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{TenantId: ""}}

	ok, reason := r.Authorize(context.Background(), "ghost-manifest")
	if ok {
		t.Fatal("expected reject for unknown manifest")
	}
	if reason != authRejectStreamNotFound {
		t.Fatalf("expected reason=%q, got %q", authRejectStreamNotFound, reason)
	}
}

func TestLivepeerAuth_CommodoreErrorReturnsCommodoreUnreachable(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = &stubCommodore{err: errors.New("rpc closed")}

	ok, reason := r.Authorize(context.Background(), "manifest-x")
	if ok {
		t.Fatal("expected reject when Commodore is unreachable")
	}
	if reason != authRejectCommodoreUnreachable {
		t.Fatalf("expected reason=%q, got %q", authRejectCommodoreUnreachable, reason)
	}
}

func TestLivepeerAuth_NoClusterPeersReturnsPeerContextMissing(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId:     "tenant-a",
		ClusterPeers: nil,
	}}

	ok, reason := r.Authorize(context.Background(), "manifest-y")
	if ok {
		t.Fatal("expected reject when Commodore returns no peers")
	}
	if reason != authRejectPeerContextMissing {
		t.Fatalf("expected reason=%q, got %q", authRejectPeerContextMissing, reason)
	}
}

func TestLivepeerAuth_PeerConfirmsLiveAuthorizesAndCaches(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId: "tenant-a",
		ClusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "peer-cluster"},
		},
	}}
	r.Federation = &stubFederation{respByCluster: map[string]*pb.QueryStreamResponse{
		"peer-cluster": {Candidates: []*pb.EdgeCandidate{{NodeId: "edge-1"}}},
	}}
	r.PeerAddrs = stubPeerAddrs{addrs: map[string]string{"peer-cluster": "peer-cluster.internal:18011"}}

	ok, reason := r.Authorize(context.Background(), "manifest-live")
	if !ok {
		t.Fatalf("expected authorize via peer, got reason=%q", reason)
	}
	if !r.PositiveCache.has("manifest-live") {
		t.Fatal("expected positive cache to be populated after peer confirmation")
	}

	// Re-authorize: must hit cache, not call Commodore again.
	commodCalls := r.Commodore.(*stubCommodore).callCount()
	ok2, _ := r.Authorize(context.Background(), "manifest-live")
	if !ok2 {
		t.Fatal("expected second authorize to hit cache")
	}
	if r.Commodore.(*stubCommodore).callCount() != commodCalls {
		t.Fatalf("expected zero additional Commodore calls on cache hit, got %d more", r.Commodore.(*stubCommodore).callCount()-commodCalls)
	}
}

func TestLivepeerAuth_PeerKnownButNotLiveReturnsStreamNotLive(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId: "tenant-a",
		ClusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "peer-cluster"},
		},
	}}
	// Peer reachable but reports zero candidates → not live anywhere.
	r.Federation = &stubFederation{respByCluster: map[string]*pb.QueryStreamResponse{
		"peer-cluster": {Candidates: nil},
	}}
	r.PeerAddrs = stubPeerAddrs{addrs: map[string]string{"peer-cluster": "peer-cluster.internal:18011"}}

	ok, reason := r.Authorize(context.Background(), "manifest-dead")
	if ok {
		t.Fatal("expected reject when no peer reports the stream live")
	}
	if reason != authRejectStreamNotLive {
		t.Fatalf("expected reason=%q, got %q", authRejectStreamNotLive, reason)
	}
}

func TestLivepeerAuth_AllPeerQueriesErrorReturnsPeerUnreachable(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId: "tenant-a",
		ClusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "peer-cluster-a"},
			{ClusterId: "peer-cluster-b"},
		},
	}}
	// Peers reachable (addrs known) but QueryStream fails for both. No peer
	// actually voted "not live", so the answer must be peer_unreachable, not
	// stream_not_live.
	r.Federation = &stubFederation{errByCluster: map[string]error{
		"peer-cluster-a": errors.New("rpc connection refused"),
		"peer-cluster-b": errors.New("rpc deadline exceeded"),
	}}
	r.PeerAddrs = stubPeerAddrs{addrs: map[string]string{
		"peer-cluster-a": "peer-a.internal:18011",
		"peer-cluster-b": "peer-b.internal:18011",
	}}

	ok, reason := r.Authorize(context.Background(), "manifest-flapping")
	if ok {
		t.Fatal("expected reject when every peer QueryStream errors")
	}
	if reason != authRejectPeerUnreachable {
		t.Fatalf("expected reason=%q, got %q", authRejectPeerUnreachable, reason)
	}
}

func TestLivepeerAuth_PeerListedButUnreachableReturnsPeerUnreachable(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId: "tenant-a",
		ClusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "peer-cluster"},
		},
	}}
	r.Federation = &stubFederation{}
	// Peer present in Commodore response but PeerManager has no addr → unreachable.
	r.PeerAddrs = stubPeerAddrs{addrs: map[string]string{}}

	ok, reason := r.Authorize(context.Background(), "manifest-isolated")
	if ok {
		t.Fatal("expected reject when no peer is reachable")
	}
	if reason != authRejectPeerUnreachable {
		t.Fatalf("expected reason=%q, got %q", authRejectPeerUnreachable, reason)
	}
}

func TestLivepeerAuth_LocalClusterPeerIsSkipped(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) bool { return false }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId: "tenant-a",
		ClusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: r.LocalCluster}, // local cluster — must be skipped from fan-out
			{ClusterId: "peer-cluster"},
		},
	}}
	fed := &stubFederation{respByCluster: map[string]*pb.QueryStreamResponse{
		"peer-cluster": {Candidates: []*pb.EdgeCandidate{{NodeId: "edge-1"}}},
	}}
	r.Federation = fed
	r.PeerAddrs = stubPeerAddrs{addrs: map[string]string{
		r.LocalCluster: "should-not-be-called",
		"peer-cluster": "peer-cluster.internal:18011",
	}}

	ok, _ := r.Authorize(context.Background(), "manifest-z")
	if !ok {
		t.Fatal("expected authorize via remote peer")
	}
	if fed.callCount(r.LocalCluster) != 0 {
		t.Fatalf("must not query the local cluster as a peer, got %d calls", fed.callCount(r.LocalCluster))
	}
	if fed.callCount("peer-cluster") != 1 {
		t.Fatalf("expected exactly one peer-cluster query, got %d", fed.callCount("peer-cluster"))
	}
}

func TestExtractManifestID(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"http://gw:8935/live/abc123/0.ts", "abc123"},
		{"http://gw:8935/live/abc123/segment-12.ts", "abc123"},
		{"/live/foo/bar.ts", "foo"},
		{"http://gw:8935/notlive/abc/0.ts", ""},
		{"", ""},
		{"://broken", ""},
	}
	for _, tc := range cases {
		got := extractManifestID(tc.raw)
		if got != tc.want {
			t.Errorf("extractManifestID(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
