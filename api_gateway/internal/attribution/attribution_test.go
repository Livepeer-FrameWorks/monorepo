package attribution

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestSanitizeURL(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		expected string
	}{
		{
			name:     "strips query and fragment",
			raw:      "https://example.com/register?utm_source=ad#section",
			expected: "https://example.com/register",
		},
		{
			name:     "keeps scheme and host for bare origin",
			raw:      "https://example.com?foo=bar",
			expected: "https://example.com",
		},
		{
			name:     "returns empty for invalid",
			raw:      "::://bad-url",
			expected: "",
		},
		{
			name:     "preserves relative path",
			raw:      "/register?utm_source=ad",
			expected: "/register",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeURL(tc.raw); got != tc.expected {
				t.Fatalf("sanitizeURL(%q) = %q, want %q", tc.raw, got, tc.expected)
			}
		})
	}
}

func TestFromRequestSanitizesURLs(t *testing.T) {
	reqURL := "https://example.com/register?utm_source=ad&utm_campaign=launch"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Referer", "https://referrer.example/path?token=secret#frag")

	attr := FromRequest(req, "web", "email_password")
	if attr == nil {
		t.Fatal("expected attribution")
	}

	if attr.LandingPage != "https://example.com/register" {
		t.Fatalf("LandingPage = %q", attr.LandingPage)
	}
	if attr.HttpReferer != "https://referrer.example/path" {
		t.Fatalf("HttpReferer = %q", attr.HttpReferer)
	}
}

func TestEnrichSanitizesURLs(t *testing.T) {
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "example.com", Path: "/start", RawQuery: "foo=bar"}}
	req.Header = http.Header{"Referer": []string{"https://referrer.example/path?secret=1"}}

	attr := Enrich(req, nil)
	if attr == nil {
		t.Fatal("expected attribution")
	}
	if attr.LandingPage != "https://example.com/start" {
		t.Fatalf("LandingPage = %q", attr.LandingPage)
	}
	if attr.HttpReferer != "https://referrer.example/path" {
		t.Fatalf("HttpReferer = %q", attr.HttpReferer)
	}
}
