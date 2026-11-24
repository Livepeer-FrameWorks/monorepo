package ingress

import (
	"regexp"
	"strings"
	"testing"
)

func TestRedactCredentialURI(t *testing.T) {
	for uri, want := range map[string]string{
		"/webrtc/sk_live":                    "/webrtc/REDACTED",
		"/whip/sk_live":                      "/whip/REDACTED",
		"/ingest/sk_live?protocol=whip":      "/ingest/REDACTED?protocol=whip",
		"/view/webrtc/sk_live":               "/view/webrtc/REDACTED",
		"/sk_live.mkv":                       "/REDACTED.mkv",
		"/view/sk_live.mkv":                  "/view/REDACTED.mkv",
		"/sk_live.webm":                      "/REDACTED.webm",
		"/view/sk_live.webm?rate=1":          "/view/REDACTED.webm?rate=1",
		"/hls/playback/index.m3u8":           "/hls/playback/index.m3u8",
		"/view/hls/playback/index.m3u8":      "/view/hls/playback/index.m3u8",
		"/view/webrtc":                       "/view/webrtc",
		"/assets/player.js":                  "/assets/player.js",
		"/healthz":                           "/healthz",
		"/view/json_playback.js?proto=x.mkv": "/view/json_playback.js?proto=x.mkv",
	} {
		if got := RedactCredentialURI(uri); got != want {
			t.Errorf("RedactCredentialURI(%q) = %q, want %q", uri, got, want)
		}
	}
}

// Caddy reads the Caddyfile field as two quoted tokens; the pattern and its
// replacement must survive that (and Jinja/YAML) verbatim.
func TestCaddyCredentialURILogFieldRoundTrips(t *testing.T) {
	field := CaddyCredentialURILogField()
	for _, s := range []string{CredentialURIRegexp, CredentialURIReplacement} {
		if strings.ContainsAny(s, "\"\\ \t\n") {
			t.Fatalf("%q contains characters that need escaping in a Caddyfile token", s)
		}
	}
	m := regexp.MustCompile(`^request>uri regexp "([^"]+)" "([^"]+)"$`).FindStringSubmatch(field)
	if m == nil || m[1] != CredentialURIRegexp || m[2] != CredentialURIReplacement {
		t.Fatalf("log field %q does not carry the pattern and replacement verbatim", field)
	}
}
