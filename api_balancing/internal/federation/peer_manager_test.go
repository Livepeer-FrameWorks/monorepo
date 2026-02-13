package federation

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"frameworks/pkg/clients/foghorn"
	pb "frameworks/pkg/proto"

	"frameworks/api_balancing/internal/state"
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

func TestEnrichFederationEventGeo_UsesPeerClusterForRemoteGeo(t *testing.T) {
	pm := &PeerManager{
		clusterID:     "local-cluster",
		ownerTenantID: "tenant-a",
		logger:        testLogger(),
		peers:         map[string]*peerState{"peer-1": {lat: 37.7749, lon: -122.4194}},
		selfGeoFunc:   func() (float64, float64, string) { return 47.6062, -122.3321, "Seattle" },
		streamPeers:   make(map[string]map[string]bool),
		metricHistory: make(map[string][]metricSample),
	}

	peerCluster := "peer-1"
	data := &pb.FederationEventData{
		EventType:   pb.FederationEventType_PEER_CONNECTED,
		PeerCluster: &peerCluster,
	}

	pm.enrichFederationEventGeo(data)

	if data.GetTenantId() != "tenant-a" {
		t.Fatalf("tenant_id = %q, want tenant-a", data.GetTenantId())
	}
	if data.GetLocalCluster() != "local-cluster" {
		t.Fatalf("local_cluster = %q, want local-cluster", data.GetLocalCluster())
	}
	if data.GetRemoteCluster() != peerCluster {
		t.Fatalf("remote_cluster = %q, want %q", data.GetRemoteCluster(), peerCluster)
	}
	if data.LocalLat == nil || data.LocalLon == nil {
		t.Fatal("expected local geo to be enriched")
	}
	if data.RemoteLat == nil || data.RemoteLon == nil {
		t.Fatal("expected remote geo to be enriched from peer cache")
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

func TestCheckReplicationCompletion_RequiresDestinationNodeLive(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, true)
	ctx := context.Background()

	record := &ActiveReplicationRecord{
		StreamName:    "tenant1+stream1",
		SourceNodeID:  "source-node",
		SourceCluster: "cluster-b",
		DestCluster:   "cluster-a",
		DestNodeID:    "dest-node",
		BaseURL:       "edge.dest.example.com",
		CreatedAt:     time.Now(),
	}
	if err := cache.SetActiveReplication(ctx, record); err != nil {
		t.Fatalf("SetActiveReplication: %v", err)
	}

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo("other-node", "edge.other.example.com", true, nil, nil, "", "", nil)
	if err := sm.UpdateStreamFromBuffer("stream1", record.StreamName, "other-node", "tenant1", "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	pm.checkReplicationCompletion()

	got, err := cache.GetActiveReplication(ctx, record.StreamName)
	if err != nil {
		t.Fatalf("GetActiveReplication: %v", err)
	}
	if got == nil {
		t.Fatal("expected active replication to remain when destination node is not live")
	}
}

func TestCheckReplicationCompletion_ClearsRecordWhenDestinationNodeLive(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, true)
	ctx := context.Background()

	record := &ActiveReplicationRecord{
		StreamName:    "tenant1+stream1",
		SourceNodeID:  "source-node",
		SourceCluster: "cluster-b",
		DestCluster:   "cluster-a",
		DestNodeID:    "dest-node",
		BaseURL:       "edge.dest.example.com",
		CreatedAt:     time.Now(),
	}
	if err := cache.SetActiveReplication(ctx, record); err != nil {
		t.Fatalf("SetActiveReplication: %v", err)
	}

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo(record.DestNodeID, "edge.dest.example.com", true, nil, nil, "", "", nil)
	if err := sm.UpdateStreamFromBuffer("stream1", record.StreamName, record.DestNodeID, "tenant1", "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	pm.checkReplicationCompletion()

	got, err := cache.GetActiveReplication(ctx, record.StreamName)
	if err != nil {
		t.Fatalf("GetActiveReplication: %v", err)
	}
	if got != nil {
		t.Fatal("expected active replication to be cleared when destination node is live")
	}
}

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

func TestIsStreamLiveOnPeer_RejectsTenantMismatch(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, false)

	ctx := context.Background()
	err := cache.SetRemoteLiveStream(ctx, "stream-1", &RemoteLiveStreamEntry{
		ClusterID: "remote-cluster",
		TenantID:  "tenant-a",
		UpdatedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("SetRemoteLiveStream: %v", err)
	}

	if cluster, ok := pm.IsStreamLiveOnPeer(ctx, "stream-1", "tenant-b"); ok || cluster != "" {
		t.Fatalf("expected tenant mismatch to fail closed, got cluster=%q ok=%v", cluster, ok)
	}

	if cluster, ok := pm.IsStreamLiveOnPeer(ctx, "stream-1", "tenant-a"); !ok || cluster != "remote-cluster" {
		t.Fatalf("expected tenant match to return remote cluster, got cluster=%q ok=%v", cluster, ok)
	}
}

func TestGetPeerS3Config(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)

	pm.mu.Lock()
	pm.peers["remote"] = &peerState{
		s3Config: &ClusterS3Config{
			ClusterID:  "remote",
			S3Bucket:   "bucket-a",
			S3Endpoint: "s3.example.com",
			S3Region:   "us-east-1",
		},
	}
	pm.mu.Unlock()

	cfg := pm.GetPeerS3Config("remote")
	if cfg == nil || cfg.S3Bucket != "bucket-a" {
		t.Fatalf("unexpected s3 config: %+v", cfg)
	}
	if got := pm.GetPeerS3Config("missing"); got != nil {
		t.Fatalf("expected nil config for missing peer, got %+v", got)
	}
}

func TestTrackAndUntrackStream_StreamScopedPeerLifecycle(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	cancelled := false

	pm.mu.Lock()
	pm.peers["remote"] = &peerState{
		lifecycle: peerStreamScoped,
		cancel: func() {
			cancelled = true
		},
	}
	pm.mu.Unlock()

	pm.TrackStream("live+alpha", []string{"", "local-cluster", "remote"})
	if !pm.streamPeers["remote"]["live+alpha"] {
		t.Fatal("expected stream to be tracked for remote cluster")
	}

	pm.UntrackStream("live+alpha")
	if !cancelled {
		t.Fatal("expected stream-scoped peer to be canceled when last stream is removed")
	}
	if _, ok := pm.streamPeers["remote"]; ok {
		t.Fatal("expected stream peer mapping to be deleted")
	}
	if _, ok := pm.peers["remote"]; ok {
		t.Fatal("expected stream-scoped peer to be removed")
	}
}

func TestUntrackStream_AlwaysOnPeerRemains(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	cancelled := false

	pm.mu.Lock()
	pm.streamPeers["remote"] = map[string]bool{"live+alpha": true}
	pm.peers["remote"] = &peerState{
		lifecycle: peerAlwaysOn,
		cancel: func() {
			cancelled = true
		},
	}
	pm.mu.Unlock()

	pm.UntrackStream("live+alpha")
	if cancelled {
		t.Fatal("always-on peer should not be canceled when stream tracking is removed")
	}
	if _, ok := pm.peers["remote"]; !ok {
		t.Fatal("always-on peer should remain registered")
	}
}

func TestLeaseHelpersWithoutCache(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	if !pm.tryAcquireLease() {
		t.Fatal("expected lease acquire to succeed without cache")
	}
	if !pm.renewLease() {
		t.Fatal("expected lease renew to succeed without cache")
	}
}

func TestCloseCancelsAllPeers(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	cancelCount := 0

	pm.mu.Lock()
	pm.peers["r1"] = &peerState{cancel: func() { cancelCount++ }}
	pm.peers["r2"] = &peerState{cancel: func() { cancelCount++ }}
	pm.mu.Unlock()

	pm.Close()
	if cancelCount != 2 {
		t.Fatalf("expected 2 peer cancels, got %d", cancelCount)
	}
	if got := len(pm.GetPeers()); got != 0 {
		t.Fatalf("expected peer map cleared on close, got %d peers", got)
	}
	select {
	case <-pm.done:
	default:
		t.Fatal("expected done channel to be closed")
	}
}

func TestRun_StopsWhenDoneClosed(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, false)
	close(pm.done)

	// Should return immediately when done is already closed.
	pm.run()
}

func seedFederationNodeAndStream(t *testing.T, sm *state.StreamStateManager, nodeID, internalName, tenantID string) {
	t.Helper()
	lat := 37.7749
	lon := -122.4194
	sm.SetNodeInfo(nodeID, "https://"+nodeID+".example.com", true, &lat, &lon, "test", "", nil)
	sm.TouchNode(nodeID, true)
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		MaxTranscodes        int
		CurrentTranscodes    int
	}{
		CPU:        15,
		RAMMax:     2048,
		RAMCurrent: 256,
		UpSpeed:    64,
		DownSpeed:  16,
		BWLimit:    10000,
		CapEdge:    true,
		Roles:      []string{"edge"},
	})
	if err := sm.UpdateStreamFromBuffer(internalName, internalName, nodeID, tenantID, "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}
	sm.UpdateNodeStats(internalName, nodeID, 12, 1, 0, 0, false)
	sm.SetStreamPlaybackID(internalName, "play-"+internalName)
}

func newNoopPool(t *testing.T) *foghorn.FoghornPool {
	t.Helper()
	pool := foghorn.NewPool(foghorn.PoolConfig{
		Logger:              testLogger(),
		HealthCheckInterval: time.Hour,
		MaxIdleTime:         time.Hour,
	})
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

type fakeClusterPeerDiscovery struct {
	resp  *pb.ListPeersResponse
	err   error
	calls int
}

func (f *fakeClusterPeerDiscovery) ListPeers(_ context.Context, _ string) (*pb.ListPeersResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

type fakeFederationPool struct {
	mu          sync.Mutex
	calls       int
	getOrCreate func(clusterID, addr string) (federationPeerClient, error)
}

func (f *fakeFederationPool) GetOrCreate(clusterID, addr string) (federationPeerClient, error) {
	f.mu.Lock()
	f.calls++
	fn := f.getOrCreate
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("no fake client configured")
	}
	return fn(clusterID, addr)
}

func (f *fakeFederationPool) Touch(string) {}

func (f *fakeFederationPool) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeFederationClient struct {
	mu       sync.Mutex
	openFunc func(ctx context.Context) (pb.FoghornFederation_PeerChannelClient, error)
	opens    int
}

func (f *fakeFederationClient) OpenPeerChannel(ctx context.Context) (pb.FoghornFederation_PeerChannelClient, error) {
	f.mu.Lock()
	f.opens++
	fn := f.openFunc
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("no open func configured")
	}
	return fn(ctx)
}

func (f *fakeFederationClient) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

func TestFlushStreamPeersToRedis_PersistsAndClears(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, false)
	ctx := context.Background()

	pm.mu.Lock()
	pm.streamPeers["remote-1"] = map[string]bool{
		"live+alpha": true,
		"live+beta":  true,
	}
	pm.flushStreamPeersToRedis("remote-1")
	pm.mu.Unlock()

	got, err := cache.GetStreamPeers(ctx, "remote-1")
	if err != nil {
		t.Fatalf("GetStreamPeers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 streams in Redis, got %d (%v)", len(got), got)
	}
	gotSet := map[string]bool{}
	for _, s := range got {
		gotSet[s] = true
	}
	if !gotSet["live+alpha"] || !gotSet["live+beta"] {
		t.Fatalf("expected both streams persisted, got %v", got)
	}

	pm.mu.Lock()
	delete(pm.streamPeers, "remote-1")
	pm.flushStreamPeersToRedis("remote-1")
	pm.mu.Unlock()

	got, err = cache.GetStreamPeers(ctx, "remote-1")
	if err != nil {
		t.Fatalf("GetStreamPeers after clear: %v", err)
	}
	if got != nil {
		t.Fatalf("expected stream peer key cleared, got %v", got)
	}
}

func TestLoadStreamPeersFromRedis_LoadsAndSkipsSelf(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	if err := cache.SetStreamPeers(ctx, "cluster-a", []string{"self-stream"}); err != nil {
		t.Fatalf("SetStreamPeers self: %v", err)
	}
	if err := cache.SetStreamPeers(ctx, "remote-1", []string{"live+alpha", "live+beta"}); err != nil {
		t.Fatalf("SetStreamPeers remote: %v", err)
	}

	pm := newTestPeerManager(t, "cluster-a", cache, false)
	pm.loadStreamPeersFromRedis()

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if _, ok := pm.streamPeers["cluster-a"]; ok {
		t.Fatal("expected self cluster stream peers to be skipped")
	}
	remote, ok := pm.streamPeers["remote-1"]
	if !ok {
		t.Fatal("expected remote-1 stream peers loaded")
	}
	if !remote["live+alpha"] || !remote["live+beta"] {
		t.Fatalf("expected both remote streams loaded, got %+v", remote)
	}
}

func TestLeaseHelpersWithCache(t *testing.T) {
	cache, _ := setupTestCache(t)

	pmA := newTestPeerManager(t, "cluster-a", cache, false)
	pmA.instanceID = "instance-a"
	pmB := newTestPeerManager(t, "cluster-a", cache, false)
	pmB.instanceID = "instance-b"

	if !pmA.tryAcquireLease() {
		t.Fatal("expected instance-a to acquire lease")
	}
	if pmB.tryAcquireLease() {
		t.Fatal("expected instance-b lease acquisition to fail while held by instance-a")
	}
	if !pmA.renewLease() {
		t.Fatal("expected instance-a renew to succeed")
	}
	if pmB.renewLease() {
		t.Fatal("expected instance-b renew to fail while held by instance-a")
	}
}

func TestUntrackStream_RemainingStreamsPersisted(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, false)
	ctx := context.Background()

	pm.mu.Lock()
	pm.streamPeers["remote-1"] = map[string]bool{
		"live+alpha": true,
		"live+beta":  true,
	}
	pm.peers["remote-1"] = &peerState{lifecycle: peerStreamScoped}
	pm.mu.Unlock()

	pm.UntrackStream("live+alpha")

	pm.mu.RLock()
	streams, ok := pm.streamPeers["remote-1"]
	pm.mu.RUnlock()
	if !ok {
		t.Fatal("expected remote-1 stream mapping to remain")
	}
	if streams["live+alpha"] {
		t.Fatal("expected live+alpha to be removed")
	}
	if !streams["live+beta"] {
		t.Fatal("expected live+beta to remain")
	}

	got, err := cache.GetStreamPeers(ctx, "remote-1")
	if err != nil {
		t.Fatalf("GetStreamPeers: %v", err)
	}
	if len(got) != 1 || got[0] != "live+beta" {
		t.Fatalf("expected Redis stream peers to contain only live+beta, got %v", got)
	}
}

func TestRecvLoop_CachesPeerPayloads(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, false)
	ctx := context.Background()
	peerID := "remote-1"

	if err := cache.SetRemoteLiveStream(ctx, "dead+stream", &RemoteLiveStreamEntry{
		ClusterID: peerID,
		TenantID:  "tenant-a",
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("seed remote live stream: %v", err)
	}
	if err := cache.SetStreamAd(ctx, peerID, &StreamAdRecord{
		InternalName: "live+ad",
		TenantID:     "tenant-a",
		PlaybackID:   "play-del",
		IsLive:       true,
		Timestamp:    time.Now().Unix(),
	}); err != nil {
		t.Fatalf("seed stream ad: %v", err)
	}
	if err := cache.SetPlaybackIndex(ctx, "play-del", "live+ad"); err != nil {
		t.Fatalf("seed playback index: %v", err)
	}

	stream := &testPeerChannelStream{
		messages: []*pb.PeerMessage{
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_EdgeTelemetry{EdgeTelemetry: &pb.EdgeTelemetry{
					StreamName:  "live+edge",
					NodeId:      "node-edge",
					BaseUrl:     "edge.remote.example.com",
					BwAvailable: 1234,
					ViewerCount: 7,
					CpuPercent:  12.5,
					RamUsed:     100,
					RamMax:      200,
					GeoLat:      12.3,
					GeoLon:      45.6,
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_ReplicationEvent{ReplicationEvent: &pb.ReplicationEvent{
					StreamName: "live+rep",
					NodeId:     "node-rep",
					ClusterId:  peerID,
					BaseUrl:    "edge.remote.example.com",
					DtscUrl:    "dtsc://edge.remote.example.com/live+rep",
					Available:  true,
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_ClusterSummary{ClusterSummary: &pb.ClusterEdgeSummary{
					Edges: []*pb.EdgeSnapshot{{
						NodeId:         "node-sum",
						BaseUrl:        "edge.sum.example.com",
						GeoLat:         1,
						GeoLon:         2,
						BwAvailableAvg: 3000,
						CpuPercentAvg:  40,
						RamUsed:        100,
						RamMax:         1000,
						TotalViewers:   12,
					}},
					Timestamp: time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_StreamLifecycle{StreamLifecycle: &pb.StreamLifecycleEvent{
					InternalName: "live+stream",
					TenantId:     "tenant-a",
					ClusterId:    peerID,
					IsLive:       true,
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_StreamLifecycle{StreamLifecycle: &pb.StreamLifecycleEvent{
					InternalName: "dead+stream",
					TenantId:     "tenant-a",
					ClusterId:    peerID,
					IsLive:       false,
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_StreamAd{StreamAd: &pb.StreamAdvertisement{
					InternalName:    "live+ad2",
					TenantId:        "tenant-a",
					PlaybackId:      "play-2",
					OriginClusterId: peerID,
					IsLive:          true,
					Edges: []*pb.PeerStreamEdge{{
						NodeId:      "node-ad",
						BaseUrl:     "edge.ad.example.com",
						DtscUrl:     "dtsc://edge.ad.example.com/live+ad2",
						IsOrigin:    true,
						BwAvailable: 4321,
						CpuPercent:  15,
						ViewerCount: 3,
						GeoLat:      1,
						GeoLon:      2,
						BufferState: "FULL",
					}},
					Timestamp: time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_StreamAd{StreamAd: &pb.StreamAdvertisement{
					InternalName: "live+ad",
					TenantId:     "tenant-a",
					IsLive:       false,
					Timestamp:    time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_ArtifactAd{ArtifactAd: &pb.ArtifactAdvertisement{
					Artifacts: []*pb.ArtifactLocation{{
						ArtifactHash: "artifact-1",
						ArtifactType: "clip",
						NodeId:       "node-art",
						BaseUrl:      "edge.art.example.com",
						SizeBytes:    99,
						AccessCount:  2,
						LastAccessed: time.Now().Unix(),
						GeoLat:       1,
						GeoLon:       2,
					}},
					Timestamp: time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &pb.PeerHeartbeat{
					ProtocolVersion:  protocolVersion,
					StreamCount:      7,
					TotalBwAvailable: 9999,
					EdgeCount:        4,
					UptimeSeconds:    123,
					Capabilities:     []string{"stream_ad"},
				}},
			},
			{
				ClusterId: peerID,
				Payload: &pb.PeerMessage_CapacitySummary{CapacitySummary: &pb.CapacitySummary{
					TotalBandwidth:     1000,
					AvailableBandwidth: 900,
					TotalEdges:         2,
					AvailableEdges:     1,
					TotalStorage:       10000,
					AvailableStorage:   8000,
					Timestamp:          time.Now().Unix(),
				}},
			},
		},
	}

	pm.recvLoop(peerID, stream)

	edges, err := cache.GetRemoteEdges(ctx, peerID)
	if err != nil || len(edges) == 0 {
		t.Fatalf("expected cached remote edge telemetry, edges=%v err=%v", edges, err)
	}

	reps, err := cache.GetRemoteReplications(ctx, "live+rep")
	if err != nil || len(reps) == 0 {
		t.Fatalf("expected cached replication entry, reps=%v err=%v", reps, err)
	}

	summary, err := cache.GetEdgeSummary(ctx, peerID)
	if err != nil || summary == nil || len(summary.Edges) != 1 {
		t.Fatalf("expected cached edge summary, summary=%v err=%v", summary, err)
	}

	liveEntry, err := cache.GetRemoteLiveStream(ctx, "live+stream")
	if err != nil || liveEntry == nil {
		t.Fatalf("expected live stream cached, entry=%v err=%v", liveEntry, err)
	}

	deadEntry, err := cache.GetRemoteLiveStream(ctx, "dead+stream")
	if err != nil {
		t.Fatalf("GetRemoteLiveStream(dead+stream): %v", err)
	}
	if deadEntry != nil {
		t.Fatalf("expected dead+stream to be deleted, got %+v", deadEntry)
	}

	ad2, err := cache.GetStreamAd(ctx, peerID, "live+ad2")
	if err != nil || ad2 == nil {
		t.Fatalf("expected stream ad cached, ad=%v err=%v", ad2, err)
	}
	if idx, pidxErr := cache.GetPlaybackIndex(ctx, "play-2"); pidxErr != nil || idx != "live+ad2" {
		t.Fatalf("expected playback index play-2 -> live+ad2, got %q err=%v", idx, pidxErr)
	}

	adDeleted, err := cache.GetStreamAd(ctx, peerID, "live+ad")
	if err != nil {
		t.Fatalf("GetStreamAd(live+ad): %v", err)
	}
	if adDeleted != nil {
		t.Fatalf("expected live+ad to be deleted, got %+v", adDeleted)
	}
	if idx, pidxErr := cache.GetPlaybackIndex(ctx, "play-del"); pidxErr != nil || idx != "" {
		t.Fatalf("expected playback index play-del cleared, got %q err=%v", idx, pidxErr)
	}

	arts, err := cache.GetRemoteArtifacts(ctx, "artifact-1")
	if err != nil || len(arts) == 0 {
		t.Fatalf("expected remote artifact cached, arts=%v err=%v", arts, err)
	}

	hb, err := cache.GetPeerHeartbeat(ctx, peerID)
	if err != nil || hb == nil {
		t.Fatalf("expected peer heartbeat cached, hb=%v err=%v", hb, err)
	}
	if hb.StreamCount != 7 || hb.EdgeCount != 4 {
		t.Fatalf("unexpected heartbeat payload: %+v", hb)
	}
}

func TestPushTelemetry_SendsTelemetryAndLifecycleToEligiblePeers(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.pool = newFoghornPoolAdapter(newNoopPool(t))

	allowed := &capturePeerChannelStream{}
	blocked := &capturePeerChannelStream{}

	pm.mu.Lock()
	pm.streamPeers["peer-allowed"] = map[string]bool{"stream-a": true}
	pm.peers["peer-allowed"] = &peerState{
		connected: true,
		stream:    allowed,
		lifecycle: peerStreamScoped,
		tenantIDs: []string{"tenant-a"},
	}
	pm.peers["peer-blocked"] = &peerState{
		connected: true,
		stream:    blocked,
		lifecycle: peerStreamScoped,
		tenantIDs: []string{"tenant-b"},
	}
	pm.mu.Unlock()

	pm.pushTelemetry()

	if len(allowed.sent) != 2 {
		t.Fatalf("expected allowed peer to receive telemetry+lifecycle (2 msgs), got %d", len(allowed.sent))
	}
	if len(blocked.sent) != 0 {
		t.Fatalf("expected blocked peer to receive 0 messages, got %d", len(blocked.sent))
	}
}

func TestPushSummary_SendsClusterSummary(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.pool = newFoghornPoolAdapter(newNoopPool(t))

	out := &capturePeerChannelStream{}
	pm.mu.Lock()
	pm.peers["peer-1"] = &peerState{connected: true, stream: out, lifecycle: peerAlwaysOn}
	pm.mu.Unlock()

	pm.pushSummary()

	if len(out.sent) != 1 {
		t.Fatalf("expected 1 summary message, got %d", len(out.sent))
	}
	msg := out.sent[0]
	payload, ok := msg.Payload.(*pb.PeerMessage_ClusterSummary)
	if !ok || payload.ClusterSummary == nil {
		t.Fatalf("expected cluster summary payload, got %#v", msg.Payload)
	}
	if len(payload.ClusterSummary.Edges) != 1 {
		t.Fatalf("expected 1 edge snapshot, got %d", len(payload.ClusterSummary.Edges))
	}
}

func TestPushArtifacts_SendsArtifactAdvertisement(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")
	sm.SetNodeArtifacts("node-a", []*pb.StoredArtifact{{
		ClipHash:     "clip-1",
		StreamName:   "stream-a",
		FilePath:     "/tmp/clip-1.mp4",
		SizeBytes:    123,
		AccessCount:  5,
		LastAccessed: time.Now().Unix(),
		ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
	}})

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.pool = newFoghornPoolAdapter(newNoopPool(t))

	out := &capturePeerChannelStream{}
	pm.mu.Lock()
	pm.peers["peer-1"] = &peerState{connected: true, stream: out, lifecycle: peerAlwaysOn}
	pm.mu.Unlock()

	pm.pushArtifacts()

	if len(out.sent) != 1 {
		t.Fatalf("expected 1 artifact advertisement, got %d", len(out.sent))
	}
	payload, ok := out.sent[0].Payload.(*pb.PeerMessage_ArtifactAd)
	if !ok || payload.ArtifactAd == nil || len(payload.ArtifactAd.Artifacts) != 1 {
		t.Fatalf("unexpected artifact payload: %#v", out.sent[0].Payload)
	}
	if payload.ArtifactAd.Artifacts[0].ArtifactHash != "clip-1" {
		t.Fatalf("expected clip hash clip-1, got %q", payload.ArtifactAd.Artifacts[0].ArtifactHash)
	}
}

func TestPushStreamAds_SendsAndFiltersByPeerAuthorization(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.pool = newFoghornPoolAdapter(newNoopPool(t))

	allowed := &capturePeerChannelStream{}
	blocked := &capturePeerChannelStream{}

	pm.mu.Lock()
	pm.streamPeers["peer-allowed"] = map[string]bool{"stream-a": true}
	pm.peers["peer-allowed"] = &peerState{
		connected: true,
		stream:    allowed,
		lifecycle: peerStreamScoped,
		tenantIDs: []string{"tenant-a"},
	}
	pm.peers["peer-blocked"] = &peerState{
		connected: true,
		stream:    blocked,
		lifecycle: peerStreamScoped,
		tenantIDs: []string{"tenant-a"},
	}
	pm.mu.Unlock()

	pm.pushStreamAds()

	if len(allowed.sent) != 1 {
		t.Fatalf("expected allowed peer to receive one stream ad, got %d", len(allowed.sent))
	}
	if len(blocked.sent) != 0 {
		t.Fatalf("expected blocked peer to receive zero stream ads, got %d", len(blocked.sent))
	}
	payload, ok := allowed.sent[0].Payload.(*pb.PeerMessage_StreamAd)
	if !ok || payload.StreamAd == nil {
		t.Fatalf("expected stream ad payload, got %#v", allowed.sent[0].Payload)
	}
	if payload.StreamAd.InternalName != "stream-a" || payload.StreamAd.TenantId != "tenant-a" {
		t.Fatalf("unexpected stream ad metadata: %+v", payload.StreamAd)
	}
	if len(payload.StreamAd.Edges) != 1 {
		t.Fatalf("expected one edge in stream ad, got %d", len(payload.StreamAd.Edges))
	}
}

func TestPushHeartbeat_SendsClusterHeartbeat(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.startTime = time.Now().Add(-3 * time.Second)

	out := &capturePeerChannelStream{}
	pm.mu.Lock()
	pm.peers["peer-1"] = &peerState{connected: true, stream: out, lifecycle: peerAlwaysOn}
	pm.mu.Unlock()

	pm.pushHeartbeat()

	if len(out.sent) != 1 {
		t.Fatalf("expected 1 heartbeat message, got %d", len(out.sent))
	}
	payload, ok := out.sent[0].Payload.(*pb.PeerMessage_PeerHeartbeat)
	if !ok || payload.PeerHeartbeat == nil {
		t.Fatalf("expected heartbeat payload, got %#v", out.sent[0].Payload)
	}
	hb := payload.PeerHeartbeat
	if hb.StreamCount != 1 || hb.EdgeCount != 1 {
		t.Fatalf("unexpected heartbeat counts: %+v", hb)
	}
	if hb.UptimeSeconds <= 0 {
		t.Fatalf("expected positive uptime, got %d", hb.UptimeSeconds)
	}
}

func TestUptimeSecondsAndStrPtr(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.startTime = time.Now().Add(-2 * time.Second)
	if got := pm.uptimeSeconds(); got <= 0 {
		t.Fatalf("expected positive uptime seconds, got %d", got)
	}
	if got := strPtr("abc"); got == nil || *got != "abc" {
		t.Fatalf("unexpected strPtr result: %v", got)
	}
}

func TestConnectPeer_NoPoolReturns(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	ps := &peerState{addr: "unused:18019"}
	pm.connectPeer("remote-1", ps)
}

func TestRunAsLeader_ExitsWhenDoneClosedAndNoPeerDiscovery(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	close(pm.done)
	pm.runAsLeader()
}

func TestNewPeerManager_CanStartAndCloseWithoutLeadership(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	if !cache.TryAcquireLeaderLease(ctx, leaderRole, "lease-holder") {
		t.Fatal("expected setup lease-holder acquisition")
	}

	pm := NewPeerManager(PeerManagerConfig{
		ClusterID:  "cluster-a",
		InstanceID: "instance-under-test",
		Pool:       newNoopPool(t),
		Cache:      cache,
		Logger:     testLogger(),
	})
	if pm == nil {
		t.Fatal("expected peer manager instance")
	}
	pm.Close()
}

func TestRefreshPeers_NoPeerDiscoveryNoop(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.refreshPeers()
	if got := len(pm.GetPeers()); got != 0 {
		t.Fatalf("expected no peers, got %d", got)
	}
}

func TestRefreshPeers_ListErrorKeepsExistingPeers(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.peerDiscovery = &fakeClusterPeerDiscovery{err: errors.New("boom")}
	pm.mu.Lock()
	pm.peers["remote-1"] = &peerState{addr: "old:18019", lastRefresh: time.Now()}
	pm.mu.Unlock()

	pm.refreshPeers()

	if addr := pm.GetPeerAddr("remote-1"); addr != "old:18019" {
		t.Fatalf("expected existing peer to be preserved, got addr=%q", addr)
	}
}

func TestRefreshPeers_ReconcilesAddUpdateAndRemove(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)

	unchanged := &peerState{
		addr:      "same:18019",
		connected: true,
		tenantIDs: []string{"tenant-old"},
	}
	changedCanceled := 0
	changedS3 := &ClusterS3Config{ClusterID: "changed", S3Bucket: "bucket-a"}
	changed := &peerState{
		addr:      "old:18019",
		connected: true,
		cancel: func() {
			changedCanceled++
		},
		s3Config: changedS3,
	}
	staleCanceled := 0
	stale := &peerState{
		addr: "stale:18019",
		cancel: func() {
			staleCanceled++
		},
	}

	pm.mu.Lock()
	pm.peers["same"] = unchanged
	pm.peers["changed"] = changed
	pm.peers["stale"] = stale
	pm.mu.Unlock()
	pm.peerDiscovery = &fakeClusterPeerDiscovery{
		resp: &pb.ListPeersResponse{
			Peers: []*pb.PeerCluster{
				{ClusterId: "same", FoghornAddr: "same:18019", SharedTenantIds: []string{"tenant-a"}},
				{ClusterId: "changed", FoghornAddr: "new:18019", SharedTenantIds: []string{"tenant-b"}},
				{ClusterId: "new-peer", FoghornAddr: "new-peer:18019", SharedTenantIds: []string{"tenant-c"}},
				{ClusterId: "skip-empty", FoghornAddr: ""},
			},
		},
	}

	pm.refreshPeers()

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if pm.peers["same"] != unchanged {
		t.Fatal("expected unchanged connected peer to be reused")
	}
	if !pm.peers["same"].connected {
		t.Fatal("expected unchanged peer to remain connected")
	}
	if len(pm.peers["same"].tenantIDs) != 1 || pm.peers["same"].tenantIDs[0] != "tenant-a" {
		t.Fatalf("expected updated tenant list for unchanged peer, got %v", pm.peers["same"].tenantIDs)
	}

	updated := pm.peers["changed"]
	if updated == nil {
		t.Fatal("expected changed peer to exist")
	}
	if updated == changed {
		t.Fatal("expected changed peer state to be replaced")
	}
	if updated.addr != "new:18019" {
		t.Fatalf("expected changed peer address to update, got %q", updated.addr)
	}
	if updated.s3Config != changedS3 {
		t.Fatal("expected changed peer to retain previous s3 config")
	}

	if _, ok := pm.peers["stale"]; ok {
		t.Fatal("expected stale peer to be removed")
	}
	if _, ok := pm.peers["new-peer"]; !ok {
		t.Fatal("expected new peer to be added")
	}
	if _, ok := pm.peers["skip-empty"]; ok {
		t.Fatal("expected peer with empty foghorn addr to be skipped")
	}
	if changedCanceled != 1 {
		t.Fatalf("expected changed peer cancel once, got %d", changedCanceled)
	}
	if staleCanceled != 1 {
		t.Fatalf("expected stale peer cancel once, got %d", staleCanceled)
	}
}

func TestConnectPeer_ExitsWhenPeerEntryMissing(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pool := &fakeFederationPool{
		getOrCreate: func(_, _ string) (federationPeerClient, error) {
			return &fakeFederationClient{
				openFunc: func(context.Context) (pb.FoghornFederation_PeerChannelClient, error) {
					return &capturePeerChannelStream{}, nil
				},
			}, nil
		},
	}
	pm.pool = pool

	pm.connectPeer("remote-1", &peerState{addr: "remote-1:18019"})
	if got := pool.callCount(); got != 0 {
		t.Fatalf("expected no pool calls when peer is missing, got %d", got)
	}
}

func TestConnectPeer_RetriesOnGetOrCreateErrorUntilDone(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.reconnectBackoff = time.Millisecond
	pool := &fakeFederationPool{
		getOrCreate: func(_, _ string) (federationPeerClient, error) {
			return nil, errors.New("dial failed")
		},
	}
	pm.pool = pool

	ps := &peerState{addr: "remote-1:18019"}
	pm.mu.Lock()
	pm.peers["remote-1"] = ps
	pm.mu.Unlock()

	done := make(chan struct{})
	go func() {
		pm.connectPeer("remote-1", ps)
		close(done)
	}()

	waitFor(t, 100*time.Millisecond, func() bool { return pool.callCount() > 0 }, "expected GetOrCreate retries")
	close(pm.done)
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("connectPeer did not exit after done closed")
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if ps.connected {
		t.Fatal("expected peer to remain disconnected after GetOrCreate failures")
	}
}

func TestConnectPeer_RetriesOnOpenPeerChannelErrorUntilDone(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.reconnectBackoff = time.Millisecond
	client := &fakeFederationClient{
		openFunc: func(context.Context) (pb.FoghornFederation_PeerChannelClient, error) {
			return nil, errors.New("open failed")
		},
	}
	pool := &fakeFederationPool{
		getOrCreate: func(_, _ string) (federationPeerClient, error) {
			return client, nil
		},
	}
	pm.pool = pool

	ps := &peerState{addr: "remote-1:18019"}
	pm.mu.Lock()
	pm.peers["remote-1"] = ps
	pm.mu.Unlock()

	done := make(chan struct{})
	go func() {
		pm.connectPeer("remote-1", ps)
		close(done)
	}()

	waitFor(t, 100*time.Millisecond, func() bool { return client.openCount() > 0 }, "expected PeerChannel open attempts")
	close(pm.done)
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("connectPeer did not exit after done closed")
	}
}

func TestConnectPeer_ConnectsThenMarksDisconnectedOnEOF(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.reconnectBackoff = 50 * time.Millisecond
	client := &fakeFederationClient{
		openFunc: func(context.Context) (pb.FoghornFederation_PeerChannelClient, error) {
			return &capturePeerChannelStream{}, nil
		},
	}
	pool := &fakeFederationPool{
		getOrCreate: func(_, _ string) (federationPeerClient, error) {
			return client, nil
		},
	}
	pm.pool = pool

	ps := &peerState{addr: "remote-1:18019"}
	pm.mu.Lock()
	pm.peers["remote-1"] = ps
	pm.mu.Unlock()

	done := make(chan struct{})
	go func() {
		pm.connectPeer("remote-1", ps)
		close(done)
	}()

	waitFor(t, 100*time.Millisecond, func() bool { return client.openCount() > 0 }, "expected successful PeerChannel open")
	waitFor(t, 100*time.Millisecond, func() bool {
		pm.mu.RLock()
		defer pm.mu.RUnlock()
		return ps.cancel != nil && !ps.connected && ps.stream == nil
	}, "expected peer to be disconnected after recv loop exits")

	close(pm.done)
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("connectPeer did not exit after done closed")
	}
}
