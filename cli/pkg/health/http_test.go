package health

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// addrPort splits an httptest.Server URL into the (address, port) pair that
// HTTPChecker.Check expects.
func addrPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split %q: %v", u.Host, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("atoi %q: %v", portStr, err)
	}
	return host, port
}

// HTTPChecker classifies the response by status class: 2xx healthy, 5xx
// unhealthy, anything else degraded. This is the load-bearing contract.
func TestHTTPCheckerStatusClassification(t *testing.T) {
	tests := []struct {
		name       string
		code       int
		wantOK     bool
		wantStatus string
	}{
		{"200 healthy", http.StatusOK, true, "healthy"},
		{"204 healthy", http.StatusNoContent, true, "healthy"},
		{"500 unhealthy", http.StatusInternalServerError, false, "unhealthy"},
		{"503 unhealthy", http.StatusServiceUnavailable, false, "unhealthy"},
		{"404 degraded", http.StatusNotFound, false, "degraded"},
		{"302 degraded", http.StatusFound, false, "degraded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
			}))
			defer srv.Close()

			addr, port := addrPort(t, srv.URL)
			res := (&HTTPChecker{}).Check(addr, port)

			if res.OK != tt.wantOK || res.Status != tt.wantStatus {
				t.Fatalf("status %d: got OK=%v Status=%q, want OK=%v Status=%q",
					tt.code, res.OK, res.Status, tt.wantOK, tt.wantStatus)
			}
			if res.Metadata["status_code"] != strconv.Itoa(tt.code) {
				t.Errorf("status_code metadata = %q, want %d", res.Metadata["status_code"], tt.code)
			}
		})
	}
}

// The checker hits the configured path; assert it actually requests it.
func TestHTTPCheckerUsesConfiguredPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	addr, port := addrPort(t, srv.URL)
	(&HTTPChecker{Path: "/readyz"}).Check(addr, port)
	if gotPath != "/readyz" {
		t.Fatalf("requested path = %q, want /readyz", gotPath)
	}

	// Default path is /health when unset.
	(&HTTPChecker{}).Check(addr, port)
	if gotPath != "/health" {
		t.Fatalf("default path = %q, want /health", gotPath)
	}
}

// Response body is captured but bounded to 1KB.
func TestHTTPCheckerBodyLimitedTo1KB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()

	addr, port := addrPort(t, srv.URL)
	res := (&HTTPChecker{}).Check(addr, port)
	if got := len(res.Metadata["response"]); got != 1024 {
		t.Fatalf("captured response len = %d, want 1024 (limited)", got)
	}
}

// A connection failure (nothing listening) is reported as unhealthy with an
// error, not a panic.
func TestHTTPCheckerConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	addr, port := addrPort(t, srv.URL)
	srv.Close() // free the port so the dial is refused

	res := (&HTTPChecker{}).Check(addr, port)
	if res.OK {
		t.Fatalf("expected OK=false on refused connection")
	}
	if res.Status != "unhealthy" || res.Error == "" {
		t.Fatalf("got Status=%q Error=%q, want unhealthy with error", res.Status, res.Error)
	}
}
