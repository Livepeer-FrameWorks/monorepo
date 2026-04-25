// Package wgpolicy holds the WireGuard policy rules that FrameWorks
// enforces before a config is applied to a kernel device. The rules are
// imported by the Privateer runtime (api_mesh) at apply time and by the
// 'mesh doctor' CLI to simulate apply outcomes against the GitOps
// manifest. 'mesh wg audit' and 'mesh status' compare stored fields and
// do not call into wgpolicy directly.
//
// Rules here are FrameWorks policy — wgctrl itself accepts a much wider
// set of configurations (roaming peers without endpoints, IPv6 mesh, peers
// with /24 AllowedIPs). FrameWorks chooses to be stricter and the rules
// live here.
package wgpolicy

import (
	"fmt"
	"net"
	"net/netip"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Config is the typed form of a WireGuard device configuration. Both the
// runtime apply path and operator tooling build this struct once at the
// boundary; rules below operate on the typed values.
type Config struct {
	PrivateKey wgtypes.Key
	Address    netip.Prefix
	ListenPort int
	Peers      []Peer
}

// Peer is a remote WireGuard peer in typed form. Endpoint is nil-able
// (wgctrl supports inbound-only roaming peers); FrameWorks policy in
// ValidatePeers rejects nil endpoints since our mesh is always endpoint-
// driven.
type Peer struct {
	PublicKey  wgtypes.Key
	Endpoint   *net.UDPAddr
	AllowedIPs []net.IPNet
	KeepAlive  int
}

// ValidateIdentity enforces self-side rules: the private key must be set,
// the self address must be IPv4 /32, and the listen port must be in the
// valid range.
func ValidateIdentity(cfg Config) error {
	zeroKey := wgtypes.Key{}
	if cfg.PrivateKey == zeroKey {
		return fmt.Errorf("private key is unset")
	}
	if cfg.ListenPort < 1 || cfg.ListenPort > 65535 {
		return fmt.Errorf("listen port %d out of range 1-65535", cfg.ListenPort)
	}
	if !cfg.Address.IsValid() {
		return fmt.Errorf("self address is unset")
	}
	if !cfg.Address.Addr().Is4() {
		return fmt.Errorf("self address %s must be IPv4", cfg.Address)
	}
	if cfg.Address.Bits() != 32 {
		return fmt.Errorf("self address %s must be /32", cfg.Address)
	}
	return nil
}

// ValidatePeers enforces peer-set rules against a known self public key:
// no zero/self/duplicate keys, every peer has an endpoint, AllowedIPs are
// IPv4 /32 with no host bits, keep_alive is non-negative.
//
// selfPub is taken as a parameter so callers without a private key (e.g.
// 'mesh doctor' simulating a hypothetical apply from the manifest's
// public-key only) can still run the same checks Privateer applies.
func ValidatePeers(peers []Peer, selfPub wgtypes.Key) error {
	zeroKey := wgtypes.Key{}
	seen := make(map[wgtypes.Key]struct{}, len(peers))
	for i, p := range peers {
		if p.PublicKey == zeroKey {
			return fmt.Errorf("peer %d: public key is the zero key", i)
		}
		if p.PublicKey == selfPub {
			return fmt.Errorf("peer %d: public key matches self", i)
		}
		if _, dup := seen[p.PublicKey]; dup {
			return fmt.Errorf("peer %d: duplicate public key %s", i, p.PublicKey)
		}
		seen[p.PublicKey] = struct{}{}

		if p.Endpoint == nil {
			return fmt.Errorf("peer %d (%s): endpoint is required", i, p.PublicKey)
		}

		if len(p.AllowedIPs) == 0 {
			return fmt.Errorf("peer %d (%s): allowed_ips is empty", i, p.PublicKey)
		}
		for j, ipnet := range p.AllowedIPs {
			ones, bits := ipnet.Mask.Size()
			if bits != 32 {
				return fmt.Errorf("peer %d (%s): allowed_ips[%d] %s must be IPv4", i, p.PublicKey, j, ipnet)
			}
			if ones != 32 {
				return fmt.Errorf("peer %d (%s): allowed_ips[%d] %s must be /32", i, p.PublicKey, j, ipnet)
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP.To4())
			if !ok {
				return fmt.Errorf("peer %d (%s): allowed_ips[%d] has invalid IPv4 %s", i, p.PublicKey, j, ipnet.IP)
			}
			pfx := netip.PrefixFrom(addr, ones)
			if pfx.Masked() != pfx {
				return fmt.Errorf("peer %d (%s): allowed_ips[%d] %s has non-zero host bits", i, p.PublicKey, j, ipnet)
			}
		}

		if p.KeepAlive < 0 {
			return fmt.Errorf("peer %d (%s): keep_alive must be non-negative", i, p.PublicKey)
		}
	}
	return nil
}

// ValidateForApply runs both identity and peer-set validation. selfPub is
// derived from cfg.PrivateKey, so this is the right entrypoint for the
// runtime apply path; tooling that only has a public key should call
// ValidateIdentity / ValidatePeers directly.
func ValidateForApply(cfg Config) error {
	if err := ValidateIdentity(cfg); err != nil {
		return err
	}
	return ValidatePeers(cfg.Peers, cfg.PrivateKey.PublicKey())
}
