package knowledge

import (
	"context"
	"strings"
	"testing"
)

// restoreSSRFAllowlist snapshots the package-global allowlist and restores it
// after the test so allowlist mutations don't leak into other tests.
func restoreSSRFAllowlist(t *testing.T) {
	t.Helper()
	prev := ssrfAllowedHosts.Load()
	t.Cleanup(func() {
		if m, ok := prev.(map[string]bool); ok {
			ssrfAllowedHosts.Store(m)
			return
		}
		SetSSRFAllowedHosts(nil)
	})
}

func TestSSRFAllowedHosts(t *testing.T) {
	restoreSSRFAllowlist(t)

	SetSSRFAllowedHosts([]string{"Internal.Service", "  spaced.host  ", "", "   "})

	cases := []struct {
		host string
		want bool
	}{
		{"internal.service", true}, // stored lowercased
		{"INTERNAL.SERVICE", true}, // lookup is case-insensitive
		{"spaced.host", true},      // surrounding whitespace trimmed on store
		{"not.listed", false},      // absent
		{"", false},                // empty entries were skipped
	}
	for _, tc := range cases {
		if got := isSSRFAllowedHost(tc.host); got != tc.want {
			t.Errorf("isSSRFAllowedHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}

	// Re-setting replaces the allowlist rather than accumulating.
	SetSSRFAllowedHosts([]string{"only.this"})
	if isSSRFAllowedHost("internal.service") {
		t.Error("old allowlist entry survived a replacement Set")
	}
	if !isSSRFAllowedHost("only.this") {
		t.Error("new allowlist entry missing after replacement Set")
	}
}

// TestNewSSRFSafeTransportRejectsPrivate exercises the dialer closure: a literal
// loopback address must be refused (DNS-rebinding defense), and a malformed
// address must fail before any lookup. Both use IP literals so no real DNS or
// network I/O occurs.
func TestNewSSRFSafeTransportRejectsPrivate(t *testing.T) {
	restoreSSRFAllowlist(t)
	SetSSRFAllowedHosts(nil) // ensure loopback is not allowlisted

	tr := NewSSRFSafeTransport()

	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("DialContext(loopback) err = %v, want private-address rejection", err)
	}

	_, err = tr.DialContext(context.Background(), "tcp", "no-port")
	if err == nil || !strings.Contains(err.Error(), "invalid address") {
		t.Fatalf("DialContext(malformed) err = %v, want invalid-address error", err)
	}
}
