package main

import (
	"slices"
	"testing"
)

func TestNormalizeDNSUpstreams(t *testing.T) {
	got := normalizeDNSUpstreams([]string{"1.1.1.1", "2001:db8::1", "9.9.9.9:5353", "[2001:db8::2]:53", " ", "resolver.internal"})
	want := []string{"1.1.1.1:53", "[2001:db8::1]:53", "9.9.9.9:5353", "[2001:db8::2]:53", "resolver.internal:53"}
	if !slices.Equal(got, want) {
		t.Fatalf("normalizeDNSUpstreams = %v, want %v", got, want)
	}
	if normalizeDNSUpstreams(nil) != nil {
		t.Fatal("expected nil upstreams when none are configured")
	}
}

func TestParseExpectedServiceTypesDedupesAndSorts(t *testing.T) {
	got := parseExpectedServiceTypes([]string{"foghorn", " commodore", "foghorn", ""})
	if !slices.Equal(got, []string{"commodore", "foghorn"}) {
		t.Fatalf("parseExpectedServiceTypes = %v", got)
	}
	if parseExpectedServiceTypes(nil) != nil {
		t.Fatal("expected nil when no services are configured")
	}
}
