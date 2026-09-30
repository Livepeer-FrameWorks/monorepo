package wireguard

import (
	"net"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func matchingDevice(cfg Config) *wgtypes.Device {
	dev := &wgtypes.Device{PrivateKey: cfg.PrivateKey, ListenPort: cfg.ListenPort}
	for _, p := range cfg.Peers {
		dev.Peers = append(dev.Peers, wgtypes.Peer{
			PublicKey:                   p.PublicKey,
			Endpoint:                    p.Endpoint,
			PersistentKeepaliveInterval: time.Duration(p.KeepAlive) * time.Second,
			AllowedIPs:                  append([]net.IPNet(nil), p.AllowedIPs...),
		})
	}
	return dev
}

func TestDeviceConfigDelta_MatchingDeviceIsUnchanged(t *testing.T) {
	cfg := validApplyConfig(t)
	if delta, changed := deviceConfigDelta(cfg, matchingDevice(cfg)); changed {
		t.Fatalf("delta against matching device = %+v, want unchanged", delta)
	}
}

// The kernel reports addresses in the 16-byte form; that alone is no change.
func TestDeviceConfigDelta_IgnoresIPRepresentation(t *testing.T) {
	cfg := validApplyConfig(t)
	dev := matchingDevice(cfg)
	dev.Peers[0].Endpoint = &net.UDPAddr{IP: cfg.Peers[0].Endpoint.IP.To16(), Port: cfg.Peers[0].Endpoint.Port}
	allowed := cfg.Peers[0].AllowedIPs[0]
	dev.Peers[0].AllowedIPs = []net.IPNet{{IP: allowed.IP.To16(), Mask: allowed.Mask}}
	if delta, changed := deviceConfigDelta(cfg, dev); changed {
		t.Fatalf("delta = %+v, want unchanged", delta)
	}
}

func TestDeviceConfigDelta_DetectsEachPeerField(t *testing.T) {
	cases := map[string]func(t *testing.T, p *wgtypes.Peer){
		"endpoint":  func(t *testing.T, p *wgtypes.Peer) { p.Endpoint = mustResolveUDP(t, "10.0.9.9:51820") },
		"keepalive": func(_ *testing.T, p *wgtypes.Peer) { p.PersistentKeepaliveInterval = 0 },
		"allowed_extra": func(t *testing.T, p *wgtypes.Peer) {
			p.AllowedIPs = append(p.AllowedIPs, mustParseCIDR(t, "10.88.0.99/32"))
		},
		"allowed_other": func(t *testing.T, p *wgtypes.Peer) { p.AllowedIPs = []net.IPNet{mustParseCIDR(t, "10.88.0.99/32")} },
		"preshared_key": func(t *testing.T, p *wgtypes.Peer) { p.PresharedKey = mustGenKey(t) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validApplyConfig(t)
			dev := matchingDevice(cfg)
			mutate(t, &dev.Peers[0])
			delta, changed := deviceConfigDelta(cfg, dev)
			if !changed || len(delta.Peers) != 1 {
				t.Fatalf("delta = %+v, want one peer update", delta)
			}
			pc := delta.Peers[0]
			if pc.Remove || delta.ReplacePeers || !pc.ReplaceAllowedIPs {
				t.Fatalf("peer config = %+v, want an in-place update replacing allowed IPs", pc)
			}
			if name == "preshared_key" && (pc.PresharedKey == nil || *pc.PresharedKey != (wgtypes.Key{})) {
				t.Fatalf("preshared key = %v, want cleared", pc.PresharedKey)
			}
		})
	}
}
