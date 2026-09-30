package wireguard

import (
	"errors"
	"net"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func testPeer(t *testing.T, endpoint, allowed string) Peer {
	t.Helper()
	return Peer{
		PublicKey:  mustGenKey(t).PublicKey(),
		Endpoint:   mustResolveUDP(t, endpoint),
		AllowedIPs: []net.IPNet{mustParseCIDR(t, allowed)},
		KeepAlive:  25,
	}
}

func devicePeer(f *fakeWgctrlClient, key wgtypes.Key) *wgtypes.Peer {
	for i := range f.device.Peers {
		if f.device.Peers[i].PublicKey == key {
			return &f.device.Peers[i]
		}
	}
	return nil
}

// markTraffic stands in for sessions carrying traffic: the counters survive
// only as long as the kernel keeps the peer.
func markTraffic(f *fakeWgctrlClient) {
	for i := range f.device.Peers {
		f.device.Peers[i].ReceiveBytes = 1000
		f.device.Peers[i].TransmitBytes = 2000
		f.device.Peers[i].LastHandshakeTime = time.Unix(1_700_000_000, 0)
	}
}

func TestLinuxManager_ReapplyingUnchangedConfigKeepsPeerSessions(t *testing.T) {
	cfg := validApplyConfig(t)
	cfg.Peers = append(cfg.Peers, testPeer(t, "10.0.0.2:51820", "10.88.0.7/32"))
	fake := &fakeWgctrlClient{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: &fakeLinkOps{}}

	if err := m.Apply(cfg); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	markTraffic(fake)
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if len(fake.configureCalls) != 1 {
		t.Fatalf("ConfigureDevice calls = %d, want 1 (unchanged reapply must not touch the device)", len(fake.configureCalls))
	}
	for _, p := range cfg.Peers {
		peer := devicePeer(fake, p.PublicKey)
		if peer == nil {
			t.Fatalf("peer %s missing after reapply", p.PublicKey)
		}
		if peer.ReceiveBytes != 1000 || peer.TransmitBytes != 2000 || peer.LastHandshakeTime.IsZero() {
			t.Fatalf("peer %s session reset by reapply: %+v", p.PublicKey, peer)
		}
	}
}

func TestLinuxManager_ApplyChangesOnlyDifferingPeers(t *testing.T) {
	unchanged := testPeer(t, "10.0.0.1:51820", "10.88.0.6/32")
	moved := testPeer(t, "10.0.0.2:51820", "10.88.0.7/32")
	stale := testPeer(t, "10.0.0.3:51820", "10.88.0.8/32")
	cfg := Config{
		PrivateKey: mustGenKey(t),
		Address:    mustParsePrefix(t, "10.88.0.5/32"),
		ListenPort: 51820,
		Peers:      []Peer{unchanged, moved, stale},
	}
	fake := &fakeWgctrlClient{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: &fakeLinkOps{}}
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	markTraffic(fake)

	moved.Endpoint = mustResolveUDP(t, "10.0.1.2:51821")
	moved.AllowedIPs = []net.IPNet{mustParseCIDR(t, "10.88.0.9/32")}
	added := testPeer(t, "10.0.0.4:51820", "10.88.0.10/32")
	cfg.Peers = []Peer{unchanged, moved, added}
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if len(fake.configureCalls) != 2 {
		t.Fatalf("ConfigureDevice calls = %d, want 2", len(fake.configureCalls))
	}
	delta := fake.configureCalls[1].cfg
	if delta.ReplacePeers {
		t.Fatal("delta must not set ReplacePeers")
	}
	if delta.PrivateKey != nil || delta.ListenPort != nil {
		t.Fatalf("delta rewrote unchanged identity: key=%v port=%v", delta.PrivateKey, delta.ListenPort)
	}
	byKey := map[wgtypes.Key]wgtypes.PeerConfig{}
	for _, pc := range delta.Peers {
		byKey[pc.PublicKey] = pc
	}
	if len(byKey) != 3 {
		t.Fatalf("delta peers = %+v, want moved, added and stale only", delta.Peers)
	}
	if _, ok := byKey[unchanged.PublicKey]; ok {
		t.Fatal("unchanged peer must not be in the delta")
	}
	if pc := byKey[moved.PublicKey]; pc.Remove || !pc.ReplaceAllowedIPs {
		t.Fatalf("moved peer config = %+v, want an update replacing allowed IPs", pc)
	}
	if pc := byKey[stale.PublicKey]; !pc.Remove {
		t.Fatalf("stale peer config = %+v, want Remove", pc)
	}
	if pc := byKey[added.PublicKey]; pc.Remove || !pc.ReplaceAllowedIPs {
		t.Fatalf("added peer config = %+v", pc)
	}

	if len(fake.device.Peers) != 3 || devicePeer(fake, stale.PublicKey) != nil {
		t.Fatalf("device peers = %+v, want unchanged, moved, added", fake.device.Peers)
	}
	if p := devicePeer(fake, unchanged.PublicKey); p.ReceiveBytes != 1000 {
		t.Fatalf("unchanged peer session reset: %+v", p)
	}
	p := devicePeer(fake, moved.PublicKey)
	if p.ReceiveBytes != 1000 {
		t.Fatalf("updated peer was re-created instead of updated: %+v", p)
	}
	if len(p.AllowedIPs) != 1 || p.AllowedIPs[0].String() != "10.88.0.9/32" {
		t.Fatalf("moved peer allowed IPs = %v, want exactly [10.88.0.9/32]", p.AllowedIPs)
	}
	if p.Endpoint.String() != moved.Endpoint.String() {
		t.Fatalf("moved peer endpoint = %v, want %v", p.Endpoint, moved.Endpoint)
	}

	if err := m.Apply(cfg); err != nil {
		t.Fatalf("third Apply: %v", err)
	}
	if len(fake.configureCalls) != 2 {
		t.Fatalf("converged reapply issued ConfigureDevice (calls = %d)", len(fake.configureCalls))
	}
}

func TestLinuxManager_ApplyWritesChangedInterfaceSettingsOnly(t *testing.T) {
	cfg := validApplyConfig(t)
	fake := &fakeWgctrlClient{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: &fakeLinkOps{}}
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	markTraffic(fake)

	cfg.ListenPort = 51821
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("port change Apply: %v", err)
	}
	cfg.PrivateKey = mustGenKey(t)
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("key change Apply: %v", err)
	}

	if len(fake.configureCalls) != 3 {
		t.Fatalf("ConfigureDevice calls = %d, want 3", len(fake.configureCalls))
	}
	portDelta := fake.configureCalls[1].cfg
	if portDelta.ListenPort == nil || *portDelta.ListenPort != 51821 || portDelta.PrivateKey != nil || len(portDelta.Peers) != 0 {
		t.Fatalf("port delta = %+v, want only ListenPort=51821", portDelta)
	}
	keyDelta := fake.configureCalls[2].cfg
	if keyDelta.PrivateKey == nil || *keyDelta.PrivateKey != cfg.PrivateKey || keyDelta.ListenPort != nil || len(keyDelta.Peers) != 0 {
		t.Fatalf("key delta = %+v, want only the new private key", keyDelta)
	}
	if fake.device.ListenPort != 51821 || fake.device.PrivateKey != cfg.PrivateKey {
		t.Fatalf("device identity = port %d key %v, want the new settings", fake.device.ListenPort, fake.device.PrivateKey)
	}
	if p := devicePeer(fake, cfg.Peers[0].PublicKey); p == nil || p.ReceiveBytes != 1000 {
		t.Fatalf("interface change reset the peer session: %+v", p)
	}
}

func TestLinuxManager_ApplyPropagatesDeviceReadError(t *testing.T) {
	fake := &fakeWgctrlClient{deviceErr: errors.New("boom")}
	link := &fakeLinkOps{}
	m := &linuxManager{interfaceName: "wg-test", client: fake, link: link}

	if err := m.Apply(validApplyConfig(t)); err == nil {
		t.Fatal("Apply should propagate the device read error, got nil")
	}
	if len(fake.configureCalls) != 0 || len(link.ensureAddressCalls) != 0 {
		t.Fatal("nothing may be configured after a failed device read")
	}
}
