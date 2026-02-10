package dns

import (
	"regexp"
	"strings"
)

var labelSanitizer = regexp.MustCompile(`[^a-z0-9-]`)

// SanitizeLabel normalizes a string for use as a DNS label (lowercase, hyphens only).
func SanitizeLabel(raw string) string {
	label := strings.ToLower(strings.TrimSpace(raw))
	label = strings.ReplaceAll(label, "_", "-")
	label = labelSanitizer.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-")
	if label == "" {
		return "default"
	}
	return label
}
