package health

import (
	"strings"
	"testing"
)

// connString builds the lib/pq DSN; an empty database must default to
// "postgres" and sslmode must stay disabled for the local probe.
func TestConnString(t *testing.T) {
	c := &PostgresChecker{User: "app", Password: "s3cret"}

	dsn := c.connString("10.0.0.1", 5432, "analytics")
	for _, want := range []string{
		"host=10.0.0.1", "port=5432", "user=app", "password=s3cret",
		"dbname=analytics", "sslmode=disable", "connect_timeout=5",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn missing %q: %s", want, dsn)
		}
	}

	// Empty database -> defaults to "postgres".
	if got := c.connString("h", 1, ""); !strings.Contains(got, "dbname=postgres") {
		t.Errorf("empty db should default to postgres: %s", got)
	}
}

// contains is a hand-rolled substring check (prefix/suffix/inner/equal). Pin its
// equivalence to the standard semantics across positions and empty inputs.
func TestContains(t *testing.T) {
	tests := []struct {
		s, sub string
		want   bool
	}{
		{"hello world", "hello", true}, // prefix
		{"hello world", "world", true}, // suffix
		{"hello world", "lo wo", true}, // inner
		{"hello", "hello", true},       // equal
		{"hello", "", true},            // empty substring
		{"", "", true},                 // both empty
		{"hi", "hello", false},         // substring longer than string
		{"hello", "xyz", false},        // absent
	}
	for _, tt := range tests {
		got := contains(tt.s, tt.sub)
		if got != tt.want {
			t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.sub, got, tt.want)
		}
		// containsInner agrees with contains except it cannot special-case the
		// whole-string-equality path for over-length substrings (guarded by the
		// loop bound), so only cross-check the non-trivial cases.
		if tt.sub != "" && len(tt.sub) <= len(tt.s) {
			if ci := containsInner(tt.s, tt.sub); ci != tt.want {
				t.Errorf("containsInner(%q, %q) = %v, want %v", tt.s, tt.sub, ci, tt.want)
			}
		}
	}
}
