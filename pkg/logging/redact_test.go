package logging

import (
	"strings"
	"testing"
)

// The whole point is that the credential does not survive into the tag.
func TestRedactSecretDoesNotContainTheSecret(t *testing.T) {
	const secret = "sk_live_abcdef0123456789"
	got := RedactSecret(secret)
	if strings.Contains(got, secret) {
		t.Fatalf("redacted form leaks the secret: %q", got)
	}
	if !strings.HasPrefix(got, "sk#") {
		t.Errorf("expected a recognisable tag prefix, got %q", got)
	}
}

// Correlation across services depends on the same key producing the same tag
// in every process.
func TestRedactSecretIsStable(t *testing.T) {
	first, second := RedactSecret("key-a"), RedactSecret("key-a")
	if first != second {
		t.Fatal("same secret produced different tags")
	}
	if first == RedactSecret("key-b") {
		t.Fatal("different secrets produced the same tag")
	}
}

// An empty value must stay empty rather than become a constant tag that looks
// like a real publisher.
func TestRedactSecretEmpty(t *testing.T) {
	if got := RedactSecret(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// A short digest would collide across the key space it is meant to distinguish;
// 128 bits keeps the correlation contract honest.
func TestRedactSecretDigestWidth(t *testing.T) {
	got := RedactSecret("anything")
	if hexLen := len(strings.TrimPrefix(got, "sk#")); hexLen != 32 {
		t.Fatalf("digest hex length = %d, want 32 (128 bits)", hexLen)
	}
}
