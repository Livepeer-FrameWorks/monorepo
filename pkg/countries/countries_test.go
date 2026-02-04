package countries

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "uppercase", input: "US", expected: "US"},
		{name: "lowercase", input: "us", expected: "US"},
		{name: "whitespace", input: "  gb ", expected: "GB"},
		{name: "three-letter", input: "Usa", expected: "USA"},
		{name: "empty", input: "", expected: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.input); got != tc.expected {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsValid(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "valid uppercase", input: "US", expected: true},
		{name: "valid lowercase", input: "us", expected: true},
		{name: "valid with whitespace", input: "  ca ", expected: true},
		{name: "invalid three-letter", input: "USA", expected: false},
		{name: "invalid code", input: "ZZZ", expected: false},
		{name: "empty", input: "", expected: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValid(tc.input); got != tc.expected {
				t.Fatalf("IsValid(%q) = %t, want %t", tc.input, got, tc.expected)
			}
		})
	}
}
