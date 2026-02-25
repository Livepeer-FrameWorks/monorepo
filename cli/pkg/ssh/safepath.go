package ssh

import (
	"fmt"
	"regexp"
	"strings"
)

var unsafePathPattern = regexp.MustCompile("[;|&$`(){}\\\\<>!?*\\[\\]#~\"'\\n\\r\\x00]")

// ValidateShellPath rejects paths containing shell metacharacters.
func ValidateShellPath(path string) (string, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return "", fmt.Errorf("path is empty")
	}
	if unsafePathPattern.MatchString(clean) {
		return "", fmt.Errorf("path contains unsafe shell characters: %q", clean)
	}
	return clean, nil
}

// ShellQuote wraps a value in single quotes with proper POSIX escaping.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
