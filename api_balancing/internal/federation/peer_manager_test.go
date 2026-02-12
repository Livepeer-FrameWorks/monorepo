package federation

import (
	"context"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	pb "frameworks/pkg/proto"
)

func TestRecordAndAverage_SingleSample(t *testing.T) {
	pm := &PeerManager{
		metricHistory: make(map[string][]metricSample),
	}

	bw, cpu := pm.recordAndAverage("node-1", 1000, 50.0)
	if bw != 1000 {
		t.Errorf("bw = %d, want 1000", bw)
	}
	if cpu != 50.0 {
		t.Errorf("cpu = %f, want 50.0", cpu)
	}
}

func TestRecordAndAverage_MultipleSamples(t *testing.T) {
	pm := &PeerManager{
		metricHistory: make(map[string][]metricSample),
	}

	pm.recordAndAverage("node-1", 1000, 40.0)
	pm.recordAndAverage("node-1", 2000, 60.0)
	bw, cpu := pm.recordAndAverage("node-1", 3000, 80.0)

	// Average of 1000, 2000, 3000 = 2000
	if bw != 2000 {
		t.Errorf("bw = %d, want 2000", bw)
	}
	// Average of 40, 60, 80 = 60
	if cpu != 60.0 {
		t.Errorf("cpu = %f, want 60.0", cpu)
	}
}

func TestRecordAndAverage_ExpiredSamplesPruned(t *testing.T) {
	pm := &PeerManager{
		metricHistory: make(map[string][]metricSample),
	}

	// Manually inject an old sample beyond the 30s window
	pm.metricHistory["node-1"] = []metricSample{
		{bwAvailable: 100, cpuPercent: 10.0, ts: time.Now().Add(-40 * time.Second)},
	}

	// New sample should prune the old one
	bw, cpu := pm.recordAndAverage("node-1", 500, 50.0)
	if bw != 500 {
		t.Errorf("bw = %d, want 500 (old sample should be pruned)", bw)
	}
	if cpu != 50.0 {
		t.Errorf("cpu = %f, want 50.0", cpu)
	}
	if len(pm.metricHistory["node-1"]) != 1 {
		t.Errorf("history len = %d, want 1", len(pm.metricHistory["node-1"]))
	}
}

func TestRecordAndAverage_SeparateNodes(t *testing.T) {
	pm := &PeerManager{
		metricHistory: make(map[string][]metricSample),
	}

	pm.recordAndAverage("node-1", 1000, 20.0)
	pm.recordAndAverage("node-2", 5000, 80.0)

	bw1, cpu1 := pm.recordAndAverage("node-1", 3000, 40.0)
	bw2, cpu2 := pm.recordAndAverage("node-2", 5000, 80.0)

	if bw1 != 2000 { // avg(1000, 3000)
		t.Errorf("node-1 bw = %d, want 2000", bw1)
	}
	if cpu1 != 30.0 { // avg(20, 40)
		t.Errorf("node-1 cpu = %f, want 30.0", cpu1)
	}
	if bw2 != 5000 { // avg(5000, 5000)
		t.Errorf("node-2 bw = %d, want 5000", bw2)
	}
	if cpu2 != 80.0 {
		t.Errorf("node-2 cpu = %f, want 80.0", cpu2)
	}
}

// newTestPeerManager creates a PeerManager suitable for unit tests.
// It does not start the background run() goroutine.
func newTestPeerManager(t *testing.T, clusterID string, cache *RemoteEdgeCache, isLeader bool) *PeerManager {
	t.Helper()
	pm := &PeerManager{
		clusterID:     clusterID,
		instanceID:    "test-instance",
		cache:         cache,
		logger:        testLogger(),
		peers:         make(map[string]*peerState),
		streamPeers:   make(map[string]map[string]bool),
		metricHistory: make(map[string][]metricSample),
		done:          make(chan struct{}),
		isLeader:      isLeader,
	}
	t.Cleanup(func() {
		select {
		case <-pm.done:
		default:
			close(pm.done)
		}
	})
	return pm
}

func TestNotifyPeers_NonLeaderRegistersAddressOnly(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, false)

	pm.NotifyPeers([]*pb.TenantClusterPeer{
		{
			ClusterId:   "remote-cluster",
			ClusterSlug: "remote",
			BaseUrl:     "example.com",
			Role:        "official",
		},
	})

	addr := pm.GetPeerAddr("remote-cluster")
	if addr == "" {
		t.Fatal("expected non-leader to have peer address registered")
	}
	if addr != "foghorn.remote.example.com:18019" {
		t.Fatalf("unexpected address: %s", addr)
	}

	// Non-leader should NOT have opened a connection
	if pm.IsPeerConnected("remote-cluster") {
		t.Fatal("expected non-leader peer to NOT be connected")
	}
}

func TestNotifyPeers_SkipsSelfAndDuplicate(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)

	peers := []*pb.TenantClusterPeer{
		{ClusterId: "local-cluster", ClusterSlug: "local", BaseUrl: "example.com"},
		{ClusterId: "", ClusterSlug: "empty", BaseUrl: "example.com"},
		{ClusterId: "remote-cluster", ClusterSlug: "remote", BaseUrl: "example.com", Role: "preferred"},
	}

	pm.NotifyPeers(peers)

	if pm.GetPeerAddr("local-cluster") != "" {
		t.Fatal("should not register self as peer")
	}
	if pm.GetPeerAddr("remote-cluster") == "" {
		t.Fatal("expected remote-cluster to be registered")
	}

	// Call again — should not duplicate
	pm.NotifyPeers(peers)
	got := pm.GetPeers()
	if len(got) != 1 {
		t.Fatalf("expected 1 peer, got %d: %v", len(got), got)
	}
}

func TestNotifyPeers_LeaderSyncsToRedis(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, true)

	// Close done so any connectPeer goroutines exit immediately
	// (we have no real FoghornPool in tests)
	close(pm.done)

	pm.NotifyPeers([]*pb.TenantClusterPeer{
		{ClusterId: "remote-1", ClusterSlug: "r1", BaseUrl: "example.com", Role: "official"},
	})

	// Verify addresses were synced to Redis
	ctx := context.Background()
	addrs, err := cache.GetPeerAddresses(ctx)
	if err != nil {
		t.Fatalf("GetPeerAddresses: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address in Redis, got %d", len(addrs))
	}
	if addrs["remote-1"] != "foghorn.r1.example.com:18019" {
		t.Fatalf("unexpected Redis address: %s", addrs["remote-1"])
	}
}

func TestLoadPeerAddressesFromRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	cache := NewRemoteEdgeCache(client, "local-cluster", testLogger())

	// Pre-populate Redis with addresses (as if written by leader)
	ctx := context.Background()
	cache.SetPeerAddresses(ctx, map[string]string{
		"remote-1": "foghorn.r1.example.com:18019",
		"remote-2": "foghorn.r2.example.com:18019",
	})

	pm := newTestPeerManager(t, "local-cluster", cache, false)

	pm.loadPeerAddressesFromRedis()

	if addr := pm.GetPeerAddr("remote-1"); addr != "foghorn.r1.example.com:18019" {
		t.Fatalf("expected remote-1 addr, got %q", addr)
	}
	if addr := pm.GetPeerAddr("remote-2"); addr != "foghorn.r2.example.com:18019" {
		t.Fatalf("expected remote-2 addr, got %q", addr)
	}
}

func TestLoadPeerAddressesFromRedis_UpdatesExistingAddress(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	cache := NewRemoteEdgeCache(client, "local-cluster", testLogger())

	pm := newTestPeerManager(t, "local-cluster", cache, false)

	// Seed a peer with old address
	pm.mu.Lock()
	pm.peers["remote-1"] = &peerState{addr: "old-addr:18019", lastRefresh: time.Now()}
	pm.mu.Unlock()

	// Leader wrote updated address to Redis
	ctx := context.Background()
	cache.SetPeerAddresses(ctx, map[string]string{
		"remote-1": "new-addr:18019",
	})

	pm.loadPeerAddressesFromRedis()

	if addr := pm.GetPeerAddr("remote-1"); addr != "new-addr:18019" {
		t.Fatalf("expected updated address, got %q", addr)
	}
}

func TestLoadPeerAddressesFromRedis_SkipsSelf(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	cache := NewRemoteEdgeCache(client, "local-cluster", testLogger())

	ctx := context.Background()
	cache.SetPeerAddresses(ctx, map[string]string{
		"local-cluster": "should-be-skipped:18019",
		"remote-1":      "foghorn.r1.example.com:18019",
	})

	pm := newTestPeerManager(t, "local-cluster", cache, false)
	pm.loadPeerAddressesFromRedis()

	if pm.GetPeerAddr("local-cluster") != "" {
		t.Fatal("should not load self as peer")
	}
	if pm.GetPeerAddr("remote-1") == "" {
		t.Fatal("expected remote-1 to be loaded")
	}
}

func TestLoadPeerAddressesFromRedis_RemovesStaleRedisPeers(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, false)

	pm.mu.Lock()
	pm.peers["redis-peer"] = &peerState{addr: "old:18019", fromRedis: true, lastRefresh: time.Now()}
	pm.peers["hint-peer"] = &peerState{addr: "hint:18019", fromRedis: false, lastRefresh: time.Now()}
	pm.mu.Unlock()

	ctx := context.Background()
	if err := cache.SetPeerAddresses(ctx, map[string]string{"other-peer": "new:18019"}); err != nil {
		t.Fatalf("SetPeerAddresses: %v", err)
	}

	pm.loadPeerAddressesFromRedis()

	if pm.GetPeerAddr("redis-peer") != "" {
		t.Fatal("expected stale redis peer to be removed")
	}
	if pm.GetPeerAddr("hint-peer") == "" {
		t.Fatal("expected non-redis hint peer to remain")
	}
	if pm.GetPeerAddr("other-peer") != "new:18019" {
		t.Fatal("expected redis peer address to be loaded")
	}
}

func TestArtifactTypeToString(t *testing.T) {
	tests := []struct {
		input pb.ArtifactEvent_ArtifactType
		want  string
	}{
		{pb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "clip"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_DVR, "dvr"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_VOD, "vod"},
		{pb.ArtifactEvent_ArtifactType(99), "clip"}, // unknown defaults to clip
	}
	for _, tt := range tests {
		got := artifactTypeToString(tt.input)
		if got != tt.want {
			t.Errorf("artifactTypeToString(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSyncPeerAddressesToRedis(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, true)

	pm.mu.Lock()
	pm.peers["remote-1"] = &peerState{addr: "foghorn.r1.example.com:18019"}
	pm.peers["remote-2"] = &peerState{addr: "foghorn.r2.example.com:18019"}
	pm.mu.Unlock()

	pm.syncPeerAddressesToRedis()

	ctx := context.Background()
	addrs, err := cache.GetPeerAddresses(ctx)
	if err != nil {
		t.Fatalf("GetPeerAddresses: %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses in Redis, got %d", len(addrs))
	}
	if addrs["remote-1"] != "foghorn.r1.example.com:18019" {
		t.Fatalf("unexpected address for remote-1: %s", addrs["remote-1"])
	}
}

type testPeerChannelStream struct {
	messages []*pb.PeerMessage
	idx      int
}

func (s *testPeerChannelStream) Send(*pb.PeerMessage) error { return nil }

func (s *testPeerChannelStream) Recv() (*pb.PeerMessage, error) {
	if s.idx >= len(s.messages) {
		return nil, io.EOF
	}
	msg := s.messages[s.idx]
	s.idx++
	return msg, nil
}

func (s *testPeerChannelStream) CloseSend() error { return nil }

func (s *testPeerChannelStream) Context() context.Context { return context.Background() }

func (s *testPeerChannelStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }

func (s *testPeerChannelStream) Trailer() metadata.MD { return metadata.MD{} }

func (s *testPeerChannelStream) SendMsg(any) error { return nil }

func (s *testPeerChannelStream) RecvMsg(any) error { return io.EOF }

func TestRecvLoop_NoCache_DropsMessagesWithoutPanic(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	stream := &testPeerChannelStream{
		messages: []*pb.PeerMessage{{
			ClusterId: "remote",
			Payload: &pb.PeerMessage_EdgeTelemetry{EdgeTelemetry: &pb.EdgeTelemetry{
				StreamName: "live+abc",
				NodeId:     "node-1",
			}},
		}},
	}

	pm.recvLoop("remote", stream)
}

type capturePeerChannelStream struct {
	sent []*pb.PeerMessage
}

func (s *capturePeerChannelStream) Send(msg *pb.PeerMessage) error {
	s.sent = append(s.sent, msg)
	return nil
}

func (s *capturePeerChannelStream) Recv() (*pb.PeerMessage, error) { return nil, io.EOF }
func (s *capturePeerChannelStream) CloseSend() error               { return nil }
func (s *capturePeerChannelStream) Context() context.Context       { return context.Background() }
func (s *capturePeerChannelStream) Header() (metadata.MD, error)   { return metadata.MD{}, nil }
func (s *capturePeerChannelStream) Trailer() metadata.MD           { return metadata.MD{} }
func (s *capturePeerChannelStream) SendMsg(any) error              { return nil }
func (s *capturePeerChannelStream) RecvMsg(any) error              { return io.EOF }

func TestNotifyPeers_UpdatesExistingPeerMetadata(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)

	pm.NotifyPeers([]*pb.TenantClusterPeer{{
		ClusterId:   "remote-cluster",
		ClusterSlug: "remote-old",
		BaseUrl:     "example.com",
		Role:        "subscribed",
	}})

	pm.NotifyPeers([]*pb.TenantClusterPeer{{
		ClusterId:   "remote-cluster",
		ClusterSlug: "remote-new",
		BaseUrl:     "example.net",
		Role:        "official",
		S3Bucket:    "bucket-a",
		S3Endpoint:  "https://s3.example.net",
		S3Region:    "us-east-1",
	}})

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	ps, ok := pm.peers["remote-cluster"]
	if !ok {
		t.Fatal("expected peer to exist")
	}
	if ps.addr != "foghorn.remote-new.example.net:18019" {
		t.Fatalf("unexpected addr: %s", ps.addr)
	}
	if ps.lifecycle != peerAlwaysOn {
		t.Fatalf("expected lifecycle peerAlwaysOn, got %v", ps.lifecycle)
	}
	if ps.s3Config == nil || ps.s3Config.S3Bucket != "bucket-a" {
		t.Fatalf("unexpected s3 config: %+v", ps.s3Config)
	}
}

func TestNotifyPeers_LeaderSyncsToRedisOnAddressChange(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, true)

	// Close done so connectPeer goroutines exit immediately
	close(pm.done)

	pm.NotifyPeers([]*pb.TenantClusterPeer{{
		ClusterId:   "remote-1",
		ClusterSlug: "r1",
		BaseUrl:     "old.example.com",
		Role:        "official",
	}})

	// Update address for the same peer
	pm.NotifyPeers([]*pb.TenantClusterPeer{{
		ClusterId:   "remote-1",
		ClusterSlug: "r1",
		BaseUrl:     "new.example.com",
		Role:        "official",
	}})

	ctx := context.Background()
	addrs, err := cache.GetPeerAddresses(ctx)
	if err != nil {
		t.Fatalf("GetPeerAddresses: %v", err)
	}
	if addrs["remote-1"] != "foghorn.r1.new.example.com:18019" {
		t.Fatalf("expected updated address in Redis, got %q", addrs["remote-1"])
	}
}

func TestShouldSendStreamToPeer_StreamScopedRequiresTrackedStreamAndTenant(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	pm.streamPeers["remote"] = map[string]bool{"live+alpha": true}

	ps := &peerState{
		lifecycle: peerStreamScoped,
		tenantIDs: []string{"tenant-a"},
	}

	if !pm.shouldSendStreamToPeer("remote", ps, "live+alpha", "tenant-a") {
		t.Fatal("expected tracked stream with allowed tenant to be sent")
	}
	if pm.shouldSendStreamToPeer("remote", ps, "live+alpha", "tenant-b") {
		t.Fatal("expected tenant mismatch to be blocked")
	}
	if pm.shouldSendStreamToPeer("remote", ps, "live+beta", "tenant-a") {
		t.Fatal("expected untracked stream to be blocked for stream-scoped peer")
	}
}

func TestBroadcastStreamLifecycle_FiltersUnauthorizedPeers(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)

	allowedStream := &capturePeerChannelStream{}
	blockedStream := &capturePeerChannelStream{}

	pm.mu.Lock()
	pm.streamPeers["allowed"] = map[string]bool{"live+alpha": true}
	pm.peers["allowed"] = &peerState{
		connected: true,
		stream:    allowedStream,
		lifecycle: peerStreamScoped,
		tenantIDs: []string{"tenant-a"},
	}
	pm.peers["blocked"] = &peerState{
		connected: true,
		stream:    blockedStream,
		lifecycle: peerStreamScoped,
		tenantIDs: []string{"tenant-a"},
	}
	pm.mu.Unlock()

	pm.BroadcastStreamLifecycle("live+alpha", "tenant-a", true)

	if len(allowedStream.sent) != 1 {
		t.Fatalf("expected allowed peer to receive 1 message, got %d", len(allowedStream.sent))
	}
	if len(blockedStream.sent) != 0 {
		t.Fatalf("expected blocked peer to receive 0 messages, got %d", len(blockedStream.sent))
	}
}
