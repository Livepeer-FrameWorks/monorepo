package control

import (
	"strings"
	"testing"

	"frameworks/api_balancing/internal/appconfig"
)

// RedactSourcePullCredential guards the one-way boundary where a connection's
// request URL leaves the platform. It has to strip our own source credential
// and leave everything else byte-identical, because tenants see these URLs and
// a re-encoded query would change what they receive for no reason.
func TestRedactSourcePullCredentialStripsOnlyPlatformSourceCredentials(t *testing.T) {
	useFoghornConfig(t, &appconfig.Foghorn{BalancerCapabilitySecret: "redaction-test-secret"})
	const base = "dtsc://origin.example:4200/live+demo"
	credentialed, err := SourcePullURL(base, "live+demo", OutboundPull{
		AttemptID: "attempt-1", TenantID: "tenant-a", SourceNodeID: "origin-1",
		DestNodeID: "edge-1", DestClusterID: "cluster-b", DTSCURL: base,
	})
	if err != nil {
		t.Fatalf("issue source pull credential: %v", err)
	}
	if got := RedactSourcePullCredential(credentialed); got != base {
		t.Fatalf("redacted = %q, want %q", got, base)
	}

	// A duplicated parameter hides the credential from a first-value-only check.
	// Such a URL never passes admission, so it reaches the tenant webhook as a
	// viewer request and must not carry the credential there.
	doubled := "dtsc://origin.example:4200/live+demo?token=decoy&token=" + SourcePullCredential(credentialed)
	if got := RedactSourcePullCredential(doubled); strings.Contains(got, sourcePullCredentialPrefix) {
		t.Fatalf("duplicated parameter smuggled the credential through redaction: %q", got)
	}

	// A tenant's own playback token shares the parameter name but not the
	// namespace, and must survive untouched along with the rest of the URL.
	unchanged := []string{
		"https://edge.example/hls/live+demo/index.m3u8?token=customer-jwt&z=1&a=2",
		"https://edge.example/hls/live+demo/index.m3u8",
		"dtsc://origin.example:4200/live+demo",
		"::not a url::",
		"",
	}
	for _, raw := range unchanged {
		if got := RedactSourcePullCredential(raw); got != raw {
			t.Fatalf("RedactSourcePullCredential(%q) = %q, want it unchanged", raw, got)
		}
	}
}
