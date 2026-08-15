package federation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
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
	data := &ipcpb.FederationEventData{
		EventType:   ipcpb.FederationEventType_PEER_CONNECTED,
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

func TestEnrichFederationEventGeo_LeavesTenantUnsetWithoutOwner(t *testing.T) {
	pm := &PeerManager{
		clusterID:     "local-cluster",
		logger:        testLogger(),
		peers:         map[string]*peerState{},
		streamPeers:   make(map[string]map[string]bool),
		metricHistory: make(map[string][]metricSample),
	}

	data := &ipcpb.FederationEventData{EventType: ipcpb.FederationEventType_LEADER_ACQUIRED}
	pm.enrichFederationEventGeo(data)

	if data.TenantId != nil {
		t.Fatalf("tenant_id = %q, want unset", data.GetTenantId())
	}
}

func TestSetOwnerTenantIDUpdatesFederationEventEnrichment(t *testing.T) {
	pm := &PeerManager{
		clusterID:     "local-cluster",
		logger:        testLogger(),
		peers:         map[string]*peerState{},
		streamPeers:   make(map[string]map[string]bool),
		metricHistory: make(map[string][]metricSample),
	}
	pm.SetOwnerTenantID("tenant-real")

	data := &ipcpb.FederationEventData{EventType: ipcpb.FederationEventType_LEADER_ACQUIRED}
	pm.enrichFederationEventGeo(data)

	if data.GetTenantId() != "tenant-real" {
		t.Fatalf("tenant_id = %q, want tenant-real", data.GetTenantId())
	}
}

// newTestPeerManager creates a PeerManager suitable for unit tests.
// It does not start the background run() goroutine.
func newTestPeerManager(t *testing.T, clusterID string, cache *RemoteEdgeCache, isLeader bool) *PeerManager {
	t.Helper()
	pm := &PeerManager{
		clusterID:           clusterID,
		instanceID:          "test-instance",
		cache:               cache,
		logger:              testLogger(),
		peers:               make(map[string]*peerState),
		streamPeers:         make(map[string]map[string]bool),
		streamTenants:       make(map[string]string),
		streamMemberships:   make(map[string]StreamPeerMembership),
		trackedTenantRefs:   make(map[string]map[string]int),
		trackedAddrRefs:     make(map[string]map[string]map[int64]int),
		trackedAlwaysOnRefs: make(map[string]int),
		metricHistory:       make(map[string][]metricSample),
		done:                make(chan struct{}),
		isLeader:            isLeader,
		leaderReady:         isLeader,
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

func TestTrackedDemand_IsNonExpiringAndRevokesOnUntrack(t *testing.T) {
	cache, redisServer := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, true)
	_, _ = pm.TrackStream(context.Background(), "live+alpha", "tenant-a", "generation-a", 1, []control.AdmissionPeerHint{{
		ClusterID: "remote-cluster",
		Addr:      "10.88.1.10:18019",
	}})
	redisServer.FastForward(48 * time.Hour)
	memberships, err := cache.LoadAllStreamPeerMemberships(context.Background())
	if err != nil {
		t.Fatalf("LoadAllStreamPeerMemberships: %v", err)
	}
	record, ok := memberships["live+alpha"]
	if !ok || record.TenantID != "tenant-a" || len(record.Peers) != 1 || record.Peers[0].ClusterID != "remote-cluster" {
		t.Fatalf("active stream membership did not survive without renewal: %+v", memberships)
	}
	replacement := newTestPeerManager(t, "local-cluster", cache, false)
	if err = replacement.loadStreamPeerMembershipsFromRedis(); err != nil {
		t.Fatalf("replacement leader reconstruction: %v", err)
	}
	if hint := replacement.localPeerHints()["remote-cluster"]; hint.Addr != "10.88.1.10:18019" || len(hint.Tenants) != 1 || hint.Tenants[0] != "tenant-a" {
		t.Fatalf("replacement leader lost long-lived stream authority: %+v", hint)
	}

	if err = pm.UntrackStream(context.Background(), "live+alpha", "tenant-a", "generation-a", 1); err != nil {
		t.Fatalf("UntrackStream: %v", err)
	}
	memberships, err = cache.LoadAllStreamPeerMemberships(context.Background())
	if err != nil {
		t.Fatalf("LoadAllStreamPeerMemberships after revoke: %v", err)
	}
	if ended, ok := memberships["live+alpha"]; !ok || ended.Active || ended.SourceRevision != 1 {
		t.Fatalf("ended stream did not retain its revision fence: %+v", memberships)
	}
}

func TestStreamMembershipRevisionFenceRejectsDelayedPriorGeneration(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, true)
	ctx := context.Background()
	hintsA := []control.AdmissionPeerHint{{ClusterID: "remote-a", Addr: "remote-a:18019"}}

	current, _ := pm.TrackStream(ctx, "live+alpha", "tenant-a", "generation-a", 10, hintsA)
	if !current {
		t.Fatal("generation A did not establish its membership")
	}
	if err := pm.UntrackStream(ctx, "live+alpha", "tenant-a", "generation-a", 10); err != nil {
		t.Fatalf("end generation A: %v", err)
	}
	current, err := pm.TrackStream(ctx, "live+alpha", "tenant-a", "generation-b", 11, nil)
	if err != nil || !current {
		t.Fatalf("generation B empty replacement = (current=%v, err=%v), want current", current, err)
	}

	// This is generation A resuming after its lock-free effect phase was paused. Its Redis CAS
	// must lose, and its old peer set must not return to either durable or process-local state.
	current, err = pm.TrackStream(ctx, "live+alpha", "tenant-a", "generation-a", 10, hintsA)
	if err != nil || current {
		t.Fatalf("delayed generation A = (current=%v, err=%v), want stale", current, err)
	}
	memberships, err := cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil {
		t.Fatalf("LoadAllStreamPeerMemberships: %v", err)
	}
	record := memberships["live+alpha"]
	if !record.Active || record.SourceGeneration != "generation-b" || record.SourceRevision != 11 || len(record.Peers) != 0 {
		t.Fatalf("generation B empty membership was not authoritative: %+v", record)
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if len(pm.streamPeers["remote-a"]) != 0 {
		t.Fatalf("generation A peer authority was resurrected: %+v", pm.streamPeers)
	}
}

func TestMembershipTombstoneCleanupRequiresAdmissionLedgerProof(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	record := StreamPeerMembership{
		StreamName: "live+ended", TenantID: "tenant-a", SourceGeneration: "generation-a",
		SourceRevision: 30, EndedAtUnixMilli: time.Now().Add(-2 * streamPeerTombstoneRetention).UnixMilli(),
	}
	if _, err := cache.EndStreamPeerMembership(ctx, record); err != nil {
		t.Fatalf("EndStreamPeerMembership: %v", err)
	}
	pm := newTestPeerManager(t, "local-cluster", cache, true)
	if err := pm.loadStreamPeerMembershipsFromRedis(); err != nil {
		t.Fatalf("loadStreamPeerMembershipsFromRedis: %v", err)
	}
	if len(pm.streamMemberships) != 0 {
		t.Fatalf("leader installed ended memberships in memory: %+v", pm.streamMemberships)
	}
	purgeable := false
	pm.canPurgeMemberships = func(_ context.Context, fences []control.AdmissionEffectFence) (map[string]bool, error) {
		if len(fences) != 1 || fences[0].InternalName != record.StreamName || fences[0].SourceRevision != record.SourceRevision {
			t.Fatalf("cleanup fences = %+v", fences)
		}
		return map[string]bool{record.StreamName: purgeable}, nil
	}
	if err := pm.cleanupStreamMembershipTombstones(ctx); err != nil {
		t.Fatalf("cleanup with pending effect: %v", err)
	}
	if exists, err := cache.client.HExists(ctx, cache.keyStreamPeerMemberships(), record.StreamName).Result(); err != nil || !exists {
		t.Fatalf("unproven tombstone was removed: exists=%v err=%v", exists, err)
	}
	purgeable = true
	if err := pm.cleanupStreamMembershipTombstones(ctx); err != nil {
		t.Fatalf("cleanup after ledger proof: %v", err)
	}
	for _, key := range []string{
		cache.keyStreamPeerMemberships(), cache.keyStreamPeerMembershipRevisions(),
		cache.keyStreamPeerMembershipGenerations(), cache.keyStreamPeerMembershipStates(),
	} {
		if exists, err := cache.client.HExists(ctx, key, record.StreamName).Result(); err != nil || exists {
			t.Fatalf("cleanup left field in %s: exists=%v err=%v", key, exists, err)
		}
	}
}

func TestEndedStreamPeerMembershipRequiresRetentionTimestamp(t *testing.T) {
	_, err := normalizeStreamPeerMembership(StreamPeerMembership{
		StreamName: "live+ended", TenantID: "tenant-a", SourceGeneration: "generation-a",
		SourceRevision: 1,
	})
	if err == nil {
		t.Fatal("ended membership without an ended timestamp was accepted")
	}
}

func TestMembershipTombstoneCleanupCannotDeleteConcurrentSuccessor(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	ended := StreamPeerMembership{
		StreamName: "live+successor", TenantID: "tenant-a", SourceGeneration: "generation-a",
		SourceRevision: 40, EndedAtUnixMilli: time.Now().Add(-2 * streamPeerTombstoneRetention).UnixMilli(),
	}
	if _, err := cache.EndStreamPeerMembership(ctx, ended); err != nil {
		t.Fatalf("EndStreamPeerMembership: %v", err)
	}
	pm := newTestPeerManager(t, "local-cluster", cache, true)
	pm.canPurgeMemberships = func(_ context.Context, _ []control.AdmissionEffectFence) (map[string]bool, error) {
		successor := StreamPeerMembership{
			StreamName: ended.StreamName, TenantID: ended.TenantID, SourceGeneration: "generation-b",
			SourceRevision: 41,
		}
		if current, err := cache.SetStreamPeerMembership(ctx, successor); err != nil || !current {
			t.Fatalf("install concurrent successor: current=%v err=%v", current, err)
		}
		return map[string]bool{ended.StreamName: true}, nil
	}
	if err := pm.cleanupStreamMembershipTombstones(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	memberships, err := cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil {
		t.Fatalf("LoadAllStreamPeerMemberships: %v", err)
	}
	successor := memberships[ended.StreamName]
	if !successor.Active || successor.SourceGeneration != "generation-b" || successor.SourceRevision != 41 {
		t.Fatalf("cleanup deleted concurrent successor: %+v", successor)
	}
}

func TestTrackedPeerAddressFailsClosedWhenValidationAndProjectionOrderDiverge(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, true)
	// A validated the retired endpoint first and paused. B validated the current endpoint later but
	// projected first, so publisher source ordering is the reverse of topology observation order.
	_, _ = pm.TrackStream(context.Background(), "live+newer-endpoint", "tenant-a", "generation-b", 20,
		[]control.AdmissionPeerHint{{ClusterID: "remote", Addr: "a-current:18019"}})
	_, _ = pm.TrackStream(context.Background(), "live+retired-endpoint", "tenant-a", "generation-a", 21,
		[]control.AdmissionPeerHint{{ClusterID: "remote", Addr: "z-retired:18019"}})
	pm.mu.RLock()
	_, hinted := pm.trackedPeerHintsLocked()["remote"]
	_, connected := pm.peers["remote"]
	pm.mu.RUnlock()
	if hinted || connected {
		t.Fatal("conflicting publisher hints invented endpoint authority from source revision order")
	}

	// A current Quartermaster lease is topology authority and resolves the conflict.
	pm.mu.Lock()
	pm.quartermasterHints = make(map[string]PeerHint)
	pm.quartermasterHints["remote"] = PeerHint{Addr: "a-current:18019", AlwaysOn: true, Tenants: []string{"tenant-a"}}
	pm.quartermasterHintsRefreshedAt = time.Now()
	pm.mu.Unlock()
	_, _ = pm.TrackStream(context.Background(), "live+retired-endpoint", "tenant-a", "generation-a", 21,
		[]control.AdmissionPeerHint{{ClusterID: "remote", Addr: "z-retired:18019"}})
	pm.mu.RLock()
	resolved := pm.peers["remote"]
	pm.mu.RUnlock()
	if resolved == nil || resolved.addr != "a-current:18019" {
		t.Fatalf("leased Quartermaster authority did not resolve conflict: %+v", resolved)
	}
}

func TestConcurrentAdmissionTrackingCannotReinstallConflictingEndpoint(t *testing.T) {
	for iteration := 0; iteration < 512; iteration++ {
		pm := newTestPeerManager(t, "local-cluster", nil, false)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for index, addr := range []string{"retired:18019", "current:18019"} {
			wg.Add(1)
			go func(index int, addr string) {
				defer wg.Done()
				<-start
				_, _ = pm.TrackStream(context.Background(), fmt.Sprintf("live+concurrent-%d", index),
					"tenant-a", fmt.Sprintf("generation-%d", index), int64(index+1),
					[]control.AdmissionPeerHint{{ClusterID: "remote", Addr: addr}})
			}(index, addr)
		}
		close(start)
		wg.Wait()

		pm.mu.RLock()
		_, authoritative := pm.trackedPeerHintsLocked()["remote"]
		_, installed := pm.peers["remote"]
		pm.mu.RUnlock()
		if authoritative || installed {
			t.Fatalf("iteration %d reinstalled endpoint authority after concurrent conflict", iteration)
		}
	}
}

func TestLeaderRefreshRecomputesAuthorityAfterWaitingForMembershipMutation(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, true)
	_, _ = pm.TrackStream(context.Background(), "live+retired", "tenant-a", "generation-a", 1,
		[]control.AdmissionPeerHint{{ClusterID: "remote", Addr: "retired:18019"}})

	// Model a refresh whose Redis read completed before a concurrent tracker entered the authority
	// critical section. The refresh is queued on pm.mu while that tracker adds the conflicting
	// membership, so it must recompute from the updated refs rather than apply an earlier snapshot.
	pm.mu.Lock()
	refreshStarted := make(chan struct{})
	refreshDone := make(chan struct{})
	go func() {
		close(refreshStarted)
		pm.reconcileLeaderPeerHints(nil)
		close(refreshDone)
	}()
	<-refreshStarted
	pm.addStreamMembershipLocked(StreamPeerMembership{
		StreamName: "live+current", TenantID: "tenant-a", SourceGeneration: "generation-b",
		SourceRevision: 2, Active: true,
		Peers: []StreamPeerTarget{{ClusterID: "remote", Addr: "current:18019"}},
	})
	pm.mu.Unlock()
	<-refreshDone

	pm.mu.RLock()
	_, authoritative := pm.trackedPeerHintsLocked()["remote"]
	_, installed := pm.peers["remote"]
	pm.mu.RUnlock()
	if authoritative || installed {
		t.Fatal("leader refresh applied stale endpoint authority after a conflicting membership")
	}
}

func TestLeasedQuartermasterAddressOverridesCapturedMembership(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	if err := cache.PublishPeerHints(ctx, "quartermaster", map[string]PeerHint{
		"remote": {Addr: "current:18019", AlwaysOn: true, Tenants: []string{"tenant-a"}},
	}); err != nil {
		t.Fatalf("PublishPeerHints: %v", err)
	}
	pm := newTestPeerManager(t, "local-cluster", cache, true)
	_, _ = pm.TrackStream(ctx, "live+captured", "tenant-a", "generation-a", 50,
		[]control.AdmissionPeerHint{{ClusterID: "remote", Addr: "retired:18019", AlwaysOn: true}})
	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("loadPeerAddressesFromRedis: %v", err)
	}
	if addr := pm.GetPeerAddr("remote"); addr != "current:18019" {
		t.Fatalf("peer address = %q, want leased Quartermaster endpoint", addr)
	}
}

func TestLoadPeerAddressesFromRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	cache := NewRemoteEdgeCache(client, "local-cluster", testLogger())

	// Pre-populate Redis with addresses (as if written by leader)
	ctx := context.Background()
	cache.PublishPeerHints(ctx, "seed-writer", map[string]PeerHint{
		"remote-1": {Addr: "foghorn.r1.example.com:18029", AlwaysOn: true},
		"remote-2": {Addr: "foghorn.r2.example.com:18029", AlwaysOn: true},
	})

	pm := newTestPeerManager(t, "local-cluster", cache, false)

	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("loadPeerAddressesFromRedis: %v", err)
	}

	if addr := pm.GetPeerAddr("remote-1"); addr != "foghorn.r1.example.com:18029" {
		t.Fatalf("expected remote-1 addr, got %q", addr)
	}
	if addr := pm.GetPeerAddr("remote-2"); addr != "foghorn.r2.example.com:18029" {
		t.Fatalf("expected remote-2 addr, got %q", addr)
	}
}

func TestLoadPeerAddressesFromRedis_LeaderConnectsImportedHint(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	if err := cache.PublishPeerHints(ctx, "seed-writer", map[string]PeerHint{
		"remote-imported": {Addr: "remote-imported:18029", AlwaysOn: true},
	}); err != nil {
		t.Fatalf("seed peer hint: %v", err)
	}
	client := &fakeFederationClient{
		openFunc: func(context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
			return &capturePeerChannelStream{}, nil
		},
	}
	pm := newTestPeerManager(t, "local-cluster", cache, true)
	pm.reconnectBackoff = time.Millisecond
	pm.pool = &fakeFederationPool{getOrCreate: func(_, _ string) (federationPeerClient, error) {
		return client, nil
	}}

	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("loadPeerAddressesFromRedis: %v", err)
	}
	waitFor(t, 100*time.Millisecond, func() bool { return client.openCount() > 0 }, "leader did not connect imported peer hint")
}

func TestLoadPeerAddressesFromRedis_UpdatesExistingAddress(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	cache := NewRemoteEdgeCache(client, "local-cluster", testLogger())

	pm := newTestPeerManager(t, "local-cluster", cache, false)

	// Seed a peer with old address
	pm.mu.Lock()
	pm.peers["remote-1"] = &peerState{addr: "old-addr:18029", lastRefresh: time.Now()}
	pm.mu.Unlock()

	// Leader wrote updated address to Redis
	ctx := context.Background()
	cache.PublishPeerHints(ctx, "seed-writer", map[string]PeerHint{
		"remote-1": {Addr: "new-addr:18029", AlwaysOn: true},
	})

	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("loadPeerAddressesFromRedis: %v", err)
	}

	if addr := pm.GetPeerAddr("remote-1"); addr != "new-addr:18029" {
		t.Fatalf("expected updated address, got %q", addr)
	}
}

func TestLoadPeerAddressesFromRedis_SkipsSelf(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	cache := NewRemoteEdgeCache(client, "local-cluster", testLogger())

	ctx := context.Background()
	cache.PublishPeerHints(ctx, "seed-writer", map[string]PeerHint{
		"local-cluster": {Addr: "should-be-skipped:18029", AlwaysOn: true},
		"remote-1":      {Addr: "foghorn.r1.example.com:18029", AlwaysOn: true},
	})

	pm := newTestPeerManager(t, "local-cluster", cache, false)
	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("loadPeerAddressesFromRedis: %v", err)
	}

	if pm.GetPeerAddr("local-cluster") != "" {
		t.Fatal("should not load self as peer")
	}
	if pm.GetPeerAddr("remote-1") == "" {
		t.Fatal("expected remote-1 to be loaded")
	}
}

func TestLoadPeerAddressesFromRedis_RemovesAllRevokedPeers(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, false)

	pm.mu.Lock()
	pm.peers["redis-peer"] = &peerState{addr: "old:18029", lastRefresh: time.Now()}
	pm.peers["hint-peer"] = &peerState{addr: "hint:18029", lastRefresh: time.Now()}
	pm.mu.Unlock()

	ctx := context.Background()
	if err := cache.PublishPeerHints(ctx, "seed-writer", map[string]PeerHint{"other-peer": {Addr: "new:18029", AlwaysOn: true}}); err != nil {
		t.Fatalf("PublishPeerHints: %v", err)
	}

	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("loadPeerAddressesFromRedis: %v", err)
	}

	if pm.GetPeerAddr("redis-peer") != "" {
		t.Fatal("expected stale redis peer to be removed")
	}
	if pm.GetPeerAddr("hint-peer") != "" {
		t.Fatal("expected locally stale peer without a live contribution to be removed")
	}
	if pm.GetPeerAddr("other-peer") != "new:18029" {
		t.Fatal("expected redis peer address to be loaded")
	}
}

func TestArtifactTypeToString(t *testing.T) {
	tests := []struct {
		input ipcpb.ArtifactEvent_ArtifactType
		want  string
	}{
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "clip"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR, "dvr"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD, "vod"},
		{ipcpb.ArtifactEvent_ArtifactType(99), "clip"}, // unknown defaults to clip
	}
	for _, tt := range tests {
		got := artifactTypeToString(tt.input)
		if got != tt.want {
			t.Errorf("artifactTypeToString(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPublishQuartermasterHints(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, true)

	pm.mu.Lock()
	pm.quartermasterHints = map[string]PeerHint{
		"remote-1": {Addr: "foghorn.r1.example.com:18029", AlwaysOn: true},
		"remote-2": {Addr: "foghorn.r2.example.com:18029", AlwaysOn: true},
	}
	pm.quartermasterHintsRefreshedAt = time.Now()
	pm.mu.Unlock()

	pm.publishQuartermasterHints()

	ctx := context.Background()
	addrs, err := cache.GetPeerAddresses(ctx)
	if err != nil {
		t.Fatalf("GetPeerAddresses: %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses in Redis, got %d", len(addrs))
	}
	if addrs["remote-1"].Addr != "foghorn.r1.example.com:18029" {
		t.Fatalf("unexpected address for remote-1: %s", addrs["remote-1"].Addr)
	}
}

type testPeerChannelStream struct {
	messages []*foghornfederationpb.PeerMessage
	idx      int
}

func (s *testPeerChannelStream) Send(*foghornfederationpb.PeerMessage) error { return nil }

func (s *testPeerChannelStream) Recv() (*foghornfederationpb.PeerMessage, error) {
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

func TestRecvLoop_RevisionFencesLifecycleAcrossChannelGenerations(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, false)
	event := func(stream string, revision int64, live bool) *foghornfederationpb.PeerMessage {
		return &foghornfederationpb.PeerMessage{
			ClusterId: "remote-1",
			Payload: &foghornfederationpb.PeerMessage_StreamLifecycle{StreamLifecycle: &foghornfederationpb.StreamLifecycleEvent{
				InternalName: stream, TenantId: "tenant-a", ClusterId: "remote-1",
				IsLive: live, SourceRevision: revision,
			}},
		}
	}

	pm.recvLoop("remote-1", &testPeerChannelStream{messages: []*foghornfederationpb.PeerMessage{
		event("new-live", 20, true), event("new-offline", 30, false),
	}})
	pm.recvLoop("remote-1", &testPeerChannelStream{messages: []*foghornfederationpb.PeerMessage{
		event("new-live", 19, false), event("new-offline", 29, true),
		{
			ClusterId: "remote-1",
			Payload: &foghornfederationpb.PeerMessage_StreamLifecycle{StreamLifecycle: &foghornfederationpb.StreamLifecycleEvent{
				InternalName: "spoofed", TenantId: "tenant-a", ClusterId: "other-cluster",
				IsLive: true, SourceRevision: 999,
			}},
		},
	}})

	if live, err := cache.GetRemoteLiveStream(context.Background(), "tenant-a", "new-live"); err != nil || live == nil || live.SourceRevision != 20 {
		t.Fatalf("older channel removed newer live marker: live=%+v err=%v", live, err)
	}
	if live, err := cache.GetRemoteLiveStream(context.Background(), "tenant-a", "new-offline"); err != nil || live != nil {
		t.Fatalf("older channel resurrected newer offline marker: live=%+v err=%v", live, err)
	}
	if live, err := cache.GetRemoteLiveStream(context.Background(), "tenant-a", "spoofed"); err != nil || live != nil {
		t.Fatalf("mismatched payload identity was accepted: live=%+v err=%v", live, err)
	}
}

// TestCheckReplicationCompletion_RequiresDestinationNodeLive preserves
// the behavioural intent of the cache-backed predecessor: when the local
// dest node hasn't gone live yet, the replication mark must NOT be
// cleared by checkReplicationCompletion. The in-flight record now lives
// on the unified stream registry.
func TestCheckReplicationCompletion_RequiresDestinationNodeLive(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, true)

	priorRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })

	const internalName = "tenant1+stream1"
	registry.MarkReplicating(internalName, "cluster-b", "dtsc://src/"+internalName, "dest-node", "edge.dest.example.com", "source-node")

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	// A different node has the stream live — checkReplicationCompletion
	// must not match it to the dest-node we recorded.
	sm.SetNodeInfo("other-node", "edge.other.example.com", true, nil, nil, "", "", nil)
	if err := sm.UpdateStreamFromBuffer("stream1", internalName, "other-node", "tenant1", "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	pm.checkReplicationCompletion()

	if _, ok := registry.LocalReplication(context.Background(), internalName); !ok {
		t.Fatal("expected replication mark to remain when destination node is not live")
	}
}

// TestCheckReplicationCompletion_ClearsRecordWhenDestinationNodeLive
// preserves the matching positive case: when the dest node has gone live,
// the registry mark is cleared and the peer broadcast fires.
func TestCheckReplicationCompletion_ClearsRecordWhenDestinationNodeLive(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "cluster-a", cache, true)

	priorRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })

	const internalName = "tenant1+stream1"
	const destNodeID = "dest-node"
	registry.MarkReplicating(internalName, "cluster-b", "dtsc://src/"+internalName, destNodeID, "edge.dest.example.com", "source-node")

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo(destNodeID, "edge.dest.example.com", true, nil, nil, "", "", nil)
	if err := sm.UpdateStreamFromBuffer("stream1", internalName, destNodeID, "tenant1", "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	pm.checkReplicationCompletion()

	if _, ok := registry.LocalReplication(context.Background(), internalName); ok {
		t.Fatal("expected replication mark to be cleared when destination node is live")
	}
}

func TestRecvLoop_NoCache_DropsMessagesWithoutPanic(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	stream := &testPeerChannelStream{
		messages: []*foghornfederationpb.PeerMessage{{
			ClusterId: "remote",
			Payload: &foghornfederationpb.PeerMessage_EdgeTelemetry{EdgeTelemetry: &foghornfederationpb.EdgeTelemetry{
				StreamName: "live+abc",
				NodeId:     "node-1",
			}},
		}},
	}

	pm.recvLoop("remote", stream)
}

type capturePeerChannelStream struct {
	sent []*foghornfederationpb.PeerMessage
}

func (s *capturePeerChannelStream) Send(msg *foghornfederationpb.PeerMessage) error {
	s.sent = append(s.sent, msg)
	return nil
}

func (s *capturePeerChannelStream) Recv() (*foghornfederationpb.PeerMessage, error) {
	return nil, io.EOF
}
func (s *capturePeerChannelStream) CloseSend() error             { return nil }
func (s *capturePeerChannelStream) Context() context.Context     { return context.Background() }
func (s *capturePeerChannelStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *capturePeerChannelStream) Trailer() metadata.MD         { return metadata.MD{} }
func (s *capturePeerChannelStream) SendMsg(any) error            { return nil }
func (s *capturePeerChannelStream) RecvMsg(any) error            { return io.EOF }

type blockingPeerChannelStream struct {
	capturePeerChannelStream
	ctx context.Context
}

func (s *blockingPeerChannelStream) Recv() (*foghornfederationpb.PeerMessage, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *blockingPeerChannelStream) Context() context.Context { return s.ctx }

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

func TestShouldSendStreamToPeer_DenyUnscopedStreamScopedPeer(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)
	pm.streamPeers["remote"] = map[string]bool{"live+alpha": true}

	ps := &peerState{
		lifecycle: peerStreamScoped,
		tenantIDs: nil,
	}

	if pm.shouldSendStreamToPeer("remote", ps, "live+alpha", "tenant-a") {
		t.Fatal("expected stream-scoped peer with empty tenantIDs to be blocked")
	}
}

func TestShouldSendStreamToPeer_AllowUnscopedAlwaysOnPeer(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, false)

	ps := &peerState{
		lifecycle: peerAlwaysOn,
		tenantIDs: nil,
	}

	if !pm.shouldSendStreamToPeer("remote", ps, "live+alpha", "tenant-a") {
		t.Fatal("expected always-on peer with empty tenantIDs to be allowed")
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

	flush := pm.wireTestWriters()
	_ = pm.BroadcastStreamLifecycle(context.Background(), "live+alpha", "tenant-a", 1, true)
	flush()

	if len(allowedStream.sent) != 1 {
		t.Fatalf("expected allowed peer to receive 1 message, got %d", len(allowedStream.sent))
	}
	if len(blockedStream.sent) != 0 {
		t.Fatalf("expected blocked peer to receive 0 messages, got %d", len(blockedStream.sent))
	}
}

func TestBroadcastStreamLifecycle_DisconnectedRequiredPeerKeepsObligationPending(t *testing.T) {
	pm := newTestPeerManager(t, "local-cluster", nil, true)
	pm.mu.Lock()
	pm.streamPeers["required"] = map[string]bool{"live+alpha": true}
	pm.peers["required"] = &peerState{lifecycle: peerStreamScoped, tenantIDs: []string{"tenant-a"}}
	pm.mu.Unlock()

	err := pm.BroadcastStreamLifecycle(context.Background(), "live+alpha", "tenant-a", 1, true)
	if err == nil {
		t.Fatal("disconnected required peer was skipped while broadcast reported success")
	}
}

func TestIsStreamLiveOnPeer_RejectsTenantMismatch(t *testing.T) {
	cache, _ := setupTestCache(t)
	pm := newTestPeerManager(t, "local-cluster", cache, false)

	ctx := context.Background()
	applied, err := cache.ApplyRemoteStreamLifecycle(ctx, "tenant-a", "stream-1", &RemoteLiveStreamEntry{
		ClusterID: "remote-cluster", TenantID: "tenant-a", SourceRevision: 1, UpdatedAt: time.Now().Unix(),
	}, true)
	if err != nil || !applied {
		t.Fatalf("ApplyRemoteStreamLifecycle: applied=%v err=%v", applied, err)
	}

	if cluster, ok := pm.IsStreamLiveOnPeer(ctx, "stream-1", "tenant-b"); ok || cluster != "" {
		t.Fatalf("expected tenant mismatch to fail closed, got cluster=%q ok=%v", cluster, ok)
	}

	if cluster, ok := pm.IsStreamLiveOnPeer(ctx, "stream-1", "tenant-a"); !ok || cluster != "remote-cluster" {
		t.Fatalf("expected tenant match to return remote cluster, got cluster=%q ok=%v", cluster, ok)
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

	_, _ = pm.TrackStream(context.Background(), "live+alpha", "tenant-a", "generation-a", 1, []control.AdmissionPeerHint{
		{ClusterID: "local-cluster", Addr: "local:18019"},
		{ClusterID: "remote", Addr: "remote:18019"},
	})
	if !pm.streamPeers["remote"]["live+alpha"] {
		t.Fatal("expected stream to be tracked for remote cluster")
	}

	_ = pm.UntrackStream(context.Background(), "live+alpha", "tenant-a", "generation-a", 1)
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
	_, _ = pm.TrackStream(context.Background(), "live+alpha", "tenant-a", "generation-a", 1, []control.AdmissionPeerHint{{
		ClusterID: "remote", Addr: "remote:18019", AlwaysOn: true,
	}})
	pm.mu.Lock()
	pm.peers["remote"].cancel = func() {
		cancelled = true
	}
	pm.mu.Unlock()

	_ = pm.UntrackStream(context.Background(), "live+alpha", "tenant-a", "generation-a", 1)
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
		ProcessingClasses    map[string]state.ClassCapacity
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
	resp  *quartermasterpb.ListPeersResponse
	err   error
	calls int
}

func (f *fakeClusterPeerDiscovery) ListPeers(_ context.Context, _ string) (*quartermasterpb.ListPeersResponse, error) {
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
	openFunc func(ctx context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error)
	opens    int
}

func (f *fakeFederationClient) OpenPeerChannel(ctx context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
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

func reserveTestPeerRunner(t *testing.T, pm *PeerManager, clusterID string, ps *peerState) peerConnectRequest {
	t.Helper()
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.peers[clusterID] = ps
	request, reserved := pm.reservePeerRunnerLocked(clusterID, ps)
	if !reserved {
		t.Fatalf("failed to reserve test runner for %s", clusterID)
	}
	return request
}

func TestStreamPeerMemberships_PersistIncrementallyAndClear(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	alpha := StreamPeerMembership{StreamName: "live+alpha", TenantID: "tenant-a", SourceGeneration: "generation-a", SourceRevision: 1, Peers: []StreamPeerTarget{{ClusterID: "remote-1", Addr: "remote-1:18019"}}}
	beta := StreamPeerMembership{StreamName: "live+beta", TenantID: "tenant-b", SourceGeneration: "generation-b", SourceRevision: 2, Peers: []StreamPeerTarget{{ClusterID: "remote-1", Addr: "remote-1:18019"}}}
	if _, err := cache.SetStreamPeerMembership(ctx, alpha); err != nil {
		t.Fatalf("persist alpha: %v", err)
	}
	if _, err := cache.SetStreamPeerMembership(ctx, beta); err != nil {
		t.Fatalf("persist beta: %v", err)
	}
	if _, err := cache.EndStreamPeerMembership(ctx, alpha); err != nil {
		t.Fatalf("end alpha: %v", err)
	}
	memberships, err := cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil {
		t.Fatalf("load memberships: %v", err)
	}
	if record, ok := memberships["live+alpha"]; !ok || record.Active {
		t.Fatal("ended stream membership did not retain an inactive fence")
	}
	if record, ok := memberships["live+beta"]; !ok || record.TenantID != "tenant-b" {
		t.Fatalf("independent beta membership was rewritten or lost: %+v", memberships)
	}
}

func TestLoadStreamPeerMembershipsFromRedis_LoadsAndSkipsSelf(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	if _, err := cache.SetStreamPeerMembership(ctx, StreamPeerMembership{StreamName: "self-stream", TenantID: "tenant-self", SourceGeneration: "generation-self", SourceRevision: 1, Peers: []StreamPeerTarget{{ClusterID: "cluster-a", Addr: "self:18019"}}}); err != nil {
		t.Fatalf("SetStreamPeerMembership self: %v", err)
	}
	for _, record := range []StreamPeerMembership{
		{StreamName: "live+alpha", TenantID: "tenant-a", SourceGeneration: "generation-a", SourceRevision: 2, Peers: []StreamPeerTarget{{ClusterID: "remote-1", Addr: "remote:18019"}}},
		{StreamName: "live+beta", TenantID: "tenant-b", SourceGeneration: "generation-b", SourceRevision: 3, Peers: []StreamPeerTarget{{ClusterID: "remote-1", Addr: "remote:18019"}}},
	} {
		if _, err := cache.SetStreamPeerMembership(ctx, record); err != nil {
			t.Fatalf("SetStreamPeerMembership remote: %v", err)
		}
	}

	pm := newTestPeerManager(t, "cluster-a", cache, false)
	pm.streamPeers["stale-remote"] = map[string]bool{"live+stale": true}
	pm.streamTenants["live+stale"] = "tenant-stale"
	if err := pm.loadStreamPeerMembershipsFromRedis(); err != nil {
		t.Fatalf("loadStreamPeerMembershipsFromRedis: %v", err)
	}

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
	if _, ok := pm.streamPeers["stale-remote"]; ok {
		t.Fatal("exact leader reconstruction retained an absent stale mapping")
	}
	hint := pm.trackedPeerHintsLocked()["remote-1"]
	if hint.Addr != "remote:18019" || len(hint.Tenants) != 2 {
		t.Fatalf("leader reconstruction lost complete peer authority: %+v", hint)
	}
}

func TestLoadAllStreamPeerMemberships_FailsClosedOnMalformedRecord(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()
	if _, err := cache.SetStreamPeerMembership(ctx, StreamPeerMembership{StreamName: "live+valid", TenantID: "tenant-a", SourceGeneration: "generation-valid", SourceRevision: 1, Peers: []StreamPeerTarget{{ClusterID: "valid", Addr: "valid:18019"}}}); err != nil {
		t.Fatalf("SetStreamPeerMembership: %v", err)
	}
	if err := cache.client.HSet(ctx, cache.keyStreamPeerMemberships(), "malformed", `{"stream_name":`).Err(); err != nil {
		t.Fatalf("seed malformed stream membership: %v", err)
	}
	if _, err := cache.LoadAllStreamPeerMemberships(ctx); err == nil {
		t.Fatal("partial stream-peer snapshot succeeded despite malformed record")
	}
}

func TestRunAsLeader_MalformedMembershipNeverBecomesReady(t *testing.T) {
	cache, _ := setupTestCache(t)
	if err := cache.client.HSet(context.Background(), cache.keyStreamPeerMemberships(), "malformed", `{"stream_name":`).Err(); err != nil {
		t.Fatalf("seed malformed stream membership: %v", err)
	}
	pm := newTestPeerManager(t, "cluster-a", cache, false)
	pm.runAsLeader()
	if pm.IsLeader() {
		t.Fatal("leader advertised readiness after incomplete authority reconstruction")
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
	for revision, stream := range []struct{ name, tenant string }{{"live+alpha", "tenant-a"}, {"live+beta", "tenant-b"}} {
		_, _ = pm.TrackStream(ctx, stream.name, stream.tenant, "generation-"+stream.name, int64(revision+1), []control.AdmissionPeerHint{{ClusterID: "remote-1", Addr: "remote:18019"}})
	}

	_ = pm.UntrackStream(context.Background(), "live+alpha", "tenant-a", "generation-live+alpha", 1)

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

	memberships, err := cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil {
		t.Fatalf("LoadAllStreamPeerMemberships: %v", err)
	}
	if alpha := memberships["live+alpha"]; alpha.Active || alpha.SourceRevision != 1 {
		t.Fatalf("expected Redis membership to retain alpha's ended fence, got %v", memberships)
	}
	if beta := memberships["live+beta"]; !beta.Active || beta.TenantID != "tenant-b" {
		t.Fatalf("expected Redis membership to retain active beta, got %v", memberships)
	}
}

func TestRecvLoop_CachesPeerPayloads(t *testing.T) {
	cache, _ := setupTestCache(t)
	// Stream-ad + playback-index storage now lives on the unified registry.
	priorRegistry := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })
	pm := newTestPeerManager(t, "cluster-a", cache, false)
	ctx := context.Background()
	peerID := "remote-1"

	if applied, err := cache.ApplyRemoteStreamLifecycle(ctx, "tenant-a", "dead+stream", &RemoteLiveStreamEntry{
		ClusterID: peerID, TenantID: "tenant-a", SourceRevision: 1, UpdatedAt: time.Now().Unix(),
	}, true); err != nil || !applied {
		t.Fatalf("seed remote live stream: applied=%v err=%v", applied, err)
	}
	// Pre-seed the registry as if a prior ad already placed the entry.
	// The withdrawal message later in this test will clear it.
	registry.UpsertFederatedSource(peerID, control.StreamEntry{
		TenantID:     "tenant-a",
		PlaybackID:   "play-del",
		InternalName: "live+ad",
	}, control.Location{IsLiveNow: true, AdTimestamp: time.Now().Unix()})

	stream := &testPeerChannelStream{
		messages: []*foghornfederationpb.PeerMessage{
			{
				ClusterId: peerID,
				Payload: &foghornfederationpb.PeerMessage_EdgeTelemetry{EdgeTelemetry: &foghornfederationpb.EdgeTelemetry{
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
				Payload: &foghornfederationpb.PeerMessage_ReplicationEvent{ReplicationEvent: &foghornfederationpb.ReplicationEvent{
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
				Payload: &foghornfederationpb.PeerMessage_ClusterSummary{ClusterSummary: &foghornfederationpb.ClusterEdgeSummary{
					Edges: []*foghornfederationpb.EdgeSnapshot{{
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
				Payload: &foghornfederationpb.PeerMessage_StreamLifecycle{StreamLifecycle: &foghornfederationpb.StreamLifecycleEvent{
					InternalName:   "live+stream",
					TenantId:       "tenant-a",
					ClusterId:      peerID,
					IsLive:         true,
					SourceRevision: 1,
				}},
			},
			{
				ClusterId: peerID,
				Payload: &foghornfederationpb.PeerMessage_StreamLifecycle{StreamLifecycle: &foghornfederationpb.StreamLifecycleEvent{
					InternalName:   "dead+stream",
					TenantId:       "tenant-a",
					ClusterId:      peerID,
					IsLive:         false,
					SourceRevision: 2,
				}},
			},
			{
				ClusterId: peerID,
				Payload: &foghornfederationpb.PeerMessage_StreamAd{StreamAd: &foghornfederationpb.StreamAdvertisement{
					InternalName:    "live+ad2",
					TenantId:        "tenant-a",
					PlaybackId:      "play-2",
					OriginClusterId: peerID,
					IsLive:          true,
					Edges: []*foghornfederationpb.PeerStreamEdge{{
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
				Payload: &foghornfederationpb.PeerMessage_StreamAd{StreamAd: &foghornfederationpb.StreamAdvertisement{
					InternalName: "live+ad",
					TenantId:     "tenant-a",
					IsLive:       false,
					Timestamp:    time.Now().Unix(),
				}},
			},
			{
				ClusterId: peerID,
				Payload: &foghornfederationpb.PeerMessage_ArtifactAd{ArtifactAd: &foghornfederationpb.ArtifactAdvertisement{
					Artifacts: []*foghornfederationpb.ArtifactLocation{{
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
				Payload: &foghornfederationpb.PeerMessage_PeerHeartbeat{PeerHeartbeat: &foghornfederationpb.PeerHeartbeat{
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
				Payload: &foghornfederationpb.PeerMessage_CapacitySummary{CapacitySummary: &foghornfederationpb.CapacitySummary{
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

	liveEntry, err := cache.GetRemoteLiveStream(ctx, "tenant-a", "live+stream")
	if err != nil || liveEntry == nil {
		t.Fatalf("expected live stream cached, entry=%v err=%v", liveEntry, err)
	}

	deadEntry, err := cache.GetRemoteLiveStream(ctx, "tenant-a", "dead+stream")
	if err != nil {
		t.Fatalf("GetRemoteLiveStream(dead+stream): %v", err)
	}
	if deadEntry != nil {
		t.Fatalf("expected dead+stream to be deleted, got %+v", deadEntry)
	}

	// Stream ad + playback-id reverse index now resolve via the unified
	// registry. The withdrawal ad for live+ad (IsLive=false) drops the
	// entry and clears the play-del reverse index.
	ad2Entry, ad2Err := registry.ResolveSourceByInternalName(ctx, "live+ad2")
	if ad2Err != nil {
		t.Fatalf("expected live+ad2 in registry: %v", ad2Err)
	}
	if _, ok := ad2Entry.Locations[peerID]; !ok {
		t.Fatalf("expected Locations[%q] for live+ad2", peerID)
	}
	if byPlay, lookupErr := registry.ResolveSourceByPlaybackID(ctx, "play-2"); lookupErr != nil || byPlay.InternalName != "live+ad2" {
		t.Fatalf("expected play-2 -> live+ad2, got %q err=%v", byPlay.InternalName, lookupErr)
	}

	// Withdrawn entries no longer answer from cache; with no Commodore
	// client wired the fall-through hydrate reports transient
	// ErrRegistryUnavailable (not ErrUnknownStream — nil client must never
	// look like authoritative not-found).
	if _, lookupErr := registry.ResolveSourceByInternalName(ctx, "live+ad"); !errors.Is(lookupErr, control.ErrRegistryUnavailable) {
		t.Fatalf("expected live+ad withdrawn, got err=%v", lookupErr)
	}
	if _, lookupErr := registry.ResolveSourceByPlaybackID(ctx, "play-del"); !errors.Is(lookupErr, control.ErrRegistryUnavailable) {
		t.Fatalf("expected play-del cleared, got err=%v", lookupErr)
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
	pm.streamMemberships["stream-a"] = StreamPeerMembership{
		StreamName: "stream-a", TenantID: "tenant-a", SourceGeneration: "generation-a", SourceRevision: 7, Active: true,
	}
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

	flush := pm.wireTestWriters()
	pm.pushTelemetry()
	flush()

	if len(allowed.sent) != 2 {
		t.Fatalf("expected allowed peer to receive telemetry+lifecycle (2 msgs), got %d", len(allowed.sent))
	}
	if lifecycle := allowed.sent[1].GetStreamLifecycle(); lifecycle == nil || lifecycle.GetSourceRevision() != 7 {
		t.Fatalf("expected lifecycle heartbeat revision 7, got %+v", lifecycle)
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

	flush := pm.wireTestWriters()
	pm.pushSummary()
	flush()

	if len(out.sent) != 1 {
		t.Fatalf("expected 1 summary message, got %d", len(out.sent))
	}
	msg := out.sent[0]
	payload, ok := msg.Payload.(*foghornfederationpb.PeerMessage_ClusterSummary)
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
	sm.SetNodeArtifacts("node-a", []*ipcpb.StoredArtifact{{
		ClipHash:     "clip-1",
		StreamName:   "stream-a",
		FilePath:     "/tmp/clip-1.mp4",
		SizeBytes:    123,
		AccessCount:  5,
		LastAccessed: time.Now().Unix(),
		ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
	}}, state.ArtifactReportOrder{Fence: 1, Seq: 1})

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.pool = newFoghornPoolAdapter(newNoopPool(t))

	out := &capturePeerChannelStream{}
	pm.mu.Lock()
	pm.peers["peer-1"] = &peerState{connected: true, stream: out, lifecycle: peerAlwaysOn}
	pm.mu.Unlock()

	flush := pm.wireTestWriters()
	pm.pushArtifacts()
	flush()

	if len(out.sent) != 1 {
		t.Fatalf("expected 1 artifact advertisement, got %d", len(out.sent))
	}
	payload, ok := out.sent[0].Payload.(*foghornfederationpb.PeerMessage_ArtifactAd)
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

	flush := pm.wireTestWriters()
	pm.pushStreamAds()
	flush()

	if len(allowed.sent) != 1 {
		t.Fatalf("expected allowed peer to receive one stream ad, got %d", len(allowed.sent))
	}
	if len(blocked.sent) != 0 {
		t.Fatalf("expected blocked peer to receive zero stream ads, got %d", len(blocked.sent))
	}
	payload, ok := allowed.sent[0].Payload.(*foghornfederationpb.PeerMessage_StreamAd)
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

	flush := pm.wireTestWriters()
	pm.pushHeartbeat()
	flush()

	if len(out.sent) != 1 {
		t.Fatalf("expected 1 heartbeat message, got %d", len(out.sent))
	}
	payload, ok := out.sent[0].Payload.(*foghornfederationpb.PeerMessage_PeerHeartbeat)
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

func TestReconciliationReservesSinglePeerRunnerBeforeLaunch(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, true)
	pm.reconnectBackoff = time.Millisecond
	client := &fakeFederationClient{
		openFunc: func(ctx context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
			return &blockingPeerChannelStream{ctx: ctx}, nil
		},
	}
	pm.pool = &fakeFederationPool{getOrCreate: func(_, _ string) (federationPeerClient, error) {
		return client, nil
	}}
	hints := map[string]PeerHint{"remote-1": {Addr: "remote-1:18029", AlwaysOn: true}}

	pm.mu.Lock()
	first := pm.reconcilePeerHintsLocked(hints)
	second := pm.reconcilePeerHintsLocked(hints)
	pm.mu.Unlock()
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("runner reservations = first:%d second:%d, want 1 then 0", len(first), len(second))
	}

	runnerDone := make(chan struct{})
	go func() {
		pm.connectPeer(first[0])
		close(runnerDone)
	}()
	waitFor(t, 100*time.Millisecond, func() bool { return client.openCount() == 1 }, "reserved runner did not open PeerChannel")

	pm.mu.Lock()
	third := pm.reconcilePeerHintsLocked(hints)
	pm.mu.Unlock()
	if len(third) != 0 {
		t.Fatalf("connected runner allowed %d duplicate launch requests", len(third))
	}

	pm.mu.Lock()
	pm.reconcilePeerHintsLocked(map[string]PeerHint{})
	pm.mu.Unlock()
	select {
	case <-runnerDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("authority revocation did not stop the reserved runner")
	}
	if got := client.openCount(); got != 1 {
		t.Fatalf("PeerChannel opens = %d, want exactly one", got)
	}
}

func TestStalePeerRunnerCleanupCannotClearSuccessorConnection(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, true)
	successorStream := &capturePeerChannelStream{}
	successorSendCh := make(chan *foghornfederationpb.PeerMessage, 1)
	canceled := false
	ps := &peerState{
		addr: "remote-1:18029", runnerToken: 2, connected: true,
		stream: successorStream, sendCh: successorSendCh, cancel: func() { canceled = true },
	}
	pm.mu.Lock()
	pm.peers["remote-1"] = ps
	pm.mu.Unlock()

	pm.releasePeerRunner(peerConnectRequest{clusterID: "remote-1", state: ps, token: 1})
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if canceled || ps.runnerToken != 2 || !ps.connected || ps.stream != successorStream || ps.sendCh != successorSendCh || ps.cancel == nil {
		t.Fatalf("stale runner cleanup mutated successor state: %+v", ps)
	}
}

func TestConnectPeer_NoPoolReturns(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)
	ps := &peerState{addr: "unused:18029"}
	request := reserveTestPeerRunner(t, pm, "remote-1", ps)
	pm.connectPeer(request)
	if ps.runnerToken != 0 {
		t.Fatal("runner reservation was not released when no pool was configured")
	}
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
	pm.peers["remote-1"] = &peerState{addr: "old:18029", lastRefresh: time.Now()}
	pm.mu.Unlock()

	pm.refreshPeers()

	if addr := pm.GetPeerAddr("remote-1"); addr != "old:18029" {
		t.Fatalf("expected existing peer to be preserved, got addr=%q", addr)
	}
}

func TestRefreshPeers_ReconcilesAddUpdateAndRemove(t *testing.T) {
	pm := newTestPeerManager(t, "cluster-a", nil, false)

	unchanged := &peerState{
		addr:      "same:18029",
		connected: true,
		tenantIDs: []string{"tenant-old"},
	}
	changedCanceled := 0
	changed := &peerState{
		addr:      "old:18029",
		connected: true,
		cancel: func() {
			changedCanceled++
		},
	}
	staleCanceled := 0
	stale := &peerState{
		addr: "stale:18029",
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
		resp: &quartermasterpb.ListPeersResponse{
			Peers: []*quartermasterpb.PeerCluster{
				{ClusterId: "same", FoghornAddr: "same:18029", SharedTenantIds: []string{"tenant-a"}},
				{ClusterId: "changed", FoghornAddr: "new:18029", SharedTenantIds: []string{"tenant-b"}},
				{ClusterId: "new-peer", FoghornAddr: "new-peer:18029", SharedTenantIds: []string{"tenant-c"}},
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
		t.Fatalf("expected authoritative tenant replacement for unchanged peer, got %v", pm.peers["same"].tenantIDs)
	}

	updated := pm.peers["changed"]
	if updated == nil {
		t.Fatal("expected changed peer to exist")
	}
	if updated != changed {
		t.Fatal("expected changed peer state to be updated in place")
	}
	if updated.addr != "new:18029" {
		t.Fatalf("expected changed peer address to update, got %q", updated.addr)
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
				openFunc: func(context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
					return &capturePeerChannelStream{}, nil
				},
			}, nil
		},
	}
	pm.pool = pool

	ps := &peerState{addr: "remote-1:18029", runnerToken: 1}
	pm.connectPeer(peerConnectRequest{clusterID: "remote-1", state: ps, token: 1})
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

	ps := &peerState{addr: "remote-1:18029"}
	request := reserveTestPeerRunner(t, pm, "remote-1", ps)

	done := make(chan struct{})
	go func() {
		pm.connectPeer(request)
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
		openFunc: func(context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
			return nil, errors.New("open failed")
		},
	}
	pool := &fakeFederationPool{
		getOrCreate: func(_, _ string) (federationPeerClient, error) {
			return client, nil
		},
	}
	pm.pool = pool

	ps := &peerState{addr: "remote-1:18029"}
	request := reserveTestPeerRunner(t, pm, "remote-1", ps)

	done := make(chan struct{})
	go func() {
		pm.connectPeer(request)
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
		openFunc: func(context.Context) (foghornfederationpb.FoghornFederation_PeerChannelClient, error) {
			return &capturePeerChannelStream{}, nil
		},
	}
	pool := &fakeFederationPool{
		getOrCreate: func(_, _ string) (federationPeerClient, error) {
			return client, nil
		},
	}
	pm.pool = pool

	ps := &peerState{addr: "remote-1:18029"}
	request := reserveTestPeerRunner(t, pm, "remote-1", ps)

	done := make(chan struct{})
	go func() {
		pm.connectPeer(request)
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
