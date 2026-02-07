package knowledge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// privateCIDRs are pre-computed at package init to avoid re-parsing on every call.
var privateCIDRs []*net.IPNet

func init() {
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",  // CGNAT
		"169.254.0.0/16", // link-local
		"fc00::/7",       // IPv6 ULA
	} {
		_, parsed, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("bad CIDR %q: %v", cidr, err))
		}
		privateCIDRs = append(privateCIDRs, parsed)
	}
}

// validateCrawlURL checks that a URL is safe to crawl: http(s) scheme and
// non-private destination. This is a fast-path check; the authoritative guard
// is the SSRF-safe dialer returned by NewSSRFSafeTransport.
func validateCrawlURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported scheme %q (only http/https allowed)", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing hostname in url")
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed for %s: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("url resolves to private/reserved address %s", ipStr)
		}
	}

	return parsed, nil
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, cidr := range privateCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// NewSSRFSafeTransport returns an http.Transport whose DialContext validates
// resolved IP addresses before connecting, preventing DNS rebinding SSRF.
func NewSSRFSafeTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("ssrf dialer: invalid address %q: %w", addr, err)
			}

			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("ssrf dialer: dns lookup %s: %w", host, err)
			}

			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip == nil {
					continue
				}
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("ssrf dialer: %s resolves to private address %s", host, ipStr)
				}
			}

			// Connect to the first resolved IP directly to prevent rebinding
			// between our check and the actual connection.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
		},
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}
