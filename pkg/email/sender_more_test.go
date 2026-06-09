package email

import "testing"

func TestSanitizeHeader_TruncatesAtFirstControlChar(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"leading newline truncates to empty", "\nBcc: attacker@example.com", ""},
		{"leading carriage return truncates to empty", "\rBcc: attacker@example.com", ""},
		{"mid-string newline keeps prefix", "value\nBcc: attacker", "value"},
		{"clean value passes through trimmed", "  Subject Line  ", "Subject Line"},
		{"no control chars", "plain", "plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeHeader(c.in); got != c.want {
				t.Fatalf("sanitizeHeader(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
