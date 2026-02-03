package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"

	pb "frameworks/pkg/proto"
	"frameworks/pkg/x402"
)

// ParseX402PaymentHeader decodes and parses an X-PAYMENT header value.
func ParseX402PaymentHeader(header string) (*pb.X402PaymentPayload, error) {
	return x402.ParsePaymentHeader(header)
}

// GetX402PaymentHeader returns the payment payload header value from a request.
// Accepts both X-PAYMENT and PAYMENT-SIGNATURE for x402 interoperability.
func GetX402PaymentHeader(r *http.Request) string {
	if r == nil {
		return ""
	}
	return GetX402PaymentHeaderFromHeaders(r.Header)
}

// GetX402PaymentHeaderFromHeaders returns the payment payload header value.
// Accepts both X-PAYMENT and PAYMENT-SIGNATURE for x402 interoperability.
func GetX402PaymentHeaderFromHeaders(headers http.Header) string {
	return x402.GetPaymentHeaderFromHeaders(headers)
}

// NetworkToChainType maps x402 network names to chain type identifiers.
func NetworkToChainType(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "base", "base-mainnet", "base-sepolia":
		return "base"
	case "arbitrum", "arbitrum-one", "arbitrum-sepolia":
		return "arbitrum"
	case "ethereum", "mainnet":
		return "ethereum"
	default:
		return "ethereum"
	}
}

// ClientIPFromRequest extracts the best-effort client IP from headers.
func ClientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return strings.TrimSpace(realIP)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// TrustedProxies holds parsed CIDR ranges for proxy trust decisions.
type TrustedProxies struct {
	cidrs []*net.IPNet
	cache sync.Map
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or IPs.
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
		cidrs = append(cidrs, &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(maskBits, maskBits),
		})
	}
	if len(cidrs) == 0 {
		return nil, invalid
	}
	return &TrustedProxies{cidrs: cidrs}, invalid
}

// IsTrusted checks if the IP is in a trusted proxy range.
func (tp *TrustedProxies) IsTrusted(ipStr string) bool {
	if tp == nil || ipStr == "" {
		return false
	}
	if cached, ok := tp.cache.Load(ipStr); ok {
		if trusted, ok := cached.(bool); ok {
			return trusted
		}
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		tp.cache.Store(ipStr, false)
		return false
	}
	for _, cidr := range tp.cidrs {
		if cidr.Contains(ip) {
			tp.cache.Store(ipStr, true)
			return true
		}
	}
	tp.cache.Store(ipStr, false)
	return false
}

// ClientIPFromRequestWithTrust extracts the client IP while honoring trusted proxies.
func ClientIPFromRequestWithTrust(r *http.Request, tp *TrustedProxies) string {
	if r == nil {
		return ""
	}
	directIP := extractRemoteAddr(r)
	if tp == nil || !tp.IsTrusted(directIP) {
		return directIP
	}

	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if ip == "" {
				continue
			}
			if !tp.IsTrusted(ip) {
				return ip
			}
		}
		for _, part := range parts {
			ip := strings.TrimSpace(part)
			if ip != "" {
				return ip
			}
		}
	}

	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return directIP
}

func extractRemoteAddr(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
