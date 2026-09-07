package restream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	allowPrivateEnv = "RESTREAM_ALLOW_PRIVATE_DESTINATIONS"
	allowedCIDRsEnv = "RESTREAM_ALLOWED_PRIVATE_CIDRS"
	deniedCIDRsEnv  = "RESTREAM_DENIED_CIDRS"
)

// ErrDestinationResolution marks a transient failure to resolve a syntactically
// valid destination. Callers should retry it; it is not a policy rejection.
var ErrDestinationResolution = errors.New("destination resolution unavailable")

// DestinationPolicy is the operator-controlled network boundary for outbound
// restream connections. CIDR exceptions are explicit and apply to private,
// loopback, and link-local destinations; unspecified, multicast, and cloud
// metadata endpoints are never valid destinations.
type DestinationPolicy struct {
	AllowPrivate bool
	AllowedCIDRs []*net.IPNet
	DeniedCIDRs  []*net.IPNet
	LookupIP     func(context.Context, string) ([]net.IP, error)
}

func DestinationPolicyFromEnvironment() (DestinationPolicy, error) {
	policy := DestinationPolicy{
		AllowPrivate: strings.EqualFold(strings.TrimSpace(os.Getenv(allowPrivateEnv)), "true") || strings.TrimSpace(os.Getenv(allowPrivateEnv)) == "1",
	}
	var err error
	policy.AllowedCIDRs, err = parseCIDRs(os.Getenv(allowedCIDRsEnv), allowedCIDRsEnv)
	if err != nil {
		return DestinationPolicy{}, err
	}
	policy.DeniedCIDRs, err = parseCIDRs(os.Getenv(deniedCIDRsEnv), deniedCIDRsEnv)
	if err != nil {
		return DestinationPolicy{}, err
	}
	policy.LookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		out := make([]net.IP, 0, len(addresses))
		for _, address := range addresses {
			out = append(out, address.IP)
		}
		return out, nil
	}
	return policy, nil
}

func parseCIDRs(raw, envName string) ([]*net.IPNet, error) {
	var networks []*net.IPNet
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if ip := net.ParseIP(value); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
				ip = ip.To4()
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s entry %q: %w", envName, value, err)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

// ValidateHost applies checks that do not require DNS. It is suitable for the
// API boundary and rejects literal forbidden addresses and well-known metadata
// hostnames before configuration is stored.
func (p DestinationPolicy) ValidateHost(host string) error {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return errors.New("destination host is required")
	}
	if isMetadataHostname(host) {
		return errors.New("cloud metadata destinations are not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		return p.validateIP(ip)
	}
	return nil
}

// ValidateURI resolves hostname destinations and checks every answer
// immediately before Mist is allowed to connect. Mist performs the final
// resolution, so this narrows SSRF exposure but cannot pin DNS by itself.
func (p DestinationPolicy) ValidateURI(ctx context.Context, parsed *url.URL) error {
	if parsed == nil {
		return errors.New("destination URI is required")
	}
	host := parsed.Hostname()
	if err := p.ValidateHost(host); err != nil {
		return err
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if p.LookupIP == nil {
		return fmt.Errorf("destination DNS resolver is unavailable: %w", ErrDestinationResolution)
	}
	addresses, err := p.LookupIP(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve destination host: %w: %w", err, ErrDestinationResolution)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("destination host resolved to no addresses: %w", ErrDestinationResolution)
	}
	for _, address := range addresses {
		if err := p.validateIP(address); err != nil {
			return fmt.Errorf("destination host resolved to a forbidden address: %w", err)
		}
	}
	return nil
}

func (p DestinationPolicy) validateIP(ip net.IP) error {
	if ip == nil {
		return errors.New("invalid destination address")
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("unspecified or multicast destinations are not allowed")
	}
	if isMetadataIP(ip) {
		return errors.New("cloud metadata destinations are not allowed")
	}
	if containsIP(p.DeniedCIDRs, ip) {
		return errors.New("destination is denied by operator policy")
	}
	if p.allowedByCIDR(ip) {
		return nil
	}
	if ip.IsPrivate() && p.AllowPrivate {
		return nil
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || !ip.IsGlobalUnicast() {
		return errors.New("private or non-public destinations require an explicit operator policy")
	}
	return nil
}

func (p DestinationPolicy) allowedByCIDR(ip net.IP) bool {
	return containsIP(p.AllowedCIDRs, ip)
}

func containsIP(networks []*net.IPNet, ip net.IP) bool {
	for _, network := range networks {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func isMetadataHostname(host string) bool {
	switch host {
	case "metadata.google.internal", "metadata.google", "instance-data.ec2.internal", "metadata.azure.internal":
		return true
	default:
		return false
	}
}

func isMetadataIP(ip net.IP) bool {
	for _, raw := range []string{
		"169.254.169.254", // AWS, Azure, Google Cloud, and OpenStack instance metadata.
		"169.254.170.2",   // AWS ECS task credentials.
		"100.100.100.200", // Alibaba Cloud instance metadata.
		"fd00:ec2::254",   // AWS IPv6 instance metadata.
	} {
		if ip.Equal(net.ParseIP(raw)) {
			return true
		}
	}
	return false
}
