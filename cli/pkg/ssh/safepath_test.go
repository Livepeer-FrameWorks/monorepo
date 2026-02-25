package ssh

import "testing"

func TestValidateShellPath(t *testing.T) {
	valid := []string{
		"/backups/postgres-20250117.sql",
		"./backups/db",
		"/opt/frameworks/data",
		"/tmp/backup-2025-01-17T12:00:00",
	}
	for _, p := range valid {
		if _, err := ValidateShellPath(p); err != nil {
			t.Errorf("ValidateShellPath(%q) rejected valid path: %v", p, err)
		}
	}

	invalid := []string{
		"; rm -rf /",
		"path$(whoami)",
		"path`id`",
		"path|cat /etc/passwd",
		"path & evil",
		"",
		"  ",
	}
	for _, p := range invalid {
		if _, err := ValidateShellPath(p); err == nil {
			t.Errorf("ValidateShellPath(%q) accepted unsafe path", p)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
	}
	for _, tt := range tests {
		got := ShellQuote(tt.input)
		if got != tt.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
