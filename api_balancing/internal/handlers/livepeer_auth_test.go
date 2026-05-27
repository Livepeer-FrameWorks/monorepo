package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

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

// localCtx returns a tenant/stream context as if local state had a record.
func localCtx(manifestID string) *LivepeerAuthContext {
	return &LivepeerAuthContext{
		TenantID:     "tenant-local",
		StreamID:     "stream-local",
		InternalName: manifestID,
	}
}

func TestLivepeerAuth_LocalStateHitAuthorizesWithoutCommodore(t *testing.T) {
	commod := &stubCommodore{}
	r := newAuthResolver(t)
	r.StreamLookup = func(m string) *LivepeerAuthContext { return localCtx(m) }
	r.Commodore = commod

	authCtx, reason := r.Authorize(context.Background(), "manifest-1")
	if authCtx == nil {
		t.Fatalf("expected authorize success, got reason=%q", reason)
	}
	if authCtx.TenantID != "tenant-local" {
		t.Fatalf("expected local tenant context, got %+v", authCtx)
	}
	if commod.callCount() != 0 {
		t.Fatalf("local hit must not call Commodore, got %d calls", commod.callCount())
	}
}

func TestLivepeerAuth_PositiveCacheHitSkipsCommodoreAndPeers(t *testing.T) {
	commod := &stubCommodore{}
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	r.Commodore = commod
	cached := &LivepeerAuthContext{TenantID: "tenant-cached", StreamID: "stream-cached", InternalName: "manifest-cached"}
	r.PositiveCache.add("manifest-cached", cached)

	authCtx, reason := r.Authorize(context.Background(), "manifest-cached")
	if authCtx == nil {
		t.Fatalf("expected cache-backed authorize success, got reason=%q", reason)
	}
	if authCtx != cached {
		t.Fatalf("expected cached context to flow through, got %+v", authCtx)
	}
	if commod.callCount() != 0 {
		t.Fatalf("cache hit must not call Commodore, got %d calls", commod.callCount())
	}
}

func TestLivepeerAuth_ProcessingSessionManifestAuthorizesFromBaseJob(t *testing.T) {
	commod := &stubCommodore{}
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	r.Commodore = commod
	r.ProcessingJob = func(_ context.Context, manifestID string, _ livepeerAuthRequest) *LivepeerAuthContext {
		if manifestID != "processing+artifact123" {
			return nil
		}
		return &LivepeerAuthContext{
			TenantID:     "tenant-processing",
			StreamID:     "stream-processing",
			InternalName: manifestID,
		}
	}

	authCtx, reason := r.Authorize(context.Background(), "processing+artifact123-4VrbXAvV")
	if authCtx == nil {
		t.Fatalf("expected processing authorize success, got reason=%q", reason)
	}
	if authCtx.TenantID != "tenant-processing" || authCtx.StreamID != "stream-processing" || authCtx.InternalName != "processing+artifact123" {
		t.Fatalf("unexpected auth context: %+v", authCtx)
	}
	if commod.callCount() != 0 {
		t.Fatalf("processing job hit must not call Commodore, got %d calls", commod.callCount())
	}
	if cached := r.PositiveCache.get("processing+artifact123-4VrbXAvV"); cached == nil {
		t.Fatal("expected suffixed processing manifest to be cached")
	}
	if cached := r.PositiveCache.get("processing+artifact123"); cached == nil {
		t.Fatal("expected base processing manifest to be cached")
	}
}

func TestLivepeerProfilesFromProcessesJSONNormalizesMistLivepeerProfiles(t *testing.T) {
	processesJSON := `[{"process":"AV","codec":"AAC"},{"process":"Livepeer","hardcoded_broadcasters":"[{\"address\":\"https://livepeer.example\"}]","target_profiles":[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"},{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"}]}]`

	got := mist.LivepeerProfilesFromProcessesJSON(processesJSON, mist.SourceMediaInfo{
		Width:  2718,
		Height: 1750,
		FPS:    24,
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 profiles, got %d: %#v", len(got), got)
	}
	want := []livepeerJSONProfile{
		{
			"name":    "360p",
			"bitrate": float64(900000),
			"fps":     24000,
			"fpsDen":  1000,
			"height":  352,
			"width":   544,
			"profile": "H264ConstrainedHigh",
			"gop":     "0.0",
		},
		{
			"name":    "480p",
			"bitrate": float64(1600000),
			"fps":     24000,
			"fpsDen":  1000,
			"height":  480,
			"width":   736,
			"profile": "H264ConstrainedHigh",
			"gop":     "0.0",
		},
	}
	assertJSONEqual(t, want, got)
	if _, ok := got[0]["track_inhibit"]; ok {
		t.Fatal("expected non-matching track_inhibit to be removed before auth response")
	}
}

func TestLivepeerProfilesFromProcessesJSONDropsInhibitedProfiles(t *testing.T) {
	processesJSON := `[{"process":"Livepeer","target_profiles":[{"name":"1080p","bitrate":6500000,"fps":0,"height":1080,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1920x1080"}]}]`

	got := mist.LivepeerProfilesFromProcessesJSON(processesJSON, mist.SourceMediaInfo{
		Width:  640,
		Height: 360,
		FPS:    30,
	})
	if len(got) != 0 {
		t.Fatalf("expected inhibited profile to be dropped, got %#v", got)
	}
}

func TestLivepeerValidatedProfilesAcceptsMistComputedHeader(t *testing.T) {
	processesJSON := `[{"process":"Livepeer","target_profiles":[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"},{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"}]}]`
	requested := []livepeerJSONProfile{
		{
			"name":    "360p",
			"bitrate": float64(900000),
			"fps":     float64(24000),
			"fpsDen":  float64(1000),
			"height":  float64(352),
			"width":   float64(544),
			"profile": "H264ConstrainedHigh",
			"gop":     "0.0",
		},
		{
			"name":    "480p",
			"bitrate": float64(1600000),
			"fps":     float64(24000),
			"fpsDen":  float64(1000),
			"height":  float64(480),
			"width":   float64(736),
			"profile": "H264ConstrainedHigh",
			"gop":     "0.0",
		},
	}

	got := livepeerValidatedProfiles(processesJSON, livepeerAuthRequest{
		Profiles:          requested,
		ContentResolution: "2718x1750",
	}, mist.SourceMediaInfo{})

	assertJSONEqual(t, requested, got)
}

func TestLivepeerValidatedProfilesAcceptsMistComputedHeaderAsAuthority(t *testing.T) {
	processesJSON := `[{"process":"Livepeer","target_profiles":[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh"}]}]`
	requested := []livepeerJSONProfile{
		{
			"name":    "360p",
			"bitrate": float64(1),
			"fps":     float64(24000),
			"fpsDen":  float64(1000),
			"height":  float64(352),
			"width":   float64(544),
			"profile": "H264ConstrainedHigh",
			"gop":     "0.0",
		},
	}

	got := livepeerValidatedProfiles(processesJSON, livepeerAuthRequest{
		Profiles:          requested,
		ContentResolution: "2718x1750",
	}, mist.SourceMediaInfo{})

	assertJSONEqual(t, requested, got)
}

func TestLivepeerValidatedProfilesRejectsMissingProfilesWithoutSourceMetadata(t *testing.T) {
	processesJSON := `[{"process":"Livepeer","target_profiles":[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh"}]}]`

	got := livepeerValidatedProfiles(processesJSON, livepeerAuthRequest{}, mist.SourceMediaInfo{})
	if got != nil {
		t.Fatalf("expected missing request profiles without source metadata to be rejected, got %#v", got)
	}
}

func assertJSONEqual(t *testing.T, want, got interface{}) {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("json mismatch\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
}

func TestLivepeerAuth_ProcessingSessionManifestFallsThroughWhenJobMissing(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	r.ProcessingJob = func(context.Context, string, livepeerAuthRequest) *LivepeerAuthContext { return nil }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{TenantId: ""}}

	authCtx, reason := r.Authorize(context.Background(), "processing+artifact123-4VrbXAvV")
	if authCtx != nil {
		t.Fatal("expected reject for unknown processing manifest")
	}
	if reason != authRejectStreamNotFound {
		t.Fatalf("expected reason=%q, got %q", authRejectStreamNotFound, reason)
	}
}

func TestLivepeerAuth_CommodoreNotFoundReturnsStreamNotFound(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	// Empty TenantId means "Commodore doesn't recognise this manifest".
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{TenantId: ""}}

	authCtx, reason := r.Authorize(context.Background(), "ghost-manifest")
	if authCtx != nil {
		t.Fatal("expected reject for unknown manifest")
	}
	if reason != authRejectStreamNotFound {
		t.Fatalf("expected reason=%q, got %q", authRejectStreamNotFound, reason)
	}
}

func TestLivepeerAuth_CommodoreErrorReturnsCommodoreUnreachable(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	r.Commodore = &stubCommodore{err: errors.New("rpc closed")}

	authCtx, reason := r.Authorize(context.Background(), "manifest-x")
	if authCtx != nil {
		t.Fatal("expected reject when Commodore is unreachable")
	}
	if reason != authRejectCommodoreUnreachable {
		t.Fatalf("expected reason=%q, got %q", authRejectCommodoreUnreachable, reason)
	}
}

func TestLivepeerAuth_NoClusterPeersReturnsPeerContextMissing(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId:     "tenant-a",
		ClusterPeers: nil,
	}}

	authCtx, reason := r.Authorize(context.Background(), "manifest-y")
	if authCtx != nil {
		t.Fatal("expected reject when Commodore returns no peers")
	}
	if reason != authRejectPeerContextMissing {
		t.Fatalf("expected reason=%q, got %q", authRejectPeerContextMissing, reason)
	}
}

func TestLivepeerAuth_PeerConfirmsLiveAuthorizesAndCaches(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId: "tenant-a",
		StreamId: "stream-a",
		ClusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "peer-cluster"},
		},
	}}
	r.Federation = &stubFederation{respByCluster: map[string]*pb.QueryStreamResponse{
		"peer-cluster": {Candidates: []*pb.EdgeCandidate{{NodeId: "edge-1"}}},
	}}
	r.PeerAddrs = stubPeerAddrs{addrs: map[string]string{"peer-cluster": "peer-cluster.internal:18011"}}

	authCtx, reason := r.Authorize(context.Background(), "manifest-live")
	if authCtx == nil {
		t.Fatalf("expected authorize via peer, got reason=%q", reason)
	}
	if authCtx.TenantID != "tenant-a" || authCtx.StreamID != "stream-a" || authCtx.InternalName != "manifest-live" {
		t.Fatalf("unexpected auth context: %+v", authCtx)
	}
	cached := r.PositiveCache.get("manifest-live")
	if cached == nil {
		t.Fatal("expected positive cache to be populated after peer confirmation")
	}
	if cached.TenantID != "tenant-a" || cached.StreamID != "stream-a" {
		t.Fatalf("expected cached context to carry tenant/stream, got %+v", cached)
	}

	// Re-authorize: must hit cache, not call Commodore again.
	commodCalls := r.Commodore.(*stubCommodore).callCount()
	authCtx2, _ := r.Authorize(context.Background(), "manifest-live")
	if authCtx2 == nil {
		t.Fatal("expected second authorize to hit cache")
	}
	if r.Commodore.(*stubCommodore).callCount() != commodCalls {
		t.Fatalf("expected zero additional Commodore calls on cache hit, got %d more", r.Commodore.(*stubCommodore).callCount()-commodCalls)
	}
}

func TestLivepeerAuth_PeerKnownButNotLiveReturnsStreamNotLive(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
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

	authCtx, reason := r.Authorize(context.Background(), "manifest-dead")
	if authCtx != nil {
		t.Fatal("expected reject when no peer reports the stream live")
	}
	if reason != authRejectStreamNotLive {
		t.Fatalf("expected reason=%q, got %q", authRejectStreamNotLive, reason)
	}
}

func TestLivepeerAuth_AllPeerQueriesErrorReturnsPeerUnreachable(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
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

	authCtx, reason := r.Authorize(context.Background(), "manifest-flapping")
	if authCtx != nil {
		t.Fatal("expected reject when every peer QueryStream errors")
	}
	if reason != authRejectPeerUnreachable {
		t.Fatalf("expected reason=%q, got %q", authRejectPeerUnreachable, reason)
	}
}

func TestLivepeerAuth_PeerListedButUnreachableReturnsPeerUnreachable(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
	r.Commodore = &stubCommodore{resp: &pb.ResolveInternalNameResponse{
		TenantId: "tenant-a",
		ClusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "peer-cluster"},
		},
	}}
	r.Federation = &stubFederation{}
	// Peer present in Commodore response but PeerManager has no addr → unreachable.
	r.PeerAddrs = stubPeerAddrs{addrs: map[string]string{}}

	authCtx, reason := r.Authorize(context.Background(), "manifest-isolated")
	if authCtx != nil {
		t.Fatal("expected reject when no peer is reachable")
	}
	if reason != authRejectPeerUnreachable {
		t.Fatalf("expected reason=%q, got %q", authRejectPeerUnreachable, reason)
	}
}

func TestLivepeerAuth_LocalClusterPeerIsSkipped(t *testing.T) {
	r := newAuthResolver(t)
	r.StreamLookup = func(string) *LivepeerAuthContext { return nil }
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

	authCtx, _ := r.Authorize(context.Background(), "manifest-z")
	if authCtx == nil {
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
