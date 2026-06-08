package chat

import "testing"

func TestIsHTTPURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://example.com", true},
		{"http://example.com/path?q=1", true},
		{"  https://example.com  ", true}, // trimmed before parsing
		{"ftp://example.com", false},      // non-http scheme
		{"example.com", false},            // no scheme/host
		{"/relative/path", false},
		{"", false},
		{"https://", false}, // scheme but no host
		{"://nohost", false},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := isHTTPURL(tc.raw); got != tc.want {
				t.Fatalf("isHTTPURL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSourceCitationParts(t *testing.T) {
	cases := []struct {
		name      string
		source    Source
		wantLabel string
		wantURL   string
		wantOK    bool
	}{
		{
			name:      "http source with label",
			source:    Source{Title: "Docs", URL: "https://example.com"},
			wantLabel: "Docs",
			wantURL:   "https://example.com",
			wantOK:    true,
		},
		{
			name:      "http source without label falls back to URL",
			source:    Source{Title: "  ", URL: "https://example.com"},
			wantLabel: "https://example.com",
			wantURL:   "https://example.com",
			wantOK:    true,
		},
		{
			name:      "non-http source with label keeps label, drops url",
			source:    Source{Title: "Internal KB", URL: "kb://doc/1"},
			wantLabel: "Internal KB",
			wantURL:   "",
			wantOK:    true,
		},
		{
			name:      "non-http source without label is not citable",
			source:    Source{Title: "  ", URL: "kb://doc/1"},
			wantLabel: "",
			wantURL:   "",
			wantOK:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, url, ok := sourceCitationParts(tc.source)
			if label != tc.wantLabel || url != tc.wantURL || ok != tc.wantOK {
				t.Fatalf("sourceCitationParts(%+v) = (%q, %q, %v), want (%q, %q, %v)",
					tc.source, label, url, ok, tc.wantLabel, tc.wantURL, tc.wantOK)
			}
		})
	}
}
