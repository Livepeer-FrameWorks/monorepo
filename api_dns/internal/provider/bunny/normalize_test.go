package bunny

import "testing"

// normalizeDomain canonicalizes a domain for comparison/storage: lower-cased,
// trimmed, and with the trailing FQDN dot removed.
func TestNormalizeDomain(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"Example.COM", "example.com"},
		{"  example.com.  ", "example.com"},
		{"sub.Example.com.", "sub.example.com"},
		{"", ""},
	} {
		if got := normalizeDomain(tt.in); got != tt.want {
			t.Errorf("normalizeDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// normalizeRecordName additionally collapses the apex markers ("" and "@") to
// the empty record name.
func TestNormalizeRecordName(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"WWW", "www"},
		{"www.example.com.", "www.example.com"},
		{"@", ""},
		{"", ""},
		{"  @  ", ""},
	} {
		if got := normalizeRecordName(tt.in); got != tt.want {
			t.Errorf("normalizeRecordName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
