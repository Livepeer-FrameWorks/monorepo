package middleware

import (
	"net"
	"net/http"
	"strings"

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
