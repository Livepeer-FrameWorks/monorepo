package middleware

import (
	"net"
	"net/http"
	"strings"

	sharedmw "github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/x402"

	"github.com/gin-gonic/gin"
)

// ParseX402PaymentHeader decodes and parses an X-PAYMENT header value.
func ParseX402PaymentHeader(header string) (*x402pb.X402PaymentPayload, error) {
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

// Proxy-trust and client-IP attribution live in pkg/middleware so Gateway and
// Foghorn resolve the caller identically: a limiter keyed on one address while
// routing scores another lets a caller be limited as themselves and geo-routed
// as someone else.
type TrustedProxies = sharedmw.TrustedProxies

func ParseTrustedProxies(config string) (*TrustedProxies, []string) {
	return sharedmw.ParseTrustedProxies(config)
}

func TrustedProxiesFromEnv(envKey string, lookup func(string) string, onWarn func([]string)) *TrustedProxies {
	return sharedmw.TrustedProxiesFromEnv(envKey, lookup, onWarn)
}

func TrustedClientIP(c *gin.Context, tp *TrustedProxies) string {
	return sharedmw.TrustedClientIP(c, tp)
}

func ClientIPFromRequestWithTrust(r *http.Request, tp *TrustedProxies) string {
	return sharedmw.ClientIPFromRequestWithTrust(r, tp)
}
