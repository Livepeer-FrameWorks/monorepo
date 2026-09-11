package mist

import "testing"

func TestIngestPublicOriginPreservesAdvertisedIdentity(t *testing.T) {
	for raw, want := range map[string]string{
		"https://node.example:8443/proxy/prefix": "https://node.example:8443",
		"http://[2001:db8::1]:18090/path":        "http://[2001:db8::1]:18090",
		"https://node.example":                   "https://node.example",
		"rtmps://node.example:2935/live/$":       "",
		"https://user:password@node.example":     "",
		"https://node.example?token=secret":      "",
		"https://node.example?":                  "",
		"https://node.example#secret":            "",
		"https://HOST:8443":                      "",
		"https://node.example/$":                 "",
		"https://node.example:invalid":           "",
		"https://":                               "",
	} {
		if got := IngestPublicOrigin(raw); got != want {
			t.Errorf("origin(%q)=%q, want %q", raw, got, want)
		}
	}
}
