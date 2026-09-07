package restream

import (
	"net/url"
	"strings"
)

// MaskTargetURI returns a display-safe destination identity. A restream URI's
// credentials may occupy any path segment, userinfo, query, or fragment, so
// only its scheme and authority are retained.
func MaskTargetURI(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "****"
	}

	masked := &url.URL{
		Scheme: strings.ToLower(parsed.Scheme),
		Host:   parsed.Host,
	}
	if parsed.Path != "" || parsed.RawPath != "" {
		masked.Path = "/redacted"
	}
	return masked.String()
}
