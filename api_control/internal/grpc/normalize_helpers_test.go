package grpc

import (
	"reflect"
	"testing"
)

func TestNormalizeIngestMode(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"PULL", "pull"},
		{"  Push  ", "push"},
		{"", ""},
	} {
		if got := normalizeIngestMode(tt.in); got != tt.want {
			t.Errorf("normalizeIngestMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// normalizeAllowedClusterIDs is the canonical persisted form for stream
// placement allowlists: trimmed, empties dropped, deduped, and sorted so two
// equivalent inputs persist identically (and equality checks are stable).
func TestNormalizeAllowedClusterIDs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays nil", nil, nil},
		{"all-empty input yields empty slice", []string{"", "  "}, []string{}},
		{"trim dedup sort", []string{" b ", "a", "b", "a "}, []string{"a", "b"}},
		{"already canonical", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		if got := normalizeAllowedClusterIDs(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: normalizeAllowedClusterIDs(%v) = %v, want %v", tt.name, tt.in, got, tt.want)
		}
	}
}
