package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Client-IP attribution shared by every service that rate-limits, geo-routes,
// or bills per caller. It lives here rather than in one service because those
// decisions must agree: a limiter keyed on one address while routing scores
// another lets a caller be limited as themselves and geo-routed as someone
// else.
//
// Forwarding headers are only believed when the immediate peer is a trusted
// proxy, since any client can send X-Forwarded-For.

// TrustedProxies holds parsed CIDR ranges for proxy trust decisions.
//
// Deliberately unmemoised: a cache keyed on peer address grows with distinct
// callers, and a configured CIDR can cover millions of addresses, so even
// caching only hits is an unbounded sink on a public endpoint. A lookup is a
// walk over a handful of prefixes.
//
// An instance from FromEnv re-reads its variable when the value changes, so
// services that reload their environment on SIGHUP pick it up without
// restarting — and so services sharing this type cannot drift apart on who a
// caller is.
type TrustedProxies struct {
	mu     sync.RWMutex
	cidrs  []*net.IPNet
	envKey string // non-empty when env-backed
	raw    string // the value cidrs was parsed from
	lookup func(string) string
	onWarn func(invalid []string)
}

// TrustedProxiesFromEnv returns an env-backed set that refreshes when the
// variable changes. onWarn, if set, is called with unparseable entries each
// time the value is (re)parsed.
func TrustedProxiesFromEnv(envKey string, lookup func(string) string, onWarn func(invalid []string)) *TrustedProxies {
	tp := &TrustedProxies{envKey: envKey, lookup: lookup, onWarn: onWarn}
	tp.refresh(lookup(envKey))
	return tp
}

func (tp *TrustedProxies) refresh(raw string) {
	raw = strings.TrimSpace(raw)
	parsed, invalid := ParseTrustedProxies(raw)

	tp.mu.Lock()
	tp.raw = raw
	if parsed != nil {
		tp.cidrs = parsed.cidrs
	} else {
		tp.cidrs = nil
	}
	tp.mu.Unlock()

	if len(invalid) > 0 && tp.onWarn != nil {
		tp.onWarn(invalid)
	}
}

// current returns the active CIDR set, re-parsing first if this instance is
// env-backed and the variable has changed.
func (tp *TrustedProxies) current() []*net.IPNet {
	if tp.envKey != "" && tp.lookup != nil {
		raw := strings.TrimSpace(tp.lookup(tp.envKey))
		tp.mu.RLock()
		changed := raw != tp.raw
		tp.mu.RUnlock()
		if changed {
			tp.refresh(raw)
		}
	}
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	return tp.cidrs
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or IPs. The second
// return value lists entries that could not be parsed, so callers can surface a
// typo instead of silently narrowing or widening trust.
func ParseTrustedProxies(config string) (*TrustedProxies, []string) {
	if strings.TrimSpace(config) == "" {
		return nil, nil
	}
	entries := strings.Split(config, ",")
	cidrs := make([]*net.IPNet, 0, len(entries))
	invalid := make([]string, 0)
	for _, entry := range entries {
		value := strings.TrimSpace(entry)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			_, cidr, err := net.ParseCIDR(value)
			if err != nil {
				invalid = append(invalid, value)
				continue
			}
			cidrs = append(cidrs, cidr)
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil {
			invalid = append(invalid, value)
			continue
		}
		maskBits := 128
		if ip.To4() != nil {
			maskBits = 32
		}
		cidrs = append(cidrs, &net.IPNet{IP: ip, Mask: net.CIDRMask(maskBits, maskBits)})
	}
	if len(cidrs) == 0 {
		return nil, invalid
	}
	return &TrustedProxies{cidrs: cidrs}, invalid
}

// IsTrusted reports whether ipStr is a configured proxy.
func (tp *TrustedProxies) IsTrusted(ipStr string) bool {
	if tp == nil || ipStr == "" {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, cidr := range tp.current() {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIPFromRequestWithTrust extracts the client IP, honouring forwarding
// headers only from an explicitly configured proxy.
//
// This type never infers trust: it believes a peer only because configuration
// named it. That matters because a private address is not by itself proof of a
// reverse proxy — a VPN peer or a machine on the same LAN also holds one, and
// could forge X-Forwarded-For to defeat per-IP limits and choose its own
// geo-routing location.
//
// A deployment should name the immediate reverse-proxy peer as narrowly as its
// topology permits. Provisioning can name loopback exactly in native mode;
// Docker deployments may need an operator override when their dynamically
// allocated bridge gateway is narrower than the generated default.
func ClientIPFromRequestWithTrust(r *http.Request, tp *TrustedProxies) string {
	if r == nil {
		return ""
	}
	directIP := RemoteAddrIP(r)
	if !tp.IsTrusted(directIP) {
		return directIP
	}

	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		// Right-to-left: the last address the trusted chain did not itself add.
		for i := len(parts) - 1; i >= 0; i-- {
			ip := canonicalIP(parts[i])
			if ip == "" {
				continue
			}
			if !tp.IsTrusted(ip) {
				return ip
			}
		}
		for _, part := range parts {
			if ip := canonicalIP(part); ip != "" {
				return ip
			}
		}
	}

	if realIP := canonicalIP(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return directIP
}

// RemoteAddrIP returns the peer address with any port stripped.
func RemoteAddrIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := canonicalIP(host); ip != "" {
			return ip
		}
		return host
	}
	if ip := canonicalIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func canonicalIP(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// TrustedClientIP is the gin-flavoured entry point. Prefer it over
// gin's ClientIP, which trusts forwarding headers from any peer unless
// SetTrustedProxies was configured (services here do not configure it).
func TrustedClientIP(c *gin.Context, tp *TrustedProxies) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return ClientIPFromRequestWithTrust(c.Request, tp)
}
