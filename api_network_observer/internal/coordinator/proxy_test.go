package coordinator

import (
	"context"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestProberGeographyDoesNotTrustUnverifiedForwardedIPs(t *testing.T) {
	api := API{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
	request := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/work", nil)
	request.RemoteAddr = "198.51.100.1:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := api.proberIP(request); got != "198.51.100.1" {
		t.Fatalf("untrusted peer spoofed geography: %s", got)
	}
	request.RemoteAddr = "10.0.0.1:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.2, 10.0.0.2")
	if got := api.proberIP(request); got != "198.51.100.2" {
		t.Fatalf("wrong client trust boundary: %s", got)
	}
}
