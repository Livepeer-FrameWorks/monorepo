package clients

import (
	"net"
	"net/http"
	"time"
)

// DefaultTransport returns a configured HTTP transport with connection limits.
// This prevents resource exhaustion during downstream failures by capping
// the number of concurrent connections per host.
//
// Without these limits, a dead downstream at 1000 req/s can spawn 30,000+
// goroutines waiting on connections, leading to OOM.
func DefaultTransport() *http.Transport {
	return &http.Transport{
		// Cap concurrent connections to any single host
		MaxConnsPerHost: 100,

		// Keep some connections warm for reuse
		MaxIdleConnsPerHost: 10,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,

		// Connection establishment timeouts
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,

		// TLS handshake timeout
		TLSHandshakeTimeout: 10 * time.Second,

		// Expect continue timeout for 100-continue responses
		ExpectContinueTimeout: 1 * time.Second,
	}
}
