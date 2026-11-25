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
		"/sk_live%2Emkv":                     "/REDACTED%2Emkv",
		"/view/sk_live%2Ewebm":               "/view/REDACTED%2Ewebm",
		"/%77ebrtc/sk_live":                  "/%77ebrtc/REDACTED",
		"/webrtc%2Fsk_live":                  "/webrtc%2FREDACTED",
		"/view%2f%77ebrtc%2fsk_live":         "/view%2f%77ebrtc%2fREDACTED",
		"/sk_live%2e%6d%6B%76?rate=1":        "/REDACTED%2e%6d%6B%76?rate=1",
		"/%69%6egest/sk_live":                "/%69%6egest/REDACTED",
		"/%2577ebrtc/sk_live":                "/%2577ebrtc/sk_live",
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

// ApplyNginxMap evaluates map entries the way Nginx does: the first matching
// regex wins and its value expands the named captures.
func applyNginxMap(t *testing.T, entries []NginxMapEntry, uri string) string {
	t.Helper()
	for _, e := range entries {
		re := regexp.MustCompile(e.Regex)
		if m := re.FindStringSubmatchIndex(uri); m != nil {
			return string(re.ExpandString(nil, e.Value, uri, m))
		}
	}
	return uri
}

func TestNginxCredentialURIMapRedactsLikeCaddy(t *testing.T) {
	entries := NginxCredentialURIMap()
	for _, uri := range []string{
		"/webrtc/sk_live", "/whip/sk_live", "/ingest/sk_live?protocol=whip", "/view/webrtc/sk_live",
		"/sk_live.mkv", "/view/sk_live.mkv", "/sk_live.webm", "/view/sk_live.webm?rate=1",
		"/webrtc/sk_live.mkv", "/hls/playback/index.m3u8", "/view/webrtc", "/healthz", "/graphql?q=1",
		"/sk_live%2Emkv", "/view/sk_live%2Ewebm", "/%77ebrtc/sk_live", "/webrtc%2Fsk_live",
		"/view%2f%77ebrtc%2fsk_live", "/sk_live%2e%6d%6B%76?rate=1", "/%69%6egest/sk_live",
	} {
		if got, want := applyNginxMap(t, entries, uri), RedactCredentialURI(uri); got != want {
			t.Errorf("nginx map logs %q as %q, Caddy filter as %q", uri, got, want)
		}
	}
	for _, e := range entries {
		if strings.ContainsAny(e.Regex+e.Value, "\"\\ \t\n;") {
			t.Fatalf("nginx map entry %+v needs escaping inside a quoted Nginx token", e)
		}
	}
}
