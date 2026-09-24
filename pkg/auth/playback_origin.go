package auth

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Limits on a playback policy's allowed origins.
const (
	MaxAllowedOrigins     = 50
	MaxAllowedOriginBytes = 256
)

// AnyOrigin in an allowed-origins list admits every origin, including a
// request that carries none.
const AnyOrigin = "*"

// ErrInvalidAllowedOrigin is returned for an allowed-origins entry that is not
// "*" or an http(s) scheme://host[:port] origin.
var ErrInvalidAllowedOrigin = errors.New("allowed origin must be * or scheme://host[:port]")

// NormalizeAllowedOrigins validates and canonicalizes a playback policy's
// allowed origins: each entry is "*" or an http(s) origin with no path,
// query, fragment, or userinfo. Scheme and host are lowercased and a default
// port is dropped, so "HTTPS://Example.com:443" and "https://example.com"
// are one entry. Duplicates collapse; order is kept.
func NormalizeAllowedOrigins(entries []string) ([]string, error) {
	if len(entries) > MaxAllowedOrigins {
		return nil, fmt.Errorf("at most %d allowed origins", MaxAllowedOrigins)
	}
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if len(entry) > MaxAllowedOriginBytes {
			return nil, fmt.Errorf("%w: %q is longer than %d bytes", ErrInvalidAllowedOrigin, entry, MaxAllowedOriginBytes)
		}
		var normalized string
		if entry == AnyOrigin {
			normalized = AnyOrigin
		} else {
			origin, ok := parseOrigin(entry, false)
			if !ok {
				return nil, fmt.Errorf("%w: %q", ErrInvalidAllowedOrigin, entry)
			}
			normalized = origin
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

// RequestOrigin returns the canonical origin of a viewer request: its Origin
// header, or the origin of its Referer when Origin is absent or "null" (as
// browsers send for some media and privacy-sensitive requests). It returns ""
// when neither names an http(s) origin.
func RequestOrigin(origin, referer string) string {
	origin = strings.TrimSpace(origin)
	if origin != "" && origin != "null" {
		if canonical, ok := parseOrigin(origin, false); ok {
			return canonical
		}
		return ""
	}
	if canonical, ok := parseOrigin(strings.TrimSpace(referer), true); ok {
		return canonical
	}
	return ""
}

// OriginAllowed reports whether a request origin (from RequestOrigin) is
// admitted by a normalized allowed-origins list. An empty list restricts
// nothing; "*" admits any origin, including none.
func OriginAllowed(allowed []string, requestOrigin string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, entry := range allowed {
		if entry == AnyOrigin {
			return true
		}
		if requestOrigin != "" && entry == requestOrigin {
			return true
		}
	}
	return false
}

// parseOrigin canonicalizes an http(s) origin. A full URL (a Referer) is
// accepted only when allowPath is set; its path, query, and fragment are
// dropped.
func parseOrigin(raw string, allowPath bool) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if !allowPath && ((u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery) {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), true
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, true
}
