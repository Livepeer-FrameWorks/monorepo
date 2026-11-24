package signalman

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

type quietSignalman struct {
	signalmanpb.UnimplementedSignalmanServiceServer
}

func (*quietSignalman) Subscribe(stream signalmanpb.SignalmanService_SubscribeServer) error {
	request, err := stream.Recv()
	if err != nil {
		return err
	}
	if err := stream.Send(&signalmanpb.ServerMessage{Message: &signalmanpb.ServerMessage_SubscriptionConfirmed{
		SubscriptionConfirmed: &signalmanpb.SubscriptionConfirmation{SubscribedChannels: request.GetSubscribe().GetChannels()},
	}}); err != nil {
		return err
	}
	tenant := "tenant-a"
	if err := stream.Send(&signalmanpb.ServerMessage{Message: &signalmanpb.ServerMessage_Event{
		Event: &signalmanpb.SignalmanEvent{Channel: signalmanpb.Channel_CHANNEL_SESSIONS, TenantId: &tenant},
	}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

// silentSignalmanProxy keeps TCP open and acknowledged while discarding the
// first transport's HTTP/2 traffic. New connections continue to reach the peer.
type silentSignalmanProxy struct {
	net.Listener
	blackhole   atomic.Bool
	connections atomic.Int64
	mu          sync.Mutex
	conns       []net.Conn
	closed      chan struct{}
	wg          sync.WaitGroup
}

func newSilentSignalmanProxy(t *testing.T, target string) *silentSignalmanProxy {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &silentSignalmanProxy{Listener: listener, closed: make(chan struct{})}
	t.Cleanup(func() {
		close(p.closed)
		_ = p.Close()
		p.mu.Lock()
		for _, conn := range p.conns {
			_ = conn.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	p.wg.Go(func() {
		for {
			client, err := p.Accept()
			if err != nil {
				return
			}
			netDialer := net.Dialer{Timeout: time.Second}
			peer, err := netDialer.DialContext(t.Context(), "tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			p.mu.Lock()
			select {
			case <-p.closed:
				p.mu.Unlock()
				_ = client.Close()
				_ = peer.Close()
				return
			default:
				p.conns = append(p.conns, client, peer)
			}
			p.mu.Unlock()
			first := p.connections.Add(1) == 1
			p.wg.Go(func() { p.pipe(peer, client, first) })
			p.wg.Go(func() { p.pipe(client, peer, first) })
		}
	})
	return p
}

func (p *silentSignalmanProxy) pipe(destination, source net.Conn, first bool) {
	buffer := make([]byte, 32*1024)
	for {
		n, err := source.Read(buffer)
		if n > 0 && (!first || !p.blackhole.Load()) {
			if _, writeErr := destination.Write(buffer[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func TestDialerRecoversASilentTransport(t *testing.T) {
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpcutil.NewServer()
	signalmanpb.RegisterSignalmanServiceServer(server, &quietSignalman{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	proxy := newSilentSignalmanProxy(t, listener.Addr().String())
	dialer, err := NewDialer(DialerConfig{AllowInsecure: true, ConnsPerAddr: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dialer.Close() })
	key := StreamKey{Channel: signalmanpb.Channel_CHANNEL_SESSIONS}
	stream, err := dialer.Open(t.Context(), proxy.Addr().String(), key)
	if err != nil {
		t.Fatal(err)
	}
	if event, recvErr := stream.Recv(); recvErr != nil || event.GetTenantId() != "tenant-a" {
		t.Fatalf("initial subscribed transport did not carry the scoped event: %v", recvErr)
	}
	proxy.blackhole.Store(true)
	ended := make(chan error, 1)
	go func() { _, recvErr := stream.Recv(); ended <- recvErr }()
	keepalive := grpcutil.ControlStreamKeepalive()
	bound := keepalive.Time + keepalive.Timeout + 5*time.Second
	select {
	case recvErr := <-ended:
		if recvErr == nil {
			t.Fatal("silent transport returned an event instead of failing")
		}
	case <-time.After(bound):
		stream.Close()
		t.Fatalf("silent Signalman transport remained subscribed beyond shared keepalive bound %s", bound)
	}
	stream.Close()
	recovered, err := dialer.Open(t.Context(), proxy.Addr().String(), key)
	if err != nil {
		t.Fatalf("pooled client did not reconnect to the recovered peer: %v", err)
	}
	defer recovered.Close()
	if event, recvErr := recovered.Recv(); recvErr != nil || event.GetTenantId() != "tenant-a" {
		t.Fatalf("replacement transport lost scoped event delivery: %v", recvErr)
	}
	if got := proxy.connections.Load(); got < 2 {
		t.Fatalf("connections=%d, want a replacement for the blackholed transport", got)
	}
}
