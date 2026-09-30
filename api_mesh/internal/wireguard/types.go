package wireguard

import (
	"net"
	"slices"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mesh/wgpolicy"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Config and Peer are type aliases onto pkg/mesh/wgpolicy so the runtime
// apply path and the 'mesh doctor' CLI share one set of types and rules.
// Methods on the runtime-specific surface (deviceConfigDelta) live as
// package functions below because Go does not allow methods on alias
// targets owned by another package.
type Config = wgpolicy.Config
type Peer = wgpolicy.Peer

// Manager defines the interface for managing the WireGuard device.
type Manager interface {
	// Init ensures the interface exists and is up.
	Init() error
	// Apply converges the interface to the given config: afterwards the
	// device holds exactly cfg's identity and peers.
	Apply(cfg Config) error
	// Close tears down the interface (if applicable).
	Close() error
}

// deviceConfigDelta returns the wgtypes.Config that converges the device
// from current to desired, and false when the device already matches.
//
// The delta never sets ReplacePeers: the kernel implements it by removing
// every peer and re-adding it, which drops each peer's session keys and
// transfer counters and forces a fresh handshake. Instead a peer that is
// missing or differs in endpoint, keepalive, allowed IPs or preshared key is
// written in full (ReplaceAllowedIPs, so its allowed IPs end up exactly the
// desired set), a peer that desired no longer lists is removed, and a peer
// that already matches is left out so its session is untouched. The private
// key and listen port are written only when they differ.
func deviceConfigDelta(desired Config, current *wgtypes.Device) (wgtypes.Config, bool) {
	var delta wgtypes.Config
	if current.PrivateKey != desired.PrivateKey {
		priv := desired.PrivateKey
		delta.PrivateKey = &priv
	}
	if current.ListenPort != desired.ListenPort {
		port := desired.ListenPort
		delta.ListenPort = &port
	}

	currentPeers := make(map[wgtypes.Key]wgtypes.Peer, len(current.Peers))
	for _, p := range current.Peers {
		currentPeers[p.PublicKey] = p
	}
	wanted := make(map[wgtypes.Key]struct{}, len(desired.Peers))
	for _, p := range desired.Peers {
		wanted[p.PublicKey] = struct{}{}
		existing, ok := currentPeers[p.PublicKey]
		if ok && peerMatches(p, existing) {
			continue
		}
		ka := time.Duration(p.KeepAlive) * time.Second
		pc := wgtypes.PeerConfig{
			PublicKey:                   p.PublicKey,
			Endpoint:                    p.Endpoint,
			PersistentKeepaliveInterval: &ka,
			ReplaceAllowedIPs:           true,
			AllowedIPs:                  p.AllowedIPs,
		}
		if ok && existing.PresharedKey != (wgtypes.Key{}) {
			// Desired peers carry no preshared key; a zero key clears one.
			pc.PresharedKey = &wgtypes.Key{}
		}
		delta.Peers = append(delta.Peers, pc)
	}
	for _, p := range current.Peers {
		if _, ok := wanted[p.PublicKey]; !ok {
			delta.Peers = append(delta.Peers, wgtypes.PeerConfig{PublicKey: p.PublicKey, Remove: true})
		}
	}

	changed := delta.PrivateKey != nil || delta.ListenPort != nil || len(delta.Peers) > 0
	return delta, changed
}

func peerMatches(desired Peer, current wgtypes.Peer) bool {
	if current.PresharedKey != (wgtypes.Key{}) {
		return false
	}
	if current.PersistentKeepaliveInterval != time.Duration(desired.KeepAlive)*time.Second {
		return false
	}
	if desired.Endpoint != nil && !udpAddrEqual(desired.Endpoint, current.Endpoint) {
		return false
	}
	return slices.Equal(allowedIPSet(desired.AllowedIPs), allowedIPSet(current.AllowedIPs))
}

func udpAddrEqual(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Port == b.Port && a.IP.Equal(b.IP) && a.Zone == b.Zone
}

// allowedIPSet canonicalizes allowed IPs for comparison: the kernel reports
// IPv4 prefixes with host bits cleared and may use the 16-byte IP form.
func allowedIPSet(ipNets []net.IPNet) []string {
	out := make([]string, 0, len(ipNets))
	for _, n := range ipNets {
		ip := n.IP
		if v4 := ip.To4(); v4 != nil && len(n.Mask) == net.IPv4len {
			ip = v4
		}
		out = append(out, (&net.IPNet{IP: ip.Mask(n.Mask), Mask: n.Mask}).String())
	}
	slices.Sort(out)
	return slices.Compact(out)
}
