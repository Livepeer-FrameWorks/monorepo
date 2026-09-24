package auth

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeAllowedOrigins(t *testing.T) {
	got, err := NormalizeAllowedOrigins([]string{
		"HTTPS://Example.com:443", "https://example.com", " http://app.example.com:8080 ", "*", "http://[::1]:3000",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://example.com", "http://app.example.com:8080", "*", "http://[::1]:3000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %v, want %v", got, want)
	}

	for _, bad := range []string{
		"example.com", "ftp://example.com", "https://example.com/path", "https://example.com?x=1",
		"https://example.com#frag", "https://user@example.com", "https://", "", "**",
	} {
		if _, err := NormalizeAllowedOrigins([]string{bad}); !errors.Is(err, ErrInvalidAllowedOrigin) {
			t.Errorf("%q: err = %v, want ErrInvalidAllowedOrigin", bad, err)
		}
	}

	tooMany := make([]string, MaxAllowedOrigins+1)
	for i := range tooMany {
		tooMany[i] = "*"
	}
	if _, err := NormalizeAllowedOrigins(tooMany); err == nil {
		t.Fatal("more than MaxAllowedOrigins entries must be rejected")
	}
}

func TestRequestOrigin(t *testing.T) {
	for _, tc := range []struct{ origin, referer, want string }{
		{"https://Example.com", "", "https://example.com"},
		{"https://example.com:443", "https://other.example/page", "https://example.com"},
		{"null", "https://embed.example/watch?v=1#t", "https://embed.example"},
		{"", "http://embed.example:8080/a/b", "http://embed.example:8080"},
		{"", "", ""},
		{"null", "", ""},
		{"chrome-extension://abc", "", ""},
		{"https://example.com/path", "", ""},
	} {
		if got := RequestOrigin(tc.origin, tc.referer); got != tc.want {
			t.Errorf("RequestOrigin(%q, %q) = %q, want %q", tc.origin, tc.referer, got, tc.want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	list := []string{"https://example.com", "http://app.example.com:8080"}
	for _, tc := range []struct {
		allowed []string
		origin  string
		want    bool
	}{
		{nil, "", true},
		{nil, "https://any.example", true},
		{list, "https://example.com", true},
		{list, "http://app.example.com:8080", true},
		{list, "https://app.example.com:8080", false},
		{list, "https://evil.example", false},
		{list, "", false},
		{[]string{"*"}, "", true},
		{[]string{"https://example.com", "*"}, "https://evil.example", true},
	} {
		if got := OriginAllowed(tc.allowed, tc.origin); got != tc.want {
			t.Errorf("OriginAllowed(%v, %q) = %v, want %v", tc.allowed, tc.origin, got, tc.want)
		}
	}
}
