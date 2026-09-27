package federation

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/grpc"
)

// channelCountingServer accepts PeerChannels and records each one.
type channelCountingServer struct {
	foghornfederationpb.UnimplementedFoghornFederationServer
	opened chan struct{}
}

func (s *channelCountingServer) PeerChannel(stream foghornfederationpb.FoghornFederation_PeerChannelServer) error {
	select {
	case s.opened <- struct{}{}:
	default:
	}
	<-stream.Context().Done()
	return nil
}

func servePeerReplica(t *testing.T) (string, *channelCountingServer) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	counting := &channelCountingServer{opened: make(chan struct{}, 8)}
	foghornfederationpb.RegisterFoghornFederationServer(server, counting)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	return listener.Addr().String(), counting
}

// silentPeerReplica accepts TCP and never answers, like a replica whose host
// stopped responding.
func silentPeerReplica(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for _, conn := range held {
			_ = conn.Close()
		}
		mu.Unlock()
	})
	return listener.Addr().String()
}

func stoppedPeerReplica(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// This cell's advertisements reach the peer cell through whichever of its
// replicas takes the PeerChannel. With the replica the channel would start on
// stopped or unresponsive, the channel opens on another replica within a few
// seconds instead of waiting for that one to return.
func TestPeerChannelMovesToAnotherReplicaOfThePeerCell(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  func(*testing.T) string
		within time.Duration
	}{
		{"first replica stopped", stoppedPeerReplica, 3 * time.Second},
		{"first replica unresponsive", silentPeerReplica, peerChannelConnectTimeout + 3*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			liveAddr, live := servePeerReplica(t)
			firstAddr := tc.first(t)
			pool := foghorn.NewPool(foghorn.PoolConfig{AllowInsecure: true, ServiceToken: "peer-test", Logger: testLogger()})
			t.Cleanup(func() { _ = pool.Close() })

			pm := newTestPeerManager(t, "cluster-eu", nil, true)
			pm.pool = newFoghornPoolAdapter(pool)
			ps := &peerState{addr: firstAddr, addrs: []string{firstAddr, liveAddr}, controlCellID: "us-cell"}
			request := reserveTestPeerRunner(t, pm, "cluster-us", ps)
			go pm.connectPeer(request)

			select {
			case <-live.opened:
			case <-time.After(tc.within):
				t.Fatalf("no PeerChannel reached the running replica %s within %s while %s was down", liveAddr, tc.within, firstAddr)
			}
			waitFor(t, time.Second, func() bool {
				pm.mu.RLock()
				defer pm.mu.RUnlock()
				return ps.connected
			}, "peer not marked connected after the channel opened on another replica")
		})
	}
}
