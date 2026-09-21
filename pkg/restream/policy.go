package restream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
)

const (
	allowPrivateEnv = "RESTREAM_ALLOW_PRIVATE_DESTINATIONS"
	allowedCIDRsEnv = "RESTREAM_ALLOWED_PRIVATE_CIDRS"
	deniedCIDRsEnv  = "RESTREAM_DENIED_CIDRS"
)

// ErrDestinationResolution marks a transient failure to resolve a syntactically
// valid destination. Callers should retry it; it is not a policy rejection.
var ErrDestinationResolution = errors.New("destination resolution unavailable")

// DestinationPolicy is the network boundary for tenant-supplied outbound
// destinations: restream push targets, where operators may add CIDR
// exceptions, and outbound webhook endpoints, which use the zero policy with
// no exceptions. CIDR exceptions are explicit and apply to private,
// loopback, and link-local destinations; unspecified, multicast, and cloud
// metadata endpoints are never valid destinations.
type DestinationPolicy struct {
	AllowPrivate bool
	AllowedCIDRs []*net.IPNet
	DeniedCIDRs  []*net.IPNet
	LookupIP     func(context.Context, string) ([]net.IP, error)
}

// DestinationPolicyFromValues builds the policy from the raw values of
// RESTREAM_ALLOW_PRIVATE_DESTINATIONS, RESTREAM_ALLOWED_PRIVATE_CIDRS, and
// RESTREAM_DENIED_CIDRS. allowPrivate is true only for "true" (any case) or "1".
func DestinationPolicyFromValues(allowPrivate, allowedCIDRs, deniedCIDRs string) (DestinationPolicy, error) {
	allowPrivate = strings.TrimSpace(allowPrivate)
	policy := DestinationPolicy{
		AllowPrivate: strings.EqualFold(allowPrivate, "true") || allowPrivate == "1",
	}
	var err error
	policy.AllowedCIDRs, err = parseCIDRs(allowedCIDRs, allowedCIDRsEnv)
	if err != nil {
		return DestinationPolicy{}, err
	}
	policy.DeniedCIDRs, err = parseCIDRs(deniedCIDRs, deniedCIDRsEnv)
	if err != nil {
		return DestinationPolicy{}, err
	}
	policy.LookupIP = systemLookupIP
	return policy, nil
}

// PublicDestinationPolicy is the policy for tenant-supplied webhook endpoints:
// public destinations only, with no operator exceptions, and hostnames
// resolved by the system resolver.
func PublicDestinationPolicy() DestinationPolicy {
	return DestinationPolicy{LookupIP: systemLookupIP}
}

func systemLookupIP(ctx context.Context, host string) ([]net.IP, error) {
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
	// A NAT64 or 6to4 address reaches the IPv4 address it embeds, so that
	// address decides, unless the operator excepted the IPv6 form itself.
	if embedded := translatedIPv4(ip); embedded != nil && !p.allowedByCIDR(ip) {
		return p.validateIP(embedded)
	}
	if p.allowedByCIDR(ip) {
		return nil
	}
	if ip.IsPrivate() && p.AllowPrivate {
		return nil
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || !ip.IsGlobalUnicast() || containsIP(nonPublicRanges, ip) {
		return errors.New("private or non-public destinations require an explicit operator policy")
	}
	return nil
}

// nonPublicRanges are ranges Go classifies as global unicast that are not
// reachable public destinations. IPv4: "this network", carrier-grade NAT
// shared address space (which overlay and VPN meshes use), IETF protocol
// assignments, documentation, benchmarking, and the reserved 240.0.0.0/4.
// IPv6: IPv4-compatible addresses (::a.b.c.d), the local-use NAT64 prefix,
// whose IPv4 position depends on a prefix length the address does not carry,
// deprecated site-local, Teredo, benchmarking, documentation, and discard-only.
// An operator CIDR exception still applies to them.
var nonPublicRanges = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/96", "64:ff9b:1::/48", "fec0::/10", "2001::/32", "2001:2::/48", "2001:db8::/32",
		"3fff::/20", "100::/64",
	} {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		out = append(out, network)
	}
	return out
}()

var (
	nat64WellKnown = mustCIDR("64:ff9b::/96")
	sixToFour      = mustCIDR("2002::/16")
)

func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}

// translatedIPv4 returns the IPv4 address a NAT64 well-known-prefix
// (64:ff9b::/96, RFC 6052) or 6to4 (2002::/16, RFC 3056) address reaches, or
// nil for any other address.
func translatedIPv4(ip net.IP) net.IP {
	if len(ip) != net.IPv6len || ip.To4() != nil {
		return nil
	}
	switch {
	case nat64WellKnown.Contains(ip):
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	case sixToFour.Contains(ip):
		return net.IPv4(ip[2], ip[3], ip[4], ip[5]).To4()
	}
	return nil
}

// ValidateIP applies the policy to one resolved address.
func (p DestinationPolicy) ValidateIP(ip net.IP) error {
	return p.validateIP(ip)
}

// ErrDialBlocked marks a connection refused at dial time because the address
// the name resolved to is forbidden by the policy.
var ErrDialBlocked = errors.New("dial blocked by destination policy")

// DialControl returns a net.Dialer Control hook that applies the policy to the
// address actually being connected, after DNS resolution. ValidateURI checks a
// resolution that may differ from the one the dialer makes, so a host that
// answers with a public address at validation and a forbidden one at connect
// time (DNS rebinding) is refused only here.
func (p DestinationPolicy) DialControl() func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrDialBlocked, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: %q is not an address", ErrDialBlocked, host)
		}
		if err := p.validateIP(ip); err != nil {
			return fmt.Errorf("%w: %w", ErrDialBlocked, err)
		}
		return nil
	}
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
