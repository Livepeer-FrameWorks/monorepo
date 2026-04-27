package wireguard

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type fakeWgctrlClient struct {
	configureCalls []configureCall
	configureErr   error
	closed         bool
}

type configureCall struct {
	name string
	cfg  wgtypes.Config
}

func (f *fakeWgctrlClient) ConfigureDevice(name string, cfg wgtypes.Config) error {
	f.configureCalls = append(f.configureCalls, configureCall{name: name, cfg: cfg})
	return f.configureErr
}

func (f *fakeWgctrlClient) Close() error {
	f.closed = true
	return nil
}

type fakeLinkOps struct {
	ensureLinkCalls    []string
	linkUpCalls        []string
	ensureAddressCalls []ensureAddressCall
	ensureRoutesCalls  []ensureRoutesCall

	ensureLinkErr    error
	linkUpErr        error
	ensureAddressErr error
	ensureRoutesErr  error
}

type ensureAddressCall struct {
	name string
	addr netip.Prefix
}

type ensureRoutesCall struct {
	name  string
	self  netip.Prefix
	peers []Peer
}

func (f *fakeLinkOps) EnsureLink(name string) error {
	f.ensureLinkCalls = append(f.ensureLinkCalls, name)
	return f.ensureLinkErr
}

func (f *fakeLinkOps) LinkUp(name string) error {
	f.linkUpCalls = append(f.linkUpCalls, name)
	return f.linkUpErr
}

func (f *fakeLinkOps) EnsureAddress(name string, addr netip.Prefix) error {
	f.ensureAddressCalls = append(f.ensureAddressCalls, ensureAddressCall{name: name, addr: addr})
	return f.ensureAddressErr
}

func (f *fakeLinkOps) EnsureRoutes(name string, self netip.Prefix, peers []Peer) error {
	f.ensureRoutesCalls = append(f.ensureRoutesCalls, ensureRoutesCall{name: name, self: self, peers: peers})
	return f.ensureRoutesErr
}

func validApplyConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		PrivateKey: mustGenKey(t),
		Address:    mustParsePrefix(t, "10.88.0.5/32"),
		ListenPort: 51820,
		Peers: []Peer{
			{
				PublicKey:  mustGenKey(t).PublicKey(),
				Endpoint:   mustResolveUDP(t, "10.0.0.1:51820"),
				AllowedIPs: []net.IPNet{mustParseCIDR(t, "10.88.0.6/32")},
				KeepAlive:  25,
			},
		},
	}
}

// TestLinuxManager_ApplyBeforeInit guards the contract that Apply requires
// a wgctrl client; the manager must be initialized (or have its client
// injected) before the device can be touched.
func TestLinuxManager_ApplyBeforeInit(t *testing.T) {
	m := &linuxManager{interfaceName: "wg-test", link: &fakeLinkOps{}}
	if err := m.Apply(validApplyConfig(t)); err == nil {
		t.Fatal("Apply before Init should error, got nil")
	}
}

// TestLinuxManager_ApplyValidatesPolicy ensures the mutation boundary
// rejects policy-invalid configs even if a caller skipped pre-validation.
func TestLinuxManager_ApplyValidatesPolicy(t *testing.T) {
	cfg := validApplyConfig(t)
	cfg.Peers[0].Endpoint = nil // policy: endpoint required

	fake := &fakeWgctrlClient{}
	link := &fakeLinkOps{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: link}

	err := m.Apply(cfg)
	if err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("expected policy rejection, got: %v", err)
	}
	if len(fake.configureCalls) != 0 {
		t.Errorf("policy-invalid Apply must not call ConfigureDevice, got %d calls", len(fake.configureCalls))
	}
	if len(link.ensureAddressCalls) != 0 {
		t.Errorf("policy-invalid Apply must not touch addresses, got %d calls", len(link.ensureAddressCalls))
	}
	if len(link.ensureRoutesCalls) != 0 {
		t.Errorf("policy-invalid Apply must not touch routes, got %d calls", len(link.ensureRoutesCalls))
	}
}

// TestLinuxManager_ApplyConfiguresDevice asserts the wgtypes.Config sent to
// wgctrl matches the typed Config and that link state is reconciled with the
// configured self prefix and peer routes.
func TestLinuxManager_ApplyConfiguresDevice(t *testing.T) {
	priv := mustGenKey(t)
	peer1 := mustGenKey(t).PublicKey()
	peer2 := mustGenKey(t).PublicKey()

	cfg := Config{
		PrivateKey: priv,
		Address:    mustParsePrefix(t, "10.88.0.5/32"),
		ListenPort: 51820,
		Peers: []Peer{
			{
				PublicKey:  peer1,
				Endpoint:   mustResolveUDP(t, "10.0.0.1:51820"),
				AllowedIPs: []net.IPNet{mustParseCIDR(t, "10.88.0.6/32")},
				KeepAlive:  25,
			},
			{
				PublicKey:  peer2,
				Endpoint:   mustResolveUDP(t, "10.0.0.2:51820"),
				AllowedIPs: []net.IPNet{mustParseCIDR(t, "10.88.0.7/32")},
				KeepAlive:  0,
			},
		},
	}

	fake := &fakeWgctrlClient{}
	link := &fakeLinkOps{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: link}

	if err := m.Apply(cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(fake.configureCalls) != 1 {
		t.Fatalf("expected 1 ConfigureDevice call, got %d", len(fake.configureCalls))
	}
	got := fake.configureCalls[0]
	if got.name != "wg-test" {
		t.Errorf("interface name = %q, want wg-test", got.name)
	}
	if !got.cfg.ReplacePeers {
		t.Error("ReplacePeers must be true (full sync)")
	}
	if got.cfg.PrivateKey == nil || *got.cfg.PrivateKey != priv {
		t.Errorf("private key mismatch")
	}
	if got.cfg.ListenPort == nil || *got.cfg.ListenPort != 51820 {
		t.Errorf("listen port = %v, want 51820", got.cfg.ListenPort)
	}
	if len(got.cfg.Peers) != 2 {
		t.Fatalf("peer count = %d, want 2", len(got.cfg.Peers))
	}
	for i, p := range got.cfg.Peers {
		if !p.ReplaceAllowedIPs {
			t.Errorf("peer %d: ReplaceAllowedIPs must be true", i)
		}
		if p.PersistentKeepaliveInterval == nil {
			t.Errorf("peer %d: PersistentKeepaliveInterval must be set", i)
		}
	}
	if got.cfg.Peers[0].PublicKey != peer1 || got.cfg.Peers[1].PublicKey != peer2 {
		t.Error("peer public keys not preserved in order")
	}
	if d := got.cfg.Peers[0].PersistentKeepaliveInterval; d == nil || *d != 25*time.Second {
		t.Errorf("peer 0 keepalive = %v, want 25s", d)
	}
	if d := got.cfg.Peers[1].PersistentKeepaliveInterval; d == nil || *d != 0 {
		t.Errorf("peer 1 keepalive = %v, want 0 (disabled)", d)
	}

	if len(link.ensureAddressCalls) != 1 {
		t.Fatalf("expected 1 EnsureAddress call, got %d", len(link.ensureAddressCalls))
	}
	if call := link.ensureAddressCalls[0]; call.name != "wg-test" || call.addr.String() != "10.88.0.5/32" {
		t.Errorf("EnsureAddress call = %+v, want {wg-test, 10.88.0.5/32}", call)
	}
	if len(link.ensureRoutesCalls) != 1 {
		t.Fatalf("expected 1 EnsureRoutes call, got %d", len(link.ensureRoutesCalls))
	}
	routeCall := link.ensureRoutesCalls[0]
	if routeCall.name != "wg-test" || routeCall.self.String() != "10.88.0.5/32" {
		t.Errorf("EnsureRoutes call = %+v, want name=wg-test self=10.88.0.5/32", routeCall)
	}
	if len(routeCall.peers) != 2 {
		t.Fatalf("EnsureRoutes peers = %d, want 2", len(routeCall.peers))
	}
	if routeCall.peers[0].AllowedIPs[0].String() != "10.88.0.6/32" ||
		routeCall.peers[1].AllowedIPs[0].String() != "10.88.0.7/32" {
		t.Errorf("EnsureRoutes peers = %+v, want peer allowed IPs preserved", routeCall.peers)
	}
}

func TestLinuxManager_ApplyPropagatesConfigureError(t *testing.T) {
	fake := &fakeWgctrlClient{configureErr: errors.New("boom")}
	link := &fakeLinkOps{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: link}

	err := m.Apply(validApplyConfig(t))
	if err == nil {
		t.Fatal("Apply should propagate ConfigureDevice error, got nil")
	}
	if len(link.ensureAddressCalls) != 0 {
		t.Error("EnsureAddress must not run after ConfigureDevice error")
	}
	if len(link.ensureRoutesCalls) != 0 {
		t.Error("EnsureRoutes must not run after ConfigureDevice error")
	}
}

func TestLinuxManager_ApplyPropagatesAddressError(t *testing.T) {
	fake := &fakeWgctrlClient{}
	link := &fakeLinkOps{ensureAddressErr: errors.New("boom")}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: link}

	if err := m.Apply(validApplyConfig(t)); err == nil {
		t.Fatal("Apply should propagate EnsureAddress error, got nil")
	}
	if len(link.ensureRoutesCalls) != 0 {
		t.Error("EnsureRoutes must not run after EnsureAddress error")
	}
}

func TestLinuxManager_ApplyPropagatesRouteError(t *testing.T) {
	fake := &fakeWgctrlClient{}
	link := &fakeLinkOps{ensureRoutesErr: errors.New("boom")}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: link}

	if err := m.Apply(validApplyConfig(t)); err == nil {
		t.Fatal("Apply should propagate EnsureRoutes error, got nil")
	}
}

func TestLinuxManager_InitCallsLinkOps(t *testing.T) {
	link := &fakeLinkOps{}
	fake := &fakeWgctrlClient{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: link}

	if err := m.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := link.ensureLinkCalls; len(got) != 1 || got[0] != "wg-test" {
		t.Errorf("EnsureLink calls = %v, want [wg-test]", got)
	}
	if got := link.linkUpCalls; len(got) != 1 || got[0] != "wg-test" {
		t.Errorf("LinkUp calls = %v, want [wg-test]", got)
	}
}

func TestLinuxManager_CloseClosesClient(t *testing.T) {
	fake := &fakeWgctrlClient{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: &fakeLinkOps{}}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !fake.closed {
		t.Error("Close should close the wgctrl client")
	}
}
