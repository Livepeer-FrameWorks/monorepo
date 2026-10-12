package federation

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// switchableQuartermaster answers ListPeers with its current peer set until it
// is taken down, then answers Unavailable.
type switchableQuartermaster struct {
	mu    sync.Mutex
	peers []*quartermasterpb.PeerCluster
	down  bool
	calls int
}

func (q *switchableQuartermaster) ListPeers(context.Context, string) (*quartermasterpb.ListPeersResponse, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.down {
		return nil, status.Error(codes.Unavailable, "quartermaster is down")
	}
	return &quartermasterpb.ListPeersResponse{Peers: append([]*quartermasterpb.PeerCluster(nil), q.peers...)}, nil
}

func (q *switchableQuartermaster) set(down bool, peers ...*quartermasterpb.PeerCluster) {
	q.mu.Lock()
	q.down = down
	if !down {
		q.peers = peers
	}
	q.mu.Unlock()
}

func peerCellB(addr string) *quartermasterpb.PeerCluster {
	return &quartermasterpb.PeerCluster{ClusterId: "cluster-b", FoghornAddr: addr, ControlCellId: "cell-b"}
}

func hasPeer(pm *PeerManager, clusterID string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	_, ok := pm.peers[clusterID]
	return ok
}

// Quartermaster is the only writer of the federation peer set, so while it is
// unreachable the set it last answered stays in force, however long the
// outage lasts: the leader keeps the peer and keeps its lease in Redis alive.
func TestQuartermasterOutage_PeerSetOutlivesTheRefreshWindow(t *testing.T) {
	cache, mr := setupTestCache(t)
	qm := &switchableQuartermaster{}
	qm.set(false, peerCellB("foghorn-b:18029"))
	pm := newTestPeerManager(t, "cluster-a", cache, true)
	pm.peerDiscovery = qm
	pm.refreshPeers()
	if !hasPeer(pm, "cluster-b") {
		t.Fatal("setup: Quartermaster's peer was not applied")
	}

	qm.set(true)
	// The outage has outlasted every window a confirmation used to be bound to:
	// the periodic refresh, its Redis lease, and the maintenance republish.
	pm.mu.Lock()
	pm.quartermasterHintsRefreshedAt = time.Now().Add(-(peerRefreshInterval + peerAddrTTL + time.Minute))
	pm.mu.Unlock()
	mr.FastForward(peerAddrTTL + time.Second)
	pm.refreshPeers()
	pm.publishQuartermasterHints()
	if err := pm.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !hasPeer(pm, "cluster-b") || pm.GetPeerAddr("cluster-b") != "foghorn-b:18029" {
		t.Fatal("leader dropped Quartermaster's last peer set while Quartermaster was unreachable")
	}
	standby := newTestPeerManager(t, "cluster-a", cache, false)
	standby.instanceID = "standby"
	if err := standby.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("standby reconcile: %v", err)
	}
	if standby.GetPeerAddr("cluster-b") != "foghorn-b:18029" {
		t.Fatal("standby replica lost the peer address while Quartermaster was unreachable")
	}
}

// A leadership handover during a Quartermaster outage keeps the peer set: the
// new leader starts from Quartermaster's last answer and keeps advertising the
// cell's streams to the peer, so the peer's viewers of them are still served.
func TestQuartermasterOutage_NewLeaderKeepsPeersAndAdvertises(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")
	addr, peer := serveAdRecordingPeer(t)
	cache, _ := setupTestCache(t)
	qm := &switchableQuartermaster{}
	qm.set(false, peerCellB(addr))

	first := newTestPeerManager(t, "cluster-a", cache, false)
	first.instanceID = "leader-1"
	first.peerDiscovery = qm
	first.pool = newPeerTestPool(t)
	firstDone := make(chan struct{})
	acquireTestLease(t, first)
	go func() { first.runAsLeader(); close(firstDone) }()
	awaitStreamAd(t, peer, "stream-a", 5*time.Second)

	qm.set(true)
	close(first.done)
	<-firstDone
	for len(peer.ads) > 0 {
		<-peer.ads
	}

	second := newTestPeerManager(t, "cluster-a", cache, false)
	second.instanceID = "leader-2"
	second.peerDiscovery = qm
	second.pool = newPeerTestPool(t)
	secondDone := make(chan struct{})
	acquireTestLease(t, second)
	go func() { second.runAsLeader(); close(secondDone) }()
	t.Cleanup(func() {
		select {
		case <-second.done:
		default:
			close(second.done)
		}
		<-secondDone
	})
	awaitStreamAd(t, peer, "stream-a", 5*time.Second)
	if second.GetPeerAddr("cluster-b") != addr {
		t.Fatal("new leader has no address for the peer Quartermaster last named")
	}
}

// An explicit Quartermaster answer still removes a peer, and a later leader
// does not bring it back from the stored peer set while Quartermaster is down.
func TestQuartermasterRemovalStillRemovesThePeer(t *testing.T) {
	cache, _ := setupTestCache(t)
	qm := &switchableQuartermaster{}
	qm.set(false, peerCellB("foghorn-b:18029"))
	pm := newTestPeerManager(t, "cluster-a", cache, true)
	pm.peerDiscovery = qm
	pm.refreshPeers()
	if !hasPeer(pm, "cluster-b") {
		t.Fatal("setup: Quartermaster's peer was not applied")
	}
	qm.set(false)
	pm.refreshPeers()
	if hasPeer(pm, "cluster-b") {
		t.Fatal("Quartermaster removed the peer but the leader kept it")
	}

	qm.set(true)
	next := newTestPeerManager(t, "cluster-a", cache, false)
	next.instanceID = "leader-2"
	next.peerDiscovery = qm
	done := make(chan struct{})
	acquireTestLease(t, next)
	go func() { next.runAsLeader(); close(done) }()
	waitFor(t, 2*time.Second, next.IsLeader, "next leader ready")
	if hasPeer(next, "cluster-b") {
		t.Fatal("a later leader restored a peer Quartermaster had removed")
	}
	close(next.done)
	<-done
}

// The leader renews its lease from the same loop that refreshes peers, so a
// refresh against an unreachable Quartermaster must neither wait for the
// connection nor run long enough to let the lease lapse between renewals.
func TestRefreshPeersIsBoundedInsideTheLeaderLease(t *testing.T) {
	var deadline time.Duration
	var singleAttempt bool
	pm := newTestPeerManager(t, "cluster-a", nil, true)
	pm.peerDiscovery = peerDiscoveryFunc(func(ctx context.Context) (*quartermasterpb.ListPeersResponse, error) {
		if until, ok := ctx.Deadline(); ok {
			deadline = time.Until(until)
		}
		singleAttempt = clients.IsSingleAttempt(ctx)
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	})
	started := time.Now()
	pm.refreshPeers()
	elapsed := time.Since(started)
	if !singleAttempt {
		t.Fatal("peer refresh waits for a ready Quartermaster connection and retries")
	}
	if budget := leaderLeaseTTL - streamAdPushInterval - time.Second; deadline <= 0 || deadline > budget || elapsed > budget {
		t.Fatalf("peer refresh deadline %s (took %s); a renewal one advertisement interval later must land inside the %s lease", deadline, elapsed, leaderLeaseTTL)
	}
}

// acquireTestLease takes the leader lease the way run does before it starts a
// leadership term.
func acquireTestLease(t *testing.T, pm *PeerManager) {
	t.Helper()
	if !pm.tryAcquireLease() {
		t.Fatalf("%s could not take the leader lease", pm.instanceID)
	}
}

type peerDiscoveryFunc func(context.Context) (*quartermasterpb.ListPeersResponse, error)

func (fn peerDiscoveryFunc) ListPeers(ctx context.Context, _ string) (*quartermasterpb.ListPeersResponse, error) {
	return fn(ctx)
}

// Quartermaster names a peer cluster without a Foghorn address while every
// Foghorn of that cluster is restarting and not yet healthy again. The cluster
// still shares tenants with this cell, so the leader keeps its last known
// address, in memory and in the stored peer set that standbys and the next
// leader read, and keeps redialing it. Only a cluster Quartermaster no longer
// names at all is removed.
func TestQuartermasterPeerWithoutHealthyFoghornKeepsLastAddress(t *testing.T) {
	cache, _ := setupTestCache(t)
	qm := &switchableQuartermaster{}
	qm.set(false, &quartermasterpb.PeerCluster{
		ClusterId:       "us-primary",
		SharedTenantIds: []string{"tenant-a"},
		FoghornAddr:     "foghorn-b:18019",
		FoghornAddrs:    []string{"foghorn-b:18019", "foghorn-b-2:18019"},
		ControlCellId:   "cell-b",
	})
	pm := newTestPeerManager(t, "cluster-a", cache, true)
	pm.peerDiscovery = qm
	pm.refreshPeers()
	if pm.GetPeerAddr("us-primary") != "foghorn-b:18019" {
		t.Fatal("setup: Quartermaster's peer was not applied")
	}

	qm.set(false, &quartermasterpb.PeerCluster{
		ClusterId:       "us-primary",
		SharedTenantIds: []string{"tenant-a"},
		FoghornAddr:     "",
		ControlCellId:   "cell-b",
	})
	pm.refreshPeers()
	if got := pm.GetPeerAddr("us-primary"); got != "foghorn-b:18019" {
		t.Fatalf("leader peer address = %q while the peer's Foghorns restart; want the last known foghorn-b:18019", got)
	}
	ctx := context.Background()
	stored, _, found, err := cache.LoadQuartermasterPeerSnapshot(ctx)
	if err != nil || !found {
		t.Fatalf("load stored peer set: found=%v err=%v", found, err)
	}
	if hint, ok := stored["us-primary"]; !ok || hint.Addr != "foghorn-b:18019" || !slices.Contains(hint.Addrs, "foghorn-b-2:18019") {
		t.Fatalf("stored peer set lost the peer's last known addresses: %+v", stored)
	}
	standby := newTestPeerManager(t, "cluster-a", cache, false)
	standby.instanceID = "standby"
	if err = standby.loadPeerAddressesFromRedis(); err != nil {
		t.Fatalf("standby reconcile: %v", err)
	}
	if got := standby.GetPeerAddr("us-primary"); got != "foghorn-b:18019" {
		t.Fatalf("standby peer address = %q while the peer's Foghorns restart; want foghorn-b:18019", got)
	}

	qm.set(false)
	pm.refreshPeers()
	if hasPeer(pm, "us-primary") {
		t.Fatal("Quartermaster no longer names the peer cluster but the leader kept it")
	}
	stored, _, _, err = cache.LoadQuartermasterPeerSnapshot(ctx)
	if err != nil {
		t.Fatalf("load stored peer set: %v", err)
	}
	if _, ok := stored["us-primary"]; ok {
		t.Fatal("stored peer set kept a cluster Quartermaster no longer names")
	}
}

// Without a previously known address there is nothing to dial, so a peer that
// Quartermaster names without a Foghorn address is not added.
func TestQuartermasterPeerWithoutAnyKnownAddressIsNotAdded(t *testing.T) {
	cache, _ := setupTestCache(t)
	qm := &switchableQuartermaster{}
	qm.set(false, &quartermasterpb.PeerCluster{ClusterId: "us-primary", SharedTenantIds: []string{"tenant-a"}})
	pm := newTestPeerManager(t, "cluster-a", cache, true)
	pm.peerDiscovery = qm
	pm.refreshPeers()
	if hasPeer(pm, "us-primary") {
		t.Fatal("leader added a peer with no address to dial")
	}
}
