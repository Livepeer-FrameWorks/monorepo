package middleware

import (
	"context"
	"net/http"
	"testing"
)

func requestFrom(t *testing.T, remoteAddr, forwarded string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.RemoteAddr = remoteAddr
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	return req
}

// A caller on the public internet can set any X-Forwarded-For. Believing it
// would let one client occupy unlimited rate-limit buckets and choose its own
// geo-routing location.
func TestClientIPIgnoresForwardedFromUntrustedPeer(t *testing.T) {
	req := requestFrom(t, "203.0.113.7:44321", "198.51.100.9")
	if got := ClientIPFromRequestWithTrust(req, nil); got != "203.0.113.7" {
		t.Fatalf("got %q, want the peer address", got)
	}
}

// Trust must never be inferred from address shape. A VPN peer or a neighbouring
// container legitimately holds a private address and could otherwise forge the
// header to escape per-IP limits and pick its own geo-routing location.
func TestClientIPDoesNotInferTrustFromPrivateAddress(t *testing.T) {
	for _, peer := range []string{"127.0.0.1:5000", "[::1]:5000", "10.1.2.3:5000", "172.18.0.4:5000", "192.168.1.10:5000"} {
		req := requestFrom(t, peer, "198.51.100.9")
		got := ClientIPFromRequestWithTrust(req, nil)
		if got == "198.51.100.9" {
			t.Errorf("peer %s: forwarded header believed without configured trust", peer)
		}
	}
}

// A configured co-located proxy is trusted. Provisioning derives that value
// from the deploy mode, and an operator can override it; this type itself only
// ever believes what it was given.
func TestClientIPTrustsConfiguredLocalProxy(t *testing.T) {
	tp, _ := ParseTrustedProxies("127.0.0.1/32,::1/128,172.16.0.0/12")
	for _, peer := range []string{"127.0.0.1:5000", "[::1]:5000", "172.18.0.4:5000"} {
		req := requestFrom(t, peer, "198.51.100.9")
		if got := ClientIPFromRequestWithTrust(req, tp); got != "198.51.100.9" {
			t.Errorf("peer %s: got %q, want the forwarded client", peer, got)
		}
	}
}

// With a chain, the client is the last entry that is not itself a trusted hop.
func TestClientIPWalksForwardedChain(t *testing.T) {
	tp, _ := ParseTrustedProxies("127.0.0.1/32,10.0.0.0/8")
	req := requestFrom(t, "127.0.0.1:5000", "198.51.100.9, 10.0.0.2, 10.0.0.3")
	if got := ClientIPFromRequestWithTrust(req, tp); got != "198.51.100.9" {
		t.Fatalf("got %q, want the original client", got)
	}
}

func TestClientIPSkipsMalformedForwardingValues(t *testing.T) {
	tp, _ := ParseTrustedProxies("127.0.0.1/32,10.0.0.0/8")
	req := requestFrom(t, "127.0.0.1:5000", "not-an-ip, 198.51.100.9, also-invalid, 10.0.0.3")
	req.Header.Set("X-Real-IP", "still-not-an-ip")
	if got := ClientIPFromRequestWithTrust(req, tp); got != "198.51.100.9" {
		t.Fatalf("got %q, want the valid untrusted client address", got)
	}
}

func TestClientIPFallsBackToPeerWhenForwardingValuesAreMalformed(t *testing.T) {
	tp, _ := ParseTrustedProxies("127.0.0.1/32")
	req := requestFrom(t, "127.0.0.1:5000", "not-an-ip, also-invalid")
	req.Header.Set("X-Real-IP", "still-not-an-ip")
	if got := ClientIPFromRequestWithTrust(req, tp); got != "127.0.0.1" {
		t.Fatalf("got %q, want the direct peer address", got)
	}
}

func TestClientIPCanonicalizesIPv6BucketIdentity(t *testing.T) {
	tp, _ := ParseTrustedProxies("127.0.0.1/32")
	req := requestFrom(t, "127.0.0.1:5000", "2001:0db8:0:0:0:0:0:1")
	if got := ClientIPFromRequestWithTrust(req, tp); got != "2001:db8::1" {
		t.Fatalf("got %q, want canonical IPv6", got)
	}
}

// An explicitly configured proxy on a public address is trusted too.
func TestClientIPTrustsConfiguredPublicProxy(t *testing.T) {
	tp, invalid := ParseTrustedProxies("203.0.113.0/24")
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid entries: %v", invalid)
	}
	req := requestFrom(t, "203.0.113.7:44321", "198.51.100.9")
	if got := ClientIPFromRequestWithTrust(req, tp); got != "198.51.100.9" {
		t.Fatalf("got %q, want the forwarded client", got)
	}
}

// Services advertise SIGHUP env reload, so an env-backed set must pick up a
// changed value — otherwise Bridge (which parses at startup) and Foghorn can
// disagree about who a caller is until one of them restarts.
func TestTrustedProxiesFromEnvPicksUpChanges(t *testing.T) {
	value := "10.0.0.0/8"
	tp := TrustedProxiesFromEnv("TRUSTED_PROXY_CIDRS", func(string) string { return value }, nil)

	if !tp.IsTrusted("10.1.2.3") {
		t.Fatal("initial value not applied")
	}
	if tp.IsTrusted("192.168.1.1") {
		t.Fatal("unlisted range trusted")
	}

	value = "192.168.0.0/16"
	if tp.IsTrusted("10.1.2.3") {
		t.Error("removed range still trusted after reload")
	}
	if !tp.IsTrusted("192.168.1.1") {
		t.Error("added range not trusted after reload")
	}

	// Clearing the variable withdraws all trust.
	value = ""
	if tp.IsTrusted("192.168.1.1") {
		t.Error("trust survived the variable being cleared")
	}
}

// Unparseable entries are surfaced on every (re)parse, not just at startup.
func TestTrustedProxiesFromEnvReportsInvalidEntries(t *testing.T) {
	var reported []string
	value := "10.0.0.0/8,nonsense"
	tp := TrustedProxiesFromEnv("TRUSTED_PROXY_CIDRS",
		func(string) string { return value },
		func(invalid []string) { reported = append(reported, invalid...) })

	if len(reported) != 1 || reported[0] != "nonsense" {
		t.Fatalf("startup warning = %v, want [nonsense]", reported)
	}

	reported = nil
	value = "10.0.0.0/8,also-nonsense"
	tp.IsTrusted("10.1.2.3") // triggers the refresh
	if len(reported) != 1 || reported[0] != "also-nonsense" {
		t.Fatalf("reload warning = %v, want [also-nonsense]", reported)
	}
}

// Both notations are documented in config/env/base.env, and a typo must be
// reported rather than silently shrinking the trust set.
func TestParseTrustedProxiesAcceptsIPsAndCIDRsAndReportsTypos(t *testing.T) {
	tp, invalid := ParseTrustedProxies("10.0.0.0/8, 203.0.113.7, not-an-ip, 999.1.1.1")
	if tp == nil {
		t.Fatal("expected parsed proxies")
	}
	if !tp.IsTrusted("10.4.5.6") {
		t.Error("CIDR entry not honoured")
	}
	if !tp.IsTrusted("203.0.113.7") {
		t.Error("bare IP entry not honoured")
	}
	if tp.IsTrusted("198.51.100.9") {
		t.Error("unlisted address must not be trusted")
	}
	if len(invalid) != 2 {
		t.Errorf("expected 2 unparseable entries reported, got %v", invalid)
	}
}
