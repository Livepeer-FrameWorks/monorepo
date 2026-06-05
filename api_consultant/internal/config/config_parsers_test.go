package config

import (
	"reflect"
	"testing"
	"time"
)

func TestParseSitemapList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"trims and drops empties", " a , ,b ,", []string{"a", "b"}},
		{"single", "https://x/sitemap.xml", []string{"https://x/sitemap.xml"}},
	}
	for _, tt := range tests {
		if got := parseSitemapList(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: parseSitemapList(%q) = %v, want %v", tt.name, tt.in, got, tt.want)
		}
	}
}

// parseDuration returns the fallback on any unparseable input rather than
// erroring, so a bad env var degrades to the default instead of crashing boot.
func TestParseDuration(t *testing.T) {
	fallback := 30 * time.Second
	if got := parseDuration("5m", fallback); got != 5*time.Minute {
		t.Errorf("parseDuration(5m) = %v, want 5m", got)
	}
	if got := parseDuration("not-a-duration", fallback); got != fallback {
		t.Errorf("parseDuration(bad) = %v, want fallback %v", got, fallback)
	}
	if got := parseDuration("", fallback); got != fallback {
		t.Errorf("parseDuration(empty) = %v, want fallback %v", got, fallback)
	}
}

// parseRateLimitOverrides parses a CSV of tenant:limit pairs, skipping any
// malformed or negative entry rather than failing the whole map.
func TestParseRateLimitOverrides(t *testing.T) {
	got := parseRateLimitOverrides(" t1:10 , t2:20 , bad , t3:notnum , t4:-5 , :5 , t5:0 ")
	want := map[string]int{"t1": 10, "t2": 20, "t5": 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseRateLimitOverrides = %v, want %v", got, want)
	}
	if got := parseRateLimitOverrides(""); len(got) != 0 {
		t.Errorf("empty input should yield empty map, got %v", got)
	}
}
