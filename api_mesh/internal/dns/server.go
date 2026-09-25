package dns

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
)

type Server struct {
	logger    logging.Logger
	udp       *dns.Server
	tcp       *dns.Server
	records   map[string][]string // hostname.internal. -> [IPs]
	mu        sync.RWMutex
	port      int
	upstreams []string // upstream resolver addresses for non-.internal queries
	// queries counts DNS responses by {type=internal|forward, status=ok|nxdomain|servfail|error}.
	queries *prometheus.CounterVec
	// inheritSockets returns pre-bound sockets to serve on; nil uses
	// systemd socket activation.
	inheritSockets func() (net.PacketConn, net.Listener, error)
}

// SetQueriesMetric installs the dns_queries_total counter; calling sites
// pass the CounterVec from privateer's agent.Metrics. nil is a no-op so
// the DNS server stays usable in tests without a Prometheus registry.
func (s *Server) SetQueriesMetric(vec *prometheus.CounterVec) {
	s.queries = vec
}

func (s *Server) recordQuery(qtype, status string) {
	if s.queries != nil {
		s.queries.WithLabelValues(qtype, status).Inc()
	}
}

func NewServer(logger logging.Logger, port int, upstreams ...string) *Server {
	if port == 0 {
		port = 53
	}
	// No default upstreams. If the provisioner didn't capture the host's
	// nameservers into UPSTREAM_DNS, non-.internal queries are REFUSED
	// rather than silently overriding the host's resolver policy with
	// public DNS.
	return &Server{
		logger:    logger,
		records:   make(map[string][]string),
		port:      port,
		upstreams: upstreams,
	}
}

func (s *Server) Start() {
	s.logger.WithField("port", s.port).Info("Starting Internal DNS Server")

	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleForward)
	mux.HandleFunc("internal.", s.handleInternal)

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	s.udp = &dns.Server{Addr: addr, Net: "udp", Handler: mux}
	s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: mux}

	inherit := s.inheritSockets
	if inherit == nil {
		inherit = systemdSockets
	}
	packetConn, listener, err := inherit()
	if err != nil {
		s.logger.WithError(err).Warn("Ignoring unusable socket-activation descriptors")
	}

	// Under systemd the sockets belong to frameworks-privateer.socket and stay
	// bound while this process restarts: queries queue in the kernel instead
	// of failing, so resolvers never fall through to the host search domain.
	// Without activation the server binds the address itself.
	if packetConn != nil {
		s.udp.PacketConn = packetConn
		s.logger.WithField("addr", packetConn.LocalAddr().String()).Info("Serving DNS UDP on socket-activated descriptor")
	}
	if listener != nil {
		s.tcp.Listener = listener
		s.logger.WithField("addr", listener.Addr().String()).Info("Serving DNS TCP on socket-activated descriptor")
	}

	go func() {
		if err := serveDNS(s.udp); err != nil {
			s.logger.WithError(err).Error("Failed to start DNS UDP server")
		}
	}()
	go func() {
		if err := serveDNS(s.tcp); err != nil {
			s.logger.WithError(err).Error("Failed to start DNS TCP server")
		}
	}()
}

func serveDNS(srv *dns.Server) error {
	if srv.PacketConn != nil || srv.Listener != nil {
		return srv.ActivateAndServe()
	}
	return srv.ListenAndServe()
}

func (s *Server) Stop() {
	if s.udp != nil {
		if err := s.udp.Shutdown(); err != nil {
			s.logger.WithError(err).Warn("Failed to shutdown DNS UDP server")
		}
	}
	if s.tcp != nil {
		if err := s.tcp.Shutdown(); err != nil {
			s.logger.WithError(err).Warn("Failed to shutdown DNS TCP server")
		}
	}
}

// UpdateRecords updates the DNS records from the list of peers/services.
// records map: hostname -> [IPs]
func (s *Server) UpdateRecords(records map[string][]string) error {
	nextRecords := make(map[string][]string, len(records))
	for name, ips := range records {
		trimmedName := strings.ToLower(strings.TrimSpace(name))
		if trimmedName == "" {
			return fmt.Errorf("dns record name is empty")
		}
		if len(ips) == 0 {
			// Allow empty service endpoint lists (e.g., during scaling/down or outages).
			// Treat as an instruction to skip/omit this record.
			continue
		}

		validated := make([]string, 0, len(ips))
		for _, ip := range ips {
			trimmedIP := strings.TrimSpace(ip)
			if trimmedIP == "" {
				return fmt.Errorf("dns record %q has empty ip", trimmedName)
			}
			if net.ParseIP(trimmedIP) == nil {
				return fmt.Errorf("dns record %q has invalid ip %q", trimmedName, trimmedIP)
			}
			validated = append(validated, trimmedIP)
		}

		fqdn := fmt.Sprintf("%s.internal.", trimmedName)
		nextRecords[fqdn] = validated
	}

	s.mu.Lock()
	s.records = nextRecords
	s.mu.Unlock()

	s.logger.WithField("count", len(s.records)).Info("Updated DNS records")
	return nil
}

func (s *Server) handleInternal(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Compress = false

	if len(r.Question) == 0 {
		return
	}

	status := "ok"
	switch r.Question[0].Qtype {
	case dns.TypeA:
		m.Authoritative = true
		domain := strings.ToLower(r.Question[0].Name)

		// Look up domain in local peer list
		s.mu.RLock()
		ips, ok := s.records[domain]
		s.mu.RUnlock()

		if ok && len(ips) > 0 {
			for _, ip := range ips {
				rr, err := dns.NewRR(fmt.Sprintf("%s A %s", domain, ip))
				if err == nil {
					m.Answer = append(m.Answer, rr)
				}
			}
			s.logger.WithField("domain", domain).Debug("DNS Query resolved")
		} else {
			m.Rcode = dns.RcodeNameError
			status = "nxdomain"
			s.logger.WithField("domain", domain).Debug("DNS Query not found")
		}
	}

	if err := w.WriteMsg(m); err != nil {
		s.logger.WithError(err).Warn("Failed to write DNS response")
		status = "error"
	}
	s.recordQuery("internal", status)
}

func (s *Server) handleForward(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) == 0 {
		return
	}

	// Without upstreams this server is authoritative for .internal only.
	// REFUSED says "not my zone"; SERVFAIL would tell a stub resolver the
	// server is failing and make it retry and stall.
	if len(s.upstreams) == 0 {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Rcode = dns.RcodeRefused
		status := "refused"
		if err := w.WriteMsg(m); err != nil {
			s.logger.WithError(err).Warn("Failed to write REFUSED DNS response")
			status = "error"
		}
		s.recordQuery("forward", status)
		return
	}

	c := new(dns.Client)
	c.Timeout = 2 * time.Second

	for _, upstream := range s.upstreams {
		resp, _, err := c.Exchange(r, upstream)
		if err != nil {
			s.logger.WithError(err).WithField("upstream", upstream).Debug("Upstream DNS query failed")
			continue
		}
		status := "ok"
		if err := w.WriteMsg(resp); err != nil {
			s.logger.WithError(err).Warn("Failed to write forwarded DNS response")
			status = "error"
		}
		s.recordQuery("forward", status)
		return
	}

	// All upstreams failed
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeServerFailure
	status := "servfail"
	if err := w.WriteMsg(m); err != nil {
		s.logger.WithError(err).Warn("Failed to write SERVFAIL DNS response")
		status = "error"
	}
	s.recordQuery("forward", status)
}
