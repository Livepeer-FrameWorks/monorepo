package wireguard

import (
	"time"

	"frameworks/pkg/mesh/wgpolicy"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Config and Peer are type aliases onto pkg/mesh/wgpolicy so the runtime
// apply path and the 'mesh doctor' CLI share one set of types and rules.
// Methods on the runtime-specific surface (toWGTypes) live as package
// functions below because Go does not allow methods on alias targets
// owned by another package.
type Config = wgpolicy.Config
type Peer = wgpolicy.Peer

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
func toWGTypes(c Config) wgtypes.Config {
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
