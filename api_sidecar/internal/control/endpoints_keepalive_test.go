package control

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"

	"google.golang.org/grpc/keepalive"
)

// blackholeProxy forwards TCP connections to target until it goes dark. From
// then on it keeps every socket open and acknowledges what the client writes,
// but passes nothing on in either direction: the client sees a Foghorn whose
// host stopped answering without closing the connection.
type blackholeProxy struct {
	addr   string
	target string
	dark   chan struct{}
	once   sync.Once

	mu    sync.Mutex
	conns []net.Conn
}

func startBlackholeProxy(t *testing.T, target string) *blackholeProxy {
	t.Helper()
	lis := listenLoopback(t)
	p := &blackholeProxy{addr: lis.Addr().String(), target: target, dark: make(chan struct{})}
	go func() {
		for {
			client, err := lis.Accept()
			if err != nil {
				return
			}
			var d net.Dialer
			upstream, err := d.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, client, upstream)
			p.mu.Unlock()
			go p.pump(upstream, client)
			go p.pump(client, upstream)
		}
	}()
	t.Cleanup(func() {
		_ = lis.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.conns {
			_ = c.Close()
		}
	})
	return p
}

// pump copies src to dst until the proxy goes dark, then reads and drops
// everything src sends. A closed src never closes dst once dark: a dead host
// does not send a FIN.
func (p *blackholeProxy) pump(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 && !p.isDark() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if !p.isDark() {
				_ = dst.Close()
			}
			return
		}
	}
}

func (p *blackholeProxy) isDark() bool {
	select {
	case <-p.dark:
		return true
	default:
		return false
	}
}

func (p *blackholeProxy) goDark() {
	p.once.Do(func() { close(p.dark) })
}

// A Foghorn that stops answering without closing the connection is left for
// another instance of the cell once a keepalive ping goes unanswered, not
// after TCP gives up on its retransmissions.
func TestControlClientFailsOverWhenFoghornStopsAnswering(t *testing.T) {
	// gRPC raises any client ping interval below 10s to 10s.
	prevKeepalive := controlKeepalive
	controlKeepalive = keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 2 * time.Second}
	t.Cleanup(func() { controlKeepalive = prevKeepalive })

	silent := startFakeFoghorn(t, false)
	proxy := startBlackholeProxy(t, silent.addr)
	staying := startFakeFoghorn(t, false)
	runControlDialer(t, []string{proxy.addr, staying.addr}, staying.addr, silent, staying)

	awaitRegistration(t, silent, 10*time.Second)
	proxy.goDark()
	darkAt := time.Now()

	bound := controlKeepalive.Time + controlKeepalive.Timeout + 3*time.Second
	registeredAt := awaitRegistration(t, staying, bound)
	if gap := registeredAt.Sub(darkAt); gap > bound {
		t.Fatalf("registered with the next instance %s after the Foghorn stopped answering, want within %s", gap, bound)
	}
}

// Foghorn answers pings that come faster than its enforcement policy admits
// with a too_many_pings GOAWAY, which would drop healthy control streams.
func TestControlKeepaliveRespectsFoghornEnforcementPolicy(t *testing.T) {
	if controlKeepalive != grpcutil.ControlStreamKeepalive() {
		t.Fatalf("control keepalive = %+v, want grpcutil.ControlStreamKeepalive() %+v", controlKeepalive, grpcutil.ControlStreamKeepalive())
	}
	if controlKeepalive.Time < grpcutil.KeepaliveMinTime {
		t.Fatalf("control stream pings every %s, below Foghorn's enforcement minimum %s", controlKeepalive.Time, grpcutil.KeepaliveMinTime)
	}
	if controlKeepalive.PermitWithoutStream {
		t.Fatal("control keepalive pings without an open stream; Foghorn's enforcement policy refuses that")
	}
}
