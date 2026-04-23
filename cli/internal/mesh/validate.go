package mesh

import (
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
)

// ValidateIdentity checks the GitOps-owned WireGuard identity for the given
// hosts. It is intentionally separate from generic manifest validation so
// non-mesh local dev manifests can still load.
func ValidateIdentity(manifest *inventory.Manifest, hostNames []string) error {
	if manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	if len(hostNames) == 0 {
		return nil
	}

	var issues []string
	if manifest.WireGuard == nil || !manifest.WireGuard.Enabled {
		issues = append(issues, "wireguard.enabled must be true when privateer is enabled")
	}
	cidrText := ""
	if manifest.WireGuard != nil {
		cidrText = strings.TrimSpace(manifest.WireGuard.MeshCIDR)
	}
	var cidr *net.IPNet
	if cidrText == "" {
		issues = append(issues, "wireguard.mesh_cidr is required")
	} else if ip, parsed, err := net.ParseCIDR(cidrText); err != nil {
		issues = append(issues, fmt.Sprintf("wireguard.mesh_cidr %q is invalid: %v", cidrText, err))
	} else if ip.To4() == nil {
		issues = append(issues, fmt.Sprintf("wireguard.mesh_cidr %q must be IPv4", cidrText))
	} else {
		cidr = parsed
	}

	names := append([]string(nil), hostNames...)
	sort.Strings(names)
	seenIPs := map[string]string{}
	for _, name := range names {
		host, ok := manifest.Hosts[name]
		if !ok {
			issues = append(issues, fmt.Sprintf("host %q is not declared", name))
			continue
		}
		if strings.TrimSpace(host.WireguardIP) == "" {
			issues = append(issues, fmt.Sprintf("host %q: wireguard_ip is required", name))
		} else {
			ip := net.ParseIP(host.WireguardIP)
			switch {
			case ip == nil || ip.To4() == nil:
				issues = append(issues, fmt.Sprintf("host %q: wireguard_ip %q is not a valid IPv4 address", name, host.WireguardIP))
			case cidr != nil && !cidr.Contains(ip):
				issues = append(issues, fmt.Sprintf("host %q: wireguard_ip %q is outside %s", name, host.WireguardIP, cidr.String()))
			}
			if previous, exists := seenIPs[host.WireguardIP]; exists {
				issues = append(issues, fmt.Sprintf("hosts %q and %q share wireguard_ip %q", previous, name, host.WireguardIP))
			} else {
				seenIPs[host.WireguardIP] = name
			}
		}
		if host.WireguardPort <= 0 || host.WireguardPort > 65535 {
			issues = append(issues, fmt.Sprintf("host %q: wireguard_port must be 1-65535", name))
		}
		if err := validateBase64Key(host.WireguardPublicKey); err != nil {
			issues = append(issues, fmt.Sprintf("host %q: wireguard_public_key: %v", name, err))
		}
		if err := validateBase64Key(host.WireguardPrivateKey); err != nil {
			issues = append(issues, fmt.Sprintf("host %q: wireguard_private_key: %v", name, err))
		} else if host.WireguardPublicKey != "" {
			pub, err := DerivePublicKey(host.WireguardPrivateKey)
			if err != nil {
				issues = append(issues, fmt.Sprintf("host %q: derive wireguard public key: %v", name, err))
			} else if pub != host.WireguardPublicKey {
				issues = append(issues, fmt.Sprintf("host %q: wireguard_public_key does not match wireguard_private_key", name))
			}
		}
	}

	if len(issues) > 0 {
		return fmt.Errorf("mesh WireGuard identity is incomplete:\n  %s", strings.Join(issues, "\n  "))
	}
	return nil
}

func validateBase64Key(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("is required")
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return fmt.Errorf("must be base64: %w", err)
	}
	if len(raw) != 32 {
		return fmt.Errorf("must decode to 32 bytes, got %d", len(raw))
	}
	return nil
}
