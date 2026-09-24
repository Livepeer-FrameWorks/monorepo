package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginMatcherAllowed(t *testing.T) {
	m := NewOriginMatcher([]string{"https://app.example.com/", "*.frameworks.network"}, false)
	cases := map[string]bool{
		"https://app.example.com":             true,
		"https://studio.frameworks.network":   true,
		"https://evil.example.org":            false,
		"https://frameworks.network.evil.org": false,
		"":                                    false,
	}
	for origin, want := range cases {
		if got := m.Allowed(origin); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestCheckWebsocketOrigin(t *testing.T) {
	release := NewOriginMatcher([]string{"https://app.example.com"}, false)
	dev := NewOriginMatcher(nil, true)

	tests := []struct {
		name    string
		matcher *OriginMatcher
		origin  string
		cookie  string
		want    bool
	}{
		{name: "release, no origin, no cookie (server SDK)", matcher: release, want: true},
		{name: "release, foreign origin, no cookie", matcher: release, origin: "https://evil.example.org", want: true},
		{name: "release, allowed origin, no cookie", matcher: release, origin: "https://app.example.com", want: true},
		{name: "release, allowed origin, cookie", matcher: release, origin: "https://app.example.com", cookie: "tok", want: true},
		{name: "release, foreign origin, cookie", matcher: release, origin: "https://evil.example.org", cookie: "tok", want: false},
		{name: "release, no origin, cookie", matcher: release, cookie: "tok", want: false},
		{name: "release, empty cookie value counts as no cookie", matcher: release, origin: "https://evil.example.org", cookie: "", want: true},
		{name: "dev, foreign origin, cookie", matcher: dev, origin: "https://evil.example.org", cookie: "tok", want: true},
		{name: "dev, no origin, cookie", matcher: dev, cookie: "tok", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/graphql/ws", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: tc.cookie})
			}
			if got := tc.matcher.CheckWebsocketOrigin(req); got != tc.want {
				t.Fatalf("CheckWebsocketOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}
