package dns

import (
	"fmt"
	"net"
	"testing"
	"time"

	"frameworks/pkg/logging"
	"github.com/miekg/dns"
)

func TestUpdateRecordsStoresFQDNs(t *testing.T) {
	server := NewServer(logging.NewLogger(), 0)

	err := server.UpdateRecords(map[string][]string{
		"edge-1": {"10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, ok := server.records["edge-1.internal."]; !ok {
		t.Fatalf("expected fqdn record to be stored, got %+v", server.records)
	}
}

func TestUpdateRecordsRejectsInvalidIPAndPreservesState(t *testing.T) {
	server := NewServer(logging.NewLogger(), 0)

	err := server.UpdateRecords(map[string][]string{
		"edge-1": {"10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	err = server.UpdateRecords(map[string][]string{
		"edge-1": {"invalid-ip"},
	})
	if err == nil {
		t.Fatal("expected error for invalid ip")
	}

	if _, ok := server.records["edge-1.internal."]; !ok {
		t.Fatalf("expected previous records to be preserved, got %+v", server.records)
	}
}

func TestUpdateRecordsNormalizesToLowerCase(t *testing.T) {
	server := NewServer(logging.NewLogger(), 0)

	err := server.UpdateRecords(map[string][]string{
		"Edge-1": {"10.0.0.2"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, ok := server.records["edge-1.internal."]; !ok {
		t.Fatalf("expected normalized fqdn record to be stored, got %+v", server.records)
	}
	if _, ok := server.records["Edge-1.internal."]; ok {
		t.Fatalf("did not expect mixed-case fqdn record, got %+v", server.records)
	}
}

func TestNewServerNoDefaultUpstreams(t *testing.T) {
	s := NewServer(logging.NewLogger(), 0)
	if len(s.upstreams) != 0 {
		t.Fatalf("expected no default upstreams (SERVFAIL for non-.internal), got %d: %v", len(s.upstreams), s.upstreams)
	}
}

func TestNewServerCustomUpstreams(t *testing.T) {
	s := NewServer(logging.NewLogger(), 0, "9.9.9.9:53")
	if len(s.upstreams) != 1 {
		t.Fatalf("expected 1 custom upstream, got %d", len(s.upstreams))
	}
	if s.upstreams[0] != "9.9.9.9:53" {
		t.Fatalf("expected upstream 9.9.9.9:53, got %s", s.upstreams[0])
	}
}

func TestHandleForwardAllUpstreamsFail(t *testing.T) {
	// Use unreachable upstreams to test SERVFAIL response
	s := NewServer(logging.NewLogger(), 0, "192.0.2.1:53")

	addr, cleanup := startTestServer(t, s)
	defer cleanup()

	c := new(dns.Client)
	c.Timeout = 5 * time.Second // longer than the 2s forwarding timeout
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)

	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("exchange error: %v", err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("expected SERVFAIL, got rcode %d", resp.Rcode)
	}
}

func TestHandleInternalReturnsLocalRecord(t *testing.T) {
	s := NewServer(logging.NewLogger(), 0)
	s.UpdateRecords(map[string][]string{
		"quartermaster": {"10.0.0.5"},
	})

	addr, cleanup := startTestServer(t, s)
	defer cleanup()

	c := new(dns.Client)
	m := new(dns.Msg)
	m.SetQuestion("quartermaster.internal.", dns.TypeA)

	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("exchange error: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("expected success, got rcode %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A record, got %T", resp.Answer[0])
	}
	if a.A.String() != "10.0.0.5" {
		t.Fatalf("expected 10.0.0.5, got %s", a.A.String())
	}
}

func TestHandleInternalNXDOMAINForMissing(t *testing.T) {
	s := NewServer(logging.NewLogger(), 0)

	addr, cleanup := startTestServer(t, s)
	defer cleanup()

	c := new(dns.Client)
	m := new(dns.Msg)
	m.SetQuestion("missing.internal.", dns.TypeA)

	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("exchange error: %v", err)
	}
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN, got rcode %d", resp.Rcode)
	}
}

// startTestServer starts the DNS server on a random port and returns the address.
func startTestServer(t *testing.T, s *Server) (string, func()) {
	t.Helper()

	// Find a free port
	var lc net.ListenConfig
	ln, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.LocalAddr().(*net.UDPAddr).Port
	ln.Close()

	s.port = port
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Register handlers on a private mux to avoid global state conflicts
	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleForward)
	mux.HandleFunc("internal.", s.handleInternal)

	udpSrv := &dns.Server{Addr: addr, Net: "udp", Handler: mux}
	go udpSrv.ListenAndServe()

	// Wait until the server is accepting connections.
	// Use .internal. query so the readiness check doesn't trigger forwarding.
	ready := make(chan struct{})
	go func() {
		for {
			c := new(dns.Client)
			c.Timeout = 500 * time.Millisecond
			m := new(dns.Msg)
			m.SetQuestion("readiness.internal.", dns.TypeA)
			_, _, err := c.Exchange(m, addr)
			if err == nil {
				close(ready)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	<-ready

	return addr, func() { udpSrv.Shutdown() }
}
