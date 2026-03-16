package dns

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"github.com/miekg/dns"
)

type Server struct {
	logger    logging.Logger
	udp       *dns.Server
	tcp       *dns.Server
	records   map[string][]string // hostname.internal. -> [IPs]
	mu        sync.RWMutex
	port      int
	upstreams []string // upstream resolver addresses for non-.internal queries
}

func NewServer(logger logging.Logger, port int, upstreams ...string) *Server {
	if port == 0 {
		port = 53
	}
	// No default upstreams. If the provisioner didn't capture the host's
	// nameservers into UPSTREAM_DNS, non-.internal queries return SERVFAIL
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

	go func() {
		if err := s.udp.ListenAndServe(); err != nil {
			s.logger.WithError(err).Error("Failed to start DNS UDP server")
		}
	}()
	go func() {
		if err := s.tcp.ListenAndServe(); err != nil {
			s.logger.WithError(err).Error("Failed to start DNS TCP server")
		}
	}()
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
			s.logger.WithField("domain", domain).Debug("DNS Query not found")
		}
	}

	if err := w.WriteMsg(m); err != nil {
		s.logger.WithError(err).Warn("Failed to write DNS response")
	}
}

func (s *Server) handleForward(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) == 0 {
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
		if err := w.WriteMsg(resp); err != nil {
			s.logger.WithError(err).Warn("Failed to write forwarded DNS response")
		}
		return
	}

	// All upstreams failed
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeServerFailure
	if err := w.WriteMsg(m); err != nil {
		s.logger.WithError(err).Warn("Failed to write SERVFAIL DNS response")
	}
}
