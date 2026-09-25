package grpc

import (
	"testing"

	"github.com/sirupsen/logrus"
)

// Register and Login check the verifier against nil to choose between
// Turnstile and the behavioral bot check. A typed nil pointer stored in the
// interface passes that check and panics inside Verify.
func TestNewTurnstileVerifierWithoutSecretIsNilInterface(t *testing.T) {
	if v := newTurnstileVerifier(""); v != nil {
		t.Fatalf("no secret must yield a nil verifier, got %#v", v)
	}
	if v := newTurnstileVerifier("secret"); v == nil {
		t.Fatal("a configured secret must yield a verifier")
	}
}

func TestNewCommodoreServerWithoutTurnstileSecretHasNoVerifier(t *testing.T) {
	s := NewCommodoreServer(CommodoreServerConfig{
		Logger:               logrus.New(),
		JWTSecret:            []byte("test-only-jwt-legacy-secret"),
		FieldEncryptionKeyID: "test",
		FieldEncryptionKey:   []byte("test-only-field-encryption-secret"),
	})
	if s.turnstileValidator != nil {
		t.Fatalf("server without a Turnstile secret must not hold a verifier, got %#v", s.turnstileValidator)
	}
}
