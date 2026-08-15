package triggers

import (
	"strings"
	"testing"
)

// The push URL carries the publishing credential in its path or query, and this
// payload is persisted to ClickHouse. The URL's shape has to survive — analytics
// reads protocol and host from it — while the key must not.
func TestRedactStreamKeyInURL(t *testing.T) {
	const key = "sk_live_abc123"

	cases := []struct {
		name string
		url  string
	}{
		{"rtmp path", "rtmp://edge-1.example.com:1935/live/" + key},
		{"srt query", "srt://edge-1.example.com:8889?streamid=" + key},
		{"whip path", "https://edge-1.example.com/webrtc/" + key},
	}

	for _, tc := range cases {
		got := redactStreamKeyInURL(tc.url, key)
		if strings.Contains(got, key) {
			t.Errorf("%s: key survived redaction: %q", tc.name, got)
		}
		if !strings.Contains(got, "edge-1.example.com") {
			t.Errorf("%s: host lost, analytics needs it: %q", tc.name, got)
		}
		if !strings.HasPrefix(got, strings.SplitN(tc.url, "://", 2)[0]+"://") {
			t.Errorf("%s: scheme lost: %q", tc.name, got)
		}
	}
}

// A URL that does not contain the key is left exactly as-is — the normal case
// once the emitting side resolves the credential out first.
func TestRedactStreamKeyInURLLeavesCleanURLsAlone(t *testing.T) {
	const clean = "rtmp://edge-1.example.com:1935/live/internal-name"
	if got := redactStreamKeyInURL(clean, "sk_live_abc123"); got != clean {
		t.Fatalf("clean URL modified: %q", got)
	}
	if got := redactStreamKeyInURL(clean, ""); got != clean {
		t.Fatalf("empty key modified the URL: %q", got)
	}
}

// The same key must produce the same mask everywhere, so a publisher's rows
// still correlate across services without carrying the secret.
func TestRedactStreamKeyInURLIsStable(t *testing.T) {
	const key = "sk_live_abc123"
	a := redactStreamKeyInURL("rtmp://h/live/"+key, key)
	b := redactStreamKeyInURL("srt://h?streamid="+key, key)

	maskOf := func(s string) string {
		idx := strings.Index(s, "sk#")
		if idx < 0 {
			t.Fatalf("no mask in %q", s)
		}
		return strings.TrimRight(s[idx:], "/?&")
	}
	if maskOf(a) != maskOf(b) {
		t.Fatalf("mask differs between URLs: %q vs %q", maskOf(a), maskOf(b))
	}
}
