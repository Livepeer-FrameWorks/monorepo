package remoteaccess

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func newTestManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"central": {
				Name:        "central",
				ExternalIP:  "203.0.113.10",
				User:        "root",
				WireguardIP: "10.99.0.1",
			},
			"billing-host": {
				Name:       "billing-host",
				ExternalIP: "203.0.113.20",
				User:       "ops",
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Host: "central", GRPCPort: 19002},
			"commodore":     {Host: "central"},
			"purser":        {Host: "billing-host", GRPCPort: 19003},
			"orphan":        {Host: "missing-host"},
		},
	}
}

// fakeTunnel is a stand-in for ssh.Tunnel created without spawning ssh.
func fakeTunnel(localAddr string, remotePort int) *ssh.Tunnel {
	return &ssh.Tunnel{
		LocalAddr:  localAddr,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
	}
}

func TestSessionEndpointResolvesViaManifest(t *testing.T) {
	t.Parallel()
	sess, err := OpenSession(Options{
		Manifest:   newTestManifest(),
		SSHKeyPath: "/tmp/key",
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	// Replace the tunnel opener with a fake that records inputs.
	type call struct {
		host       string
		user       string
		key        string
		remotePort int
	}
	var calls []call
	port := 40000
	sess.openTunnel = func(_ context.Context, opts ssh.LocalForwardOptions) (*ssh.Tunnel, error) {
		calls = append(calls, call{
			host:       opts.Config.Address,
			user:       opts.Config.User,
			key:        opts.Config.KeyPath,
			remotePort: opts.RemotePort,
		})
		port++
		return fakeTunnel("127.0.0.1:"+itoa(port), opts.RemotePort), nil
	}

	ep, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "quartermaster", DefaultGRPCPort: 19002})
	if err != nil {
		t.Fatalf("Endpoint(quartermaster): %v", err)
	}
	if !strings.HasPrefix(ep.DialAddr, "127.0.0.1:") {
		t.Errorf("DialAddr should bind to loopback; got %s", ep.DialAddr)
	}
	if ep.ServerName != "10.99.0.1" {
		t.Errorf("ServerName = %q, want mesh address 10.99.0.1", ep.ServerName)
	}

	if got := len(calls); got != 1 {
		t.Fatalf("expected 1 ssh tunnel, got %d", got)
	}
	if calls[0].host != "203.0.113.10" || calls[0].user != "root" || calls[0].key != "/tmp/key" || calls[0].remotePort != 19002 {
		t.Errorf("tunnel call mismatch: %+v", calls[0])
	}
}

func TestSessionReusesTunnelForSameHostSamePort(t *testing.T) {
	t.Parallel()
	mf := newTestManifest()
	// Force commodore onto the same host+port as quartermaster so the tunnel
	// can be reused (commodore inherits the QM port via the default).
	commodore := mf.Services["commodore"]
	commodore.GRPCPort = 19002
	mf.Services["commodore"] = commodore

	sess, _ := OpenSession(Options{Manifest: mf})
	t.Cleanup(func() { _ = sess.Close() })

	port := 50000
	openCalls := 0
	sess.openTunnel = func(_ context.Context, opts ssh.LocalForwardOptions) (*ssh.Tunnel, error) {
		openCalls++
		port++
		return fakeTunnel("127.0.0.1:"+itoa(port), opts.RemotePort), nil
	}

	if _, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "quartermaster", DefaultGRPCPort: 19002}); err != nil {
		t.Fatalf("qm endpoint: %v", err)
	}
	if _, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "commodore", DefaultGRPCPort: 19002}); err != nil {
		t.Fatalf("commodore endpoint: %v", err)
	}
	if openCalls != 1 {
		t.Fatalf("expected 1 tunnel for two services on same host+port; got %d", openCalls)
	}
}

func TestSessionOpensSecondTunnelForDifferentPortOnSameHost(t *testing.T) {
	t.Parallel()
	mf := newTestManifest()
	// Quartermaster uses 19002 on "central"; reassign commodore to "central"
	// with a different port to exercise the per-host:port fallback tunnel.
	commodore := mf.Services["commodore"]
	commodore.Host = "central"
	commodore.GRPCPort = 19001
	mf.Services["commodore"] = commodore

	sess, _ := OpenSession(Options{Manifest: mf})
	t.Cleanup(func() { _ = sess.Close() })

	port := 60000
	var seenPorts []int
	sess.openTunnel = func(_ context.Context, opts ssh.LocalForwardOptions) (*ssh.Tunnel, error) {
		seenPorts = append(seenPorts, opts.RemotePort)
		port++
		return fakeTunnel("127.0.0.1:"+itoa(port), opts.RemotePort), nil
	}

	if _, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "quartermaster", DefaultGRPCPort: 19002}); err != nil {
		t.Fatalf("qm endpoint: %v", err)
	}
	if _, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "commodore", DefaultGRPCPort: 19001}); err != nil {
		t.Fatalf("commodore endpoint: %v", err)
	}
	if len(seenPorts) != 2 {
		t.Fatalf("expected 2 tunnels for 2 ports on same host; got %v", seenPorts)
	}
}

// TestSessionServerNameIsAuthoritativeNotLoopback locks the security-relevant
// invariant: a tunneled endpoint's ServerName must be the SAN-bearing hostname
// the cert is issued against, never the loopback dial address. Without this,
// a non-dev-profile caller would feed `127.0.0.1` into the gRPC TLS verifier
// and either fail outright or — worse — silently succeed against a misconfigured cert.
func TestSessionServerNameIsAuthoritativeNotLoopback(t *testing.T) {
	t.Parallel()
	sess, _ := OpenSession(Options{Manifest: newTestManifest()})
	t.Cleanup(func() { _ = sess.Close() })

	port := 30000
	sess.openTunnel = func(_ context.Context, opts ssh.LocalForwardOptions) (*ssh.Tunnel, error) {
		port++
		return fakeTunnel("127.0.0.1:"+itoa(port), opts.RemotePort), nil
	}

	ep, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "purser", DefaultGRPCPort: 19003})
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if ep.ServerName == "" {
		t.Fatal("ServerName must not be empty for a tunneled endpoint")
	}
	if strings.HasPrefix(ep.ServerName, "127.0.0.1") || ep.ServerName == "localhost" {
		t.Fatalf("ServerName must not be a loopback name; got %q", ep.ServerName)
	}
	if ep.ServerName != "203.0.113.20" {
		t.Errorf("ServerName = %q, want billing-host's external IP 203.0.113.20", ep.ServerName)
	}
}

func TestSessionInsecureFlagPropagated(t *testing.T) {
	t.Parallel()
	sess, _ := OpenSession(Options{
		Manifest:      newTestManifest(),
		AllowInsecure: true,
	})
	t.Cleanup(func() { _ = sess.Close() })

	sess.openTunnel = func(_ context.Context, opts ssh.LocalForwardOptions) (*ssh.Tunnel, error) {
		return fakeTunnel("127.0.0.1:65000", opts.RemotePort), nil
	}

	ep, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "purser", DefaultGRPCPort: 19003})
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if !ep.Insecure {
		t.Error("AllowInsecure=true should propagate to Endpoint.Insecure")
	}
}

func TestSessionEndpointCachesPerService(t *testing.T) {
	t.Parallel()
	sess, _ := OpenSession(Options{Manifest: newTestManifest()})
	t.Cleanup(func() { _ = sess.Close() })

	calls := 0
	sess.openTunnel = func(_ context.Context, opts ssh.LocalForwardOptions) (*ssh.Tunnel, error) {
		calls++
		return fakeTunnel("127.0.0.1:65001", opts.RemotePort), nil
	}

	ep1, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "quartermaster", DefaultGRPCPort: 19002})
	if err != nil {
		t.Fatalf("first Endpoint: %v", err)
	}
	ep2, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "quartermaster", DefaultGRPCPort: 19002})
	if err != nil {
		t.Fatalf("second Endpoint: %v", err)
	}
	if ep1 != ep2 {
		t.Errorf("expected cached endpoint; got %+v vs %+v", ep1, ep2)
	}
	if calls != 1 {
		t.Errorf("expected 1 tunnel open for cached service; got %d", calls)
	}
}

func TestSessionErrorPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		target  ServiceTarget
		wantSub string
	}{
		{"missing service", ServiceTarget{Name: "ghost", DefaultGRPCPort: 1}, "not in manifest"},
		{"empty name", ServiceTarget{}, "ServiceTarget.Name is required"},
		{"missing host record", ServiceTarget{Name: "orphan", DefaultGRPCPort: 1}, "host \"missing-host\" not in manifest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess, _ := OpenSession(Options{Manifest: newTestManifest()})
			t.Cleanup(func() { _ = sess.Close() })
			_, err := sess.Endpoint(context.Background(), tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q; got %v", tc.wantSub, err)
			}
		})
	}
}

func TestSessionTunnelOpenerErrorPropagates(t *testing.T) {
	t.Parallel()
	sess, _ := OpenSession(Options{Manifest: newTestManifest()})
	t.Cleanup(func() { _ = sess.Close() })

	sess.openTunnel = func(_ context.Context, _ ssh.LocalForwardOptions) (*ssh.Tunnel, error) {
		return nil, errors.New("ssh dial refused")
	}

	_, err := sess.Endpoint(context.Background(), ServiceTarget{Name: "quartermaster", DefaultGRPCPort: 19002})
	if err == nil || !strings.Contains(err.Error(), "ssh dial refused") {
		t.Fatalf("expected propagated tunnel error; got %v", err)
	}
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	sess, _ := OpenSession(Options{Manifest: newTestManifest()})
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// itoa avoids strconv import noise in this otherwise-stdlib-light test file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
