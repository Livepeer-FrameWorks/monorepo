package cmd

import (
	"reflect"
	"testing"
)

func TestValidateIP(t *testing.T) {
	// Intent: guard admin input — accept well-formed IPv4 and IPv6 literals,
	// reject anything net.ParseIP cannot parse (hostnames, partial octets,
	// empty), so a bad address never reaches downstream commands.
	valid := []string{"192.168.1.1", "0.0.0.0", "::1", "2001:db8::ff00:42:8329"}
	for _, ip := range valid {
		if err := validateIP(ip); err != nil {
			t.Fatalf("validateIP(%q) unexpected error: %v", ip, err)
		}
	}
	invalid := []string{"", "999.0.0.1", "192.168.1", "not-an-ip", "example.com"}
	for _, ip := range invalid {
		if err := validateIP(ip); err == nil {
			t.Fatalf("validateIP(%q) = nil error, want rejection", ip)
		}
	}
}

func TestParseCommaList(t *testing.T) {
	// Intent: split a CSV flag value into trimmed, non-empty items. Empty/blank
	// input yields nil (not an empty slice). Note: duplicates are intentionally
	// preserved — parseCommaList does not deduplicate.
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty is nil", "", nil},
		{"whitespace is nil", "   ", nil},
		{"single", "a", []string{"a"}},
		{"trims and drops empties", " a , ,b ,, c ", []string{"a", "b", "c"}},
		{"preserves duplicates", "a,a,b", []string{"a", "a", "b"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommaList(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseCommaList(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
