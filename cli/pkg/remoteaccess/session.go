// Package remoteaccess opens SSH local-forward tunnels to service hosts and
// returns gRPC dial endpoints scoped to a single provisioning phase. Callers
// open one Session, fetch Endpoints by service name, and Close the session
// when finalization is done. One tunnel is opened per unique service host,
// reused across services that live on the same host.
//
// This exists so cluster-provisioning code stops scattering ad-hoc SSH tunnel
// or address-resolution logic at every gRPC call site. Callers ask the session
// "give me an endpoint for purser" and dial it; the session owns the tunnel
// lifecycle and the TLS server-name policy.
package remoteaccess

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// Endpoint is the dial address callers feed into a gRPC client. DialAddr is a
// loopback address served by the SSH local-forward; ServerName is the
// authoritative hostname the service certificate is issued against, used as
// the TLS SNI/verification name when Insecure is false.
type Endpoint struct {
	DialAddr   string
	ServerName string
	Insecure   bool
}

// Options configures a Session.
type Options struct {
	// Manifest is the cluster manifest used to resolve service hosts and ports.
	Manifest *inventory.Manifest
	// SSHKeyPath is the operator's SSH key, mirroring the --ssh-key CLI flag.
	// Empty falls back to ssh-agent and ~/.ssh defaults.
	SSHKeyPath string
	// AllowInsecure mirrors isDevProfile: when true, returned endpoints carry
	// Insecure=true and TLS verification is skipped by the gRPC client. The
	// tunnel is still opened — it's a transport, not a TLS decision.
	AllowInsecure bool
	// SSHTimeout bounds resolution + dial steps. Default 10s.
	SSHTimeout time.Duration
	// ReadyTimeout bounds the post-spawn TCP probe on each tunnel.
	// Default 5s.
	ReadyTimeout time.Duration
}

// ServiceTarget describes how to reach a single service. Callers identify
// the service by name and supply a default gRPC port; per-service overrides
// in the manifest still win.
type ServiceTarget struct {
	// Name is the service name as used in manifest.Services (e.g. "quartermaster").
	Name string
	// DefaultGRPCPort is used when manifest.Services[Name].GRPCPort is unset.
	DefaultGRPCPort int
}

// Session holds one or more tunnels for the duration of a provisioning phase.
// Endpoints are cached: repeated calls for the same service return the same
// loopback address.
type Session struct {
	manifest      *inventory.Manifest
	sshKey        string
	allowInsecure bool
	sshTimeout    time.Duration
	readyTimeout  time.Duration

	mu        sync.Mutex
	tunnels   map[string]*ssh.Tunnel // keyed by hostKey
	endpoints map[string]Endpoint    // keyed by service name

	// openTunnel is the seam tests replace to avoid spawning real ssh.
	openTunnel func(ctx context.Context, opts ssh.LocalForwardOptions) (*ssh.Tunnel, error)
}

// OpenSession constructs a Session. It does not open any tunnels eagerly;
// tunnels are created lazily on the first Endpoint call per service host.
func OpenSession(opts Options) (*Session, error) {
	if opts.Manifest == nil {
		return nil, errors.New("remoteaccess: Manifest is required")
	}
	sshTimeout := opts.SSHTimeout
	if sshTimeout <= 0 {
		sshTimeout = 10 * time.Second
	}
	readyTimeout := opts.ReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = 5 * time.Second
	}
	return &Session{
		manifest:      opts.Manifest,
		sshKey:        opts.SSHKeyPath,
		allowInsecure: opts.AllowInsecure,
		sshTimeout:    sshTimeout,
		readyTimeout:  readyTimeout,
		tunnels:       make(map[string]*ssh.Tunnel),
		endpoints:     make(map[string]Endpoint),
		openTunnel:    ssh.LocalForward,
	}, nil
}

// Endpoint resolves a service-name → dialable loopback endpoint. The tunnel
// for the underlying host is opened lazily on first request and reused across
// subsequent services on the same host.
func (s *Session) Endpoint(ctx context.Context, target ServiceTarget) (Endpoint, error) {
	if s == nil {
		return Endpoint{}, errors.New("remoteaccess: nil Session")
	}
	if target.Name == "" {
		return Endpoint{}, errors.New("remoteaccess: ServiceTarget.Name is required")
	}

	s.mu.Lock()
	if ep, ok := s.endpoints[target.Name]; ok {
		s.mu.Unlock()
		return ep, nil
	}
	s.mu.Unlock()

	hostKey, remotePort, serverName, err := s.resolveTarget(target)
	if err != nil {
		return Endpoint{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check after acquiring the write lock.
	if ep, ok := s.endpoints[target.Name]; ok {
		return ep, nil
	}

	tun, ok := s.tunnels[hostKey]
	if !ok {
		t, terr := s.dialHost(ctx, hostKey, remotePort)
		if terr != nil {
			return Endpoint{}, terr
		}
		s.tunnels[hostKey] = t
		tun = t
	} else if tun.RemotePort != remotePort {
		// Two services on the same host but different remote ports require
		// distinct tunnels. Open a second one keyed by host:port.
		hostPortKey := fmt.Sprintf("%s:%d", hostKey, remotePort)
		t2, ok2 := s.tunnels[hostPortKey]
		if !ok2 {
			tnew, terr := s.dialHost(ctx, hostKey, remotePort)
			if terr != nil {
				return Endpoint{}, terr
			}
			s.tunnels[hostPortKey] = tnew
			tun = tnew
		} else {
			tun = t2
		}
	}

	ep := Endpoint{
		DialAddr:   tun.LocalAddr,
		ServerName: serverName,
		Insecure:   s.allowInsecure,
	}
	s.endpoints[target.Name] = ep
	return ep, nil
}

// Close tears down every tunnel opened by the session. Safe to call multiple
// times.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	tunnels := s.tunnels
	s.tunnels = make(map[string]*ssh.Tunnel)
	s.endpoints = make(map[string]Endpoint)
	s.mu.Unlock()

	var errs []error
	for _, t := range tunnels {
		if err := t.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resolveTarget extracts (host, port, serverName) from the manifest for the
// requested service. ServerName is the authoritative dial address that would
// have been used without tunneling — preserves existing TLS SAN matching.
func (s *Session) resolveTarget(t ServiceTarget) (hostKey string, remotePort int, serverName string, err error) {
	svc, ok := s.manifest.Services[t.Name]
	if !ok {
		return "", 0, "", fmt.Errorf("remoteaccess: service %q not in manifest", t.Name)
	}
	hostKey = svc.Host
	if hostKey == "" && len(svc.Hosts) > 0 {
		hostKey = svc.Hosts[0]
	}
	if hostKey == "" {
		return "", 0, "", fmt.Errorf("remoteaccess: service %q has no host", t.Name)
	}
	host, ok := s.manifest.GetHost(hostKey)
	if !ok {
		return "", 0, "", fmt.Errorf("remoteaccess: service %q host %q not in manifest", t.Name, hostKey)
	}

	remotePort = t.DefaultGRPCPort
	if svc.GRPCPort != 0 {
		remotePort = svc.GRPCPort
	}
	if remotePort <= 0 {
		return "", 0, "", fmt.Errorf("remoteaccess: service %q has no gRPC port (default %d)", t.Name, t.DefaultGRPCPort)
	}

	// ServerName is the cert-bearing hostname for the service: the mesh
	// address if present, else the host's external IP. The gRPC client uses
	// it as the TLS SNI / verification name, since the dial address is the
	// loopback endpoint of the local-forward and won't appear in any SAN.
	serverName = s.manifest.MeshAddress(hostKey)
	if serverName == "" || serverName == hostKey {
		serverName = host.ExternalIP
	}
	if serverName == "" {
		return "", 0, "", fmt.Errorf("remoteaccess: service %q has no usable address for TLS server name", t.Name)
	}
	return hostKey, remotePort, serverName, nil
}

// dialHost opens an SSH local-forward to the given host's loopback at remotePort.
func (s *Session) dialHost(ctx context.Context, hostKey string, remotePort int) (*ssh.Tunnel, error) {
	host, ok := s.manifest.GetHost(hostKey)
	if !ok {
		return nil, fmt.Errorf("remoteaccess: host %q not in manifest", hostKey)
	}
	if host.ExternalIP == "" {
		return nil, fmt.Errorf("remoteaccess: host %q has no external_ip", hostKey)
	}

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		User:     host.User,
		KeyPath:  s.sshKey,
		HostName: hostKey,
		Timeout:  s.sshTimeout,
	}
	tun, err := s.openTunnel(ctx, ssh.LocalForwardOptions{
		Config:       cfg,
		RemoteHost:   "127.0.0.1",
		RemotePort:   remotePort,
		ReadyTimeout: s.readyTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("remoteaccess: open tunnel to %q: %w", hostKey, err)
	}
	return tun, nil
}
