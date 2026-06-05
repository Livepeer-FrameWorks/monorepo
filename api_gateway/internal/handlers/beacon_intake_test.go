package handlers

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// redactURL is the server-side guarantee that signed playback tokens (?jwt=…)
// and other credentials never reach analytics storage. It must strip from the
// first '?' or '#' and be a no-op otherwise.
func TestRedactURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://edge/play/abc.m3u8", "https://edge/play/abc.m3u8"},
		{"https://edge/play/abc.m3u8?jwt=secret.token.sig", "https://edge/play/abc.m3u8"},
		{"https://edge/play/abc.m3u8#frag", "https://edge/play/abc.m3u8"},
		{"https://edge/play?a=1#b", "https://edge/play"},
		{"https://edge/play#x?notaquery", "https://edge/play"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := redactURL(tt.in); got != tt.want {
			t.Errorf("redactURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.ContainsAny(redactURL(tt.in), "?#") {
			t.Errorf("redactURL(%q) still contains a query/fragment delimiter", tt.in)
		}
	}
}

// validContentID trims and bounds a client-supplied id: reject empty (after
// trim) and anything over 256 chars; otherwise return the trimmed value.
func TestValidContentID(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		wantID string
		wantOK bool
	}{
		{"plain", "abc123", "abc123", true},
		{"trims whitespace", "  abc  ", "abc", true},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
		{"max length ok", strings.Repeat("x", 256), strings.Repeat("x", 256), true},
		{"over max rejected", strings.Repeat("x", 257), "", false},
	}
	for _, tt := range tests {
		gotID, gotOK := validContentID(tt.in)
		if gotID != tt.wantID || gotOK != tt.wantOK {
			t.Errorf("%s: validContentID(%q) = (%q, %t), want (%q, %t)", tt.name, tt.in, gotID, gotOK, tt.wantID, tt.wantOK)
		}
	}
}

// newBeaconEventID is the canonical dedup key; it must always return a valid,
// non-empty UUID and not collide across calls.
func TestNewBeaconEventID(t *testing.T) {
	seen := make(map[string]struct{})
	for range 100 {
		id := newBeaconEventID()
		if id == "" {
			t.Fatal("newBeaconEventID returned empty string")
		}
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("newBeaconEventID returned non-UUID %q: %v", id, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("newBeaconEventID produced a duplicate: %q", id)
		}
		seen[id] = struct{}{}
	}
}
