package wireguard

import (
	"fmt"
	"net/netip"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// ValidateForApply enforces FrameWorks-specific mesh policy on a Config that
// is already type-valid (parsing at the agent boundary handles WG-shape
// concerns: keys, endpoints, prefixes). The checks here are policy, not
// protocol — wgctrl would happily accept a self-peer or a roaming peer
// without an endpoint, but our mesh model does not.
func ValidateForApply(cfg Config) error {
	zeroKey := wgtypes.Key{}
	if cfg.PrivateKey == zeroKey {
		return fmt.Errorf("private key is unset")
	}
	selfKey := cfg.PrivateKey.PublicKey()
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

	seen := make(map[wgtypes.Key]struct{}, len(cfg.Peers))
	for i, p := range cfg.Peers {
		if p.PublicKey == zeroKey {
			return fmt.Errorf("peer %d: public key is the zero key", i)
		}
		if p.PublicKey == selfKey {
			return fmt.Errorf("peer %d: public key matches self", i)
		}
		if _, dup := seen[p.PublicKey]; dup {
			return fmt.Errorf("peer %d: duplicate public key %s", i, p.PublicKey)
		}
		seen[p.PublicKey] = struct{}{}

		// Endpoint required: FrameWorks does not run inbound-only roaming
		// peers today. Quartermaster filters peers without endpoints; if one
		// reaches Apply, treat it as a programming error.
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
				// A wider mask would silently capture other peers' traffic.
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
