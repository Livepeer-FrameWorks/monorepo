package resolvers

import (
	"reflect"
	"testing"
)

func TestParseSignalmanAddrs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "host:1", []string{"host:1"}},
		{"three", "a:1,b:2,c:3", []string{"a:1", "b:2", "c:3"}},
		{"trims and skips blanks", "  a:1 , ,b:2,", []string{"a:1", "b:2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSignalmanAddrs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSignalmanAddrs(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRotateAddrsIsDeterministicPerTenant(t *testing.T) {
	addrs := []string{"a:1", "b:2", "c:3"}
	first := rotateAddrs(addrs, "tenant-A")
	second := rotateAddrs(addrs, "tenant-A")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same tenant should produce same order: %v vs %v", first, second)
	}
	if len(first) != 3 {
		t.Fatalf("rotateAddrs lost entries: %v", first)
	}
	// Distinct tenants don't all collide on the same head replica.
	seen := map[string]bool{}
	for _, tid := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen[rotateAddrs(addrs, tid)[0]] = true
	}
	if len(seen) < 2 {
		t.Fatalf("tenant rotation produced no spread across 8 tenants: %v", seen)
	}
}
