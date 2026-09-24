package middleware

import (
	"net/http"
	"strings"
)

// OriginMatcher decides whether a browser Origin belongs to the configured
// allowed origins. Entries of the form "*.example.com" match any subdomain.
// In dev mode every origin is allowed.
type OriginMatcher struct {
	devMode          bool
	exact            map[string]bool
	wildcardSuffixes []string
}

// NewOriginMatcher builds a matcher from the configured allowed origins.
func NewOriginMatcher(allowedOrigins []string, devMode bool) *OriginMatcher {
	m := &OriginMatcher{devMode: devMode, exact: make(map[string]bool)}
	for _, origin := range allowedOrigins {
		trimmed := strings.TrimRight(origin, "/")
		if strings.HasPrefix(trimmed, "*.") {
			m.wildcardSuffixes = append(m.wildcardSuffixes, trimmed[1:])
		} else {
			m.exact[trimmed] = true
		}
	}
	return m
}

// Allowed reports whether origin is one of the configured allowed origins.
func (m *OriginMatcher) Allowed(origin string) bool {
	if m.devMode {
		return true
	}
	if m.exact[origin] {
		return true
	}
	for _, suffix := range m.wildcardSuffixes {
		if idx := strings.Index(origin, "://"); idx >= 0 && strings.HasSuffix(origin[idx+3:], suffix) {
			return true
		}
	}
	return false
}

// CheckWebsocketOrigin is the upgrade origin check of the GraphQL WebSocket.
//
// The only credential a browser attaches to an upgrade on its own is the
// access_token cookie, so that is the only case a foreign page could ride
// (cross-site WebSocket hijacking): an upgrade carrying it must come from an
// allowed origin. Without the cookie, identity comes solely from the
// connection_init Authorization value or wallet headers the client chose to
// send, which a foreign page cannot borrow; such upgrades are accepted from any
// origin, including none (non-browser clients such as the Go SDK send no
// Origin). This mirrors the HTTP CORS rule, which rejects a foreign Origin only
// when the request also carries cookies.
func (m *OriginMatcher) CheckWebsocketOrigin(r *http.Request) bool {
	if m.devMode {
		return true
	}
	cookie, err := r.Cookie("access_token")
	if err != nil || cookie.Value == "" {
		return true
	}
	origin := r.Header.Get("Origin")
	return origin != "" && m.Allowed(origin)
}
