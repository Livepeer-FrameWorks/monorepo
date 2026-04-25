package wireguard

import (
	"net"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Config is the desired state of the WireGuard interface, expressed in typed
// values parsed once at the agent's outer boundary. Strings live in proto,
// JSON, and env — not here.
type Config struct {
	PrivateKey wgtypes.Key
	Address    netip.Prefix // self mesh address, e.g. 10.88.0.5/32
	ListenPort int
	Peers      []Peer
}

// Peer is a remote WireGuard peer in typed form.
//
// Endpoint is nil-able: WireGuard accepts inbound-only roaming peers without
// an endpoint. FrameWorks policy in policy.go decides whether nil endpoints
// are acceptable for a given mesh role.
type Peer struct {
	PublicKey  wgtypes.Key
	Endpoint   *net.UDPAddr
	AllowedIPs []net.IPNet
	KeepAlive  int
}

// Manager defines the interface for managing the WireGuard device.
type Manager interface {
	// Init ensures the interface exists and is up.
	Init() error
	// Apply configures the interface with the given config (full sync).
	Apply(cfg Config) error
	// Close tears down the interface (if applicable).
	Close() error
}

// toWGTypes maps the typed Config onto wgctrl's wgtypes.Config. ReplacePeers
// and ReplaceAllowedIPs are both set so each apply is a full sync — the
// device ends up exactly matching cfg, with no leftover peers or AllowedIPs
// from a previous apply.
func (c Config) toWGTypes() wgtypes.Config {
	priv := c.PrivateKey
	listenPort := c.ListenPort

	peers := make([]wgtypes.PeerConfig, len(c.Peers))
	for i, p := range c.Peers {
		ka := time.Duration(p.KeepAlive) * time.Second
		peers[i] = wgtypes.PeerConfig{
			PublicKey:                   p.PublicKey,
			Endpoint:                    p.Endpoint,
			PersistentKeepaliveInterval: &ka,
			ReplaceAllowedIPs:           true,
			AllowedIPs:                  p.AllowedIPs,
		}
	}

	return wgtypes.Config{
		PrivateKey:   &priv,
		ListenPort:   &listenPort,
		ReplacePeers: true,
		Peers:        peers,
	}
}
