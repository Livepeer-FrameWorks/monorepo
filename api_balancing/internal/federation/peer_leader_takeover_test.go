package federation

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/grpc"
)

// adRecordingPeer is a peer cell's replica that records every stream
// advertisement arriving on a PeerChannel.
type adRecordingPeer struct {
	foghornfederationpb.UnimplementedFoghornFederationServer
	ads chan *foghornfederationpb.StreamAdvertisement
}

func (s *adRecordingPeer) PeerChannel(stream foghornfederationpb.FoghornFederation_PeerChannelServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if ad := msg.GetStreamAd(); ad != nil {
			select {
			case s.ads <- ad:
			default:
			}
		}
	}
}

func serveAdRecordingPeer(t *testing.T) (string, *adRecordingPeer) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	peer := &adRecordingPeer{ads: make(chan *foghornfederationpb.StreamAdvertisement, 64)}
	foghornfederationpb.RegisterFoghornFederationServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	return listener.Addr().String(), peer
}

func awaitStreamAd(t *testing.T, peer *adRecordingPeer, internalName string, within time.Duration) time.Time {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ad := <-peer.ads:
			if ad.GetInternalName() == internalName {
				return time.Now()
			}
		case <-deadline:
			t.Fatalf("no advertisement of %s reached the peer within %s", internalName, within)
		}
	}
}

func newPeerTestPool(t *testing.T) federationPeerPool {
	t.Helper()
	pool := foghorn.NewPool(foghorn.PoolConfig{AllowInsecure: true, ServiceToken: "peer-test", Logger: testLogger()})
	t.Cleanup(func() { _ = pool.Close() })
	return newFoghornPoolAdapter(pool)
}

// A newly connected PeerChannel carries this cell's live streams at once. The
// periodic push runs every streamAdPushInterval; no ticker runs here, so an
// advertisement can only come from the connection itself.
func TestConnectPeer_AdvertisesLiveStreamsWhenTheChannelConnects(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")
	addr, peer := serveAdRecordingPeer(t)

	pm := newTestPeerManager(t, "cluster-a", nil, true)
	pm.pool = newPeerTestPool(t)
	ps := &peerState{addr: addr, lifecycle: peerAlwaysOn, controlCellID: "cell-b"}
	request := reserveTestPeerRunner(t, pm, "cluster-b", ps)
	go pm.connectPeer(request)

	awaitStreamAd(t, peer, "stream-a", streamAdPushInterval/2)
}

// When the replica holding the peer-manager lease dies, the standby takes the
// lease and advertises the cell's streams before peers drop them. A peer drops
// an advertised source 30s after the edge report it carries, and that report
// is up to one Helmsman report interval plus the whole-second truncation old
// when sent. What remains after the dead leader's lease runs out is the budget
// for the standby to notice, connect and advertise.
func TestStandbyLeaderAdvertisesBeforePeersDropTheCellsStreams(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedFederationNodeAndStream(t, sm, "node-a", "stream-a", "tenant-a")
	addr, peer := serveAdRecordingPeer(t)

	cache, mr := setupTestCache(t)
	ctx := context.Background()
	if !cache.TryAcquireLeaderLease(ctx, leaderRole, "crashed-leader") {
		t.Fatal("setup: crashed leader could not take the lease")
	}
	if err := cache.PublishPeerHints(ctx, "crashed-leader", map[string]PeerHint{
		"cluster-b": {Addr: addr, AlwaysOn: true, ControlCellID: "cell-b"},
	}); err != nil {
		t.Fatalf("setup: publish peer hints: %v", err)
	}

	pm := newTestPeerManager(t, "cluster-a", cache, false)
	pm.instanceID = "standby"
	pm.pool = newPeerTestPool(t)
	runDone := make(chan struct{})
	go func() {
		pm.run()
		close(runDone)
	}()
	t.Cleanup(func() {
		select {
		case <-pm.done:
		default:
			close(pm.done)
		}
		<-runDone
	})

	// The standby's first attempt meets the crashed leader's live lease.
	time.Sleep(200 * time.Millisecond)
	if pm.IsLeader() {
		t.Fatal("standby took a lease another instance still held")
	}

	const (
		peerSourceEvidence = 30 * time.Second
		edgeReportAge      = 10*time.Second + time.Second
	)
	budget := peerSourceEvidence - edgeReportAge - leaderLeaseTTL
	mr.Del(fmt.Sprintf("{%s}:leader:%s", "cluster-a", leaderRole))
	expired := time.Now()

	advertised := awaitStreamAd(t, peer, "stream-a", budget)
	if !pm.IsLeader() {
		t.Fatal("advertisement arrived but the standby does not hold the lease")
	}
	t.Logf("standby advertised %s after the dead leader's lease ran out (budget %s)", advertised.Sub(expired).Round(time.Millisecond), budget)
}
