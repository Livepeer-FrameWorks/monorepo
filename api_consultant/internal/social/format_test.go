package social

import (
	"slices"
	"testing"
)

func TestFormatContentType(t *testing.T) {
	tests := []struct {
		in   ContentType
		want string
	}{
		{ContentPlatformStats, "Platform Stats"},
		{ContentFederation, "Federation"},
		{ContentKnowledge, "Knowledge"},
		{ContentType("something_else"), "something_else"}, // default: raw value
	}
	for _, tt := range tests {
		if got := formatContentType(tt.in); got != tt.want {
			t.Errorf("formatContentType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// formatDataPoints turns a data map into human "Label: value" lines:
// snake_case keys become Title Case, whole-number floats drop their decimals,
// fractional floats keep two places, and overly long strings (>100 chars) are
// dropped rather than bloating a post.
func TestFormatDataPoints(t *testing.T) {
	if got := formatDataPoints(nil); got != nil {
		t.Errorf("nil map should yield nil, got %v", got)
	}

	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"whole float drops decimals", map[string]any{"viewer_count": float64(5)}, "Viewer Count: 5"},
		{"fractional float keeps two", map[string]any{"avg_score": 5.5}, "Avg Score: 5.50"},
		{"short string passes through", map[string]any{"region": "eu-west"}, "Region: eu-west"},
		{"other type uses default verb", map[string]any{"enabled": true}, "Enabled: true"},
	}
	for _, tt := range tests {
		got := formatDataPoints(tt.in)
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("%s: formatDataPoints(%v) = %v, want [%q]", tt.name, tt.in, got, tt.want)
		}
	}

	// Long strings are skipped entirely.
	long := make([]byte, 101)
	for i := range long {
		long[i] = 'x'
	}
	got := formatDataPoints(map[string]any{"blurb": string(long), "region": "eu"})
	if slices.ContainsFunc(got, func(s string) bool { return len(s) > 100 }) {
		t.Errorf("long string should have been dropped, got %v", got)
	}
	if !slices.Contains(got, "Region: eu") {
		t.Errorf("short entry should survive alongside dropped long one, got %v", got)
	}
}
