package middleware

import (
	"context"
	"net/http"
	"testing"
)

func TestClientIPFromRequestWithTrustIgnoresSpoofedHeaders(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	req.Header.Set("X-Real-IP", "198.51.100.10")

	if got := ClientIPFromRequestWithTrust(req, nil); got != "203.0.113.5" {
		t.Fatalf("expected direct IP, got %q", got)
	}
}

func TestClientIPFromRequestWithTrustHonorsProxyChain(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.2, 203.0.113.10")

	trusted, invalid := ParseTrustedProxies("203.0.113.0/24")
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid proxy entries: %v", invalid)
	}

	if got := ClientIPFromRequestWithTrust(req, trusted); got != "198.51.100.2" {
		t.Fatalf("expected forwarded IP, got %q", got)
	}
}
