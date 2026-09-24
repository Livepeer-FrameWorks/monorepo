package dns

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/miekg/dns"
)

func TestListenFDCountRequiresMatchingPID(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(key string) string { return vars[key] }
	}
	cases := []struct {
		name string
		vars map[string]string
		want int
	}{
		{"not activated", map[string]string{}, 0},
		{"matching pid", map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "2"}, 2},
		{"other process", map[string]string{"LISTEN_PID": "7", "LISTEN_FDS": "2"}, 0},
		{"garbage count", map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "x"}, 0},
		{"negative count", map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "-1"}, 0},
	}
	for _, tc := range cases {
		if got := listenFDCount(env(tc.vars), 42); got != tc.want {
			t.Errorf("%s: listenFDCount = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// passedSockets binds a UDP and TCP socket the way systemd does and returns
// dup'd descriptors, in the stream-then-datagram order systemd passes them
// for a unit listing ListenStream before ListenDatagram.
func passedSockets(t *testing.T) (udpAddr, tcpAddr string, files []*os.File) {
	t.Helper()
	var lc net.ListenConfig
	pc, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	udpFile, err := pc.(*net.UDPConn).File()
	if err != nil {
		t.Fatalf("udp file: %v", err)
	}
	tcpFile, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatalf("tcp file: %v", err)
	}
	udpAddr, tcpAddr = pc.LocalAddr().String(), ln.Addr().String()
	// Only the dup'd descriptors remain, as in a socket-activated process.
	_ = pc.Close()
	_ = ln.Close()
	return udpAddr, tcpAddr, []*os.File{tcpFile, udpFile}
}

func TestSocketsFromFilesClassifiesStreamAndDatagram(t *testing.T) {
	udpAddr, tcpAddr, files := passedSockets(t)
	pc, ln, err := socketsFromFiles(files)
	if err != nil {
		t.Fatalf("socketsFromFiles: %v", err)
	}
	defer pc.Close()
	defer ln.Close()
	if pc.LocalAddr().String() != udpAddr {
		t.Fatalf("packet conn addr = %s, want %s", pc.LocalAddr(), udpAddr)
	}
	if ln.Addr().String() != tcpAddr {
		t.Fatalf("listener addr = %s, want %s", ln.Addr(), tcpAddr)
	}
}

func TestSocketsFromFilesRejectsNonSocket(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-socket")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	pc, ln, err := socketsFromFiles([]*os.File{f})
	if err == nil || pc != nil || ln != nil {
		t.Fatalf("socketsFromFiles(regular file) = %v, %v, %v; want error and no sockets", pc, ln, err)
	}
}

func TestStartServesOnInheritedSockets(t *testing.T) {
	udpAddr, tcpAddr, files := passedSockets(t)

	s := NewServer(logging.NewLogger(), 0)
	if err := s.UpdateRecords(map[string][]string{"quartermaster": {"10.0.0.5"}}); err != nil {
		t.Fatalf("UpdateRecords: %v", err)
	}
	s.inheritSockets = func() (net.PacketConn, net.Listener, error) { return socketsFromFiles(files) }
	s.Start()
	defer s.Stop()

	for _, target := range []struct{ net, addr string }{{"udp", udpAddr}, {"tcp", tcpAddr}} {
		c := &dns.Client{Net: target.net, Timeout: 500 * time.Millisecond}
		m := new(dns.Msg)
		m.SetQuestion("quartermaster.internal.", dns.TypeA)
		var (
			resp *dns.Msg
			err  error
		)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			resp, _, err = c.Exchange(m, target.addr)
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s query on inherited socket %s: %v", target.net, target.addr, err)
		}
		if len(resp.Answer) != 1 {
			t.Fatalf("%s: expected 1 answer, got %d", target.net, len(resp.Answer))
		}
	}
}
