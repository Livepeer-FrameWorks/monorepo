package handlers

import "testing"

// redactEmail keeps the domain (needed for routing/analytics) but masks the local
// part to a single leading rune; anything that is not a single local@domain pair
// is treated as opaque and fully redacted.
func TestRedactEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
		want  string
	}{
		{name: "empty stays empty", email: "", want: ""},
		{name: "whitespace only stays empty", email: "   ", want: ""},
		{name: "standard email masks local, keeps domain", email: "user@example.com", want: "u***@example.com"},
		{name: "leading/trailing space trimmed", email: "  user@example.com  ", want: "u***@example.com"},
		{name: "empty local part", email: "@example.com", want: "***@example.com"},
		{name: "no at sign is fully redacted", email: "notanemail", want: "[redacted]"},
		{name: "multiple at signs is fully redacted", email: "a@b@c.com", want: "[redacted]"},
		{name: "multibyte local first rune preserved", email: "über@example.com", want: "ü***@example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactEmail(tc.email); got != tc.want {
				t.Fatalf("redactEmail(%q) = %q, want %q", tc.email, got, tc.want)
			}
		})
	}
}

func TestRedactName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty stays empty", input: "", want: ""},
		{name: "whitespace only stays empty", input: "   ", want: ""},
		{name: "ascii name", input: "Marco", want: "M***"},
		{name: "trimmed before masking", input: "  Marco  ", want: "M***"},
		{name: "multibyte first rune preserved", input: "Élan", want: "É***"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactName(tc.input); got != tc.want {
				t.Fatalf("redactName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
